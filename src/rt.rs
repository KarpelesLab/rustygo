//! Process entry point for a compiled Go `main` package.

use crate::panic::GoPanic;
use std::io::Write;

/// gc's `fatal error: ` report, then exit status 2: for failures a program
/// cannot recover from, such as every goroutine being blocked.
pub fn fatal(msg: &[u8]) -> ! {
    crate::stack::leak_all();
    let mut err = std::io::stderr().lock();
    let _ = err.write_all(b"fatal error: ");
    let _ = err.write_all(msg);
    let _ = err.write_all(b"\n");
    std::process::exit(2)
}

/// Writes `n` bytes at `p` to standard error, for the runtime's own
/// reporting paths.
///
/// # Safety
///
/// `p` must point at `n` readable bytes, as its Go caller guarantees.
pub fn write_err(p: crate::unsafe_ptr::UPtr, n: i64) {
    if n <= 0 || p.addr() == 0 {
        return;
    }
    // SAFETY: the caller's contract.
    let bytes = unsafe { std::slice::from_raw_parts(p.addr() as usize as *const u8, n as usize) };
    let _ = std::io::stderr().lock().write_all(bytes);
}

/// The monotonic clock in nanoseconds, from an arbitrary origin.
pub fn nanotime() -> i64 {
    static START: std::sync::OnceLock<std::time::Instant> = std::sync::OnceLock::new();
    START
        .get_or_init(std::time::Instant::now)
        .elapsed()
        .as_nanos() as i64
}

/// Sleeps for at least `ns` nanoseconds, as `time.Sleep`.
///
/// gc parks the goroutine and runs the others. rustygo has one goroutine
/// (roadmap M2), so there is nothing else to run and sleeping the thread is
/// the same thing.
pub fn nanosleep(ns: i64) {
    if ns <= 0 {
        return;
    }
    std::thread::sleep(std::time::Duration::from_nanos(ns as u64));
}

/// Runs a Go program: the package initializers, then `main.main`, then exit.
///
/// The generated `fn main()` is a single call to this. An unrecovered Go panic
/// is reported the way gc reports it and exits with status 2. The first line
/// (`panic: …`) matches gc exactly; the goroutine trace that follows does not
/// yet.
pub fn run_main(init: fn(), main: fn()) -> ! {
    let result = std::panic::catch_unwind(|| {
        init();
        main();
    });
    match result {
        Ok(()) => exit(0),
        Err(payload) => report_unrecovered(payload),
    }
}

/// Reports a panic nothing recovered, the way gc does, and ends the process.
///
/// Every goroutine ends this way when its panic escapes: Go stops the whole
/// program, because there is nobody left to tell.
pub fn report_unrecovered(payload: alloc::boxed::Box<dyn core::any::Any + Send>) -> ! {
    crate::stack::leak_all();
    let mut err = std::io::stderr().lock();
    match payload.downcast::<GoPanic>() {
        Ok(p) => {
            let _ = err.write_all(b"panic: ");
            let _ = err.write_all(p.text());
            let _ = err.write_all(b"\n\ngoroutine 1 [running]:\n");
        }
        // A Rust panic that is not a Go panic is a runtime or emitter bug;
        // Rust's hook has already printed the message.
        Err(_) => {
            let _ = err.write_all(b"fatal error: unexpected Rust panic\n");
        }
    }
    std::process::exit(2)
}

/// The process's arguments, as `os.Args`.
pub fn args() -> crate::slice::Slice<crate::place::Slot<crate::string::GoStr>> {
    strings(std::env::args_os().map(|a| bytes_of(a.as_encoded_bytes())))
}

/// The process's environment, as `KEY=value` strings.
pub fn envs() -> crate::slice::Slice<crate::place::Slot<crate::string::GoStr>> {
    strings(std::env::vars_os().map(|(k, v)| {
        let mut entry = k.into_encoded_bytes();
        entry.push(b'=');
        entry.extend_from_slice(v.as_encoded_bytes());
        bytes_of(&entry)
    }))
}

/// A Go string holding a copy of these bytes. The OS hands us bytes, which
/// is exactly what a Go string is.
fn bytes_of(b: &[u8]) -> crate::string::GoStr {
    crate::string::GoStr::from_bytes(b)
}

/// Collects strings into a Go slice, rooting what is built so far: every
/// string allocates, and each allocation may collect.
///
/// A root slot holds a snapshot of the reference, not the local's address, so
/// the slice has to be re-rooted after every `append`: the append leaves
/// `out` pointing at a fresh backing array, and producing the next string
/// allocates. Rooting only at the top of a `for` body would miss exactly
/// that window, because the iterator's `next` runs before the body.
fn strings(
    mut items: impl Iterator<Item = crate::string::GoStr>,
) -> crate::slice::Slice<crate::place::Slot<crate::string::GoStr>> {
    let mut out = crate::slice::Slice::<crate::place::Slot<crate::string::GoStr>>::zero();
    let frame = crate::gc::Frame::<2>::new();
    frame.scope(|| {
        loop {
            frame.set(0, &out);
            let Some(s) = items.next() else { break };
            frame.set(1, &s);
            out = out.append(s);
        }
    });
    out
}

/// `fcntl(2)`, which gc puts in its runtime: `(value, errno)`.
pub fn fcntl(fd: i32, cmd: i32, arg: i32) -> (i32, i32) {
    #[cfg(all(target_os = "linux", target_arch = "x86_64"))]
    const SYS_FCNTL: u64 = 72;
    #[cfg(all(target_os = "linux", target_arch = "aarch64"))]
    const SYS_FCNTL: u64 = 25;
    #[cfg(not(target_os = "linux"))]
    const SYS_FCNTL: u64 = 0;
    let (r1, _, errno) =
        crate::syscall::syscall6(SYS_FCNTL, fd as u64, cmd as u64, arg as u64, 0, 0, 0);
    if errno != 0 {
        return (-1, errno as i32);
    }
    (r1 as i32, 0)
}

/// The wall clock: whole seconds and nanoseconds since the Unix epoch, as
/// gc's `walltime` reports them.
pub fn walltime() -> (i64, i32) {
    match std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH) {
        Ok(d) => (d.as_secs() as i64, d.subsec_nanos() as i32),
        Err(e) => {
            let d = e.duration();
            (-(d.as_secs() as i64), -(d.subsec_nanos() as i32))
        }
    }
}

/// Ends the process with this status, as `syscall.Exit`.
///
/// Nothing is flushed, because nothing is buffered: a Go program's writes
/// have already reached the file descriptor. gc's `exit` is the same.
pub fn exit(code: i32) -> ! {
    crate::stack::leak_all();
    std::process::exit(code)
}
