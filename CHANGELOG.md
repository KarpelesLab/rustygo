# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.4](https://github.com/KarpelesLab/rustygo/compare/v0.0.3...v0.0.4) - 2026-10-07

### Other

- CI has been red since October 4th, and this is why
- The M:N scheduler, landing at one worker
- What more than one worker gets wrong is memory, not a context
- The parallel program runs at the GOMAXPROCS it is given
- The roadmap says what more than one worker still gets wrong
- reflect's TryRecv had the race select had
- One worker by default, and what is known about why
- A collector that loses a race was still holding its roots on its thread
- Two workers by default, until three stop corrupting a context
- Which goroutine is leaving is the scheduler's to say, not the caller's
- Stopping the world, and GOMAXPROCS at the number of CPUs
- Goroutines on worker threads, and a park that cannot lose a wakeup
- Runtime state that belongs to a thread, and state that belongs to the program
- 74 programs, and the whole set passes under torture as well as plainly
- reflect.ArrayOf, answered from the arrays a program already has
- A struct holding a func value does not have gc's layout
- The standard descriptors, closed by their own finalizers
- 127 of Go's own tests, with recover.go whole
- recover sees through the frames that stand in for a deferred call
- The missing descriptor that is not missing
- A timer that must fire while a goroutine spins, not only while it parks
- HTTP/2, and what the BoGo suite says
- What crypto's own tests say, and the two things crypto/tls still trips on
- HTTP/2 over TLS, and large bodies with it
- The clock syscall reads directly, and the bridge into libc it never crosses
- reflect.MakeFunc, which is Value.Call run backwards
- A named map or channel that contains itself, through the same name
- A pointer the collector cannot see, because it became an integer
- The metrics and the GC statistics the collector can actually answer
- Why an allocation test fails, now that it has been measured
- gRPC, protobuf, Prometheus and pgx compile, so the table should say so
- The three tables, measured on this tree instead of three trees ago
- What the threaded scheduler found before it found its own bug
- A map lookup keyed on bytes does not need the string either
- One copy where string and []byte conversions made two
- The HTTPS program makes its own certificate
- The runtime hooks that each stopped one package building
- A named func type that returns itself
- one document for the packages a sweep had time for and the rest
- `noasm`, which is what libraries outside the standard library call `purego`
- which failures to count apart is the report's judgement, not the sweep's
- What the front page claims, brought back to what is true
- One conversion, nine comparisons, and none of them made a string
- Where the allocations rustygo makes and gc does not actually are
- How a program outside the standard library reaches the kernel
- Comparing bytes as a string without making the string
- The types reflect can be asked to make, and the one it can make
- Channels through reflection, and what a type says about ranging
- What the compiler can express, measured for the whole library at once
- measure what cannot be built, and in what order to fix it
- Sleep gives up the processor however short it is
- A read deadline is a moment, and it was being read as a duration
- A differential program for the file layer
- $GODEBUG reaches the standard library, and SIGSYS can be ignored
- A tick says when it was due, a reset does not queue a timer twice, and a
- A goroutine that only makes system calls lets the others run
- A loop that does not wait for rustc
- The same system call on every thread, where there is one
- Signals reach Go, and a broken standard output ends the program
- What os, time and net/url could not even type-check
- The FIPS module's XOR, under Rust's rules about alignment
- A big array is filled in place, not built on the stack first
- HTTPS, end to end, in one process
- A run cap that a TLS handshake fits inside under GC torture
- encoding/json passes its own test suite
- A named type that contains itself
- Do not commit the worktrees agents work in
- Build one package at a time
- Goexit through a frame that defers, and a test binary's own directory

## [0.0.3](https://github.com/KarpelesLab/rustygo/compare/v0.0.2...v0.0.3) - 2026-09-29

### Other

- Calling a function through reflection
- A linkname on a variable, and a table of what the standard library's tests say
- rustygo test: the standard library's own tests
- Say what works now
- Starting a process, and a method nobody could find
- A frame that was returning normally when its own defer panicked
- Methods through reflection, and the pair of func values each one needs
- A defer that outlives its loop body, and what unsafe.Slice refuses
- Waiters by address, and three things chanlinear was really failing on
- Finalizers, and the phase of the collector that orders them
- net/http, client and server, and the livelock in the way
- One crate per band, and constant tables as static data
- ReadMemStats reports what the collector knows
- Untyped constants that reach the emitter, and reflect's two headers
- Five bugs Go's own tests found, one of them the collector's
- the netpoller, timers, and what net asked for on the way
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
