//! The builtin `print` and `println`.
//!
//! These are not `fmt`: they write to stderr, `println` separates operands with
//! spaces where `print` does not, and each operand type has the runtime's own
//! format. Programs in the differential suite rely on the exact bytes, so this
//! follows gc's `runtime/print.go` for the pinned Go release. That file is
//! part of the per-release contract: Go 1.26 prints floats in `strconv`'s
//! shortest `%g` form (`1.5`, `1e+300`), and older releases printed
//! `+1.500000e+000`.
//!
//! Generated code builds a slice of [`Arg`]s and calls `print` or
//! `println` (`std` only). [`format`] does the formatting without I/O, which is what
//! `no_std` targets and the tests use.

use alloc::string::String;
use alloc::vec::Vec;
use core::fmt::Write;

/// One operand of `print` / `println`, already classified by the emitter.
#[derive(Debug, Clone, Copy)]
pub enum Arg<'a> {
    /// `bool`.
    Bool(bool),
    /// Any signed integer type, widened.
    Int(i64),
    /// Any unsigned integer type, `uintptr` included, widened.
    Uint(u64),
    /// Pointers, `unsafe.Pointer`, channels, maps and funcs: printed in hex.
    Pointer(u64),
    /// `float32`.
    Float32(f32),
    /// `float64`.
    Float64(f64),
    /// `complex64`, as (real, imaginary).
    Complex64(f32, f32),
    /// `complex128`, as (real, imaginary).
    Complex128(f64, f64),
    /// `string`: arbitrary bytes, written as they are.
    Str(&'a [u8]),
    /// A slice, printed as gc does: `[len/cap]0xaddr`.
    Slice {
        /// `len(s)`.
        len: i64,
        /// `cap(s)`.
        cap: i64,
        /// The backing array's address.
        addr: u64,
    },
    /// An interface value, printed as gc does: the data word in hex.
    Iface {
        /// The boxed value's address.
        data: u64,
    },
    /// Untyped `nil`.
    Nil,
}

