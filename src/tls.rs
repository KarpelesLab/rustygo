//! Where the runtime's state lives: with a thread, or with the program.
//!
//! The distinction is the whole of what makes a scheduler over several
//! threads possible, so every piece of runtime state declares which it is.
//!
//! Some state belongs to whichever goroutine a thread is running — its shadow
//! stack of GC roots, the panics it is handling — and the scheduler swaps it
//! in and out around every context switch (DESIGN §4). That state is per
//! *thread*, and `rt_global!` declares it: reaching it costs a thread-local
//! access and no lock, which matters because the shadow stack is touched by
//! every generated function.
//!
//! The rest belongs to the program rather than to a thread: the goroutine
//! table and its run queue, the heap, the table of who is waiting where, the
//! netpoller's descriptors. Every thread sees the same one, so every access
//! takes a lock, and `rt_shared!` declares that.
//!
//! `no_std` targets have neither threads nor thread-locals. A plain static
//! does for the first kind, and a lock that can never contend does for the
//! second, so it spins instead of asking an operating system that may not be
//! there. This module is the one place that line is drawn.

/// A global holding single-threaded state on `no_std` targets.
#[cfg(not(feature = "std"))]
pub struct SingleThreaded<T>(core::cell::UnsafeCell<T>);

// SAFETY: `no_std` targets run one thread (M5 revisits this with the
// scheduler), so no two accesses can overlap.
#[cfg(not(feature = "std"))]
unsafe impl<T> Sync for SingleThreaded<T> {}

#[cfg(not(feature = "std"))]
impl<T> SingleThreaded<T> {
    /// Wraps the initial value.
    pub const fn new(v: T) -> Self {
        SingleThreaded(core::cell::UnsafeCell::new(v))
    }

    /// Runs `f` on the value, mirroring `LocalKey::with`.
    pub fn with<R>(&self, f: impl FnOnce(&T) -> R) -> R {
        // SAFETY: single-threaded, and `with` hands out only a shared
        // reference, so the interior-mutable cell inside `T` governs any
        // mutation.
        f(unsafe { &*self.0.get() })
    }
}

/// Declares state that belongs to one thread, with a const initializer.
macro_rules! rt_global {
    ($(#[$m:meta])* static $name:ident: $t:ty = $init:expr;) => {
        #[cfg(feature = "std")]
        std::thread_local! {
            $(#[$m])*
            static $name: $t = const { $init };
        }

        $(#[$m])*
        #[cfg(not(feature = "std"))]
        static $name: $crate::tls::SingleThreaded<$t> =
            $crate::tls::SingleThreaded::new($init);
    };
}

/// Declares state the whole program shares, behind a lock.
macro_rules! rt_shared {
    ($(#[$m:meta])* static $name:ident: $t:ty = $init:expr;) => {
        $(#[$m])*
        static $name: $crate::tls::Lock<$t> = $crate::tls::Lock::new($init);
    };
}

/// A lock around state the whole program shares.
///
/// The interface is a closure rather than a guard, which is not a matter of
/// taste: a thread that switches goroutines while holding one of these hands
/// the next goroutine a lock it cannot take. Making the critical section a
/// closure body puts the end of it where it can be seen.
pub struct Lock<T> {
    #[cfg(feature = "std")]
    inner: std::sync::Mutex<T>,
    #[cfg(not(feature = "std"))]
    held: core::sync::atomic::AtomicBool,
    #[cfg(not(feature = "std"))]
    value: core::cell::UnsafeCell<T>,
    /// Which thread holds it, for the reentrancy check below. Zero when free.
    #[cfg(debug_assertions)]
    owner: core::sync::atomic::AtomicUsize,
}

// SAFETY: the lock is what serializes access, so `T` only has to be sendable
// between the threads that take turns at it.
#[cfg(not(feature = "std"))]
unsafe impl<T: Send> Sync for Lock<T> {}

impl<T> Lock<T> {
    /// Wraps the initial value. Const, so the state behind it needs no
    /// lazy-init check on paths as hot as allocation.
    pub const fn new(v: T) -> Lock<T> {
        Lock {
            #[cfg(feature = "std")]
            inner: std::sync::Mutex::new(v),
            #[cfg(not(feature = "std"))]
            held: core::sync::atomic::AtomicBool::new(false),
            #[cfg(not(feature = "std"))]
            value: core::cell::UnsafeCell::new(v),
            #[cfg(debug_assertions)]
            owner: core::sync::atomic::AtomicUsize::new(0),
        }
    }

    /// Runs `body` with the state locked.
    #[cfg(feature = "std")]
    pub fn with<R>(&self, body: impl FnOnce(&mut T) -> R) -> R {
        self.not_mine_already();
        // Poisoning is deliberately ignored. A critical section here writes a
        // few words and does not call back into Go, so the only way one
        // unwinds is a bug in the runtime — and the report that matters is
        // that bug's, not a lock error raised by every thread that comes
        // along afterwards.
        let mut guard = self.inner.lock().unwrap_or_else(|e| e.into_inner());
        // Declared after the guard, so it is cleared while the lock is still
        // held: clearing it afterwards could erase the next thread's claim.
        let _owner = self.claim();
        body(&mut guard)
    }

    /// Runs `body` with the state locked.
    #[cfg(not(feature = "std"))]
    pub fn with<R>(&self, body: impl FnOnce(&mut T) -> R) -> R {
        use core::sync::atomic::Ordering;
        self.not_mine_already();
        // Never contended: `no_std` is single-threaded until M5 gives it a
        // scheduler. Spinning is therefore the honest primitive — it asks
        // nothing of an operating system that may not be there — and if those
        // targets ever do get threads, a critical section long enough to
        // matter would want a futex instead.
        while self.held.swap(true, Ordering::Acquire) {
            core::hint::spin_loop();
        }
        let _owner = self.claim();
        let _release = Release(&self.held);
        // SAFETY: this thread holds the lock, so no other reference to the
        // value exists, and `_release` gives it back even if `body` unwinds.
        body(unsafe { &mut *self.value.get() })
    }

    /// Panics if this thread is the one already holding the lock.
    ///
    /// Taking a lock twice would not report itself the way a doubly borrowed
    /// `RefCell` does: it would hang, and a hang in the middle of a
    /// differential run says nothing about where it came from. Reading the
    /// owner *before* waiting is what makes the check useful, since the whole
    /// case it catches is a wait that will never end. Debug builds only.
    #[inline]
    fn not_mine_already(&self) {
        #[cfg(debug_assertions)]
        assert_ne!(
            self.owner.load(core::sync::atomic::Ordering::Relaxed),
            thread_id(),
            "a runtime lock was taken twice by the same thread"
        );
    }

    /// Records this thread as the owner until the result is dropped.
    #[inline]
    fn claim(&self) -> Owner<'_, T> {
        #[cfg(debug_assertions)]
        self.owner
            .store(thread_id(), core::sync::atomic::Ordering::Relaxed);
        Owner(self)
    }
}

/// Clears the owner recorded by [`Lock::claim`], unwinding included.
struct Owner<'a, T>(#[allow(dead_code)] &'a Lock<T>);

impl<T> Drop for Owner<'_, T> {
    #[inline]
    fn drop(&mut self) {
        #[cfg(debug_assertions)]
        self.0.owner.store(0, core::sync::atomic::Ordering::Relaxed);
    }
}

/// Gives a spin lock back, unwinding included.
#[cfg(not(feature = "std"))]
struct Release<'a>(&'a core::sync::atomic::AtomicBool);

#[cfg(not(feature = "std"))]
impl Drop for Release<'_> {
    #[inline]
    fn drop(&mut self) {
        self.0.store(false, core::sync::atomic::Ordering::Release);
    }
}

