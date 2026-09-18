//! Runtime for Go packages compiled to Rust by the rustygo compiler.
//!
//! Generated code is safe Rust and calls into this crate for everything Go
//! has and Rust does not. The `unsafe` in the project lives here, the same way
//! Rust's own `std` confines it.
//!
//! This crate is at the M0 spike stage (see `docs/ROADMAP.md`). What exists:
//!
//! * [`ops`]: Go integer semantics that differ from Rust's (shifts,
//!   division).
//! * [`print`]: the builtin `print` / `println`.
//! * [`panic`](mod@panic): Go panics carried on Rust unwinding.
//! * `rt`: the process entry point for a Go `main` (`std` only).
//!
//! Still to come, by milestone: the collector, `Gc<T>` / `Ptr<T>`, strings,
//! slices, maps and interfaces (M1); goroutines, channels and timers (M2);
//! the `syscall` layer and netpoller (M3).
//!
//! # Features
//!
//! * `std` (default): hosted targets. Without it the crate is `no_std` +
//!   `alloc`.
//! * `gc-torture`: debug collector that collects at every safe point.

#![no_std]

extern crate alloc;
#[cfg(feature = "std")]
extern crate std;

pub mod ops;
pub mod panic;
pub mod print;
#[cfg(feature = "std")]
pub mod rt;
