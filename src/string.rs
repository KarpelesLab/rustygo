//! Go strings: immutable byte sequences, not necessarily UTF-8.

use crate::ops;
use crate::trace::{Trace, Tracer};
use crate::value::GoValue;

/// A Go `string`: a pointer and a length, over bytes that are either a
/// literal in the binary or an array on the heap.
///
/// A substring points into the middle of those bytes; the collector resolves
/// such an interior pointer back to its object, and skips addresses that are
/// not on the heap (DESIGN §3).
#[derive(Clone, Copy)]
pub struct GoStr {
    ptr: *const u8,
    len: usize,
}

impl Trace for GoStr {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        t.edge(self.ptr as usize);
    }
}

impl PartialEq for GoStr {
    fn eq(&self, other: &Self) -> bool {
        self.bytes() == other.bytes()
    }
}

impl Eq for GoStr {}

impl PartialOrd for GoStr {
    fn partial_cmp(&self, other: &Self) -> Option<core::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for GoStr {
    fn cmp(&self, other: &Self) -> core::cmp::Ordering {
        self.bytes().cmp(other.bytes())
    }
}

impl core::hash::Hash for GoStr {
    fn hash<H: core::hash::Hasher>(&self, h: &mut H) {
        self.bytes().hash(h);
    }
}

impl core::fmt::Debug for GoStr {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        core::fmt::Debug::fmt(&alloc::string::String::from_utf8_lossy(self.bytes()), f)
    }
}

impl GoValue for GoStr {
    #[inline]
    fn zero() -> Self {
        GoStr::lit(b"")
    }
}

impl GoStr {
    /// A string constant: bytes in the binary, which the collector ignores.
    #[inline]
    pub const fn lit(b: &'static [u8]) -> Self {
        GoStr {
            ptr: b.as_ptr(),
            len: b.len(),
        }
    }

    /// The bytes, borrowed for as long as this handle is.
    #[inline]
    pub fn bytes(&self) -> &[u8] {
        // SAFETY: `ptr`/`len` describe either a literal in the binary or a
        // live heap array; generated code keeps the string rooted while it
        // holds it (DESIGN §3).
        unsafe { core::slice::from_raw_parts(self.ptr, self.len) }
    }

    /// `len(s)`.
    #[inline]
    pub fn len(self) -> i64 {
        self.len as i64
    }

    /// `s == ""`.
    #[inline]
    pub fn is_empty(self) -> bool {
        self.len == 0
    }

    /// A string of `len` bytes, written by `fill`. A safe point: it may
    /// collect before it allocates.
    fn build(len: usize, fill: impl FnOnce(&mut [u8])) -> GoStr {
        if len == 0 {
            return GoStr::lit(b"");
        }
        GoStr {
            ptr: crate::heap::allocate_bytes(len, fill).as_ptr(),
            len,
        }
    }

    /// `a + b`.
    pub fn concat(self, other: GoStr) -> GoStr {
        if self.is_empty() {
            return other;
        }
        if other.is_empty() {
            return self;
        }
        let (a, b) = (self, other);
        GoStr::build(a.len + b.len, |out| {
            // Written straight into the object: no temporary copy. Reading
            // `a` and `b` here is safe because the caller keeps both rooted
            // across the allocation (DESIGN §3).
            out[..a.len].copy_from_slice(a.bytes());
            out[a.len..].copy_from_slice(b.bytes());
        })
    }

    /// `s[i]`, for a signed index.
    #[inline]
    pub fn at(self, i: i64) -> u8 {
        self.bytes()[ops::index(i, self.len)]
    }

    /// `s[i]`, for an unsigned index.
    #[inline]
    pub fn at_u(self, i: u64) -> u8 {
        self.bytes()[ops::index_u(i, self.len)]
    }

    /// `s[lo:hi]`; a missing `hi` means `len(s)`. The result shares the
    /// bytes, as in Go.
    pub fn slice(self, lo: i64, hi: Option<i64>) -> GoStr {
        let (lo, hi) = ops::slice_bounds(lo, hi, self.len);
        GoStr {
            // SAFETY: `lo <= hi <= len`, so this stays inside the bytes (one
            // past the end is allowed).
            ptr: unsafe { self.ptr.add(lo) },
            len: hi - lo,
        }
    }

    /// `string(r)` for an integer `r`: its UTF-8 encoding, or U+FFFD if it is
    /// not a valid code point.
    pub fn from_rune(r: i64) -> GoStr {
        let c = u32::try_from(r)
            .ok()
            .and_then(char::from_u32)
            .unwrap_or(char::REPLACEMENT_CHARACTER);
        let mut buf = [0u8; 4];
        let s = c.encode_utf8(&mut buf);
        let bytes = s.as_bytes();
        GoStr::build(bytes.len(), |out| out.copy_from_slice(bytes))
    }

    /// The iterator of `for i, r := range s`.
    #[inline]
    pub fn iter(self) -> StrIter {
        StrIter { s: self, i: 0 }
    }
}

