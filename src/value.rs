//! Go values: every Go type maps to a `Copy` Rust type with a zero value.
//!
//! Go values have no destructors and copy bitwise, so the value form of every
//! Go type is `Copy`. [`GoValue::zero`] is Go's zero value. It stands in for
//! `Default`, which arrays implement only up to length 32.

/// A Rust type that represents the value of some Go type.
pub trait GoValue: Copy + 'static {
    /// Go's zero value for the type.
    fn zero() -> Self;
}

macro_rules! zero_is {
    ($z:expr => $($t:ty),*) => {$(
        impl GoValue for $t {
            #[inline]
            fn zero() -> Self { $z }
        }
    )*};
}

zero_is!(0 => i8, i16, i32, i64, u8, u16, u32, u64);
zero_is!(0.0 => f32, f64);
zero_is!(false => bool);
zero_is!(() => ());

impl<T: GoValue, const N: usize> GoValue for [T; N] {
    #[inline]
    fn zero() -> Self {
        [T::zero(); N]
    }
}

macro_rules! tuple_zero {
    ($($t:ident),+) => {
        impl<$($t: GoValue),+> GoValue for ($($t,)+) {
            #[inline]
            fn zero() -> Self { ($($t::zero(),)+) }
        }
    };
}

tuple_zero!(A);
tuple_zero!(A, B);
tuple_zero!(A, B, C);
tuple_zero!(A, B, C, D);
tuple_zero!(A, B, C, D, E);
tuple_zero!(A, B, C, D, E, F);
tuple_zero!(A, B, C, D, E, F, G);
tuple_zero!(A, B, C, D, E, F, G, H);
tuple_zero!(A, B, C, D, E, F, G, H, I);
tuple_zero!(A, B, C, D, E, F, G, H, I, J);
tuple_zero!(A, B, C, D, E, F, G, H, I, J, K);
tuple_zero!(A, B, C, D, E, F, G, H, I, J, K, L);
