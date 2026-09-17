# Roadmap

Milestones are ordered by what they de-risk, not by what is fun. Each one ends
with something runnable and a number to compare against gc Go.

## M0 — Spike (decides whether the rest happens)

Goal: the smallest end-to-end path, deliberately incomplete.

* `rustygo emit` over a single package: functions, `int`/`string`/`bool`,
  `if`/`for`, calls, structs by value.
* **No GC** — leak everything. **No goroutines.** **No stdlib**: a
  hand-written `println` in the runtime crate.
* Relooper for irreducible CFGs, exercised by a `goto` test.
* Benchmarks: a scalar loop, a struct-field loop, a string concat loop — gc Go
  versus rustygo versus Go-on-WASM.

**Exit criteria:** 20 hand-written programs produce identical stdout under gc
and rustygo; measured shadow-stack and fat-pointer overhead written into
[DESIGN.md §11](DESIGN.md#11-performance-expectations). If the gap looks
unrecoverable, stop and revisit
[option B](RATIONALE.md#b-go--wasm--translate-wasm-to-safe-rust).

## M1 — Runtime core

* Precise mark-sweep collector, shadow-stack roots, `Trace` derive, safe points.
* `Gc<T>`, `Ptr<T>` with interior pointers, `Slice<T>`, `GoStr`, `GoMap`,
  arrays, closures with traced environments.
* Interfaces: `TypeDesc`, vtables, type assertions and type switches.
* `defer` / `panic` / `recover` on Rust unwinding, with Go's panic messages.
* Single-threaded only; `go` statements still rejected.
* GC torture mode: collect at every safe point, poison freed objects.

**Exit criteria:** a non-trivial single-goroutine program (a `sort` + `strings`
exercise, a small interpreter) runs correctly; Go's `test/` directory passes for
the non-concurrent, non-reflect subset.

## M2 — Goroutines, channels, `sync`

* Stackful coroutines: context switch for x86-64 and aarch64, growable stacks.
* M:N scheduler, work stealing, `GOMAXPROCS`, cooperative preemption at safe
  points.
* `chan` with Go's fairness, `select` with random ready-case choice, `sync`
  (`Mutex`, `WaitGroup`, `Once`, `RWMutex`), `sync/atomic` on Rust atomics.
* Stop-the-world collection across threads.

**Exit criteria:** concurrency tests from Go's suite pass; a
producer/consumer benchmark within a stated factor of gc Go, with the factor
published rather than hidden.

## M3 — Standard library bring-up

* Compile the real stdlib with the `purego` tag.
* Reimplement the bottom layer: `runtime`, `syscall`, `os`, `reflect`
  internals, `internal/bytealg`-style packages.
* Netpoller on `epoll`/`kqueue`; `net` and `net/http` client working.
* Type descriptors complete enough for `fmt` and `encoding/json`.

**Exit criteria:** `fmt`, `strings`, `bytes`, `errors`, `sort`, `time`,
`encoding/json`, `net/http` (client) pass their own tests; published pass-rate
table per package.

## M4 — Interop, both directions

* `//go:rust` declarations with the bridge type set; handle types for Rust
  objects, with `Drop` wired to Go finalizers.
* `Runtime::new()` / `rt.enter()` for Rust programs embedding Go packages.
* Emitter-enforced bridge rules (no unpinned Go pointer outliving a call).
* **Proof of value:** call [purecrypto](https://github.com/KarpelesLab/purecrypto)
  from Go through rustygo and check it against `tss-lib`'s vectors; call
  [compcol](https://github.com/KarpelesLab/compcol) and compare with
  `compress/gzip`.

**Exit criteria:** one of the hand-maintained Go/Rust pairs can be retired in a
branch — the Rust implementation reached from Go, passing the Go side's tests.

## M5 — Targets

* fullrust static Linux binaries (needs `panic=unwind` on that toolchain).
* `wasm32`: blocking analysis instead of coroutines; single-threaded mode.
* `no_std` subset for purestd and kintane: single heap, no netpoller, no
  preemption.

**Exit criteria:** the M1 exercise program runs on fullrust, on WASM in a
browser, and on kintane.

## M6 — Performance

* Escape analysis → plain Rust locals, no `Gc`, no shadow-stack entry.
* Safe-point and bounds-check elision where `rustc` already proves the bound.
* Devirtualization of monomorphic interface calls.
* Hot primitives (`memmove`, `IndexByte`, hashing, `math/big`) delegated to Rust
  crates.
* Allocator: size classes, per-thread buffers; generational marking if measured
  to help.

**Exit criteria:** within 2× of gc Go on a published benchmark set, and faster
than Go-on-WASM everywhere.

## M7 — Real workload

* Build and run an actual KLB service (a `spotlib`-based daemon is the
  candidate) under rustygo, side by side with its gc build, on the same traffic.
* Document what is unsupported, in one list, without softening it.

## Non-goals for the first year

Race detector · `cgo` · async preemption of call-free loops ·
`reflect.StructOf` and runtime type construction · 32-bit targets ·
plugin/`go:linkname` tricks in third-party code · human-readable output.
