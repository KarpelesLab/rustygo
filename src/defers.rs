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
use crate::trace::{Trace, Tracer};
use alloc::vec::Vec;
use core::cell::RefCell;

/// The deferred calls of one function, innermost last.
#[derive(Default)]
pub struct Defers {
    list: RefCell<Vec<(fn(Env), Env)>>,
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

    /// Runs the deferred calls, last deferred first.
    ///
    /// If several panic, the last one wins and the rest still run, as in Go.
    #[cfg(feature = "std")]
    pub fn run(&self) {
        let mut pending: Option<alloc::boxed::Box<dyn core::any::Any + Send>> = None;
        loop {
            let Some((f, env)) = self.list.borrow_mut().pop() else {
                break;
            };
            if let Err(p) = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| f(env))) {
                pending = Some(p);
            }
        }
        if let Some(p) = pending {
            std::panic::resume_unwind(p);
        }
    }

    /// Runs the deferred calls, last deferred first (no unwinding here).
    #[cfg(not(feature = "std"))]
    pub fn run(&self) {
        loop {
            let Some((f, env)) = self.list.borrow_mut().pop() else {
                break;
            };
            f(env);
        }
    }
}
