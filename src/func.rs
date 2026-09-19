//! Go function values and closures.
//!
//! A func value is a code pointer plus an environment: the variables the
//! closure captured, held in one heap-allocated place struct that the emitter
//! generates (DESIGN §2). Go captures by reference, so those fields are
//! usually pointers to the captured variables' own places, and two closures
//! over the same variable see each other's writes.
//!
//! The code pointer's Rust type carries the Go signature, with the
//! environment as a leading parameter: `func(int) string` becomes
//! `Func<fn(Env, i64) -> GoStr>`. A plain function used as a value gets a
//! generated shim with the same shape and an empty environment.

use crate::panic::{RuntimeError, runtime_error};
use crate::place::Ptr;
use crate::trace::{Trace, Tracer};
use crate::value::GoValue;

/// A closure's captured environment: the address of its place struct, or 0.
///
/// The emitter pairs each code pointer with the environment type it expects,
/// which is what makes [`Env::cast`] safe in generated code.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Env(usize);

impl Env {
    /// No environment: a plain function, or a nil func value.
    pub const NONE: Env = Env(0);

    /// The environment holding a closure's captured variables.
    #[inline]
    pub fn of<P>(p: Ptr<P>) -> Env {
        Env(p.addr() as usize)
    }

    /// Recovers the environment as the place type the closure's body
    /// expects, which is the type its `MakeClosure` allocated.
    #[inline]
    pub fn cast<P>(self) -> Ptr<P> {
        // SAFETY (invariant): generated code only casts an environment back
        // to the type the matching closure allocated, and the func value
        // keeps it alive.
        unsafe { Ptr::from_addr(self.0) }
    }
}

impl Trace for Env {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        t.edge(self.0);
    }
}

/// A Go func value: `F` is `fn(Env, ..) -> ..` for its signature.
pub struct Func<F: Copy + 'static> {
    code: Option<F>,
    env: Env,
}

impl<F: Copy> Clone for Func<F> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<F: Copy> Copy for Func<F> {}

impl<F: Copy> GoValue for Func<F> {
    #[inline]
    fn zero() -> Self {
        Func {
            code: None,
            env: Env::NONE,
        }
    }
}

impl<F: Copy> Trace for Func<F> {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        self.env.trace(t);
    }
}

impl<F: Copy> PartialEq for Func<F> {
    /// Go only compares func values against nil.
    fn eq(&self, other: &Self) -> bool {
        self.is_nil() == other.is_nil()
    }
}

impl<F: Copy> Func<F> {
    /// A func value over `code`, closing over `env`.
    #[inline]
    pub fn new(code: F, env: Env) -> Self {
        Func {
            code: Some(code),
            env,
        }
    }

    /// `f == nil`.
    #[inline]
    pub fn is_nil(self) -> bool {
        self.code.is_none()
    }

    /// The code to call, or Go's panic for calling a nil func value.
    #[inline]
    pub fn code(self) -> F {
        match self.code {
            Some(f) => f,
            None => runtime_error(RuntimeError::NilDeref),
        }
    }

    /// The environment to pass it.
    #[inline]
    pub fn env(self) -> Env {
        self.env
    }

    /// The address `println` shows.
    pub fn addr(self) -> u64 {
        self.env.0 as u64
    }
}
