//! Go slices: a view of a run of places in a backing array.
//!
//! `Slice<P>` is a pointer, a length and a capacity, over places of type `P`
//! (DESIGN §2), so `&s[i]` is an ordinary pointer to the element's place and
//! two slices can share and alias one array exactly as in Go. The pointer may
//! point into the middle of that array after a reslice; the collector
//! resolves it back to the whole object, so the array stays alive and is
//! traced in full.

use crate::heap;
use crate::ops;
use crate::place::{Place, Ptr};
use crate::trace::{Trace, Tracer};
use crate::value::GoValue;

/// A Go slice `[]T`, over places of type `P`.
pub struct Slice<P: 'static> {
    ptr: *mut P,
    len: usize,
    cap: usize,
}

impl<P> Clone for Slice<P> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<P> Copy for Slice<P> {}

impl<P> GoValue for Slice<P> {
    #[inline]
    fn zero() -> Self {
        Slice {
            ptr: core::ptr::null_mut(),
            len: 0,
            cap: 0,
        }
    }
}

impl<P> Trace for Slice<P> {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        t.edge(self.ptr as usize);
    }
}

impl<P> Slice<P> {
    /// `len(s)`.
    #[inline]
    pub fn len(self) -> i64 {
        self.len as i64
    }

    /// `cap(s)`.
    #[inline]
    pub fn cap(self) -> i64 {
        self.cap as i64
    }

    /// `len(s) == 0`.
    #[inline]
    pub fn is_empty(self) -> bool {
        self.len == 0
    }

    /// `s == nil`. A slice with no backing array is nil, even at length 0.
    #[inline]
    pub fn is_nil(self) -> bool {
        self.ptr.is_null()
    }

    /// The address `println` shows, and what the collector resolves.
    #[inline]
    pub fn addr(self) -> u64 {
        self.ptr as usize as u64
    }

    /// `&s[i]`, bounds-checked against the length.
    #[inline]
    pub fn at(self, i: i64) -> Ptr<P> {
        self.elem(ops::index(i, self.len))
    }

    /// `&s[i]` for an unsigned index.
    #[inline]
    pub fn at_u(self, i: u64) -> Ptr<P> {
        self.elem(ops::index_u(i, self.len))
    }

    #[inline]
    fn elem(self, i: usize) -> Ptr<P> {
        // SAFETY: `i` is inside the slice, which is inside the backing array.
        Ptr::to_place(unsafe { &*self.ptr.add(i) })
    }

    /// `s[lo:hi]` and `s[lo:hi:max]`: a view of the same array. Missing `hi`
    /// means `len(s)`, missing `max` means `cap(s)`, and slicing may reach
    /// past the length up to the capacity.
    pub fn slice(self, lo: i64, hi: Option<i64>, max: Option<i64>) -> Slice<P> {
        let (lo, hi, max) = ops::slice3_bounds(lo, hi, max, self.len, self.cap);
        Slice {
            // SAFETY: `lo <= cap`, so this is inside the array or one past
            // its end.
            ptr: unsafe { self.ptr.add(lo) },
            len: hi - lo,
            cap: max - lo,
        }
    }
}

impl<P: Place + Trace> Slice<P> {
    /// `make([]T, len, cap)`. A safe point: it allocates.
    pub fn make(len: i64, cap: i64) -> Slice<P> {
        let (len, cap) = ops::make_bounds(len, cap);
        Slice {
            ptr: heap::allocate_array::<P>(cap).as_ptr(),
            len,
            cap,
        }
    }

    /// The backing array of an array value or composite literal: `n` places,
    /// all zero, viewed at full length.
    pub fn with_len(n: i64) -> Slice<P> {
        Self::make(n, n)
    }

    /// `append(s, v)` for one element, which is how the emitter spells any
    /// append. Growing allocates, so this is a safe point.
    pub fn append(self, v: P::Value) -> Slice<P> {
        let mut s = self;
        if s.len == s.cap {
            s = s.grow(1);
        }
        // SAFETY: `len < cap` now, so this place is inside the array.
        unsafe { &*s.ptr.add(s.len) }.store(v);
        Slice {
            ptr: s.ptr,
            len: s.len + 1,
            cap: s.cap,
        }
    }

    /// Returns a slice with room for `extra` more elements, copying if the
    /// backing array has to grow. Go's growth: double while small, then
    /// widen by about a quarter.
    fn grow(self, extra: usize) -> Slice<P> {
        let need = self.len + extra;
        let mut cap = self.cap;
        if cap == 0 {
            cap = need;
        }
        while cap < need {
            cap = if cap < 256 { cap * 2 } else { cap + cap / 4 };
        }
        let ptr = heap::allocate_array::<P>(cap).as_ptr();
        for i in 0..self.len {
            // SAFETY: `i < len <= cap` on both sides; the source is this
            // slice's array, the destination the fresh one.
            unsafe { (*ptr.add(i)).store((*self.ptr.add(i)).load()) };
        }
        Slice {
            ptr,
            len: self.len,
            cap,
        }
    }

