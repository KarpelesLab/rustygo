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
  first test program does.
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
  `panic = "unwind"` work on fullrust and on a `no_std` kintane build? If not,
  M1 lowers `defer`/`recover` differently, and changing that decision later
  means rewriting core codegen.
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

## M1 — Runtime core

* Precise mark-sweep collector, shadow-stack roots, `Trace` derive, and safe
  points at calls and loop back-edges. Every pointer store goes through one
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
* The `// run` tests in Go's `test/` directory pass for the non-concurrent,
  non-reflect subset, with the pass rate published.
* GC torture mode passes the same set.

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

* Concurrency tests from Go's suite pass.
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
* Build caching. The compiled standard library becomes its own crate or crates,
  built once per Go version and target and then reused, so rebuilding a small
  program does not recompile `fmt`.

**Exit criteria:**

* `fmt`, `strings`, `bytes`, `errors`, `sort`, `time`, `encoding/json` and
  `net/http` pass their own tests, with a published pass-rate table per package.
* Stdlib build time and a small program's incremental rebuild time are recorded
  (decision gate 3).

## M4 — Interop, both directions

* `//go:rust` declarations with the full bridge type set. Handle types for Rust
  objects, with `Drop` wired to Go finalizers.
* Crate versions. A `//go:rust` declaration names a crate, so something must pin
  its version: a manifest next to `go.mod`, resolved by Cargo, so the Rust side
  of a Go build is reproducible.
* Cargo integration for the other direction: a `build.rs` helper, so a Cargo
  project can depend on Go packages and build them with a plain `cargo build`.
* `Runtime::new()` / `rt.enter()` for Rust programs embedding Go packages.
* Bridge rules enforced by the emitter, so no unpinned Go pointer outlives a
  call. Long-running Rust calls enter the M2 blocking-call state.
* **Proof of value:** call [purecrypto](https://github.com/KarpelesLab/purecrypto)
  from Go through rustygo and check it against `tss-lib`'s vectors; call
  [compcol](https://github.com/KarpelesLab/compcol) and compare with
  `compress/gzip`.

**Exit criteria:** one of the hand-maintained Go/Rust pairs can be retired in a
branch, in each direction:

* the Rust implementation, called from Go, passes the Go side's tests;
* for another pair, a Rust program calls the Go implementation and passes the
  Rust side's tests.

## M5 — Targets

* Native macOS (x86-64, aarch64): `kqueue` netpoller, Darwin `syscall` layer.
* Native Windows (x86-64, aarch64): IOCP netpoller, Windows context switch
  (TIB stack bounds, guard pages via `VirtualAlloc`), and the Windows `syscall`
  package. That package includes `SyscallN` and lazy DLL procedures, which
  `golang.org/x/sys/windows` and most Windows Go code call through.
  `windows-msvc` is the Rust target, so panics unwind through SEH.
* fullrust static Linux binaries (needs `panic=unwind` on that toolchain; M0
  already checked it).
* `no_std` subset for purestd and kintane: single heap, no netpoller, no
  preemption, fixed small stacks where there is no lazy commit.

**Exit criteria:**

* The M3 per-package pass-rate table is published for macOS and Windows,
  alongside Linux.
* The M1 exercise program runs on fullrust and on kintane, from the same source,
  with no target-specific code in the program itself.

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
* The unsupported list is published.

## Non-goals for the first year

Race detector · `cgo` · WASM targets · async preemption of call-free loops ·
`reflect.StructOf` and runtime type construction · 32-bit targets ·
plugin/`go:linkname` tricks in third-party code · human-readable output ·
Go versions other than the pinned one ·
`runtime/pprof`, `runtime/trace` and Delve support (stubs that compile, so
importing `net/http/pprof` does not break a build).
