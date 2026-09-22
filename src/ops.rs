//! Go integer semantics that Rust spells differently.
//!
//! Plain `+`, `-` and `*` map to Rust's `wrapping_*` methods directly and need
//! nothing here. Shifts and division do:
//!
//! * Go shifts by the full count. Shifting by at least the operand's width
//!   gives 0, or sign fill for a signed right shift; Rust masks the count.
//! * A negative shift count panics in Go.
//! * Integer division by zero panics, but `MinInt / -1` wraps silently.
//!
//! Generated code widens every shift count to `u64` first. Counts of a signed
//! type go through [`shift_count`], which rejects negative values.
//!
//! Also here: index and slice bounds checks with gc's messages, and
//! float-to-integer conversions matching gc per architecture ([`float`]).

use crate::panic::{RuntimeError, runtime_error};

/// Checks a signed index against a length, panicking with Go's message.
#[inline]
pub fn index(i: i64, len: usize) -> usize {
    if i < 0 || i as u64 >= len as u64 {
        runtime_error(RuntimeError::Index { index: i, len });
    }
    i as usize
}

/// Checks an unsigned index against a length, panicking with Go's message.
#[inline]
pub fn index_u(i: u64, len: usize) -> usize {
    if i >= len as u64 {
        runtime_error(RuntimeError::IndexU { index: i, len });
    }
    i as usize
}

/// Checks `[lo:hi]` against a length, as gc does: the high bound first, then
/// the low bound against it. A missing `hi` means `len`.
pub fn slice_bounds(lo: i64, hi: Option<i64>, len: usize) -> (usize, usize) {
    let h = match hi {
        Some(h) => {
            if h < 0 || h as u64 > len as u64 {
                runtime_error(RuntimeError::SliceHigh { high: h, len });
            }
            h as usize
        }
        None => len,
    };
    if lo < 0 || lo as u64 > h as u64 {
        runtime_error(RuntimeError::SliceLow { low: lo, high: h });
    }
    (lo as usize, h)
}

/// Float-to-integer conversions, matching gc on each architecture.
///
/// Go leaves out-of-range and NaN conversions implementation-specific, and gc
/// simply uses the hardware instruction. On x86-64 that is `CVTTSD2SQ` /
/// `CVTTSD2SL`, which yield the minimum integer for anything they cannot
/// represent. On aarch64 it is `FCVTZS` / `FCVTZU`, which saturate and send
/// NaN to 0, exactly like Rust's `as`.
///
/// gc converts to the narrow types through a wider one: `int8`/`int16`/
/// `uint8`/`uint16` through int32, and `uint32` through int64. A float32
/// operand is widened to float64 first, which is exact.
pub mod float {
    /// `CVTTSD2SQ`: truncate to i64, or `i64::MIN` if out of range or NaN.
    #[cfg(target_arch = "x86_64")]
    #[inline]
    fn cvt64(x: f64) -> i64 {
        // -2^63 <= x < 2^63, false for NaN.
        if (-9223372036854775808.0..9223372036854775808.0).contains(&x) {
            x as i64
        } else {
            i64::MIN
        }
    }

    /// `CVTTSD2SL`: truncate to i32, or `i32::MIN` if out of range or NaN.
    #[cfg(target_arch = "x86_64")]
    #[inline]
    fn cvt32(x: f64) -> i32 {
        // -2^31 - 1 itself truncates out of range, and `as` then saturates
        // it to i32::MIN, the same answer as the hardware's.
        if (-2147483649.0..2147483648.0).contains(&x) {
            x as i32
        } else {
            i32::MIN
        }
    }

    #[cfg(not(target_arch = "x86_64"))]
    #[inline]
    fn cvt64(x: f64) -> i64 {
        x as i64
    }

    #[cfg(not(target_arch = "x86_64"))]
    #[inline]
    fn cvt32(x: f64) -> i32 {
        x as i32
    }

    /// `int64(x)`, `int(x)`.
    #[inline]
    pub fn to_i64(x: f64) -> i64 {
        cvt64(x)
    }

    /// `int32(x)`; `int16(x)` and `int8(x)` truncate this.
    #[inline]
    pub fn to_i32(x: f64) -> i32 {
        cvt32(x)
    }

