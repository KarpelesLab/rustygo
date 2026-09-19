//! Places: addressable Go storage, and pointers to it.
//!
//! Go lets any number of pointers reach the same storage and write through all
//! of them (DESIGN §2, "field access and aliasing"). Generated code therefore
//! never holds a Rust reference into Go storage across anything else. Every
//! access copies a value in or out of a [`Place`], and all mutation goes
//! through `Cell`-like [`Slot`]s.
//!
//! Each Go type has a *value* form (a [`GoValue`]) and a *place* form:
//!
//! * a scalar, string or pointer lives in a [`Slot`];
//! * a struct lives in an emitted struct of places, one per field, so `&s.f`
//!   is an ordinary pointer to that field's place;
//! * an array `[N]T` lives in `[P; N]`, where `P` is `T`'s place, so `&a[i]`
//!   works the same way.
//!
//! A [`Ptr`] points at a place.
//!
//! **M0 status:** there is no collector yet, so allocation leaks (roadmap M0,
//! "No collector"), and a place is `'static`. Slots are single-threaded
//! `Cell`s. M1 puts places under the collector and M2 makes slots shareable
//! between threads (DESIGN §13, question 6); the API generated code sees stays
//! the same.

use crate::panic::{RuntimeError, runtime_error};
use crate::value::GoValue;
use alloc::boxed::Box;
use core::cell::Cell;

/// Addressable storage for a value of some Go type.
pub trait Place: 'static {
    /// The value form stored here.
    type Value: GoValue;
    /// Fresh storage holding `v`.
    fn new(v: Self::Value) -> Self;
    /// Copies the value out.
    fn load(&self) -> Self::Value;
    /// Copies `v` in.
    fn store(&self, v: Self::Value);
}

/// The place for a scalar, string, pointer or any other value accessed as a
/// whole.
pub struct Slot<T>(Cell<T>);

impl<T: GoValue> Place for Slot<T> {
    type Value = T;
    #[inline]
    fn new(v: T) -> Self {
        Slot(Cell::new(v))
    }
    #[inline]
    fn load(&self) -> T {
        self.0.get()
    }
    #[inline]
    fn store(&self, v: T) {
        self.0.set(v)
    }
}

impl<P: Place, const N: usize> Place for [P; N] {
    type Value = [P::Value; N];
    fn new(v: Self::Value) -> Self {
        core::array::from_fn(|i| P::new(v[i]))
    }
    fn load(&self) -> Self::Value {
        core::array::from_fn(|i| self[i].load())
    }
    fn store(&self, v: Self::Value) {
        for (p, v) in self.iter().zip(v) {
            p.store(v);
        }
    }
}

/// A Go pointer: nil, or a reference to a place.
pub struct Ptr<P: 'static>(Option<&'static P>);

impl<P> Clone for Ptr<P> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<P> Copy for Ptr<P> {}

impl<P> PartialEq for Ptr<P> {
    fn eq(&self, other: &Self) -> bool {
        match (self.0, other.0) {
            (Some(a), Some(b)) => core::ptr::eq(a, b),
            (None, None) => true,
            _ => false,
        }
    }
}

impl<P> Eq for Ptr<P> {}

impl<P> GoValue for Ptr<P> {
    #[inline]
    fn zero() -> Self {
        Ptr(None)
    }
}

impl<P: Place> Ptr<P> {
    /// `new(T)` or a heap-allocated variable: fresh storage holding `v`.
    pub fn alloc(v: P::Value) -> Self {
        Ptr(Some(Box::leak(Box::new(P::new(v)))))
    }

    /// `*p`.
    #[inline]
    pub fn load(self) -> P::Value {
        self.get().load()
    }

    /// `*p = v`.
    #[inline]
    pub fn store(self, v: P::Value) {
        self.get().store(v)
    }
}

impl<P> Ptr<P> {
    /// The nil pointer.
    pub const NIL: Self = Ptr(None);

    /// A pointer to existing storage, for `&x.f` and `&a[i]`.
    #[inline]
    pub fn from_ref(r: &'static P) -> Self {
        Ptr(Some(r))
    }

    /// The place, or Go's nil-dereference panic.
    #[inline]
    pub fn get(self) -> &'static P {
        match self.0 {
            Some(r) => r,
            None => runtime_error(RuntimeError::NilDeref),
        }
    }

    /// The address, as `println` shows it.
    pub fn addr(self) -> u64 {
        self.0.map_or(0, |r| r as *const P as usize as u64)
    }
}
