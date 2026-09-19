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
//! Allocation goes through the collector ([`crate::heap`]). A [`Ptr`] is one
//! word: an interior pointer such as `&s.f` points straight at the field's
//! place, and marking resolves it back to its object. No reference into the
//! heap is ever handed to generated code — [`Ptr::project`] takes a closure,
//! so a borrow cannot outlive the call.
//!
//! **M1 status:** slots are single-threaded `Cell`s. M2 makes them shareable
//! between threads (DESIGN §13, question 6); the API generated code sees stays
//! the same.

use crate::heap;
use crate::panic::{RuntimeError, runtime_error};
use crate::trace::{Trace, Tracer};
use crate::value::GoValue;
use core::cell::Cell;
use core::ptr::NonNull;

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

/// A Go pointer: nil, or the address of a place.
pub struct Ptr<P: 'static>(Option<NonNull<P>>);

impl<P> Clone for Ptr<P> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<P> Copy for Ptr<P> {}

impl<P> PartialEq for Ptr<P> {
    fn eq(&self, other: &Self) -> bool {
        self.addr() == other.addr()
    }
}

impl<P> Eq for Ptr<P> {}

impl<P> GoValue for Ptr<P> {
    #[inline]
    fn zero() -> Self {
        Ptr(None)
    }
}

impl<P: Place + Trace> Ptr<P> {
    /// `new(T)` or a heap-allocated variable: fresh storage holding `v`.
    ///
    /// This is a safe point: it may collect before it allocates, so every
    /// reference the caller still needs must be rooted (DESIGN §3).
    pub fn alloc(v: P::Value) -> Self {
        Ptr(Some(heap::allocate(P::new(v))))
    }
}

impl<P: Place> Ptr<P> {
    /// `*p`.
    #[inline]
    pub fn load(self) -> P::Value {
        self.place().load()
    }

    /// `*p = v`.
    #[inline]
    pub fn store(self, v: P::Value) {
        self.place().store(v)
    }
}

impl<P> Ptr<P> {
    /// The nil pointer.
    pub const NIL: Self = Ptr(None);

    /// The nil pointer, as an inherent method (`GoValue::zero` needs the
    /// trait in scope).
    #[inline]
    pub fn zero() -> Self {
        Ptr(None)
    }

    /// A pointer to a place that lives for the rest of the program: a
    /// package-level variable's storage.
    #[inline]
    pub fn to_global(p: &'static P) -> Self {
        Ptr(Some(NonNull::from(p)))
    }

    /// `&x.f` and `&a[i]`: the pointer to a place inside this one.
    ///
    /// The closure sees the place only for the duration of the call, so no
    /// borrow into the heap escapes. Panics like Go if this pointer is nil.
    #[inline]
    pub fn project<Q>(self, f: impl FnOnce(&P) -> &Q) -> Ptr<Q> {
        Ptr(Some(NonNull::from(f(self.place()))))
    }

    /// The place, or Go's nil-dereference panic.
    #[inline]
    fn place(self) -> &'static P {
        match self.0 {
            // SAFETY: a non-nil `Ptr` holds the address of a live place: it
            // came from `alloc`, from `project` on a live place, or from a
            // registered global. Generated code keeps every pointer it still
            // needs rooted, so the collector cannot free it (DESIGN §3).
            Some(p) => unsafe { p.as_ref() },
            None => runtime_error(RuntimeError::NilDeref),
        }
    }

    /// The address, as `println` shows it. Zero for nil.
    #[inline]
    pub fn addr(self) -> u64 {
        self.0.map_or(0, |p| p.as_ptr() as usize as u64)
    }
}

impl<P> Trace for Ptr<P> {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        t.edge(self.addr() as usize);
    }
}

impl<T: Trace + GoValue> Trace for Slot<T> {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        self.load().trace(t);
    }
}
