//! Go's `unsafe.Pointer` and the `unsafe` builtins.
//!
//! Generated code is safe Rust except where the Go source itself imports
//! `unsafe` (DESIGN §7). There, a conversion from `unsafe.Pointer` back to a
//! typed pointer reinterprets memory exactly as gc does, which is why
//! generated structs use C layout (Go's layout) and a [`crate::place::Slot`]
//! is transparent over its value: a place has the bytes gc would give it.
//!
//! The collector treats an `unsafe.Pointer` like any pointer: it may point
//! into the middle of an object, and marking resolves it (DESIGN §3). A
//! `uintptr` is only an integer, as Go's rules say, and keeps nothing alive.

use crate::place::Ptr;
use crate::slice::Slice;
use crate::string::GoStr;
use crate::trace::{Trace, Tracer};
use crate::value::GoValue;

/// A Go `unsafe.Pointer`: an address, typed as nothing.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
#[repr(transparent)]
pub struct UPtr(usize);

impl GoValue for UPtr {
    #[inline]
    fn zero() -> Self {
        UPtr(0)
    }
}

impl Trace for UPtr {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        t.edge(self.0);
    }
}

impl UPtr {
    /// `unsafe.Pointer(p)` for a typed pointer.
    #[inline]
    pub fn from_ptr<P>(p: Ptr<P>) -> Self {
        UPtr(p.addr() as usize)
    }

    /// `unsafe.Pointer(u)` for a `uintptr`.
    #[inline]
    pub fn from_addr(u: u64) -> Self {
        UPtr(u as usize)
    }

    /// `uintptr(p)`.
    #[inline]
    pub fn addr(self) -> u64 {
        self.0 as u64
    }

    /// `(*T)(p)`: the same address as a pointer to a place of type `P`.
    ///
    /// # Safety
    ///
    /// This is Go's own `unsafe` contract: the memory at the address must be
    /// valid as a `P`, as it must be under gc.
    #[inline]
    pub unsafe fn to_ptr<P>(self) -> Ptr<P> {
        // SAFETY: the caller's contract.
        unsafe { Ptr::from_addr(self.0) }
    }

    /// `unsafe.Add(p, n)`.
    #[inline]
    pub fn offset(self, n: i64) -> Self {
        UPtr(self.0.wrapping_add(n as usize))
    }
}

impl<P> Slice<P> {
    /// `unsafe.Slice(ptr, len)`: a slice over `len` places starting at `ptr`.
    ///
    /// # Safety
    ///
    /// Go's contract: `ptr` must point at `len` consecutive valid places.
    #[inline]
    pub unsafe fn from_raw(ptr: Ptr<P>, len: i64) -> Self {
        if len < 0 {
            crate::panic::runtime_error(crate::panic::RuntimeError::UnsafeSliceLen);
        }
        if ptr.addr() == 0 && len > 0 {
            crate::panic::runtime_error(crate::panic::RuntimeError::UnsafeSliceNil);
        }
        // SAFETY: the caller's contract.
        unsafe { Slice::from_parts(ptr.addr() as usize as *mut P, len as usize, len as usize) }
    }

    /// `unsafe.SliceData(s)`: the address of the first element, or nil.
    #[inline]
    pub fn data_ptr(self) -> Ptr<P> {
        // SAFETY: the slice's own data pointer, or null for a nil slice.
        unsafe { Ptr::from_addr(self.addr() as usize) }
    }
}

impl GoStr {
    /// `unsafe.String(ptr, len)`: a string over `len` bytes starting at
    /// `ptr`, which Go forbids modifying afterwards.
    ///
    /// # Safety
    ///
    /// Go's contract: `ptr` must point at `len` readable bytes.
    #[inline]
    pub unsafe fn from_raw(ptr: Ptr<crate::place::Slot<u8>>, len: i64) -> Self {
        if len < 0 {
            crate::panic::runtime_error(crate::panic::RuntimeError::UnsafeStringLen);
        }
        // SAFETY: the caller's contract; a `Slot<u8>` is a byte.
        unsafe { GoStr::from_parts(ptr.addr() as usize as *const u8, len as usize) }
    }

    /// `unsafe.StringData(s)`: the address of the bytes.
    #[inline]
    pub fn data_ptr(self) -> Ptr<crate::place::Slot<u8>> {
        // SAFETY: the string's own bytes (or a literal); Go forbids writes
        // through this pointer, as the caller's contract.
        unsafe { Ptr::from_addr(self.bytes().as_ptr() as usize) }
    }
}
