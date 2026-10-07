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
    crash_copy(b"fatal error: ");
    crash_copy(msg);
    crash_copy(b"\n");
    // gc prints every goroutine's stack here. rustygo cannot yet render a Go
    // stack, but the Rust one says which runtime path gave up, which is what
    // a deadlock or a failed self-test needs to be diagnosed at all.
    if std::env::var_os("RUSTYGO_TRACE").is_some_and(|v| v == "1") {
        let trace = std::backtrace::Backtrace::force_capture();
        let _ = err.write_all(alloc::format!("{trace}\n").as_bytes());
    }
    std::process::exit(2)
}

/// Where a fatal report goes besides standard error, as `debug.SetCrashOutput`
/// asks: a descriptor, or all ones for none.
///
/// A program keeps its own crash log this way, and the point of it is that the
/// log survives when stderr does not — so the descriptor is held here rather
/// than wrapped in anything, and written to with the raw system call, because
/// by the time it is used the program is already past saving.
static CRASH_FD: core::sync::atomic::AtomicUsize = core::sync::atomic::AtomicUsize::new(usize::MAX);

/// Sets that descriptor and reports the one it replaces, which the caller
/// closes; all ones means there was none.
pub fn set_crash_fd(fd: usize) -> usize {
    CRASH_FD.swap(fd, core::sync::atomic::Ordering::AcqRel)
}

