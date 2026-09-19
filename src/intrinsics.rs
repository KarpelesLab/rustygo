//! Bodies for functions gc implements in assembly or inside its own runtime,
//! which the emitter maps here by name (internal/emit/intrinsics.go).

/// `bytealg.IndexByte` and `IndexByteString`: the first index of `c`, or -1.
#[inline]
pub fn index_byte(s: &[u8], c: u8) -> i64 {
    s.iter().position(|&b| b == c).map_or(-1, |i| i as i64)
}

/// `bytealg.Index` and `IndexString`: the first index of `sep` in `s`, or
/// -1. An empty `sep` is found at 0.
pub fn index(s: &[u8], sep: &[u8]) -> i64 {
    if sep.is_empty() {
        return 0;
    }
    if sep.len() > s.len() {
        return -1;
    }
    s.windows(sep.len())
        .position(|w| w == sep)
        .map_or(-1, |i| i as i64)
}

/// `bytealg.Compare`: -1, 0 or +1, bytewise.
pub fn compare(a: &[u8], b: &[u8]) -> i64 {
    match a.cmp(b) {
        core::cmp::Ordering::Less => -1,
        core::cmp::Ordering::Equal => 0,
        core::cmp::Ordering::Greater => 1,
    }
}

/// `bytealg.Count` and `CountString`: occurrences of `c`.
pub fn count(s: &[u8], c: u8) -> i64 {
    s.iter().filter(|&&b| b == c).count() as i64
}