    /// `uint64(x)`, `uint(x)`, `uintptr(x)`.
    #[inline]
    pub fn to_u64(x: f64) -> u64 {
        #[cfg(target_arch = "x86_64")]
        {
            // gc's sequence: below 2^63 convert directly; otherwise (NaN
            // included) convert x - 2^63 and set the top bit.
            if x < 9223372036854775808.0 {
                cvt64(x) as u64
            } else {
                cvt64(x - 9223372036854775808.0) as u64 | 1 << 63
            }
        }
        #[cfg(not(target_arch = "x86_64"))]
        {
            x as u64
        }
    }

    /// `uint32(x)`: through int64 on every architecture (checked against gc
    /// on aarch64, where `uint32(-3e9)` is 1294967296, not 0).
    #[inline]
    pub fn to_u32(x: f64) -> u32 {
        cvt64(x) as u32
    }
}

/// Checks `[lo:hi:max]` against a slice's length and capacity, as gc does:
/// the widest bound first, then inwards. Missing bounds default to `len` and
/// `cap`, and `hi` may reach past the length up to the capacity.
pub fn slice3_bounds(
    lo: i64,
    hi: Option<i64>,
    max: Option<i64>,
    len: usize,
    cap: usize,
) -> (usize, usize, usize) {
    let m = match max {
        Some(m) => {
            if m < 0 || m as u64 > cap as u64 {
                runtime_error(RuntimeError::SliceCap { max: m, cap });
            }
            m as usize
        }
        None => cap,
    };
    let h = match hi {
        Some(h) => {
            if h < 0 || h as u64 > m as u64 {
                runtime_error(RuntimeError::SliceHigh { high: h, len: m });
            }
            h as usize
        }
        None => len.min(m),
    };
    if lo < 0 || lo as u64 > h as u64 {
        runtime_error(RuntimeError::SliceLow { low: lo, high: h });
    }
    (lo as usize, h, m)
}

/// gc's `maxAlloc`: one byte short of the address space its heap can cover,
/// and so the largest object that can exist.
const MAX_ALLOC: u64 = if usize::BITS >= 64 {
    (1 << 48) - 1
} else {
    (1 << 31) - 1
};

/// Checks `make([]T, len, cap)`, where `elem` is the element size.
///
/// The diagnosis is gc's. When something is out of range it names the length
/// if the length alone is bad, and the capacity otherwise: `make([]T, n)`
/// passes `n` as both, and naming the length is clearer when the capacity was
/// never written down (golang.org/issue/4085).
pub fn make_bounds(len: i64, cap: i64, elem: usize) -> (usize, usize) {
    if bad_count(cap, elem) || len < 0 || len > cap {
        if bad_count(len, elem) {
            runtime_error(RuntimeError::MakeLen { len });
        }
        runtime_error(RuntimeError::MakeCap { cap });
    }
    (len as usize, cap as usize)
}

/// Whether this many elements of this size cannot exist: a negative count, or
/// more bytes than the heap can address. A zero-sized element never overflows,
/// which is why `make([]struct{}, 1<<40)` is a legal Go program.
fn bad_count(n: i64, elem: usize) -> bool {
    n < 0
        || (n as u64)
            .checked_mul(elem as u64)
            .is_none_or(|bytes| bytes > MAX_ALLOC)
}

/// Converts a signed shift count to the unsigned count the [`GoInt`] shifts
/// take, panicking with Go's runtime error if it is negative.
#[inline]
pub fn shift_count(s: i64) -> u64 {
    if s < 0 {
        runtime_error(RuntimeError::NegativeShift);
    }
    s as u64
}

/// Go's semantics for the integer operations that differ from Rust's.
pub trait GoInt: Copy {
    /// `self << s`: 0 once `s` reaches the width.
    fn go_shl(self, s: u64) -> Self;
    /// `self >> s`: 0 (unsigned, or non-negative signed) or -1 (negative
    /// signed) once `s` reaches the width.
    fn go_shr(self, s: u64) -> Self;
    /// `self / rhs`, truncated, panicking on zero; `MIN / -1` wraps to `MIN`.
    fn go_div(self, rhs: Self) -> Self;
    /// `self % rhs`, sign of the dividend, panicking on zero; `MIN % -1` is 0.
    fn go_rem(self, rhs: Self) -> Self;
}

