# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.3](https://github.com/KarpelesLab/rustygo/compare/v0.0.2...v0.0.3) - 2026-09-25

### Other

- Do not run the context-switch test where there is no switch
- Goroutines, channels and select
- The differential test says which platforms a program needs
- recover, exactly as Go defines it, and five bugs behind it
- Reflection, the file layer, and fmt

## [0.0.2](https://github.com/KarpelesLab/rustygo/compare/v0.0.1...v0.0.2) - 2026-09-19

### Other

- internal/godebug, and a diagnostic naming the caller
- Fix dead-initialization elimination: initializers read variables too
- The standard library, compiled for real: strings, strconv, sort, errors
- unsafe.Pointer, supported as gc supports it
- Test memory cap is best-effort: macOS refuses ulimit -v
- heap tests: root the new string across the node's allocation
- GC torture poisons what it frees; harnesses cap test-binary memory
- Root what the recover block reads
- Two bugs Go's own tests found: type aliases and zero-size addresses
- complex numbers, and go/ssa's nil-check intrinsic
- Measure rustygo against Go's own test/ directory: 34/141
- M1 status
- M1 status
- a non-trivial program, and two bugs it found
- maps
- interfaces, and panic/recover on top of them
- name the deferred-call type; docs for defer
- defer
- closures and func values
- slices
- a precise mark-sweep collector
- cgo is supported where a C toolchain exists; drop kintane
- M0 status
- measure shadow-stack root bookkeeping (0-8%)
- panic=unwind works on fullrust; no_std still open
- uint32(float) goes through int64 on aarch64 too
- structure control flow; publish benchmarks and compile scaling
- compile single-goroutine Go to Rust, checked against gc
- CI, crates.io, docs.rs and license badges
