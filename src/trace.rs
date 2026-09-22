//! Tracing: how the collector walks a value's references.
//!
//! Generated code implements [`Trace`] for every Go type it emits (a struct's
//! value form and its place form), calling [`Tracer::edge`] for each word
//! that may point into the heap. The collector never interprets memory
//! itself — that is what makes the collector precise.

use crate::heap::Heap;
use alloc::vec::Vec;

/// Walks the references in a value.
pub trait Trace: 'static {
    /// Reports every possible heap reference in `self` to `t`.
    fn trace(&self, t: &mut Tracer<'_>);
}

/// Erased [`Trace::trace`], stored per object in the heap table. The second
/// argument is the object's payload size, which is how an array of `T` traces
/// every element without storing a count.
pub type TraceFn = unsafe fn(*const u8, usize, &mut Tracer<'_>);

/// The erased trace function for one `T`.
pub fn trace_fn<T: Trace>() -> TraceFn {
    |p, _, t| {
        // SAFETY: the caller passes the address of a live `T`, which is what
        // the heap table records alongside this function.
        let v = unsafe { &*(p as *const T) };
        v.trace(t);
    }
}

/// The erased trace function for a contiguous run of `T`, sized by the
/// object's payload.
pub fn trace_array_fn<T: Trace>() -> TraceFn {
    |p, bytes, t| {
        // A zero-sized element holds no references, and there is no count of
        // them to compute: `[]struct{}` is a real Go type, and its backing
        // array is zero bytes however long the slice is.
        if size_of::<T>() == 0 {
            return;
        }
        let n = bytes / size_of::<T>();
        // SAFETY: the object holds `n` initialized, contiguous `T`s: that is
        // how `heap::allocate_array` laid it out.
        let vs = unsafe { core::slice::from_raw_parts(p as *const T, n) };
        for v in vs {
            v.trace(t);
        }
    }
}

/// The collector's mark phase, as generated code sees it.
pub struct Tracer<'h> {
    pub(crate) heap: &'h mut Heap,
    pub(crate) work: Vec<usize>,
}

macro_rules! no_refs {
    ($($t:ty),*) => {$(
        impl Trace for $t {
            #[inline]
            fn trace(&self, _: &mut Tracer<'_>) {}
        }
    )*};
}

no_refs!(bool, i8, i16, i32, i64, u8, u16, u32, u64, f32, f64, ());

impl<T: Trace, const N: usize> Trace for [T; N] {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        for v in self {
            v.trace(t);
        }
    }
}

macro_rules! tuple_trace {
    ($($t:ident $i:tt),+) => {
        impl<$($t: Trace),+> Trace for ($($t,)+) {
            #[inline]
            fn trace(&self, t: &mut Tracer<'_>) { $(self.$i.trace(t);)+ }
        }
    };
}

tuple_trace!(A 0);
tuple_trace!(A 0, B 1);
tuple_trace!(A 0, B 1, C 2);
tuple_trace!(A 0, B 1, C 2, D 3);
tuple_trace!(A 0, B 1, C 2, D 3, E 4);
tuple_trace!(A 0, B 1, C 2, D 3, E 4, F 5);
tuple_trace!(A 0, B 1, C 2, D 3, E 4, F 5, G 6);
tuple_trace!(A 0, B 1, C 2, D 3, E 4, F 5, G 6, H 7);
tuple_trace!(A 0, B 1, C 2, D 3, E 4, F 5, G 6, H 7, I 8);
tuple_trace!(A 0, B 1, C 2, D 3, E 4, F 5, G 6, H 7, I 8, J 9);
tuple_trace!(A 0, B 1, C 2, D 3, E 4, F 5, G 6, H 7, I 8, J 9, K 10);
tuple_trace!(A 0, B 1, C 2, D 3, E 4, F 5, G 6, H 7, I 8, J 9, K 10, L 11);