/// This thread's number: 1 for the first thread to ask, one more for each
/// thread after it.
///
/// Only for telling one thread from another, which at present only the
/// reentrancy check above wants — so in a release build nothing calls it, and
/// saying so is cheaper than making the check pay for itself. It is not an
/// index into anything, so the number of a thread that has exited is simply
/// never reused.
#[cfg(feature = "std")]
#[cfg_attr(not(debug_assertions), allow(dead_code))]
pub fn thread_id() -> usize {
    use core::sync::atomic::{AtomicUsize, Ordering};
    static NEXT: AtomicUsize = AtomicUsize::new(1);
    rt_global! {
        static ID: core::cell::Cell<usize> = core::cell::Cell::new(0);
    }
    ID.with(|id| {
        if id.get() == 0 {
            id.set(NEXT.fetch_add(1, Ordering::Relaxed));
        }
        id.get()
    })
}

/// The only thread there is, on a target without threads.
#[cfg(not(feature = "std"))]
pub fn thread_id() -> usize {
    1
}

#[cfg(all(test, feature = "std"))]
mod tests {
    use super::*;

    rt_shared! {
        static COUNT: usize = 0;
    }

    #[test]
    fn shared_state_is_shared_between_threads() {
        // Four threads, a thousand increments each: the count is exact only
        // if every one of them was serialized by the lock.
        let mut hands = alloc::vec::Vec::new();
        for _ in 0..4 {
            hands.push(std::thread::spawn(|| {
                for _ in 0..1000 {
                    COUNT.with(|c| *c += 1);
                }
            }));
        }
        for h in hands {
            h.join().expect("thread");
        }
        assert_eq!(COUNT.with(|c| *c), 4000);
    }

    #[test]
    fn threads_are_numbered_apart() {
        let mine = thread_id();
        assert_eq!(mine, thread_id(), "stable within a thread");
        let theirs = std::thread::spawn(thread_id).join().expect("thread");
        assert_ne!(mine, theirs);
    }

    #[test]
    #[should_panic(expected = "taken twice")]
    #[cfg(debug_assertions)]
    fn a_lock_taken_twice_is_reported_rather_than_hanging() {
        COUNT.with(|_| COUNT.with(|_| ()));
    }
}