macro_rules! go_int {
    ($($t:ty => $shr_saturated:expr),* $(,)?) => {$(
        impl GoInt for $t {
            #[inline]
            fn go_shl(self, s: u64) -> Self {
                if s >= u64::from(<$t>::BITS) { 0 } else { self << s }
            }

            #[inline]
            fn go_shr(self, s: u64) -> Self {
                if s >= u64::from(<$t>::BITS) {
                    let saturated: fn($t) -> $t = $shr_saturated;
                    saturated(self)
                } else {
                    self >> s
                }
            }

            #[inline]
            fn go_div(self, rhs: Self) -> Self {
                if rhs == 0 {
                    runtime_error(RuntimeError::DivideByZero);
                }
                self.wrapping_div(rhs)
            }

            #[inline]
            fn go_rem(self, rhs: Self) -> Self {
                if rhs == 0 {
                    runtime_error(RuntimeError::DivideByZero);
                }
                self.wrapping_rem(rhs)
            }
        }
    )*};
}

go_int! {
    u8 => |_| 0, u16 => |_| 0, u32 => |_| 0, u64 => |_| 0,
    i8 => |x| x >> (i8::BITS - 1),
    i16 => |x| x >> (i16::BITS - 1),
    i32 => |x| x >> (i32::BITS - 1),
    i64 => |x| x >> (i64::BITS - 1),
}

#[cfg(all(test, feature = "std"))]
mod tests {
    use super::*;
    use crate::panic::GoPanic;
    use std::panic::catch_unwind;

    fn panic_text(f: impl FnOnce() + std::panic::UnwindSafe) -> std::string::String {
        let payload = catch_unwind(f).expect_err("expected a Go panic");
        let p = payload.downcast::<GoPanic>().expect("payload is a GoPanic");
        std::string::String::from_utf8(p.text().to_vec()).unwrap()
    }

    #[test]
    fn shifts_past_width() {
        assert_eq!(1u8.go_shl(8), 0);
        assert_eq!(1u64.go_shl(63), 1 << 63);
        assert_eq!(1u64.go_shl(64), 0);
        assert_eq!(1u64.go_shl(u64::MAX), 0);
        assert_eq!(u32::MAX.go_shr(32), 0);
        assert_eq!((-8i64).go_shr(2), -2);
        assert_eq!((-8i64).go_shr(64), -1);
        assert_eq!(8i64.go_shr(1000), 0);
        assert_eq!((-1i8).go_shl(8), 0);
    }

    #[test]
    fn negative_shift_count_panics() {
        assert_eq!(shift_count(3), 3);
        assert_eq!(
            panic_text(|| {
                shift_count(-1);
            }),
            "runtime error: negative shift amount"
        );
    }

    #[test]
    fn index_and_slice_messages_match_gc() {
        assert_eq!(index(2, 3), 2);
        assert_eq!(slice_bounds(1, None, 5), (1, 5));
        let cases: [(&str, fn()); 7] = [
            ("runtime error: index out of range [-1]", || {
                index(-1, 5);
            }),
            (
                "runtime error: index out of range [10] with length 5",
                || {
                    index(10, 5);
                },
            ),
            (
                "runtime error: index out of range [9223372036854775808] with length 5",
                || {
                    index_u(1 << 63, 5);
                },
            ),
            (
                "runtime error: slice bounds out of range [:10] with length 5",
                || {
                    slice_bounds(0, Some(10), 5);
                },
            ),
            ("runtime error: slice bounds out of range [3:2]", || {
                slice_bounds(3, Some(2), 5);
            }),
            ("runtime error: slice bounds out of range [10:5]", || {
                slice_bounds(10, None, 5);
            }),
            ("runtime error: slice bounds out of range [:-1]", || {
                slice_bounds(0, Some(-1), 5);
            }),
        ];
        for (want, f) in cases {
            assert_eq!(panic_text(f), want);
        }
        assert_eq!(
            panic_text(|| {
                slice_bounds(-1, None, 5);
            }),
            "runtime error: slice bounds out of range [-1:]"
        );
    }

    #[test]
    fn division() {
        assert_eq!((-7i64).go_div(2), -3);
        assert_eq!((-7i64).go_rem(2), -1);
        assert_eq!(i64::MIN.go_div(-1), i64::MIN);
        assert_eq!(i64::MIN.go_rem(-1), 0);
        assert_eq!(i8::MIN.go_div(-1), i8::MIN);
        assert_eq!(
            panic_text(|| {
                1u32.go_div(0);
            }),
            "runtime error: integer divide by zero"
        );
        assert_eq!(
            panic_text(|| {
                1i16.go_rem(0);
            }),
            "runtime error: integer divide by zero"
        );
    }
}
