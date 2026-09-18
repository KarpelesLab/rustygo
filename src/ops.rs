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

use crate::panic::{RuntimeError, runtime_error};

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
