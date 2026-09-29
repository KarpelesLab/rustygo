//! `defer`.
//!
//! A function that defers keeps a list of thunks: a code pointer and the
//! environment holding the arguments, which Go evaluates at the `defer`
//! statement, not when the call runs. The environment is an ordinary heap
//! place struct, so deferred arguments stay reachable for the collector
//! (DESIGN §5).
//!
//! Generated code runs its body inside `catch_unwind` and calls [`Defers::run`]
//! afterwards, on the way out and on the way through a panic alike. Running
//! the list *after* catching, rather than from a destructor, is what lets a
//! deferred call panic without aborting — and is where `recover` will hook in
//! once interfaces exist.

use crate::func::Env;
use crate::gc::Frame;
use crate::trace::{Trace, Tracer};
use alloc::vec::Vec;
use core::cell::RefCell;

/// One deferred call: the thunk, and the environment holding the arguments.
type Deferred = (fn(Env), Env);

/// `defer f(args)` onto the list a [`Defers::handle`] names, which is how a
/// range-over-func body defers on its enclosing function's behalf.
///
/// Safe in the way [`crate::func::Env::cast`] is: generated code only ever
/// passes a handle `handle` made, and only while the frame holding that list
/// is still running its own iterator.
pub fn push_to(stack: crate::unsafe_ptr::UPtr, f: fn(Env), env: Env) {
    // SAFETY (invariant): as above.
    if let Some(defers) = unsafe { (stack.addr() as *const Defers).as_ref() } {
        defers.push(f, env);
    }
}

/// The deferred calls of one function, innermost last.
#[derive(Default)]
pub struct Defers {
    list: RefCell<Vec<Deferred>>,
}

impl Trace for Defers {
    fn trace(&self, t: &mut Tracer<'_>) {
        for (_, env) in self.list.borrow().iter() {
            env.trace(t);
        }
    }
}

impl Defers {
    /// An empty list.
    pub fn new() -> Self {
        Defers {
            list: RefCell::new(Vec::new()),
        }
    }

    /// The handle `ssa:deferstack()` hands to a range-over-func body.
    ///
    /// A `defer` inside such a body belongs to the function containing the
    /// `range`, not to the body: the body is a closure go/ssa passes to the
    /// iterator, and Go says the deferred call runs when the enclosing
    /// function returns. go/ssa passes the enclosing list into the closure as
    /// a captured value, typed as an opaque pointer, and this is that value.
    #[inline]
    pub fn handle(&self) -> crate::unsafe_ptr::UPtr {
        crate::unsafe_ptr::UPtr::from_addr(self as *const Defers as u64)
    }

    /// `defer f(args)`: the arguments are already in `env`.
    #[inline]
    pub fn push(&self, f: fn(Env), env: Env) {
        self.list.borrow_mut().push((f, env));
    }

    /// Runs the deferred calls, last deferred first, on the way out of a
    /// normal return.
    ///
    /// Usually nothing here can `recover`, the frame not being the one that
    /// panicked. The exception is Go's: when this frame is itself the call a
    /// panicking frame deferred, a `defer recover()` of its own recovers that
    /// panic, because it runs as if called from this frame (test/recover1.go,
    /// test6). [`crate::panic::Boundary::normal`] works out which case this is.
    ///
    /// If several panic, the last one wins and the rest still run, as in Go.
    #[cfg(feature = "std")]
    pub fn run(&self) {
        self.drain(None);
    }

    /// Runs the deferred calls while this frame's panic is being handled, so
    /// that a deferred call may `recover` it.
    ///
    /// It stops at the call that recovers: Go then resumes the frame, which
    /// runs whatever is left of the list on its normal path.
    #[cfg(feature = "std")]
    pub fn run_panicking(&self, depth: usize) {
        self.drain(Some(depth));
    }

    /// Runs the list, in whichever of the two ways applies, changing from one
    /// to the other as panics come and go.
    ///
    /// A frame that is returning normally can still end up panicking: one of
    /// its own deferred calls may panic, and Go treats the frame as a
    /// panicking one from that moment. What is left of the list runs as a
    /// panicking frame's does, and one of those calls may `recover` the panic
    /// — after which the frame is returning normally again, and the rest of
    /// the list runs that way. That is how `try` in test/recover.go returns a
    /// value its deferred call recovered.
    #[cfg(feature = "std")]
    fn drain(&self, recoverable: Option<usize>) {
        let mut mode = recoverable;
        // A panic one of this frame's own deferred calls raised, which this
        // frame is therefore the one to carry on with if nothing recovers it.
        let mut ours = None;
        loop {
            match self.drain_in(mode) {
                // Either the list is empty or a deferred call recovered.
                None => match ours {
                    // It recovered a panic from this frame's own list, so the
                    // frame is on its normal way out again and what is left of
                    // the list runs that way.
                    Some(depth) if crate::panic::recovered(depth) => {
                        ours = None;
                        mode = None;
                    }
                    _ => break,
                },
                Some(depth) => {
                    mode = Some(depth);
                    ours = Some(depth);
                }
            }
        }
        if let Some(depth) = ours
            && !crate::panic::recovered(depth)
        {
            crate::panic::resume(depth);
        }
    }

    /// Runs the list in one mode, and returns the depth of a panic a deferred
    /// call raised while the frame was returning normally, if that happened:
    /// the rest of the list has to run as a panicking frame's does.
    #[cfg(feature = "std")]
    fn drain_in(&self, mode: Option<usize>) -> Option<usize> {
        // Once popped, the call's environment is no longer reachable through
        // the list, so it is rooted here while it runs. Slot 1 holds the
        // value of a panic caught below.
        let frame = Frame::<2>::new();
        let mut began = None;
        frame.scope(|| {
            let _boundary = match mode {
                // A deferred call of this frame is the one that may recover,
                // and this frame is the one just outside it.
                Some(depth) => crate::panic::Boundary::panicking(frame.addr(), depth),
                None => crate::panic::Boundary::normal(frame.addr()),
            };
            loop {
                let Some((f, env)) = self.list.borrow_mut().pop() else {
                    break;
                };
                frame.set(0, &env);
                if let Err(p) = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| f(env))) {
                    // The panic waits here while the rest of the list runs,
                    // and those calls allocate: nothing else holds its value
                    // until it reaches the panic stack.
                    if let Some(go) = p.downcast_ref::<crate::panic::GoPanic>() {
                        let value = go.value();
                        frame.set(1, &value);
                    }
                    match mode {
                        // Already panicking: the new panic takes the old one's
                        // place, keeping the depth this scope's boundary was
                        // built with, so the rest of the list can recover it.
                        Some(depth) => crate::panic::replace(depth, p),
                        // The frame was on its way out normally. From here it
                        // is a panicking one, which the caller arranges by
                        // running the rest of the list again in the other
                        // mode.
                        None => {
                            began = Some(crate::panic::begin(p));
                            return;
                        }
                    }
                }
                // Go resumes the frame as soon as a deferred call recovers;
                // what is left of the list runs on its normal path.
                if mode.is_some_and(crate::panic::recovered) {
                    break;
                }
            }
        });
        began
    }

    /// Runs the deferred calls, last deferred first (no unwinding here).
    #[cfg(not(feature = "std"))]
    pub fn run(&self) {
        let frame = Frame::<1>::new();
        frame.scope(|| {
            loop {
                let Some((f, env)) = self.list.borrow_mut().pop() else {
                    break;
                };
                frame.set(0, &env);
                f(env);
            }
        });
    }
}
