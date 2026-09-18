//! Go panics, carried on Rust unwinding.
//!
//! A Go `panic` becomes a Rust unwind whose payload is a [`GoPanic`]. Runtime
//! errors (division by zero, negative shifts, and later nil dereference, bounds
//! and failed type assertions) use the same payload with Go's exact text,
//! because programs and tests match on it.
//!
//! For now the payload holds only the text Go prints after `panic: `. From M1,
//! when interfaces exist, it carries the panic value as an `any`, so `recover`
//! can hand it back and runtime errors satisfy `runtime.Error`.

use alloc::vec::Vec;

/// Payload of a Rust unwind that implements a Go panic.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GoPanic {
    text: Vec<u8>,
}

impl GoPanic {
    /// Builds a payload from the text Go prints after `panic: `. Go strings
    /// are arbitrary bytes, so the text is too.
    pub fn new(text: impl Into<Vec<u8>>) -> Self {
        GoPanic { text: text.into() }
    }

    /// The text Go prints after `panic: `.
    pub fn text(&self) -> &[u8] {
        &self.text
    }
}

/// The runtime errors generated code can raise, with Go's messages.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
#[non_exhaustive]
pub enum RuntimeError {
    /// Integer `/` or `%` by zero.
    DivideByZero,
    /// A shift by a negative signed count.
    NegativeShift,
}

impl RuntimeError {
    /// The message exactly as gc prints it after `panic: `.
    pub fn message(self) -> &'static str {
        match self {
            RuntimeError::DivideByZero => "runtime error: integer divide by zero",
            RuntimeError::NegativeShift => "runtime error: negative shift amount",
        }
    }
}

/// Starts a Go panic.
///
/// With `std` this unwinds with a [`GoPanic`] payload and does not run Rust's
/// panic hook, so nothing Rust-flavored reaches stderr; `rt::run_main`
/// prints the Go-style report. Without `std` it is an ordinary Rust panic
/// until the `no_std` unwinding question (DESIGN §13, question 2) is settled.
#[cold]
pub fn go_panic(p: GoPanic) -> ! {
    #[cfg(feature = "std")]
    {
        std::panic::resume_unwind(alloc::boxed::Box::new(p))
    }
    #[cfg(not(feature = "std"))]
    {
        panic!("{}", alloc::string::String::from_utf8_lossy(&p.text))
    }
}

/// Raises a Go runtime error.
#[cold]
pub fn runtime_error(e: RuntimeError) -> ! {
    go_panic(GoPanic::new(e.message()))
}