/// Writes a piece of the report to the crash descriptor, if there is one.
fn crash_copy(bytes: &[u8]) {
    let fd = CRASH_FD.load(core::sync::atomic::Ordering::Acquire);
    if fd == usize::MAX || bytes.is_empty() {
        return;
    }
    #[cfg(all(target_os = "linux", target_arch = "x86_64"))]
    const SYS_WRITE: u64 = 1;
    #[cfg(all(target_os = "linux", target_arch = "aarch64"))]
    const SYS_WRITE: u64 = 64;
    #[cfg(not(target_os = "linux"))]
    const SYS_WRITE: u64 = 0;
    let mut done = 0;
    while done < bytes.len() {
        let (n, _, errno) = crate::syscall::syscall6(
            SYS_WRITE,
            fd as u64,
            bytes[done..].as_ptr() as u64,
            (bytes.len() - done) as u64,
            0,
            0,
            0,
        );
        if errno != 0 || n == 0 {
            return;
        }
        done += n as usize;
    }
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

/// Sleeps this thread for at least `ns` nanoseconds.
///
/// gc's `runtime.nanosleep` is the same low-level thing: it stops the thread
/// rather than parking the goroutine. What `time.Sleep` uses is
/// `sched::sleep_until`, which parks and leaves the worker to the others.
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
    // The program runs on a goroutine of its own, like every other one, and
    // this thread becomes the first of the scheduler's workers. What happens
    // when `main` returns, panics or calls `runtime.Goexit` is decided there,
    // because by then this thread may be running something else entirely
    // (`sched::run_program`).
    #[cfg(any(
        all(target_arch = "x86_64", not(windows)),
        all(target_arch = "aarch64", not(windows))
    ))]
    crate::sched::run_program(init, main);

    // Where there is no context switch yet, `main` runs on this thread, as it
    // did before there was a scheduler at all. Giving the program a goroutine
    // of its own would mean a context switch before the first statement, so
    // every program — `hello` included — would die on a platform that can
    // still run all of the language that does not start a goroutine. Only `go`
    // reaches the unsupported path now, which is what it should cost.
    //
    // Windows x64 makes `rdi`, `rsi` and `xmm6`-`xmm15` callee-saved and keeps
    // the stack's bounds in the thread information block, so it needs a switch
    // of its own (roadmap M5).
    #[cfg(not(any(
        all(target_arch = "x86_64", not(windows)),
        all(target_arch = "aarch64", not(windows))
    )))]
    {
        let result = std::panic::catch_unwind(|| {
            init();
            main();
        });
        match result {
            Ok(()) => exit(0),
            // `runtime.Goexit` in the main goroutine ends it without ending
            // the program: what is left runs, and the scheduler reports a
            // deadlock once nothing can.
            Err(payload) if crate::sched::is_goexit(&*payload) => crate::sched::park_forever(),
            Err(payload) => report_unrecovered(payload),
        }
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
            crash_copy(b"panic: ");
            crash_copy(p.text());
            crash_copy(b"\n\ngoroutine 1 [running]:\n");
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

// Signals.
//
// gc installs a handler for every signal a program asks to hear about, queues
// what arrives, and hands it to the goroutine `os/signal` keeps waiting in
// `signal_recv`. rustygo does the same with the least machinery that is still
// correct. A handler may interrupt any thread between any two instructions, so
// all this one does is set a bit — an atomic `or` is one of the few things that
// is safe there — and the waiting goroutine takes bits off, parking in between
// so the rest of the program runs. Two deliveries of one signal before the
// receiver looks collapse into one, which is also what gc's queue does.
//
// The handler is installed with `rt_sigaction` rather than through the C
// library, like every other system call rustygo makes (src/syscall.rs). On
// x86-64 the kernel refuses to deliver a signal unless it is told how the
// handler returns, so there is a trampoline below; the architectures whose
// kernel supplies its own do not have one.

/// The signals delivered since Go last asked, one bit each, for `sig` in
/// 1..=64 at bit `sig - 1`.
///
/// Gated with its readers: every one of them is Linux-only, because the handler
/// is installed with `rt_sigaction` rather than through the C library, so on
/// another platform these three are dead and `-D warnings` says so.
#[cfg(target_os = "linux")]
static PENDING: core::sync::atomic::AtomicU64 = core::sync::atomic::AtomicU64::new(0);

/// The signals `os/signal` wants delivered, and the ones it wants dropped.
///
/// The kernel's own answer to "is this ignored?" is not Go's: Rust's runtime
/// ignores `SIGPIPE` before any Go code runs, and gc instead installs a handler
/// for nearly everything and keeps its own two sets. So these are kept too,
/// and they are what [`sigpipe`] and `signal_ignored` read.
#[cfg(target_os = "linux")]
static CAUGHT: core::sync::atomic::AtomicU64 = core::sync::atomic::AtomicU64::new(0);
#[cfg(target_os = "linux")]
static IGNORED: core::sync::atomic::AtomicU64 = core::sync::atomic::AtomicU64::new(0);

/// `rt_sigaction`, and the three calls that raise a signal at this thread.
#[cfg(all(target_os = "linux", target_arch = "x86_64"))]
const NR_SIG: (u64, u64, u64, u64) = (13, 39, 186, 234);
#[cfg(all(target_os = "linux", target_arch = "aarch64"))]
const NR_SIG: (u64, u64, u64, u64) = (134, 172, 178, 131);

// The two dispositions that are not a handler; the flag a caught signal is
// installed with, which restarts the system call the signal interrupted, as gc
// does, so that a read in progress is not turned into an `EINTR` the Go code
// above never expected; and the width of a signal mask, which the kernel wants
// told because it has had two.

/// `SIG_DFL`.
#[cfg(target_os = "linux")]
const SIG_DFL: usize = 0;
/// `SIG_IGN`.
#[cfg(target_os = "linux")]
const SIG_IGN: usize = 1;
/// `SA_RESTART`.
#[cfg(target_os = "linux")]
const SA_RESTART: u64 = 0x1000_0000;
/// `sizeof(sigset_t)`, the last argument of `rt_sigaction`.
#[cfg(target_os = "linux")]
const SIGSET_BYTES: u64 = 8;

/// The handler every enabled signal gets: one atomic `or`, and nothing else.
///
/// It runs on whatever stack the interrupted goroutine was using, which is why
/// it stays this small. gc gives its handler a stack of its own, because gc's
/// handler walks the goroutine, decides what the signal means and may start a
/// traceback; this one needs a frame and an immediate.
#[cfg(target_os = "linux")]
extern "C" fn catch(sig: i32) {
    if (1..=64).contains(&sig) {
        PENDING.fetch_or(1u64 << (sig - 1), core::sync::atomic::Ordering::Release);
    }
}

/// How a handler returns on x86-64: `rt_sigreturn`, which is all the C
/// library's own restorer is. The kernel will not deliver a signal without
/// one on this architecture.
#[cfg(all(target_os = "linux", target_arch = "x86_64"))]
#[unsafe(naked)]
unsafe extern "C" fn restorer() {
    core::arch::naked_asm!("mov rax, 15", "syscall")
}

/// Sets this signal's disposition.
#[cfg(target_os = "linux")]
fn sigaction(sig: u32, handler: usize) {
    // The kernel's own `struct sigaction`: the handler, the flags, the restorer
    // on the architectures that return through one, and then the mask, which a
    // handler that only touches an atomic does not need to widen.
    #[cfg(target_arch = "x86_64")]
    const SA_RESTORER: u64 = 0x0400_0000;
    let flags = if handler > SIG_IGN { SA_RESTART } else { 0 };
    #[cfg(target_arch = "x86_64")]
    let act: [usize; 4] = [
        handler,
        (flags | SA_RESTORER) as usize,
        restorer as *const () as usize,
        0,
    ];
    #[cfg(not(target_arch = "x86_64"))]
    let act: [usize; 3] = [handler, flags as usize, 0];
    crate::syscall::syscall6(
        NR_SIG.0,
        sig as u64,
        act.as_ptr() as u64,
        0,
        SIGSET_BYTES,
        0,
        0,
    );
}

/// Whether this is a signal number the kernel will let anyone catch: not zero,
/// which means "no signal", not past the last one, and not `SIGKILL` or
/// `SIGSTOP`.
#[cfg(target_os = "linux")]
fn catchable(sig: u32) -> bool {
    (1..=64).contains(&sig) && sig != 9 && sig != 19
}

/// Puts this signal into one of the two sets, or neither. A signal is never in
/// both: `os/signal` either wants it, drops it, or has given it back.
#[cfg(target_os = "linux")]
fn claim(sig: u32, caught: bool, ignored: bool) {
    use core::sync::atomic::Ordering;
    let bit = 1u64 << (sig - 1);
    let set = |m: &core::sync::atomic::AtomicU64, yes: bool| {
        if yes {
            m.fetch_or(bit, Ordering::Release);
        } else {
            m.fetch_and(!bit, Ordering::Release);
        }
    };
    set(&CAUGHT, caught);
    set(&IGNORED, ignored);
}

/// Starts delivering this signal to Go, as `os/signal.Notify` asks.
#[cfg(target_os = "linux")]
pub fn signal_enable(sig: u32) {
    if catchable(sig) {
        claim(sig, true, false);
        sigaction(sig, catch as *const () as usize);
    }
}

/// Puts this signal back to whatever the kernel does with it by default, which
/// for most of them is to end the process.
#[cfg(target_os = "linux")]
pub fn signal_disable(sig: u32) {
    if catchable(sig) {
        claim(sig, false, false);
        sigaction(sig, SIG_DFL);
    }
}

/// Ignores this signal outright, as `os/signal.Ignore` asks.
#[cfg(target_os = "linux")]
pub fn signal_ignore(sig: u32) {
    if catchable(sig) {
        claim(sig, false, true);
        sigaction(sig, SIG_IGN);
    }
}

/// Whether this signal is being ignored, which is what `os/signal.Ignored`
/// reports.
#[cfg(target_os = "linux")]
pub fn signal_ignored(sig: u32) -> bool {
    use core::sync::atomic::Ordering;
    catchable(sig) && IGNORED.load(Ordering::Acquire) & (1u64 << (sig - 1)) != 0
}

/// The next signal delivered, waiting for one if none has been.
///
/// `os/signal` calls this from a goroutine of its own and does nothing else
/// with it, so waiting here is waiting for the program. There is no descriptor
/// to park on for a signal, so this is the second place rustygo polls (the
/// other is a child's exit, in [`crate::syscall::syscall6_go`]): a short wait,
/// during which the scheduler runs whatever is runnable and only sleeps the
/// thread when nothing is.
#[cfg(target_os = "linux")]
pub fn signal_recv() -> u32 {
    use core::sync::atomic::Ordering;
    loop {
        let bits = PENDING.load(Ordering::Acquire);
        if bits != 0 {
            let sig = bits.trailing_zeros() + 1;
            // Only this signal's bit: a handler may have set others since the
            // load above.
            PENDING.fetch_and(!(1u64 << (sig - 1)), Ordering::AcqRel);
            return sig;
        }
        crate::sched::sleep_until(nanotime() + 2_000_000);
    }
}

/// Waits until no delivered signal is still waiting to be handed to Go, which
/// is what `os/signal.Stop` waits for before it forgets a channel.
#[cfg(target_os = "linux")]
pub fn signal_wait_until_idle() {
    use core::sync::atomic::Ordering;
    while PENDING.load(Ordering::Acquire) != 0 {
        crate::sched::sleep_until(nanotime() + 2_000_000);
    }
}

/// Dies of `SIGPIPE`, as gc does when a write to a closed pipe was a write to
/// standard output or standard error.
///
/// Rust's runtime ignores `SIGPIPE` so that a write to a broken pipe returns
/// `EPIPE` instead of ending the process, which is what `os` wants for every
/// descriptor a program opened itself: the error goes back to the caller. For
/// the two it did not open, Go's rule is the shell's — a program at the end of
/// a pipeline whose reader has gone away stops, rather than printing a write
/// error for every line — and `os` asks for that here. A program that asked to
/// hear about `SIGPIPE` itself hears about it instead, exactly as under gc.
#[cfg(target_os = "linux")]
pub fn sigpipe() {
    use core::sync::atomic::Ordering;
    const SIGPIPE: u32 = 13;
    const BIT: u64 = 1 << (SIGPIPE - 1);
    // A program that asked to be told about it is told about it, and one that
    // asked for it to be dropped has it dropped: either way the write error
    // goes back to the caller and nothing dies. Only when nobody claimed it is
    // the default action what is left — and Rust's runtime turned that off
    // before any Go code ran, so it has to be turned back on to be raised.
    if IGNORED.load(Ordering::Acquire) & BIT != 0 {
        return;
    }
    if CAUGHT.load(Ordering::Acquire) & BIT != 0 {
        PENDING.fetch_or(BIT, Ordering::Release);
        return;
    }
    sigaction(SIGPIPE, SIG_DFL);
    let (_, getpid, gettid, tgkill) = NR_SIG;
    let (pid, _, _) = crate::syscall::syscall6(getpid, 0, 0, 0, 0, 0, 0);
    let (tid, _, _) = crate::syscall::syscall6(gettid, 0, 0, 0, 0, 0, 0);
    crate::syscall::syscall6(tgkill, pid, tid, SIGPIPE as u64, 0, 0, 0);
}

/// On a platform whose signals rustygo does not reach yet, nothing is ever
/// delivered to Go and a broken standard output stays an error returned to the
/// caller.
#[cfg(not(target_os = "linux"))]
pub fn sigpipe() {}

/// Signals are not reached on this platform, so none is ever enabled.
#[cfg(not(target_os = "linux"))]
pub fn signal_enable(_sig: u32) {}

/// Signals are not reached on this platform.
#[cfg(not(target_os = "linux"))]
pub fn signal_disable(_sig: u32) {}

/// Signals are not reached on this platform, and one that is never delivered
/// needs no ignoring.
#[cfg(not(target_os = "linux"))]
pub fn signal_ignore(_sig: u32) {}

/// Signals are not reached on this platform, so every one of them is in effect
/// ignored.
#[cfg(not(target_os = "linux"))]
pub fn signal_ignored(_sig: u32) -> bool {
    true
}

/// Nothing delivers a signal on this platform, so the wait is for ever, which
/// is what it is under gc in a process that is sent none.
#[cfg(not(target_os = "linux"))]
pub fn signal_recv() -> u32 {
    crate::sched::park_forever()
}

/// Nothing is ever queued, so there is nothing to wait for.
#[cfg(not(target_os = "linux"))]
pub fn signal_wait_until_idle() {}

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

/// Whether the runtime should report what the scheduler and the poller are
/// doing, for diagnosing a program that stops making progress.
///
/// `RUSTYGO_TRACE=sched` turns it on. It is read once: the check is on the
/// path every park takes.
#[cfg(feature = "std")]
pub fn trace_sched() -> bool {
    use std::sync::OnceLock;
    static ON: OnceLock<bool> = OnceLock::new();
    *ON.get_or_init(|| std::env::var_os("RUSTYGO_TRACE").is_some_and(|v| v == "sched"))
}

/// Writes one line of runtime trace to standard error.
#[cfg(feature = "std")]
pub fn trace(what: &str) {
    let _ = std::io::Write::write_all(
        &mut std::io::stderr().lock(),
        alloc::format!("rustygo/sched: {what}\n").as_bytes(),
    );
}