    /// `append(s, src...)`: appends every element of another slice.
    pub fn append_slice(self, src: Slice<P>) -> Slice<P> {
        let mut s = self;
        if s.cap - s.len < src.len {
            s = s.grow(src.len);
        }
        for i in 0..src.len {
            // SAFETY: room was just made, and `i` is inside `src`.
            unsafe { (*s.ptr.add(s.len + i)).store((*src.ptr.add(i)).load()) };
        }
        Slice {
            ptr: s.ptr,
            len: s.len + src.len,
            cap: s.cap,
        }
    }

    /// `copy(dst, src)`: copies `min(len(dst), len(src))` elements and
    /// returns that count. Overlapping slices copy as if through a temporary,
    /// as in Go.
    pub fn copy_from(self, src: Slice<P>) -> i64 {
        let n = self.len.min(src.len);
        if self.ptr as usize <= src.ptr as usize {
            for i in 0..n {
                // SAFETY: `i < n <= len` of both slices.
                unsafe { (*self.ptr.add(i)).store((*src.ptr.add(i)).load()) };
            }
        } else {
            for i in (0..n).rev() {
                // SAFETY: as above, back to front, so an overlapping copy
                // still reads each element before overwriting it.
                unsafe { (*self.ptr.add(i)).store((*src.ptr.add(i)).load()) };
            }
        }
        n as i64
    }
}

impl<P: Place> Slice<P> {
    /// The element values, for conversions and `println`.
    pub fn load_all(self) -> alloc::vec::Vec<P::Value> {
        (0..self.len)
            // SAFETY: `i < len`, inside the backing array.
            .map(|i| unsafe { (*self.ptr.add(i)).load() })
            .collect()
    }
}

impl Slice<crate::place::Slot<u8>> {
    /// `[]byte(s)`: a fresh array holding the string's bytes.
    pub fn of_str(s: crate::string::GoStr) -> Self {
        let out: Self = Slice::make(s.len(), s.len());
        for (i, b) in s.bytes().iter().enumerate() {
            // SAFETY: `i` is inside the array just allocated for them.
            unsafe { (*out.ptr.add(i)).store(*b) };
        }
        out
    }

    /// `string(b)`: a fresh string holding the slice's bytes.
    pub fn to_str(self) -> crate::string::GoStr {
        let bytes = self.load_all();
        crate::string::GoStr::from_bytes(&bytes)
    }

    /// `append(b, s...)`: appends a string's bytes.
    pub fn append_str(self, s: crate::string::GoStr) -> Self {
        let mut out = self;
        let n = s.len() as usize;
        if out.cap - out.len < n {
            out = out.grow(n);
        }
        for (i, b) in s.bytes().iter().enumerate() {
            // SAFETY: room was just made for `n` more elements.
            unsafe { (*out.ptr.add(out.len + i)).store(*b) };
        }
        Slice {
            ptr: out.ptr,
            len: out.len + n,
            cap: out.cap,
        }
    }
}

impl Slice<crate::place::Slot<i32>> {
    /// `[]rune(s)`: the string's code points, invalid bytes becoming U+FFFD.
    pub fn of_str(s: crate::string::GoStr) -> Self {
        let mut runes = alloc::vec::Vec::new();
        let mut it = s.iter();
        loop {
            let (ok, _, r) = it.advance();
            if !ok {
                break;
            }
            runes.push(r);
        }
        let out: Self = Slice::make(runes.len() as i64, runes.len() as i64);
        for (i, r) in runes.iter().enumerate() {
            // SAFETY: `i` is inside the array just allocated for them.
            unsafe { (*out.ptr.add(i)).store(*r) };
        }
        out
    }

    /// `string(runes)`: their UTF-8 encoding.
    pub fn to_str(self) -> crate::string::GoStr {
        let mut bytes = alloc::vec::Vec::new();
        for r in self.load_all() {
            let c = u32::try_from(r)
                .ok()
                .and_then(char::from_u32)
                .unwrap_or(char::REPLACEMENT_CHARACTER);
            let mut buf = [0u8; 4];
            bytes.extend_from_slice(c.encode_utf8(&mut buf).as_bytes());
        }
        crate::string::GoStr::from_bytes(&bytes)
    }
}

/// A slice over an existing array place, for `a[:]` on an array pointer.
pub fn of_array<P, const N: usize>(a: &'static [P; N]) -> Slice<P> {
    Slice {
        ptr: a.as_ptr() as *mut P,
        len: N,
        cap: N,
    }
}
