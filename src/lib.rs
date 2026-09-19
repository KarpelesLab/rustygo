//! Runtime for Go packages compiled to Rust by the rustygo compiler.
//!
//! Generated code is safe Rust and calls into this crate for everything Go
//! has and Rust does not. The `unsafe` in the project lives here, the same way
//! Rust's own `std` confines it.
//!
//! This crate is at the M0 spike stage (see `docs/ROADMAP.md`). What exists:
//!
//! * [`value`] and [`place`]: Go values, addressable storage and pointers.
//! * [`string`]: Go strings.
//! * [`ops`]: Go integer semantics that differ from Rust's (shifts,
//!   division) and bounds checks.
//! * [`print`]: the builtin `print` / `println`.
//! * [`panic`](mod@panic): Go panics carried on Rust unwinding.
//! * `rt`: the process entry point for a Go `main` (`std` only).
//! * [`prelude`]: what generated code imports.
//! * [`gc`] and [`heap`]: GC roots and the collector.
//! * [`trace`]: how the collector walks a value's references.
//!
//! Still to come, by milestone: slices, maps, closures and interfaces (M1); goroutines, channels and timers (M2);
//! the `syscall` layer and netpoller (M3).
//!
//! # Features
//!
//! * `std` (default): hosted targets. Without it the crate is `no_std` +
//!   `alloc`.
//! * `gc-torture`: debug collector that collects at every safe point.

#![no_std]

extern crate alloc;

#[macro_use]
mod tls;
#[cfg(feature = "std")]
extern crate std;

pub mod defers;
pub mod func;
pub mod gc;
pub mod heap;
pub mod iface;
pub mod map;
pub mod ops;
pub mod panic;
pub mod place;
pub mod prelude;
pub mod print;
#[cfg(feature = "std")]
pub mod rt;
pub mod slice;
pub mod string;
pub mod trace;
pub mod value;
