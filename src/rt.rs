//! Process entry point for a compiled Go `main` package.

use crate::panic::GoPanic;
use std::io::Write;

/// gc's `fatal error: ` report, then exit status 2: for failures a program
/// cannot recover from, such as every goroutine being blocked.
pub fn fatal(msg: &[u8]) -> ! {
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
        Ok(()) => std::process::exit(0),
        Err(payload) => {
            let mut err = std::io::stderr().lock();
            match payload.downcast::<GoPanic>() {
                Ok(p) => {
                    let _ = err.write_all(b"panic: ");
                    let _ = err.write_all(p.text());
                    let _ = err.write_all(b"\n\ngoroutine 1 [running]:\n");
                }
                // A Rust panic that is not a Go panic is a runtime or emitter
                // bug; Rust's hook has already printed the message.
                Err(_) => {
                    let _ = err.write_all(b"fatal error: unexpected Rust panic\n");
                }
            }
            std::process::exit(2)
        }
    }
}
