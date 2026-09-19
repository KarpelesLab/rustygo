//! Go panics, carried on Rust unwinding.
//!
//! A Go `panic` becomes a Rust unwind whose payload is a [`GoPanic`]. Runtime
//! errors (division by zero, bounds, nil dereference, …) use the same payload
//! with gc's exact text, because programs and tests match on it.
//!
//! For now the payload holds only the text gc prints after `panic: `. From M1,
//! when interfaces exist, it carries the panic value as an `any`, so `recover`
//! can hand it back and runtime errors satisfy `runtime.Error`.

use crate::iface::{Data, ErasedFn, Iface, MethodId, TypeDesc};
use crate::print::{self, Arg};
use crate::string::GoStr;
use alloc::format;
use alloc::string::String;
use alloc::vec::Vec;

/// Payload of a Rust unwind that implements a Go panic: the value `recover`
/// hands back, and the text gc prints after `panic: `.
#[derive(Clone)]
pub struct GoPanic {
    text: Vec<u8>,
    value: Iface,
}

impl GoPanic {
    /// Builds a payload from the text gc prints after `panic: `. Go strings
    /// are arbitrary bytes, so the text is too.
    pub fn new(text: impl Into<Vec<u8>>) -> Self {
        GoPanic {
            text: text.into(),
            value: Iface::nil(),
        }
    }

    /// The same, carrying the value `recover` returns.
    pub fn with_value(text: impl Into<Vec<u8>>, value: Iface) -> Self {
        GoPanic {
            text: text.into(),
            value,
        }
    }

    /// The text gc prints after `panic: `.
    pub fn text(&self) -> &[u8] {
        &self.text
    }

    /// The value `recover` returns.
    pub fn value(&self) -> Iface {
        self.value
    }
}

// The runtime's own error type, as `recover` hands it back: a value whose
// Error() and String() return gc's message. Its method ids come from the
// emitter, which numbers every method in the program, so the descriptor is
// built once at startup.
rt_global! {
    static RUNTIME_ERROR: core::cell::Cell<Option<&'static TypeDesc>> =
        core::cell::Cell::new(None);
}

/// Teaches the runtime the ids of `Error() string` and `String() string`, so
/// a recovered runtime error satisfies `error` like gc's does. Generated
/// `main` calls this before anything else.
pub fn init_runtime_errors(error_id: MethodId, string_id: MethodId) {
    fn message(d: Data) -> GoStr {
        d.cast::<crate::place::Slot<GoStr>>().load()
    }
    let methods: &'static [(MethodId, ErasedFn)] =
        alloc::boxed::Box::leak(alloc::boxed::Box::new(if error_id <= string_id {
            [
                (error_id, ErasedFn::new(message as *const ())),
                (string_id, ErasedFn::new(message as *const ())),
            ]
        } else {
            [
                (string_id, ErasedFn::new(message as *const ())),
                (error_id, ErasedFn::new(message as *const ())),
            ]
        }));
    let desc: &'static TypeDesc = alloc::boxed::Box::leak(alloc::boxed::Box::new(TypeDesc {
        name: "runtime.Error",
        methods,
        equal: Some(|a, b| message(a) == message(b)),
        hash: Some(|d| crate::map::GoKey::go_hash(&message(d))),
        print: |d, out| out.extend_from_slice(message(d).bytes()),
    }));
    RUNTIME_ERROR.with(|c| c.set(Some(desc)));
}

/// The interface value for a runtime error's message, if the program has one.
fn runtime_error_value(msg: &str) -> Iface {
    match RUNTIME_ERROR.with(|c| c.get()) {
        Some(desc) => {
            let s = GoStr::from_bytes(msg.as_bytes());
            // Boxing allocates again, so the string needs a root of its own
            // until the interface value holds it.
            let frame = crate::gc::Frame::<1>::new();
            frame.scope(|| {
                frame.set(0, &s);
                Iface::new(
                    desc,
                    Data::of(crate::place::Ptr::<crate::place::Slot<GoStr>>::alloc(s)),
                )
            })
        }
        None => Iface::nil(),
    }
}

// The panic currently being handled on this goroutine, between the unwind
// and either `recover` or the resume.
#[cfg(feature = "std")]
rt_global! {
    static CURRENT: core::cell::RefCell<Option<GoPanic>> =
        core::cell::RefCell::new(None);
}

