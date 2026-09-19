//! Garbage-collection roots: the shadow stack.
//!
//! Generated code keeps every Go pointer held in a Rust local visible to the
//! collector (DESIGN §3). A function with such locals owns a [`Frame`] of
//! root slots on its own stack and runs its body inside [`Frame::scope`],
//! which links the frame into a per-thread chain for exactly the duration of
//! the body, unwinding included. Every assignment to a pointer-holding local
//! also writes its slot.
//!
//! The chain holds raw pointers to frames on the stack. Only `scope` links
//! them, and its guard never leaves this module, so a frame cannot be leaked
//! into the chain (by `mem::forget`, say) and then outlived.
//!
//! A slot holds either a reference itself (a pointer or string, recorded with
//! [`Frame::set`]), or the address of a local that contains references, with
//! its trace function ([`Frame::set_local`]). The second kind covers struct
//! and array values held in locals: the slot tracks the local, so it stays
//! correct as the local is reassigned.

use crate::place::Ptr;
use crate::string::GoStr;
use crate::trace::{Trace, TraceFn, Tracer, trace_fn};
use core::cell::Cell;
use core::marker::PhantomData;

/// A value whose GC-relevant word can be recorded in a root slot.
pub trait Root {
    /// The word the collector will trace (an object address), or 0.
    fn root_word(&self) -> usize;
}

impl<P> Root for Ptr<P> {
    #[inline]
    fn root_word(&self) -> usize {
        self.addr() as usize
    }
}

impl Root for crate::func::Env {
    #[inline]
    fn root_word(&self) -> usize {
        self.addr() as usize
    }
}

impl Root for crate::iface::Iface {
    #[inline]
    fn root_word(&self) -> usize {
        self.data().addr() as usize
    }
}

impl<F: Copy> Root for crate::func::Func<F> {
    #[inline]
    fn root_word(&self) -> usize {
        self.addr() as usize
    }
}

impl<P> Root for crate::slice::Slice<P> {
    #[inline]
    fn root_word(&self) -> usize {
        self.addr() as usize
    }
}

impl Root for GoStr {
    #[inline]
    fn root_word(&self) -> usize {
        self.bytes().as_ptr() as usize
    }
}

/// The header every frame starts with, whatever its slot count. `slots` and
/// `len` let the collector walk a frame without knowing `N`.
#[repr(C)]
struct Header {
    prev: Cell<*const Header>,
    linked: Cell<bool>,
    slots: Cell<*const Slot>,
    len: usize,
}

/// One root: either a reference, or a local to trace in place.
#[derive(Default)]
struct Slot {
    word: Cell<usize>,
    size: Cell<usize>,
    trace: Cell<Option<TraceFn>>,
}

/// One function's root slots.
#[repr(C)]
pub struct Frame<const N: usize> {
    header: Header,
    slots: [Slot; N],
}

rt_global! {
    static TOP: Cell<*const Header> = Cell::new(core::ptr::null());
}

impl<const N: usize> Default for Frame<N> {
    fn default() -> Self {
        Self::new()
    }
}

impl<const N: usize> Frame<N> {
    /// Empty slots. The frame must be entered before use.
    #[inline]
    pub fn new() -> Self {
        Frame {
            header: Header {
                prev: Cell::new(core::ptr::null()),
                linked: Cell::new(false),
                slots: Cell::new(core::ptr::null()),
                len: N,
            },
            slots: core::array::from_fn(|_| Slot::default()),
        }
    }

    /// Runs `body` with this frame linked at the top of the thread's chain.
    /// The borrow keeps the frame in place while it is linked.
    ///
    /// # Panics
    ///
    /// If the frame is already linked (a nested `scope` on the same frame).
    #[inline]
    pub fn scope<R>(&self, body: impl FnOnce() -> R) -> R {
        assert!(!self.header.linked.replace(true), "frame linked twice");
        self.header.slots.set(self.slots.as_ptr());
        let me: *const Header = &self.header;
        TOP.with(|top| self.header.prev.set(top.replace(me)));
        let _guard = FrameGuard {
            header: &self.header,
            _frame: PhantomData,
        };
        body()
    }

