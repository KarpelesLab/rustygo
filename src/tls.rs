//! Per-thread runtime state.
//!
//! The heap, the shadow stack and the panic in flight are per-goroutine, and
//! M1 runs one goroutine on one thread, so they live in thread-locals. On
//! `no_std` targets there are no thread-locals; those targets are
//! single-threaded until M5 gives them a scheduler, so a plain static does
//! the same job.

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

/// Declares per-thread runtime state, with a const initializer.
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
