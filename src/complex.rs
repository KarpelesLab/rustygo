//! Go's `complex64` and `complex128`.
//!
//! Addition, subtraction and multiplication are the obvious formulas, which
//! is what gc compiles inline. Division is Smith's algorithm with C99's
//! corrections for infinities and zeros, ported from gc's
//! `runtime.complex128div`, so the results match bit for bit — including the
//! cases where a naive formula would produce NaN.
//!
//! A `complex64` divides by widening to `complex128`, dividing, and
//! narrowing back, as gc does.

use crate::trace::{Trace, Tracer};
use crate::value::GoValue;

/// A Go complex number over `f32` (`complex64`) or `f64` (`complex128`).
#[derive(Clone, Copy, PartialEq, Debug)]
pub struct Complex<F> {
    /// `real(z)`.
    pub re: F,
    /// `imag(z)`.
    pub im: F,
}

/// `complex64`.
pub type Complex64 = Complex<f32>;
/// `complex128`.
pub type Complex128 = Complex<f64>;

impl<F> Complex<F> {
    /// `complex(re, im)`.
    #[inline]
    pub const fn new(re: F, im: F) -> Self {
        Complex { re, im }
    }
}

impl<F: GoValue> GoValue for Complex<F> {
    #[inline]
    fn zero() -> Self {
        Complex {
            re: F::zero(),
            im: F::zero(),
        }
    }
}

impl<F: 'static> Trace for Complex<F> {
    #[inline]
    fn trace(&self, _: &mut Tracer<'_>) {}
}

macro_rules! complex_ops {
    ($f:ty) => {
        impl core::ops::Add for Complex<$f> {
            type Output = Self;
            #[inline]
            fn add(self, o: Self) -> Self {
                Complex {
                    re: self.re + o.re,
                    im: self.im + o.im,
                }
            }
        }

        impl core::ops::Sub for Complex<$f> {
            type Output = Self;
            #[inline]
            fn sub(self, o: Self) -> Self {
                Complex {
                    re: self.re - o.re,
                    im: self.im - o.im,
                }
            }
        }

        impl core::ops::Mul for Complex<$f> {
            type Output = Self;
            #[inline]
            fn mul(self, o: Self) -> Self {
                Complex {
                    re: self.re * o.re - self.im * o.im,
                    im: self.re * o.im + self.im * o.re,
                }
            }
        }

        impl core::ops::Neg for Complex<$f> {
            type Output = Self;
            #[inline]
            fn neg(self) -> Self {
                Complex {
                    re: -self.re,
                    im: -self.im,
                }
            }
        }
    };
}

complex_ops!(f32);
complex_ops!(f64);

impl Complex<f64> {
    /// `a / b`, following gc's `runtime.complex128div`.
    pub fn divide(self, m: Self) -> Self {
        let n = self;
        let (mut e, mut f);
        // Smith's algorithm: divide by the larger component first, so the
        // intermediate values cannot overflow.
        if m.re.abs() >= m.im.abs() {
            let ratio = m.im / m.re;
            let denom = m.re + ratio * m.im;
            e = (n.re + n.im * ratio) / denom;
            f = (n.im - n.re * ratio) / denom;
        } else {
            let ratio = m.re / m.im;
            let denom = m.im + ratio * m.re;
            e = (n.re * ratio + n.im) / denom;
            f = (n.im * ratio - n.re) / denom;
        }

        if e.is_nan() && f.is_nan() {
            // Correct the result to infinities and zeros where C99 says to
            // (ISO/IEC 9899:1999, G.5.1).
            let (mut a, mut b) = (n.re, n.im);
            let (mut c, mut d) = (m.re, m.im);
            if m.re == 0.0 && m.im == 0.0 && (!a.is_nan() || !b.is_nan()) {
                e = f64::INFINITY.copysign(c) * a;
                f = f64::INFINITY.copysign(c) * b;
            } else if (a.is_infinite() || b.is_infinite()) && c.is_finite() && d.is_finite() {
                a = inf2one(a);
                b = inf2one(b);
                e = f64::INFINITY * (a * c + b * d);
                f = f64::INFINITY * (b * c - a * d);
            } else if (c.is_infinite() || d.is_infinite()) && a.is_finite() && b.is_finite() {
                c = inf2one(c);
                d = inf2one(d);
                e = 0.0 * (a * c + b * d);
                f = 0.0 * (b * c - a * d);
            }
        }
        Complex::new(e, f)
    }

    /// `complex64(z)`.
    #[inline]
    pub fn to_c64(self) -> Complex<f32> {
        Complex::new(self.re as f32, self.im as f32)
    }
}

impl Complex<f32> {
    /// `a / b`: gc widens to complex128, divides, and narrows back.
    #[inline]
    pub fn divide(self, o: Self) -> Self {
        self.to_c128().divide(o.to_c128()).to_c64()
    }

    /// `complex128(z)`.
    #[inline]
    pub fn to_c128(self) -> Complex<f64> {
        Complex::new(self.re as f64, self.im as f64)
    }
}

/// ±1 for an infinity of that sign, ±0 otherwise: gc's `inf2one`.
fn inf2one(f: f64) -> f64 {
    let g = if f.is_infinite() { 1.0 } else { 0.0 };
    f64::copysign(g, f)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn division_handles_the_hard_cases() {
        let c = |re, im| Complex::<f64>::new(re, im);
        assert_eq!(c(1.0, 2.0).divide(c(3.0, 4.0)), c(0.44, 0.08));
        // Dividing by zero gives infinities, not NaNs.
        let z = c(1.0, 1.0).divide(c(0.0, 0.0));
        assert!(z.re.is_infinite() && z.im.is_infinite());
        // A finite numerator over an infinite denominator goes to zero.
        let z = c(1.0, 1.0).divide(c(f64::INFINITY, 0.0));
        assert_eq!(z, c(0.0, 0.0));
        assert_eq!(c(6.0, 4.0) * c(0.5, 0.0), c(3.0, 2.0));
    }
}
