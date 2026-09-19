//! Go panics, carried on Rust unwinding.
//!
//! A Go `panic` becomes a Rust unwind whose payload is a [`GoPanic`]. Runtime
//! errors (division by zero, bounds, nil dereference, …) use the same payload
//! with gc's exact text, because programs and tests match on it.
//!
//! For now the payload holds only the text gc prints after `panic: `. From M1,
//! when interfaces exist, it carries the panic value as an `any`, so `recover`
//! can hand it back and runtime errors satisfy `runtime.Error`.

use crate::print::{self, Arg};
use alloc::format;
use alloc::string::String;
use alloc::vec::Vec;

/// Payload of a Rust unwind that implements a Go panic.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GoPanic {
    text: Vec<u8>,
}

impl GoPanic {
    /// Builds a payload from the text gc prints after `panic: `. Go strings
    /// are arbitrary bytes, so the text is too.
    pub fn new(text: impl Into<Vec<u8>>) -> Self {
        GoPanic { text: text.into() }
    }

    /// The text gc prints after `panic: `.
    pub fn text(&self) -> &[u8] {
        &self.text
    }
}

/// The runtime errors generated code can raise.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
#[non_exhaustive]
pub enum RuntimeError {
    /// Integer `/` or `%` by zero.
    DivideByZero,
    /// A shift by a negative signed count.
    NegativeShift,
    /// Dereferencing nil.
    NilDeref,
    /// A signed index outside `[0, len)`.
    Index {
        /// The index.
        index: i64,
        /// The length it was checked against.
        len: usize,
    },
    /// An unsigned index at or past `len`.
    IndexU {
        /// The index.
        index: u64,
        /// The length it was checked against.
        len: usize,
    },
    /// A slice expression's high bound outside `[0, len]`.
    SliceHigh {
        /// The high bound.
        high: i64,
        /// The length it was checked against.
        len: usize,
    },
    /// A slice expression's low bound outside `[0, high]`.
    SliceLow {
        /// The low bound.
        low: i64,
        /// The (already checked) high bound.
        high: usize,
    },
}

impl RuntimeError {
    /// The message exactly as gc prints it after `panic: `.
    pub fn message(self) -> String {
        let detail = match self {
            RuntimeError::DivideByZero => String::from("integer divide by zero"),
            RuntimeError::NegativeShift => String::from("negative shift amount"),
            RuntimeError::NilDeref => {
                String::from("invalid memory address or nil pointer dereference")
            }
            RuntimeError::Index { index, .. } if index < 0 => {
                format!("index out of range [{index}]")
            }
            RuntimeError::Index { index, len } => {
                format!("index out of range [{index}] with length {len}")
            }
            RuntimeError::IndexU { index, len } => {
                format!("index out of range [{index}] with length {len}")
            }
            RuntimeError::SliceHigh { high, .. } if high < 0 => {
                format!("slice bounds out of range [:{high}]")
            }
            RuntimeError::SliceHigh { high, len } => {
                format!("slice bounds out of range [:{high}] with length {len}")
            }
            RuntimeError::SliceLow { low, .. } if low < 0 => {
                format!("slice bounds out of range [{low}:]")
            }
            RuntimeError::SliceLow { low, high } => {
                format!("slice bounds out of range [{low}:{high}]")
            }
        };
        format!("runtime error: {detail}")
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
        panic!("{}", String::from_utf8_lossy(&p.text))
    }
}

/// Raises a Go runtime error.
#[cold]
pub fn runtime_error(e: RuntimeError) -> ! {
    go_panic(GoPanic::new(e.message()))
}

/// `panic(v)` for a value of a predeclared type, printed as gc's
/// `printpanicval` does: like `print`, with newlines in strings followed by
/// a tab.
#[cold]
pub fn panic_value(v: Arg<'_>) -> ! {
    let mut text = Vec::new();
    push_panicval(&mut text, v);
    go_panic(GoPanic::new(text))
}

/// `panic(v)` for a value of a named type with a predeclared underlying type,
/// printed as gc's `printanycustomtype` does: `main.T(5)`, `main.S("x")`.
#[cold]
pub fn panic_custom(type_name: &str, v: Arg<'_>) -> ! {
    let mut text = Vec::from(type_name.as_bytes());
    if let Arg::Str(_) = v {
        text.extend_from_slice(b"(\"");
        push_panicval(&mut text, v);
        text.extend_from_slice(b"\")");
    } else {
        text.push(b'(');
        push_panicval(&mut text, v);
        text.push(b')');
    }
    go_panic(GoPanic::new(text))
}

/// `panic(nil)`.
#[cold]
pub fn panic_nil() -> ! {
    go_panic(GoPanic::new("panic called with nil argument"))
}

fn push_panicval(out: &mut Vec<u8>, v: Arg<'_>) {
    match v {
        Arg::Str(s) => {
            for &b in s {
                out.push(b);
                if b == b'\n' {
                    out.push(b'\t');
                }
            }
        }
        _ => print::format(out, &[v], false),
    }
}