    /// Records a reference in slot `i`.
    #[inline]
    pub fn set(&self, i: usize, v: &impl Root) {
        self.slots[i].word.set(v.root_word());
        self.slots[i].trace.set(None);
    }

    /// Records a local that holds references, to be traced where it lives.
    /// The borrow makes the local outlive the frame.
    #[inline]
    pub fn set_local<'a, T: Trace>(&'a self, i: usize, v: &'a T) {
        self.slots[i].word.set(v as *const T as usize);
        self.slots[i].size.set(size_of::<T>());
        self.slots[i].trace.set(Some(trace_fn::<T>()));
    }
}

/// Unlinks a frame when dropped, including during unwinding. Private: only
/// `Frame::scope` creates one, so it cannot be leaked.
struct FrameGuard<'a> {
    header: &'a Header,
    _frame: PhantomData<&'a ()>,
}

impl Drop for FrameGuard<'_> {
    #[inline]
    fn drop(&mut self) {
        let prev = self.header.prev.get();
        TOP.with(|top| top.set(prev));
        self.header.linked.set(false);
    }
}

/// Reports every root on this thread's shadow stack to the collector.
pub(crate) fn trace_roots(t: &mut Tracer<'_>) {
    let mut p = TOP.with(|top| top.get());
    while !p.is_null() {
        // SAFETY: see `depth`.
        let header = unsafe { &*p };
        for i in 0..header.len {
            // SAFETY: `slots` points at this frame's `[Slot; N]`, which is
            // alive while the frame is linked, and `len` is that `N`.
            let slot = unsafe { &*header.slots.get().add(i) };
            let word = slot.word.get();
            match slot.trace.get() {
                None => t.edge(word),
                // SAFETY: the slot holds the address of a live local, and
                // the trace function came from that local's own type.
                Some(trace) if word != 0 => unsafe { trace(word as *const u8, slot.size.get(), t) },
                Some(_) => {}
            }
        }
        p = header.prev.get();
    }
}

/// Number of frames currently linked on this thread (for tests and the
/// collector's own sanity checks).
pub fn depth() -> usize {
    let mut n = 0;
    let mut p = TOP.with(|t| t.get());
    while !p.is_null() {
        n += 1;
        // SAFETY: only `Frame::scope` links a header, and its private guard
        // unlinks it before the frame's borrow ends. Scopes nest, so guards
        // unlink in LIFO order, and every header on the chain is live.
        p = unsafe { (*p).prev.get() };
    }
    n
}

#[cfg(all(test, feature = "std"))]
mod tests {
    use super::*;

    #[test]
    fn frames_link_and_unlink_even_when_unwinding() {
        assert_eq!(depth(), 0);
        let outer = Frame::<2>::new();
        outer.scope(|| {
            outer.set(0, &GoStr::lit(b"x"));
            assert_eq!(depth(), 1);
            let inner = Frame::<1>::new();
            assert_eq!(inner.scope(depth), 2);
            assert_eq!(depth(), 1);
            let r = std::panic::catch_unwind(|| {
                let f = Frame::<1>::new();
                f.scope(|| {
                    assert_eq!(depth(), 2);
                    panic!("unwind through a frame");
                })
            });
            assert!(r.is_err());
            assert_eq!(depth(), 1);
        });
        assert_eq!(depth(), 0);
    }

    #[test]
    fn a_frame_cannot_be_linked_twice() {
        let f = Frame::<1>::new();
        let r =
            std::panic::catch_unwind(core::panic::AssertUnwindSafe(|| f.scope(|| f.scope(|| ()))));
        assert!(r.is_err());
        assert_eq!(depth(), 0);
        f.scope(|| assert_eq!(depth(), 1));
    }
}
