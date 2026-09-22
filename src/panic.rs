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

/// Teaches the runtime the ids of `Error() string`, `String() string` and
/// `RuntimeError()`, so a recovered runtime error satisfies both `error` and
/// `runtime.Error` like gc's does. Generated `main` calls this before
/// anything else.
pub fn init_runtime_errors(error_id: MethodId, string_id: MethodId, runtime_error_id: MethodId) {
    fn message(d: Data) -> GoStr {
        d.cast::<crate::place::Slot<GoStr>>().load()
    }
    // `RuntimeError()` says nothing and returns nothing: it exists so that
    // only the runtime's own errors satisfy `runtime.Error`.
    fn runtime_error_marker(_: Data) {}
    let mut ms = alloc::vec![
        (error_id, ErasedFn::new(message as *const ())),
        (string_id, ErasedFn::new(message as *const ())),
        (
            runtime_error_id,
            ErasedFn::new(runtime_error_marker as *const ())
        ),
    ];
    // The method table is searched by id, so it has to be sorted.
    ms.sort_unstable_by_key(|&(id, _)| id);
    let methods: &'static [(MethodId, ErasedFn)] = alloc::vec::Vec::leak(ms);
    let desc: &'static TypeDesc = alloc::boxed::Box::leak(alloc::boxed::Box::new(TypeDesc {
        name: "runtime.Error",
        short: "Error",
        pkg_path: "runtime",
        kind: 24, // reflect.String: the value is its message
        size: size_of::<GoStr>(),
        align: align_of::<GoStr>(),
        methods,
        equal: Some(|a, b| message(a) == message(b)),
        hash: Some(|d| crate::map::GoKey::go_hash(&message(d))),
        print: |d, out| out.extend_from_slice(message(d).bytes()),
        ..TypeDesc::DEFAULT
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

// The panics being handled on this goroutine, outermost first. A panic
// raised while another is being handled does not replace it: Go lets the
// inner one be recovered and then carries on with the outer, so each frame
// that catches an unwind adds its own and takes it back off.
#[cfg(feature = "std")]
rt_global! {
    static CURRENT: core::cell::RefCell<alloc::vec::Vec<GoPanic>> =
        core::cell::RefCell::new(alloc::vec::Vec::new());
}

/// Traces the values of the panics being handled.
///
/// A panic's value is a reference like any other, and between the unwind and
/// the `recover` the panic stack is the only thing holding it: the frame that
/// raised it has gone. Deferred calls run in between, and they allocate.
#[cfg(feature = "std")]
pub(crate) fn trace_panics(t: &mut crate::trace::Tracer<'_>) {
    // A collection cannot happen while the stack is borrowed — pushing and
    // popping a panic allocates nothing on the Go heap — but a torn borrow
    // would be a crash, so it is asked for rather than assumed.
    CURRENT.with(|c| {
        if let Ok(panics) = c.try_borrow() {
            for p in panics.iter() {
                crate::trace::Trace::trace(&p.value, t);
            }
        }
    });
}

#[cfg(not(feature = "std"))]
pub(crate) fn trace_panics(_: &mut crate::trace::Tracer<'_>) {}

/// Takes this goroutine's panics away, leaving none.
///
/// A panic belongs to the goroutine handling it, so the scheduler moves the
/// stack of them with the goroutine (DESIGN §4). Moving a `Vec` is three
/// words and no allocation.
#[cfg(feature = "std")]
pub fn take_panics() -> alloc::vec::Vec<GoPanic> {
    CURRENT.with(|c| core::mem::take(&mut *c.borrow_mut()))
}

/// Gives the thread the panics of the goroutine taking over.
#[cfg(feature = "std")]
pub fn put_panics(panics: alloc::vec::Vec<GoPanic>) {
    CURRENT.with(|c| *c.borrow_mut() = panics);
}

/// Takes the running deferred call's recover boundary away.
#[cfg(feature = "std")]
pub fn take_boundary() -> (usize, usize) {
    BOUNDARY.with(|b| b.replace((0, 0)))
}

/// Gives the thread the boundary of the goroutine taking over.
#[cfg(feature = "std")]
pub fn put_boundary(boundary: (usize, usize)) {
    BOUNDARY.with(|b| b.set(boundary));
}

/// Takes over a caught panic so the deferred calls can `recover` it, and
/// returns the depth to hand back to [`recovered`] and [`resume`].
///
/// A Rust panic that is not a Go panic is a runtime or emitter bug, and
/// keeps unwinding.
#[cfg(feature = "std")]
pub fn begin(payload: alloc::boxed::Box<dyn core::any::Any + Send>) -> usize {
    match payload.downcast::<GoPanic>() {
        Ok(p) => CURRENT.with(|c| {
            let mut v = c.borrow_mut();
            v.push(*p);
            v.len() - 1
        }),
        Err(other) => std::panic::resume_unwind(other),
    }
}

// The frame of the `Defers::run` that is invoking a deferred call, and the
// depth of the panic that call may recover. The frame tells the deferred
// call apart from anything it goes on to call; the depth says which panic is
// its own, so a second `recover` in the same call cannot reach the panic of
// a frame further out.
#[cfg(feature = "std")]
rt_global! {
    static BOUNDARY: core::cell::Cell<(usize, usize)> = core::cell::Cell::new((0, 0));
}

/// Marks the deferred calls of one frame as running, and restores the
/// previous mark when dropped — a panic inside a deferred call runs the
/// deferred calls of frames further out, each with a boundary of its own.
#[cfg(feature = "std")]
pub struct Boundary((usize, usize));

#[cfg(feature = "std")]
impl Boundary {
    /// The deferred calls of a panicking frame are running. `frame` is the
    /// address of the scope invoking them — the frame just outside each call —
    /// and `depth` is the panic they may recover, as [`begin`] returned it.
    #[inline]
    pub fn panicking(frame: usize, depth: usize) -> Boundary {
        Boundary(BOUNDARY.with(|b| b.replace((frame, depth))))
    }

    /// The deferred calls of a frame that is returning normally are running.
    ///
    /// Nothing recovers, unless this frame is itself the call a panicking
    /// frame deferred: then Go treats a `defer recover()` here as a call from
    /// this frame, and it recovers that panic. That case is exactly the frame
    /// just outside this scope having been invoked by the panicking scope, so
    /// the boundary becomes that frame — which is what a deferred `recover`
    /// sees as its caller, while anything deeper sees this scope instead.
    #[inline]
    pub fn normal(frame: usize) -> Boundary {
        let (outer, depth) = BOUNDARY.with(|b| b.get());
        let mut b = (0, 0);
        if outer != 0 {
            // SAFETY: `frame` is a linked frame, alive for as long as it is
            // linked, and so is the frame it was linked onto.
            let caller = unsafe { crate::gc::parent_of(frame) };
            if caller.is_some_and(|c| unsafe { crate::gc::parent_of(c) } == Some(outer)) {
                b = (caller.unwrap_or(0), depth);
            }
        }
        Boundary(BOUNDARY.with(|c| c.replace(b)))
    }
}

#[cfg(feature = "std")]
impl Drop for Boundary {
    #[inline]
    fn drop(&mut self) {
        BOUNDARY.with(|b| b.set(self.0));
    }
}

/// `recover()`: the value of the panic being handled, and an end to it.
///
/// Go's rule, which programs do depend on: a value comes back only when
/// `recover` is called *directly* by a function that a defer invoked. The
/// caller's frame is the one the collector already tracks, so the test is
/// whether the frame just outside it belongs to the `Defers::run` that is
/// running the call. Anything that deferred function calls has a frame of
/// its own in between and recovers nothing; nor does `defer recover()`,
/// whose `recover` runs with no Go frame of its own at all.
#[cfg(feature = "std")]
pub fn recover() -> Iface {
    let (frame, depth) = BOUNDARY.with(|b| b.get());
    if frame == 0 || crate::gc::caller_frame() != frame {
        return Iface::nil();
    }
    let p = CURRENT.with(|c| {
        let mut v = c.borrow_mut();
        // Only this call's own panic, and only while it is still in flight:
        // once recovered, `recover` says nil however often it is called.
        if v.len() == depth + 1 { v.pop() } else { None }
    });
    p.map_or(Iface::nil(), |p| p.value())
}

/// Whether the panic begun at `depth` was recovered.
#[cfg(feature = "std")]
pub fn recovered(depth: usize) -> bool {
    CURRENT.with(|c| c.borrow().len() <= depth)
}

/// Carries on unwinding with the panic begun at `depth`.
#[cfg(feature = "std")]
pub fn resume(depth: usize) -> ! {
    let p = CURRENT.with(|c| {
        let mut v = c.borrow_mut();
        if v.len() <= depth {
            // Recovered: the caller was meant to resume at its recover block
            // instead. Taking the next panic down would carry on with a
            // panic belonging to a frame further out.
            return None;
        }
        // Anything above this panic belongs to a frame that has already
        // unwound past its own handler.
        v.truncate(depth + 1);
        v.pop()
    });
    match p {
        Some(p) => go_panic(p),
        None => unreachable!("resume of a panic that was already recovered"),
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
    /// `unsafe.Slice` with a negative length.
    UnsafeSliceLen,
    /// `unsafe.Slice(nil, n)` with `n > 0`.
    UnsafeSliceNil,
    /// `unsafe.String` with a negative length.
    UnsafeStringLen,
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
            RuntimeError::UnsafeSliceLen => String::from("unsafe.Slice: len out of range"),
            RuntimeError::UnsafeSliceNil => {
                String::from("unsafe.Slice: ptr is nil and len is not zero")
            }
            RuntimeError::UnsafeStringLen => String::from("unsafe.String: len out of range"),
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
///
/// With `RUSTYGO_TRACE=1` it first writes a Rust backtrace to stderr, which
/// is how a runtime error is located until Go-level traces arrive (M3).
#[cold]
pub fn runtime_error(e: RuntimeError) -> ! {
    runtime_error_msg(e.message())
}

/// The same, for a runtime error whose message the caller builds: a failed
/// type assertion, which gc reports as a `*runtime.TypeAssertionError`.
///
/// What matters beyond the text is that the value `recover` hands back
/// satisfies `error` and `runtime.Error`, as every runtime error does.
#[cold]
pub fn runtime_error_msg(msg: String) -> ! {
    #[cfg(feature = "std")]
    if std::env::var_os("RUSTYGO_TRACE").is_some_and(|v| v == "1") {
        let trace = std::backtrace::Backtrace::force_capture();
        let report = alloc::format!("rustygo: {msg}\n{trace}\n");
        let _ = std::io::Write::write_all(&mut std::io::stderr().lock(), report.as_bytes());
    }
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
