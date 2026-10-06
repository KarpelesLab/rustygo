//! `sync/atomic`, which gc writes in assembly.
//!
//! The package's types — `atomic.Int64`, `atomic.Value`, `atomic.Pointer[T]` —
//! are gc's own Go and are built out of nothing but these functions. So is
//! `sync.Mutex`, and `sync.WaitGroup`, and `sync.Once`. While there was one
//! thread the runtime could answer them with plain Go loads and stores; the
//! moment there are two, a mutex unlocks itself and the program says
//! `sync: unlock of unlocked mutex`.
//!
//! Every operation takes the address of a word the Go side owns and is
//! sequentially consistent. Go's memory model promises that of its atomics with
//! respect to one another and the standard library leans on it, so an ordering
//! weaker than Go's would show up first in `sync`, as a hang.
//!
//! A Go pointer is one word in rustygo — a nil-checked address and nothing more
//! — so `atomic.Pointer[T]` and `CompareAndSwapPointer` need no double-width
//! compare-and-swap. That was DESIGN §13 question 7, and the answer is that the
//! question does not arise.
//!
//! The address is an invariant rather than a safety contract, the way
//! [`crate::sched::sem_acquire`]'s is: every caller is a `sync/atomic` function
//! the emitter resolved, so the address is a Go variable of exactly this width,
//! and Go's own alignment rules — which rustygo keeps (DESIGN §7) — are what
//! make the wider ones aligned.

use core::sync::atomic::{AtomicI32, AtomicI64, AtomicU32, AtomicU64, AtomicUsize, Ordering};

/// What every operation here uses, because it is what Go promises.
const ORDER: Ordering = Ordering::SeqCst;

macro_rules! ops {
    (
        $t:ty, $a:ty, $name:literal,
        $load:ident, $store:ident, $swap:ident, $cas:ident,
        $add:ident, $and:ident, $or:ident
    ) => {
        #[doc = concat!("`atomic.Load", $name, "`.")]
        #[inline]
        pub fn $load(addr: usize) -> $t {
            word::<$a>(addr).load(ORDER)
        }

        #[doc = concat!("`atomic.Store", $name, "`.")]
        #[inline]
        pub fn $store(addr: usize, v: $t) {
            word::<$a>(addr).store(v, ORDER)
        }

        #[doc = concat!("`atomic.Swap", $name, "`: the value that was there.")]
        #[inline]
        pub fn $swap(addr: usize, v: $t) -> $t {
            word::<$a>(addr).swap(v, ORDER)
        }

        #[doc = concat!("`atomic.CompareAndSwap", $name, "`: whether it was `old`, and now `new`.")]
        #[inline]
        pub fn $cas(addr: usize, old: $t, new: $t) -> bool {
            word::<$a>(addr)
                .compare_exchange(old, new, ORDER, ORDER)
                .is_ok()
        }

        #[doc = concat!("`atomic.Add", $name, "`, which returns the *new* value, as Go's does, and wraps, as Go's does.")]
        #[inline]
        pub fn $add(addr: usize, d: $t) -> $t {
            word::<$a>(addr).fetch_add(d, ORDER).wrapping_add(d)
        }

        #[doc = concat!("`atomic.And", $name, "`, which returns the *old* value, as Go's does.")]
        #[inline]
        pub fn $and(addr: usize, mask: $t) -> $t {
            word::<$a>(addr).fetch_and(mask, ORDER)
        }

        #[doc = concat!("`atomic.Or", $name, "`, which returns the old value.")]
        #[inline]
        pub fn $or(addr: usize, mask: $t) -> $t {
            word::<$a>(addr).fetch_or(mask, ORDER)
        }
    };
}

/// The word at an address a `sync/atomic` function was given.
#[inline]
fn word<A>(addr: usize) -> &'static A {
    // SAFETY (invariant): the module comment. The address is a Go variable of
    // this width, alive for the call and for as long as any Go code can reach
    // it, which is longer.
    unsafe { &*(addr as *const A) }
}

ops!(
    i32, AtomicI32, "Int32", load_i32, store_i32, swap_i32, cas_i32, add_i32, and_i32, or_i32
);
ops!(
    u32, AtomicU32, "Uint32", load_u32, store_u32, swap_u32, cas_u32, add_u32, and_u32, or_u32
);
ops!(
    i64, AtomicI64, "Int64", load_i64, store_i64, swap_i64, cas_i64, add_i64, and_i64, or_i64
);
ops!(
    u64, AtomicU64, "Uint64", load_u64, store_u64, swap_u64, cas_u64, add_u64, and_u64, or_u64
);
// `uintptr` and `unsafe.Pointer` are the same word in rustygo, and both are a
// pointer's width, so one set of operations answers for both.
ops!(
    usize,
    AtomicUsize,
    "Uintptr",
    load_usize,
    store_usize,
    swap_usize,
    cas_usize,
    add_usize,
    and_usize,
    or_usize
);

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_word_is_read_and_written_as_one() {
        let v: i64 = 7;
        let addr = &raw const v as usize;
        assert_eq!(load_i64(addr), 7);
        assert_eq!(add_i64(addr, 3), 10, "Add gives back the new value");
        assert_eq!(swap_i64(addr, 1), 10, "Swap gives back the old one");
        assert!(cas_i64(addr, 1, 2));
        assert!(!cas_i64(addr, 1, 3), "no longer 1");
        assert_eq!(and_i64(addr, 0), 2, "And gives back the old value");
        assert_eq!(load_i64(addr), 0);
        or_i64(addr, 0b101);
        assert_eq!(load_i64(addr), 0b101);
        store_i64(addr, -1);
        assert_eq!(load_i64(addr), -1);
        assert_eq!(add_i64(addr, 1), 0, "it wraps where Go's wraps");
    }

    /// The whole point: four threads adding to one word, and the total is right
    /// only if no two of them lost each other's write.
    #[test]
    #[cfg(feature = "std")]
    fn increments_from_several_threads_are_all_there() {
        let v = alloc::boxed::Box::new(0i64);
        let addr = &raw const *v as usize;
        let mut hands = alloc::vec::Vec::new();
        for _ in 0..4 {
            hands.push(std::thread::spawn(move || {
                for _ in 0..10_000 {
                    add_i64(addr, 1);
                }
            }));
        }
        for h in hands {
            h.join().expect("thread");
        }
        assert_eq!(load_i64(addr), 40_000);
    }
}