/// Takes over a caught panic so the deferred calls can `recover` it.
///
/// A Rust panic that is not a Go panic is a runtime or emitter bug, and
/// keeps unwinding.
#[cfg(feature = "std")]
pub fn begin(payload: alloc::boxed::Box<dyn core::any::Any + Send>) {
    match payload.downcast::<GoPanic>() {
        Ok(p) => CURRENT.with(|c| *c.borrow_mut() = Some(*p)),
        Err(other) => std::panic::resume_unwind(other),
    }
}

/// `recover()`: the value of the panic being handled, and an end to it.
#[cfg(feature = "std")]
pub fn recover() -> Iface {
    CURRENT
        .with(|c| c.borrow_mut().take())
        .map_or(Iface::nil(), |p| p.value())
}

/// Whether the panic being handled was recovered.
#[cfg(feature = "std")]
pub fn recovered() -> bool {
    CURRENT.with(|c| c.borrow().is_none())
}

/// Carries on unwinding with the panic being handled.
#[cfg(feature = "std")]
pub fn resume() -> ! {
    match CURRENT.with(|c| c.borrow_mut().take()) {
        Some(p) => go_panic(p),
        None => unreachable!("resume without a panic in flight"),
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
    /// A three-index slice expression's `max` outside `[0, cap]`.
    SliceCap {
        /// The max bound.
        max: i64,
        /// The capacity it was checked against.
        cap: usize,
    },
    /// `make` with a negative or too-large capacity.
    MakeCap {
        /// The requested capacity.
        cap: i64,
    },
    /// `make` with a length outside `[0, cap]`.
    MakeLen {
        /// The requested length.
        len: i64,
    },
    /// `m[k] = v` on a nil map.
    NilMapWrite,
    /// A map key whose dynamic type is not hashable.
    UnhashableKey {
        /// The dynamic type's name.
        type_name: &'static str,
    },
    /// `==` on interfaces holding an uncomparable type.
    UncomparableType {
        /// The dynamic type's name.
        type_name: &'static str,
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
        // gc prints a few runtime errors without the `runtime error:` tag.
        match self {
            RuntimeError::NilMapWrite => {
                return String::from("assignment to entry in nil map");
            }
            RuntimeError::UnhashableKey { type_name } => {
                return format!("runtime error: hash of unhashable type {type_name}");
            }
            RuntimeError::UncomparableType { type_name } => {
                return format!("runtime error: comparing uncomparable type {type_name}");
            }
            _ => {}
        }
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
            RuntimeError::SliceCap { max, .. } if max < 0 => {
                format!("slice bounds out of range [::{max}]")
            }
            RuntimeError::SliceCap { max, cap } => {
                format!("slice bounds out of range [::{max}] with capacity {cap}")
            }
            RuntimeError::MakeCap { .. } => String::from("makeslice: cap out of range"),
            RuntimeError::MakeLen { .. } => String::from("makeslice: len out of range"),
            RuntimeError::SliceLow { low, .. } if low < 0 => {
                format!("slice bounds out of range [{low}:]")
            }
            RuntimeError::SliceLow { low, high } => {
                format!("slice bounds out of range [{low}:{high}]")
            }
            RuntimeError::NilMapWrite
            | RuntimeError::UnhashableKey { .. }
            | RuntimeError::UncomparableType { .. } => unreachable!("handled above"),
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
    let msg = e.message();
    let value = runtime_error_value(&msg);
    go_panic(GoPanic::with_value(msg, value))
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

/// `panic(v)` where the value is an interface: its type descriptor knows how
/// gc prints it (an `error` prints `Error()`, a named basic type prints
/// `main.T(v)`, and so on).
#[cold]
pub fn panic_iface(v: Iface) -> ! {
    if v.is_nil() {
        panic_nil();
    }
    // Rendering the value calls back into generated code — an error's
    // Error(), which may allocate — so the value needs a root meanwhile.
    let frame = crate::gc::Frame::<1>::new();
    let mut text = Vec::new();
    frame.scope(|| {
        frame.set(0, &v);
        v.print_to(&mut text);
    });
    // gc indents the continuation lines of a multi-line panic value.
    let mut indented = Vec::with_capacity(text.len());
    for b in text {
        indented.push(b);
        if b == b'\n' {
            indented.push(b'\t');
        }
    }
    go_panic(GoPanic::with_value(indented, v))
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
