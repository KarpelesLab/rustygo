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

    #[cfg(feature = "std")]
    fn drain(&self, recoverable: Option<usize>) {
        let mut pending: Option<alloc::boxed::Box<dyn core::any::Any + Send>> = None;
        // Once popped, the call's environment is no longer reachable through
        // the list, so it is rooted here while it runs. Slot 1 holds the
        // value of a panic caught below.
        let frame = Frame::<2>::new();
        frame.scope(|| {
            let _boundary = match recoverable {
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
                    pending = Some(p);
                }
                // Go resumes the frame as soon as a deferred call recovers;
                // what is left of the list runs on its normal path.
                if recoverable.is_some_and(crate::panic::recovered) {
                    break;
                }
            }
        });
        if let Some(p) = pending {
            std::panic::resume_unwind(p);
        }
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
