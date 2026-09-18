//! Process entry point for a compiled Go `main` package.

use crate::panic::GoPanic;
use std::io::Write;

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
