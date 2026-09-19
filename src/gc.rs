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
//! **M0 status:** there is no collector yet, so nothing walks the chain. The
//! emitter produces frames only when `RUSTYGO_SHADOWSTACK=1`, to measure their
//! cost against the same benchmarks (DESIGN §13, question 1). The collector
//! arrives in M1 and walks this chain as its root set.

use crate::place::Ptr;
use crate::string::GoStr;
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

impl Root for GoStr {
    #[inline]
    fn root_word(&self) -> usize {
        self.bytes().as_ptr() as usize
    }
}

/// The header every frame starts with, whatever its slot count.
#[repr(C)]
struct Header {
    prev: Cell<*const Header>,
    linked: Cell<bool>,
    len: usize,
}

/// One function's root slots.
#[repr(C)]
pub struct Frame<const N: usize> {
    header: Header,
    slots: [Cell<usize>; N],
}

std::thread_local! {
    static TOP: Cell<*const Header> = const { Cell::new(core::ptr::null()) };
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
                len: N,
            },
            slots: [const { Cell::new(0) }; N],
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
        let me: *const Header = &self.header;
        TOP.with(|top| self.header.prev.set(top.replace(me)));
        let _guard = FrameGuard {
            header: &self.header,
            _frame: PhantomData,
        };
        body()
    }

    /// Records the root in slot `i`.
    #[inline]
    pub fn set(&self, i: usize, v: &impl Root) {
        self.slots[i].set(v.root_word());
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn frames_link_and_unlink_even_when_unwinding() {
        assert_eq!(depth(), 0);
        let outer = Frame::<2>::new();
        outer.scope(|| {
            outer.set(0, &GoStr::lit(b"x"));
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