/// State of a `for i, r := range s` loop.
#[derive(Clone, Copy, Debug)]
pub struct StrIter {
    s: GoStr,
    i: usize,
}

impl GoValue for StrIter {
    fn zero() -> Self {
        StrIter {
            s: GoStr::zero(),
            i: 0,
        }
    }
}

impl Trace for StrIter {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        self.s.trace(t);
    }
}

impl StrIter {
    /// Advances: `(ok, index, rune)`. Invalid UTF-8 yields U+FFFD one byte at
    /// a time, as in Go.
    pub fn advance(&mut self) -> (bool, i64, i32) {
        if self.i >= self.s.len {
            return (false, 0, 0);
        }
        let start = self.i;
        let (r, n) = decode_rune(&self.s.bytes()[start..]);
        self.i += n;
        (true, start as i64, r)
    }
}

/// `utf8.DecodeRune` on a non-empty slice: the rune and its width, or
/// `(U+FFFD, 1)` for anything that is not a minimal, non-surrogate encoding.
pub fn decode_rune(p: &[u8]) -> (i32, usize) {
    const ERR: (i32, usize) = (0xFFFD, 1);
    let b0 = p[0];
    if b0 < 0x80 {
        return (b0 as i32, 1);
    }
    // Width and the accepted range of the second byte, per Go's `first` and
    // `acceptRanges` tables.
    let (size, lo, hi) = match b0 {
        0xC2..=0xDF => (2, 0x80, 0xBF),
        0xE0 => (3, 0xA0, 0xBF),
        0xE1..=0xEC | 0xEE..=0xEF => (3, 0x80, 0xBF),
        0xED => (3, 0x80, 0x9F),
        0xF0 => (4, 0x90, 0xBF),
        0xF1..=0xF3 => (4, 0x80, 0xBF),
        0xF4 => (4, 0x80, 0x8F),
        _ => return ERR,
    };
    if p.len() < size || !(lo..=hi).contains(&p[1]) {
        return ERR;
    }
    let cont = |b: u8| (0x80..=0xBF).contains(&b);
    match size {
        2 => ((((b0 & 0x1F) as i32) << 6) | (p[1] & 0x3F) as i32, 2),
        3 if cont(p[2]) => (
            (((b0 & 0x0F) as i32) << 12) | (((p[1] & 0x3F) as i32) << 6) | (p[2] & 0x3F) as i32,
            3,
        ),
        4 if cont(p[2]) && cont(p[3]) => (
            (((b0 & 0x07) as i32) << 18)
                | (((p[1] & 0x3F) as i32) << 12)
                | (((p[2] & 0x3F) as i32) << 6)
                | (p[3] & 0x3F) as i32,
            4,
        ),
        _ => ERR,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn runes(s: &'static [u8]) -> alloc::vec::Vec<(i64, i32)> {
        let mut it = GoStr::lit(s).iter();
        let mut out = alloc::vec::Vec::new();
        loop {
            let (ok, i, r) = it.advance();
            if !ok {
                return out;
            }
            out.push((i, r));
        }
    }

    #[test]
    fn range_decodes_like_go() {
        assert_eq!(
            runes("aé€😀".as_bytes()),
            [(0, 0x61), (1, 0xE9), (3, 0x20AC), (6, 0x1F600)]
        );
        // Invalid bytes, a truncated sequence, an overlong encoding and a
        // surrogate: each bad byte is U+FFFD of width 1.
        assert_eq!(
            runes(b"\xff\xe2\x82"),
            [(0, 0xFFFD), (1, 0xFFFD), (2, 0xFFFD)]
        );
        assert_eq!(runes(b"\xc0\x80"), [(0, 0xFFFD), (1, 0xFFFD)]);
        assert_eq!(
            runes(b"\xed\xa0\x80"),
            [(0, 0xFFFD), (1, 0xFFFD), (2, 0xFFFD)]
        );
    }

    #[test]
    fn from_rune() {
        assert_eq!(GoStr::from_rune(0x41).bytes(), b"A");
        assert_eq!(GoStr::from_rune(0x20AC).bytes(), "€".as_bytes());
        assert_eq!(GoStr::from_rune(-1).bytes(), "\u{FFFD}".as_bytes());
        assert_eq!(GoStr::from_rune(0xD800).bytes(), "\u{FFFD}".as_bytes());
        assert_eq!(GoStr::from_rune(0x110000).bytes(), "\u{FFFD}".as_bytes());
    }

    #[test]
    fn concat_and_slice() {
        let s = GoStr::lit(b"hello").concat(GoStr::lit(b", world"));
        assert_eq!(s.bytes(), b"hello, world");
        assert_eq!(s.slice(7, None).bytes(), b"world");
        assert_eq!(s.slice(0, Some(5)).bytes(), b"hello");
        assert!(GoStr::lit(b"abc") < GoStr::lit(b"abd"));
        assert!(GoStr::lit(b"ab") < GoStr::lit(b"abc"));
    }
}