/// Appends what `print(args...)` (or `println` when `ln` is set) writes.
pub fn format(out: &mut Vec<u8>, args: &[Arg<'_>], ln: bool) {
    for (i, a) in args.iter().enumerate() {
        if ln && i > 0 {
            out.push(b' ');
        }
        match *a {
            Arg::Bool(b) => out.extend_from_slice(if b { b"true" } else { b"false" }),
            Arg::Int(v) => {
                if v < 0 {
                    out.push(b'-');
                }
                push_uint(out, v.unsigned_abs());
            }
            Arg::Uint(v) => push_uint(out, v),
            Arg::Pointer(v) => push_hex(out, v),
            Arg::Float32(v) => push_float(out, f64::from(v), true),
            Arg::Float64(v) => push_float(out, v, false),
            Arg::Complex64(re, im) => push_complex(out, f64::from(re), f64::from(im), true),
            Arg::Complex128(re, im) => push_complex(out, re, im, false),
            Arg::Slice { len, cap, addr } => {
                out.push(b'[');
                push_uint(out, len as u64);
                out.push(b'/');
                push_uint(out, cap as u64);
                out.push(b']');
                push_hex(out, addr);
            }
            Arg::Iface { data } => {
                out.push(b'(');
                push_hex(out, data);
                out.push(b',');
                push_hex(out, data);
                out.push(b')');
            }
            Arg::Str(s) => out.extend_from_slice(s),
            Arg::Nil => out.extend_from_slice(b"nil"),
        }
    }
    if ln {
        out.push(b'\n');
    }
}

/// The builtin `print`: operands back to back, on stderr.
#[cfg(feature = "std")]
pub fn print(args: &[Arg<'_>]) {
    emit(args, false);
}

/// The builtin `println`: operands separated by spaces, then a newline, on
/// stderr.
#[cfg(feature = "std")]
pub fn println(args: &[Arg<'_>]) {
    emit(args, true);
}

#[cfg(feature = "std")]
fn emit(args: &[Arg<'_>], ln: bool) {
    use std::io::Write;
    let mut buf = Vec::new();
    format(&mut buf, args, ln);
    // One write per call, so lines from concurrent goroutines do not
    // interleave mid-line. gc ignores write errors here too.
    let _ = std::io::stderr().lock().write_all(&buf);
}

fn push_uint(out: &mut Vec<u8>, mut v: u64) {
    let mut buf = [0u8; 20];
    let mut i = buf.len();
    loop {
        i -= 1;
        buf[i] = b'0' + (v % 10) as u8;
        v /= 10;
        if v == 0 {
            break;
        }
    }
    out.extend_from_slice(&buf[i..]);
}

fn push_hex(out: &mut Vec<u8>, mut v: u64) {
    const DIGITS: &[u8; 16] = b"0123456789abcdef";
    let mut buf = [0u8; 16];
    let mut i = buf.len();
    loop {
        i -= 1;
        buf[i] = DIGITS[(v % 16) as usize];
        v /= 16;
        if v == 0 {
            break;
        }
    }
    out.extend_from_slice(b"0x");
    out.extend_from_slice(&buf[i..]);
}

/// `strconv.AppendFloat(v, 'g', -1, bits)`: the shortest decimal that
/// round-trips at the operand's own width, in `%e` form when the decimal
/// exponent is below -4 or at least 6. The layout around the digits is a port
/// of `formatDigits`, `fmtE` and `fmtF`.
fn push_float(out: &mut Vec<u8>, v: f64, float32: bool) {
    if v.is_nan() {
        out.extend_from_slice(b"NaN");
        return;
    }
    if v.is_infinite() {
        out.extend_from_slice(if v > 0.0 { b"+Inf" } else { b"-Inf" });
        return;
    }
    if v.is_sign_negative() {
        out.push(b'-');
    }

    let sci = shortest_sci(v.abs(), float32);
    let (mantissa, exp) = sci.split_once('e').expect("`{:e}` output has an exponent");
    let exp: i32 = exp.parse().expect("`{:e}` exponent is an integer");
    let digits: Vec<u8> = mantissa.bytes().filter(|&b| b != b'.').collect();
    // Go represents zero as no digits at decimal point 0.
    let (digits, dp) = if digits == b"0" {
        (&[][..], 0)
    } else {
        (&digits[..], exp + 1)
    };
    let nd = digits.len() as i32;

    let x = dp - 1;
    if !(-4..6).contains(&x) {
        // fmtE with prec = nd - 1: every digit, then a 2- or 3-digit exponent.
        out.push(*digits.first().unwrap_or(&b'0'));
        if nd > 1 {
            out.push(b'.');
            out.extend_from_slice(&digits[1..]);
        }
        out.push(b'e');
        out.push(if x < 0 { b'-' } else { b'+' });
        let x = x.unsigned_abs();
        if x >= 100 {
            out.push(b'0' + (x / 100) as u8);
        }
        out.push(b'0' + (x / 10 % 10) as u8);
        out.push(b'0' + (x % 10) as u8);
    } else {
        // fmtF with prec = max(nd - dp, 0).
        if dp > 0 {
            for i in 0..dp {
                out.push(*digits.get(i as usize).unwrap_or(&b'0'));
            }
        } else {
            out.push(b'0');
        }
        if nd > dp {
            out.push(b'.');
            for j in dp..nd {
                out.push(if j < 0 { b'0' } else { digits[j as usize] });
            }
        }
    }
}

/// The digits Go's Ryū picks, as `d.ddde±x`: the shortest string that parses
/// back to `v`, and among those the closest to `v`, ties to even.
///
/// Rust's `{:e}` finds the shortest length, but on an exact tie between two
/// candidates of that length it does not pick the even one. Rust's
/// fixed-precision mode is correctly rounded with ties to even, so at the same
/// length it gives Go's choice, as long as that choice still round-trips (near
/// a power of two the rounding interval is lopsided, and the nearest string
/// can fall outside it).
fn shortest_sci(v: f64, float32: bool) -> String {
    // gc quirk (Go 1.26): at a power of two, Dragonbox rounds a tie up unless
    // the binary exponent is the one where ties occur, -77 for float64 and
    // -35 for float32. gc's `dboxFtoa32` tests float64's -77, so float32's
    // single tie, 2^-12 = 0.000244140625, rounds up instead of to even.
    if float32 && v == 0.000244140625 {
        return String::from("2.4414063e-4");
    }
    let mut shortest = String::new();
    let mut rounded = String::new();
    // `v` came from an f32 when `float32` is set, so narrowing is exact.
    let _ = if float32 {
        write!(shortest, "{:e}", v as f32)
    } else {
        write!(shortest, "{:e}", v)
    };
    let prec = shortest
        .bytes()
        .take_while(|&b| b != b'e')
        .filter(u8::is_ascii_digit)
        .count()
        - 1;
    let _ = if float32 {
        write!(rounded, "{:.*e}", prec, v as f32)
    } else {
        write!(rounded, "{:.*e}", prec, v)
    };
    let round_trips = if float32 {
        rounded.parse::<f32>() == Ok(v as f32)
    } else {
        rounded.parse::<f64>() == Ok(v)
    };
    if round_trips { rounded } else { shortest }
}

/// `strconv.AppendComplex(c, 'g', -1, bits)`: `(re±imi)`, where the imaginary
/// part always carries a sign.
fn push_complex(out: &mut Vec<u8>, re: f64, im: f64, complex64: bool) {
    out.push(b'(');
    push_float(out, re, complex64);
    let i = out.len();
    push_float(out, im, complex64);
    if out[i] != b'+' && out[i] != b'-' {
        out.insert(i, b'+');
    }
    out.extend_from_slice(b"i)");
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fmt(args: &[Arg<'_>], ln: bool) -> Vec<u8> {
        let mut out = Vec::new();
        format(&mut out, args, ln);
        out
    }

    // Expected strings are gc's output for the same operands.
    #[test]
    fn println_separates_and_terminates() {
        let args = [
            Arg::Str(b"x ="),
            Arg::Int(-42),
            Arg::Bool(true),
            Arg::Uint(u64::MAX),
            Arg::Nil,
        ];
        assert_eq!(fmt(&args, true), b"x = -42 true 18446744073709551615 nil\n");
        assert_eq!(fmt(&args, false), b"x =-42true18446744073709551615nil");
    }

    #[test]
    fn integers_and_pointers() {
        assert_eq!(fmt(&[Arg::Int(0)], false), b"0");
        assert_eq!(fmt(&[Arg::Int(i64::MIN)], false), b"-9223372036854775808");
        assert_eq!(fmt(&[Arg::Pointer(0)], false), b"0x0");
        assert_eq!(fmt(&[Arg::Pointer(0xc000012345)], false), b"0xc000012345");
        // uintptr is an integer to println, not a pointer.
        assert_eq!(fmt(&[Arg::Uint(0xc000012345)], false), b"824633795397");
    }

    #[test]
    fn floats_like_gc() {
        let cases: &[(f64, &[u8])] = &[
            (0.0, b"0"),
            (-0.0, b"-0"),
            (1.0, b"1"),
            (1.5, b"1.5"),
            (-2.25, b"-2.25"),
            (100.0, b"100"),
            (0.001, b"0.001"),
            (0.0001, b"0.0001"),
            (0.00001, b"1e-05"),
            (123456.0, b"123456"),
            (1234567.0, b"1.234567e+06"),
            (1e300, b"1e+300"),
            (9.9999999, b"9.9999999"),
            (0.1, b"0.1"),
            (123456789.0, b"1.23456789e+08"),
            (5e-324, b"5e-324"),
            (f64::MAX, b"1.7976931348623157e+308"),
            (2.0 / 3.0, b"0.6666666666666666"),
            (f64::INFINITY, b"+Inf"),
            (f64::NEG_INFINITY, b"-Inf"),
            (f64::NAN, b"NaN"),
        ];
        for &(v, want) in cases {
            assert_eq!(fmt(&[Arg::Float64(v)], false), want, "value {v}");
        }
    }

    // The literals are exact binary values; their full digits are the point.
    #[test]
    #[allow(clippy::excessive_precision)]
    fn ties_round_to_even() {
        // Both are exactly halfway between two shortest candidates.
        assert_eq!(fmt(&[Arg::Float32(186839.125)], false), b"186839.12");
        assert_eq!(fmt(&[Arg::Float32(444838.625)], false), b"444838.62");
        assert_eq!(
            fmt(&[Arg::Float64(f64::from_bits(0x430fddecd228dcda))], false),
            b"1.1212166857430032e+15"
        );
    }

    #[test]
    #[allow(clippy::excessive_precision)]
    fn float32_power_of_two_tie_matches_gc() {
        assert_eq!(
            fmt(&[Arg::Float32(0.000244140625)], false),
            b"0.00024414063"
        );
        assert_eq!(
            fmt(&[Arg::Float32(-0.000244140625)], false),
            b"-0.00024414063"
        );
        // The same value as a float64 is exact in fewer digits.
        assert_eq!(
            fmt(&[Arg::Float64(0.000244140625)], false),
            b"0.000244140625"
        );
    }

    #[test]
    fn float32_uses_its_own_shortest_form() {
        assert_eq!(fmt(&[Arg::Float32(0.1)], false), b"0.1");
        assert_eq!(
            fmt(&[Arg::Float64(f64::from(0.1f32))], false),
            b"0.10000000149011612"
        );
        assert_eq!(fmt(&[Arg::Float32(f32::MAX)], false), b"3.4028235e+38");
    }

    #[test]
    fn complex() {
        assert_eq!(fmt(&[Arg::Complex128(1.0, -2.0)], false), b"(1-2i)");
        assert_eq!(fmt(&[Arg::Complex128(0.5, 3.0)], false), b"(0.5+3i)");
        assert_eq!(
            fmt(&[Arg::Complex64(0.1, f32::INFINITY)], false),
            b"(0.1+Infi)"
        );
    }
}
