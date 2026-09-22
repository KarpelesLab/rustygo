# Roadmap

Milestones are ordered by what they de-risk, not by what is fun. Each one ends
with something runnable and a number to compare against gc Go.

## Throughout

Standing rules that apply from M0 on, instead of being added later:

* **One pinned Go release.** rustygo targets one Go version at a time: the
  current stable release when M0 starts. That version fixes three things:
  `go/ssa`, the standard library being compiled, and the `go:linkname`
  contract between that library and the reimplemented `runtime`. The contract
  changes every release, so moving to a newer Go is scheduled work with its own
  diff of stubs and linknames, not a dependency bump.
* **Differential harness in CI.** Every test program is built with gc and with
  rustygo and compared on stdout, stderr, exit status and the first line of any
  panic. Full goroutine traces are not compared. The harness exists before the
  first test program does. From M3 on it also has a service mode
  ([DESIGN §12](DESIGN.md#12-correctness-strategy)): start a server, drive it
  with a client, compare what comes back.
* **Tracked numbers, published in the repository:** runtime against gc Go,
  emitted Rust size, `rustc` wall time and binary size, plus GC pause times
  from M1 on. Exit criteria cite these numbers, so each is measured at every
  milestone and not just once.
* **Platforms: Linux, macOS and Windows** on x86-64 and aarch64. Linux comes
  first, and macOS and Windows ports land in M5. Windows diverges the most, so
  the M2 context switch and the M3 netpoller and `syscall` layer are designed
  for it from the start, not retrofitted:
  * a different callee-saved register set (`xmm6`–`xmm15` on x64);
  * TIB stack bounds to update on every switch;
  * IOCP completion I/O in place of readiness polling;
  * a `syscall` package built around handles and DLL calls.

## The yardstick: Go's own test suite

rustygo is as good as its pass rate on the tests the Go project already wrote
for itself. Hand-written programs and service tests catch the first bugs; Go's
own suite is what says the compiler is right. It is run in full from the
moment it can be:

* **Scope.** Two parts, both from the pinned Go release:
  * the `test/` directory, run under the same directives
    `cmd/internal/testdir` understands (`run`, `runoutput`, `build`, …);
  * `go test std`, the tests of every standard-library package.

  `cmd/...` is the gc toolchain's own test suite and is out of scope. Its
  programs are not: building `gofmt` and `vet` with rustygo is a free
  large-program test.
* **Runner.** `rustygo test` emits `test2json` output, and the harness compares
  per test against a gc run on the same machine, so tests that are flaky under
  gc are not charged to rustygo. `-short` runs on every commit and the full
  suite nightly.
* **No silent gc.** Many tests build helper programs through
  `internal/testenv`, which calls `go build` or `go run`. The runner puts a
  rustygo-backed `go` shim first on `PATH`, so those helpers are
  rustygo-built. Without it, part of the suite would quietly be testing gc.
* **Exclusions are few and justified.** A test may be excluded only if it checks
  gc's *implementation* and not Go's *behavior*. Allowed categories:
  * gc-specific runtime internals (stack copying, GC pacer internals,
    `internal/abi` layouts);
  * `errorcheck` tests of the gc compiler's diagnostic text;
  * assembly;
  * `-race`, and `cgo` until M4 lands it;
  * goroutine-trace formatting.

  Every exclusion is listed, with its category, in the repository. Anything
  else that fails counts as a failure, including `testing.AllocsPerRun`
  checks that need escape analysis (M6).
* **Pass rate published** per package and per test, from M1 on for `test/` and
  from M3 on for `std`, with the full list of failing tests. The number only
  goes up; a drop is a CI failure.

The end state is every non-excluded test passing on every supported platform.
Each milestone below states how far along that line it gets.

## Decision gates

[RATIONALE.md](RATIONALE.md#what-would-make-this-project-wrong) lists what
would make this project wrong. Each item is checked at a fixed point, and the
result is written down whichever way it goes:

| Risk (RATIONALE) | Checked at | Evidence |
|---|---|---|
| 1. Unrecoverable slowdown | end of M0 | spike benchmarks, with the M6 techniques pulled forward if needed |
| 3. `rustc` time and crate size | end of M0, again at end of M3 | M0: compile-time scaling per line of Go. M3: full-stdlib build and incremental rebuild times |
| 4. Duplication cheaper than assumed | before M2 starts | cost of the next planned Go/Rust pair against the remaining roadmap |
| 2. `reflect` impractical | end of M3 | `fmt` and `encoding/json` test pass rates |

## M0 — Spike (decides whether the rest happens)

Goal: the smallest end-to-end path, deliberately incomplete. The code it emits
already has the final shape, so the numbers it produces are meaningful.

* `rustygo emit` / `rustygo build` over a single package: functions, integer,
  `string` and `bool` types, `if`/`for`, calls, structs, and pointers to
  structs and fields (`&s.f`).
* Go's arithmetic edge cases, which cost little to get right now and a lot to
  find later:
  * wrapping overflow;
  * shifts by at least the operand's width, which give 0 or sign fill, where
    Rust masks the count instead;
  * negative shift counts, which panic;
  * `MinInt64 / -1`, which wraps without panicking;
  * division by zero, which panics;
  * float → int conversion, matched against gc on amd64.
* Control-flow structuring from `go/ssa` blocks. Reducible CFGs become
  `loop`/`if`/labeled `break` again. Irreducible ones (from `goto`) go through a
  dispatch loop, exercised by a `goto` test.
* **No collector:** everything leaks. The real shadow-stack pushes and `Ptr<T>`
  fat pointers are emitted anyway, so their cost is measured, not guessed.
  **No goroutines.** **No stdlib:** builtin `print`/`println` are implemented
  in the runtime crate.
* Settle the heap access model
  ([DESIGN §2](DESIGN.md#field-access-and-aliasing)): how generated code reads
  and writes through `Gc`/`Ptr` soundly when Go aliases freely. Every later
  milestone emits code in this shape.
* Answer [open question 2](DESIGN.md#13-open-questions): does
  `panic = "unwind"` work on fullrust? If not, M1 lowers `defer`/`recover`
  differently, and changing that decision later means rewriting core codegen.
  (`no_std` is an opt-in M5 target and gates nothing here.)
* Benchmarks: a scalar loop, a struct-field loop, a pointer-chasing loop and a
  string concat loop, gc Go against rustygo, native in both cases.

**Exit criteria:**

* 20 hand-written programs produce identical output under gc and rustygo. The
  harness compares both streams, because builtin `println` writes to stderr.
* Measured shadow-stack and fat-pointer overhead is written into
  [DESIGN.md §11](DESIGN.md#11-performance-expectations).
* Emitted-Rust size and `rustc` time per thousand lines of Go are recorded, with
  an extrapolation to the size of the stdlib.

If the gap looks unrecoverable, attack it in the compiler: pull escape analysis
and bounds-check elision forward from M6. Falling back on a WASM route is not an
option; it is ruled out on both performance and native access. If the compiler
work fails too, decision gate 1 says stop.

**Status (2026-09-19): exit criteria met, with one item carried into M1.**

* 26 programs match gc on stdout, stderr and exit status. CI runs them on
  Linux, macOS (arm64) and Windows.
* Benchmarks and compile scaling are in [BENCHMARKS.md](BENCHMARKS.md), and
  the findings are in [DESIGN §11](DESIGN.md#11-performance-expectations):
  * scalar, field and call code runs at gc's speed or faster, and pointer
    chasing within 1.1×;
  * shadow-stack upkeep costs 0–8%;
  * strings run at 2.2×, set by the leaking allocator;
  * about 4 minutes of release build for the whole stdlib, by extrapolation.
* Heap access model: settled for one thread (places with copy-in/copy-out,
  DESIGN §2). The multi-threaded half is open question 6, for M2.
* `panic = "unwind"` works on fullrust; `no_std` is untested and gates nothing
  before M5.
* Decision gate 1 is not in sight.
* Carried into M1: the cost of `Ptr` carrying its object's handle. The place
  model (DESIGN §2) replaced the planned byte-offset fat pointer, so there
  was nothing to measure without the collector.

## M1 — Runtime core

* Precise mark-sweep collector, shadow-stack roots, emitted `Trace` impls, and
  safe points at calls and loop back-edges. Every pointer store goes through one
  emitter choke point, so a write barrier can be added in M6 without
  redesigning codegen.
* Finalizers and weak references (`runtime.SetFinalizer`, `weak`). `unique`
  depends on them, and `net/netip` depends on `unique`.
* `Gc<T>`, `Ptr<T>` with interior pointers, `Slice<T>`, `GoStr`, `GoMap`,
  arrays, closures with traced environments.
* Interfaces: `TypeDesc`, vtables, type assertions and type switches.
* `defer` / `panic` / `recover` on Rust unwinding, or on the fallback lowering
  if M0 says so, with Go's panic messages.
* Stdlib plumbing. Even `strings` and `sort` pull in `internal/bytealg`,
  `internal/reflectlite`, `unsafe` and assembly stubs:
  * an overlay GOROOT that swaps in rustygo's `runtime`,
    `internal/reflectlite` and `sync/atomic`;
  * resolution of bodyless, assembly-backed functions: each one gets a Rust
    implementation or a redirect to the package's generic Go fallback;
  * resolution of the stdlib's `go:linkname` references into `runtime`;
  * leaf packages compiled for real: `unicode/utf8`, `math/bits`, `strings`,
    `strconv`, `sort`, `slices`.
* A minimal `//go:rust` call (scalars and `[]byte` passed into a Rust function).
  Interop is the reason for the whole project, so it gets exercised from the
  first runtime on, not first seen in M4.
* Single-threaded only; `go` statements are still rejected.
* GC torture mode: collect at every safe point, poison freed objects.

**Exit criteria:**

* A non-trivial single-goroutine program runs correctly: a `sort` + `strings`
  exercise, or a small interpreter.
* The `test/` directory's non-concurrent, non-reflect tests pass, and the
  pass rate for the whole directory is published.
* GC torture mode passes the same set.

**Status (2026-09-19): most of M1 is in.**

* Done: the collector (precise mark-sweep, shadow-stack roots, an object
  table that resolves interior pointers), slices, strings on the heap, maps,
  closures and func values, interfaces with dynamic dispatch and type
  switches, `defer`, `panic` and `recover`, and package globals as roots.
* `recover` follows Go's rule exactly, down to the cases Go's own tests call
  "here be dragons": a value only for the function a defer invoked, only that
  frame's own panic, panics nesting rather than replacing each other, and
  `defer recover()` recovering in one order and doing nothing in the other
  ([DESIGN §5](DESIGN.md#5-control-flow-defer-panic-recover)). A program with
  no `recover` pays nothing for it.
* 44 differential programs match gc, every one of them also under GC torture
  (a collection at every allocation), which is what checks that the emitted
  roots are complete.
* The exit program is there: `testdata/programs/interp`, a small expression
  language with a parser, an interface hierarchy, maps of variadic closures,
  and evaluation errors handled through `recover`.
* Not yet: `runtime.SetFinalizer` and weak references; Go's `test/` directory
  as a tracked pass rate; the standard-library plumbing.
* Standard-library plumbing: **done for the packages that do not need
  reflection or files.** `strings`, `strconv`, `sort`, `errors`, `unicode`,
  `math/bits` and `unicode/utf8` compile from the real GOROOT, unmodified,
  and match gc (`testdata/programs/stdlib1`). The overlay GOROOT
  (`internal/goroot`) swaps in rustygo's `runtime`, `internal/reflectlite`
  and the assembly half of `sync/atomic`; `go:linkname` is resolved in both
  directions; leaf assembly has Rust intrinsics; and package initializers
  skip building variables nothing reads, which took `strings.ToUpper` from
  140,605 emitted lines of Rust to 4,029.
* The next wall was M3's, not M1's, and it has since come down: see the M3
  status below — `reflect`, the file layer and `fmt` are in.

## M2 — Goroutines, channels, `sync`, timers

* Stackful coroutines: a context switch for x86-64 (System V) and aarch64
  (AAPCS64). The Windows x64 ABI is written and tested at the same time, even
  though the rest of the Windows port waits for M5. Stacks are fixed-size
  virtual reservations, committed lazily, with guard pages. Rust frames hold raw addresses into the stack, so Go-style stack
  copying is not available ([DESIGN §4](DESIGN.md#4-goroutines-and-the-scheduler)).
* A panic never unwinds across a context switch. It stops at the goroutine's
  entry frame after running defers. If unrecovered, it becomes Go's fatal
  `panic: …` exit for the whole process.
* M:N scheduler, work stealing, `GOMAXPROCS`, cooperative preemption at safe
  points, `runtime.LockOSThread`.
* Blocking calls. A goroutine entering a blocking syscall, or a long call into
  Rust, hands its scheduler slot to another thread and counts as parked at a
  safe point. Neither the other goroutines nor a stop-the-world collection
  wait for it. A monitor thread (Go's `sysmon`) takes slots back from calls that
  block without announcing it.
* Runtime timers on the scheduler: `time.Sleep`, `time.Timer`, `time.After`.
  Go's concurrency tests use them everywhere.
* `chan` with Go's fairness, `select` with random ready-case choice.
* The real `sync` package, compiled on the runtime-provided semaphores and
  notify lists its linkname hooks expect. `sync/atomic` on Rust atomics,
  including `atomic.Pointer` on two-word `Ptr<T>` values
  ([DESIGN §13](DESIGN.md#13-open-questions)).
* Stop-the-world collection across threads, with every goroutine's shadow stack
  as a root set.
* Deadlock detection: `all goroutines are asleep` is a fatal error, as in gc.

**Exit criteria:**

* Go's concurrency tests pass: `test/chan/` and the rest of `test/` that uses
  goroutines, plus the `sync`, `sync/atomic` and `context` package tests.
* A producer/consumer benchmark runs within a stated factor of gc Go, with the
  factor published instead of hidden.
* 100k parked goroutines fit in a stated memory budget.

## M3 — Standard library bring-up

* Compile the real stdlib with the `purego` tag, completing the inventory of
  bodyless functions and `go:linkname` references for every package it reaches.
* Reimplement the rest of the bottom layer: `runtime`, `syscall`, `os`,
  `reflect` internals, `internal/bytealg`-style packages.
* Netpoller on `epoll`, behind an interface that also fits `kqueue` and
  completion-based IOCP, so the M5 ports only add backends. `net`,
  `crypto/tls`, and `net/http` client and server on top of it.
* Process-level pieces services rely on: `os/signal`, `os/exec`, environment,
  arguments, exit codes.
* `runtime.Caller`/`Callers` and panic stack traces in Go terms: the emitter
  writes a Rust-line → Go-position table, which is resolved against debug info
  at run time. `log`, `testing` and error-wrapping packages all ask for callers.
* Type descriptors complete enough for `fmt` and `encoding/json`.
* `rustygo test`: test-main generation and the `testing` package, so the stdlib
  is measured by its own tests.
* Service tests. These end-to-end tests are cheap to write, and each one covers
  the netpoller, the scheduler, the collector under load, `crypto/tls` and
  `net/http` at once:
  * a `net/http` server built with rustygo is driven by a client built with gc;
  * the scenarios are plain HTTP, HTTPS, HTTP/2, keep-alive, streamed and large
    bodies, client timeouts and context cancellation, and graceful shutdown on
    `SIGTERM`;
  * every client/server pairing runs: gc×rustygo, rustygo×gc and
    rustygo×rustygo;
  * a load variant of the same scenarios reports latency and RSS against the
    gc build.
* Build caching. The compiled standard library becomes its own crate or crates,
  built once per Go version and target and then reused, so rebuilding a small
  program does not recompile `fmt`.

**Exit criteria:**

* The whole of `go test std` runs under rustygo, with the pass-rate table and
  the exclusion list published.
* `fmt`, `strings`, `bytes`, `errors`, `sort`, `time`, `encoding/json` and
  `net/http` pass all their non-excluded tests.
* `gofmt` and `vet`, built with rustygo, produce the same output as gc builds
  over the Go source tree.
* The service-test scenarios pass in every client/server pairing, and the load
  variant's latency and RSS against gc are published.
* Stdlib build time and a small program's incremental rebuild time are recorded
  (decision gate 3).

**Status (2026-09-22): `reflect`, the file layer and `fmt` are in.**

* `reflect` is rustygo's own package in the overlay, written in ordinary Go
  over 25 runtime questions about a type descriptor
  ([DESIGN §6](DESIGN.md#6-reflect-and-type-descriptors)). Struct field
  offsets are gc's, computed by go/types with gc's `types.Sizes`, so a struct
  reflects identically under both compilers.
* **`fmt` compiles and runs unmodified**: `%v`, `%+v`, `%#v`, `%T`, `%q`,
  `%x`, width and precision, `Stringer`, `error`, and `fmt.Errorf("%w")`
  wrapping, over structs, slices and maps — matching gc byte for byte
  (`testdata/programs/fmtbasics`).
* **The file layer reaches the kernel.** `internal/runtime/syscall.Syscall6`
  is a raw system call in Rust inline assembly (x86-64 and aarch64), and
  `syscall`, `os`, `io/fs` and `time` are the real packages above it. So
  `fmt.Println`, `os.Stdout`/`Stderr`, `os.Args`, `os.Getenv`, `os.Exit` with
  its exit hooks, and `time.Now`/`Sleep` all work
  (`testdata/programs/stdlib2`).
* Go's own `test/` directory is at **74 of 141 (52%)**, up from 41 before
  this work; the remaining failures are mostly channels and goroutines (M2),
  plus the parts of `reflect` listed below.
* Decision gate 2 (`reflect` impractical) is answered: not impractical. A
  descriptor per concrete type, with the map operations generated beside it,
  carries `fmt` without a single change to the standard library's source.
* Not yet: `Value.Call`/`New`/`Zero`/`MakeSlice`, `Type.Method`/`Implements`,
  `encoding/json`, the netpoller and `net/http`, `rustygo test` and the
  service tests.

## M4 — Interop, both directions

* `//go:rust` declarations with the full bridge type set. Handle types for Rust
  objects, with `Drop` wired to Go finalizers.
* Crate versions. A `//go:rust` declaration names a crate, so something must pin
  its version: a manifest next to `go.mod`, resolved by Cargo, so the Rust side
  of a Go build is reproducible.
* Cargo integration for the other direction: a `build.rs` helper, so a Cargo
  project can depend on Go packages and build them with a plain `cargo build`.
* **cgo** ([DESIGN §9a](DESIGN.md#9a-cgo)), on targets with a C toolchain:
  load packages with `CGO_ENABLED=1`, so `go/packages` does the cgo
  processing; bind the generated `_Cfunc_*` symbols to Rust `extern "C"`
  declarations; compile the package's C through the generated crate's build
  script, honoring `#cgo CFLAGS`/`LDFLAGS`; treat each C call as a blocking
  call. C calling back into Go waits for the scheduler.
* `Runtime::new()` / `rt.enter()` for Rust programs embedding Go packages.
* Bridge rules enforced by the emitter, so no unpinned Go pointer outlives a
  call. Long-running Rust calls enter the M2 blocking-call state.
* **Proof of value:** call [purecrypto](https://github.com/KarpelesLab/purecrypto)
  from Go through rustygo and check it against `tss-lib`'s vectors; call
  [compcol](https://github.com/KarpelesLab/compcol) and compare with
  `compress/gzip`.

**Exit criteria:**

* A Go package that `import "C"`s builds and passes its tests on a target with
  a C toolchain; `net` and `os/user` work in cgo mode as well as pure-Go mode.
* One of the hand-maintained Go/Rust pairs can be retired in a branch, in each
  direction:

  * the Rust implementation, called from Go, passes the Go side's tests;
  * for another pair, a Rust program calls the Go implementation and passes
    the Rust side's tests.

## M5 — Targets

* Native macOS (x86-64, aarch64): `kqueue` netpoller, Darwin `syscall` layer.
* Native Windows (x86-64, aarch64): IOCP netpoller, Windows context switch
  (TIB stack bounds, guard pages via `VirtualAlloc`), and the Windows `syscall`
  package. That package includes `SyscallN` and lazy DLL procedures, which
  `golang.org/x/sys/windows` and most Windows Go code call through.
  `windows-msvc` is the Rust target, so panics unwind through SEH.
* fullrust static Linux binaries (needs `panic=unwind` on that toolchain; M0
  already checked it).
* `no_std` subset for purestd: single heap, no netpoller, no preemption, fixed
  small stacks where there is no lazy commit.
* These targets are opt-in, and give up cgo (M4) and everything else that
  needs a C toolchain. The default build stays on the platform's usual Rust
  target.

**Exit criteria:**

* The `test/` and `std` pass-rate tables are published for macOS and Windows,
  within a stated margin of Linux.
* The M3 service tests pass on macOS and Windows. Those runs exercise the
  `kqueue` and IOCP netpollers end to end.
* The M1 exercise program runs on fullrust and on a `no_std` purestd build,
  from the same source, with no target-specific code in the program itself.

## M6 — Performance

* Escape analysis → plain Rust locals, no `Gc`, no shadow-stack entry.
* Bounds-check elision where `rustc` can prove the bound.
* Safe-point elision only on loops with a bounded trip count. Every other loop
  keeps its back-edge poll, which is why async preemption stays a non-goal.
* Devirtualization of monomorphic interface calls.
* Hot primitives delegated to Rust crates: `memmove`, `IndexByte`, hashing,
  `math/big`, and the `crypto/*` cores (AES, SHA, field arithmetic) through
  purecrypto.
* Allocator: size classes and per-thread buffers. Generational marking only if
  measurements show it helps.
* Collector: parallel marking. Concurrent marking behind the M1 write-barrier
  choke point, if pause times turn out to matter more to services than
  throughput.

**Exit criteria:**

* Within 2× of gc Go on a published benchmark set, measured natively, and no
  benchmark more than 4× off.
* p99 GC pause stated for a heap-heavy benchmark.
* The `testing.AllocsPerRun` tests in `std` pass. They count heap allocations,
  so they are a direct check of escape analysis.

## M7 — Real workload

* Build and run an actual KLB service (a `spotlib`-based daemon is the
  candidate) under rustygo, side by side with its gc build, on the same traffic.
* Document the debugging story honestly. Delve does not work. gdb and `perf`
  see Rust frames, and crash backtraces map back to Go positions through the M3
  table.
* Document what is unsupported in one list, without softening it.

**Exit criteria:**

* The service runs on production traffic next to its gc build for two weeks.
* Latency (p50/p99), RSS and CPU are compared and published.
* The unsupported list is published. It includes every test in `test/` and
  `std` that still fails, one by one, next to the exclusion list.

## Non-goals for the first year

Race detector · WASM targets · async preemption of call-free loops ·
`reflect.StructOf` and runtime type construction · 32-bit targets ·
plugin/`go:linkname` tricks in third-party code · human-readable output ·
Go versions other than the pinned one ·
`runtime/pprof`, `runtime/trace` and Delve support (stubs that compile, so
importing `net/http/pprof` does not break a build).
