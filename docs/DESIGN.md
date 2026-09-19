# rustygo design

Working document. Everything here is a decision to be validated by the M0 spike,
not a settled fact.

## 1. Pipeline

```
go/packages ──► go/types ──► go/ssa ──► lowering passes ──► Rust emitter ──► rustc
```

* **Front end: `golang.org/x/tools/go/ssa`.** Typed, already in SSA form, with
  `defer`, `panic`, closures, interfaces and generics resolved into instructions.
  TinyGo takes the same input, which is the strongest available evidence that it
  is enough.
* **Not the gc compiler's SSA.** By that point structured control flow is gone,
  and Rust has no `goto`. `go/ssa` keeps blocks and phis, and the emitter
  rebuilds them into labeled blocks and loops (`break 'b`, `continue 'l`),
  following Ramsey's "Beyond Relooper". Only irreducible CFGs (a `goto` into
  a loop body) fall back to a loop around a `match` on the block index. M0
  measured why structuring is mandatory: LLVM compiles that fallback to an
  indirect jump per basic block, which cost 6× on a field-update loop.
* **Unit of output:** one Rust module per Go package, one crate per build. Go
  import cycles are impossible, so module ordering is a topological sort. The
  exception is the standard library: from M3 on it is its own crate (or
  crates), built once per Go version and target and cached. Recompiling `fmt`
  on every build would make the toolchain unusable. Finer crate granularity for
  user packages is a later optimization.
* **One published crate.** The runtime is the single `rustygo` crate on
  crates.io, with cargo features (`std`, `gc-torture`, …) in place of
  sub-crates. Generated crates, including the cached stdlib, depend on it and
  are build artifacts that are never published. The emitter writes every
  `impl Trace` itself, so there is no derive macro and no proc-macro crate.
* **The compiler is a Go module** (`github.com/KarpelesLab/rustygo`) in the same
  repository, because `go/ssa` is a Go library. The release tag `vX.Y.Z`
  versions both halves at once.

### Tool UX

```sh
rustygo build ./cmd/app          # -> ./out/app (via rustc)
rustygo emit  ./cmd/app          # -> ./out/crate/  (inspectable Rust)
rustygo test  ./...              # go test semantics, run through rustc
```

`GOARCH` stays a real value (64-bit: `amd64`/`arm64` type sizes) so `go/packages`
resolves the standard library normally. Target selection rides on a `rustygo`
build tag, plus `rustygo_nostd` for the constrained targets, so
packages can ship `foo_rustygo.go`. This mirrors what TinyGo does and avoids
teaching `go list` a GOARCH it rejects.

## 2. Type mapping

| Go | Rust | Notes |
|---|---|---|
| `bool`, `int8`…`int64`, `uint…`, `float32/64` | `bool`, `i8`…`i64`, `u…`, `f32/f64` | all arithmetic via `wrapping_*`; `/` and `%` check for zero and panic |
| `int`, `uint`, `uintptr` | `i64`, `u64`, `u64` | 64-bit only for now |
| `complex64/128` | runtime `Complex32/64` | |
| `string` | `GoStr { data: Gc<[u8]>, off: u32, len: u32 }` | **not** `String`: Go strings are arbitrary bytes |
| `[]T` | `Slice<P>`: pointer, len, cap over places of `T` | aliasing, reslicing and `append` growth as in Go |
| `[N]T` | `[T; N]` | value type, copied on assignment |
| `map[K]V` | `Gc<GoMap<K, V>>` | runtime hash map, randomized iteration order |
| `chan T` | `Gc<Chan<T>>` | runtime, integrates with scheduler |
| `func(...)` | `Func<fn(Env, ..) -> ..>` | code pointer + environment; the captured env is a traced place struct |
| `struct` | `struct` + emitted `impl Trace` | field order preserved; `Gc<T>` when heap-allocated |
| `*T` | `Ptr<T>` | see interior pointers |
| `interface{...}` | `Iface { typ: &'static TypeDesc, val: Word }` | vtable hangs off `TypeDesc` |
| `unsafe.Pointer` | `Ptr<Opaque>` | restricted; see §7 |
| generics | monomorphized by `go/ssa` | no Rust generics needed |

### Values, places and interior pointers

Every Go type has two Rust forms (implemented in M0, `src/value.rs` and
`src/place.rs`):

* The **value** form is a plain `Copy` type. Go values copy bitwise and have
  no destructors, so this always works.
* The **place** form is addressable storage for that value. Scalars, strings
  and pointers live in a `Slot<T>` (a cell accessed only by copying in and
  out). A struct lives in an emitted struct with one place per field. An
  array `[N]T` lives in `[P; N]`.

A Go pointer is a `Ptr<P>`, a reference to a place, with nil as its zero value.
`&s.Field` and `&arr[i]` therefore need no offsets and no layout descriptors:
they are ordinary pointers to the field's or element's own place. `&slice[i]`
works the same way from M1. Loading `*p` for a whole struct reads each field
place, and storing writes each one.

This replaces the byte-offset fat pointer of the first draft
(`Ptr { base, offset }`, with runtime bounds checks against a layout
descriptor). Place types keep every access typed and need no layout checks.
In M1 a `Ptr` also carries the handle of its enclosing object, which is what
keeps the whole object alive; the place reference stays as it is. Escape
analysis (M6) turns non-escaping places back into plain Rust locals.

### Field access and aliasing

Go lets any number of pointers reach the same object and write through all of
them, even from two goroutines at once. Rust's `&mut` forbids exactly that. In
Rust, a data race through non-atomic accesses is undefined behavior, even where
Go gives it a defined (if unhelpful) meaning for word-sized values. The
runtime's accessor API has to be sound for *any* code the emitter produces, so:

* No Rust reference into Go storage is handed to generated code except the
  `&'static` place references inside `Ptr`, and those allow only `load` and
  `store`, which copy. `p.x = f(p)` is a call, then a store; nothing is
  borrowed across the call. (M0 does exactly this: slots are `Cell`s.)
* Concurrent access is still open. Plain loads and stores are UB under a Go data
  race; relaxed atomics for every heap word are sound but block some
  optimizations. M0 measures the difference and decides (§13).

## 3. Memory and the collector

* **Precise tracing collector**, not reference counting: Go programs make cycles
  routinely, and `Rc` plus a cycle collector was rejected as both slow and
  single-threaded.
* **Roots:** generated functions maintain a shadow stack of `Gc` handles. Cheap
  push/pop, no stack maps, no `unsafe` in generated code. Escape analysis keeps
  non-escaping values out of it entirely.
* **Safe points** at every call, every allocation and every loop back-edge —
  enough for a stop-the-world collector that never interrupts a thread
  mid-object. A value needs a root slot only if it is live across one of
  these, or is handed to one; a liveness pass decides
  (`internal/emit/roots.go`).
* **Objects are entries in a table**, not headers in front of the payload:
  address range, layout, mark bit and trace function. The table is what makes
  interior pointers work — `&s.f`, `&a[i]` and a substring are ordinary
  one-word pointers, and marking resolves any address back to its object by
  binary search. Words that resolve to nothing (a string literal in the
  binary, nil, a non-heap address) are skipped, so no `Ptr` needs a second
  word for its base.
* **Phase 1 (M1, implemented):** stop-the-world mark-sweep, one heap, one
  allocation per object through the system allocator. Correct and boring, and
  measurably so: the table costs a per-object entry and a sort per
  collection ([§11](#11-performance-expectations)).
* **GC torture** (`RUSTYGO_GCTORTURE=1`, the runtime's `gc-torture` feature)
  collects at every allocation. The whole differential suite passes under it,
  which is what checks that the emitted roots are complete.
* **Phase 2 (M6):** a size-class allocator with per-thread buffers, which
  also replaces the table's binary search with address arithmetic, then
  generational or incremental marking if measurements demand it. Incremental
  marking needs a write barrier, which is why every pointer store goes through
  a single emitter path from the start.
* **Finalizers / weak refs:** `runtime.SetFinalizer`, Go 1.24's `weak`, and
  `unique` ride on the collector's post-mark phase.

## 4. Goroutines and the scheduler

* **Stackful coroutines.** Every Go function may block, so colouring functions
  `async` infects nearly the whole program and forces boxed futures for
  recursion. A context switch (per-architecture assembly in the runtime) keeps
  generated code straight-line and lets any function block anywhere.
* **M:N scheduler** over OS threads, work-stealing run queues, `GOMAXPROCS`
  honored. Preemption is cooperative at safe points, which are calls and loop
  back-edges (§3), so every loop stays preemptible as long as its back-edge
  poll remains. Go's signal-based async preemption is out of scope. For the
  same reason, M6 may elide safe points only in loops with a bounded trip count.
* **Stacks are reserved, not grown.** Go grows a stack by copying it and
  rewriting every pointer into it, which requires knowing where all those
  pointers are. Rust frames hold raw addresses of locals that no compiler
  reports: generated code, the runtime, `std`, and any crate reached through
  interop all do it. So each goroutine instead gets a fixed virtual reservation
  (configurable, and generous, since only touched pages cost memory),
  committed lazily by the OS, with a guard page below it. Overflow hits the
  guard page, and `rustc`'s stack probes guarantee it cannot be skipped over;
  the result is Go's fatal `stack exceeds limit` error. Stacks of exited
  goroutines are pooled, and their pages are released with `madvise`
  (`VirtualFree(MEM_DECOMMIT)` on Windows).
  Segmented stacks, which Rust itself tried and dropped, are not revisited.
  Cost: recursion depth is capped by the reservation, not by
  `debug.SetMaxStack`, and a goroutine that once went deep keeps those pages
  until it exits.
* **Blocking calls** (syscalls, long calls into Rust) hand the thread's
  scheduler slot to another thread and count as a safe point, so they block
  neither other goroutines nor a stop-the-world collection.
* **Channels and `select`** live in the runtime: same FIFO fairness, same
  blocking semantics, same random choice among ready cases.
* **Netpoller:** `epoll`/`kqueue`/IOCP (or Rust `std` blocking threads on
  constrained targets) driving the same parking primitives as channels.

## 5. Control flow, `defer`, `panic`, `recover`

* `defer` → a per-frame defer list in the runtime; frames that own defers are
  wrapped so the list runs on both normal return and unwinding.
* `panic` → a Rust panic carrying a `GoPanic` payload; `recover` inspects the
  payload from inside a deferred call. Requires `panic = "unwind"`.
* Runtime panics (nil dereference, index out of range, divide by zero, failed
  type assertion, closed-channel send) map to the same payload with Go's
  messages, because programs match on them.
* `goto` and labeled `break`/`continue` come out of `go/ssa` as ordinary CFG
  edges and are reconstructed by the structuring pass (§1).

## 6. `reflect` and type descriptors

`reflect` is not optional: `fmt` and `encoding/json` are in every program.

* Emit a `TypeDesc` for every type the program instantiates: kind, size, field
  names and offsets, tags, method table, element/key types.
* `reflect.Value` becomes `(&'static TypeDesc, Ptr<Opaque>)` — exactly the
  runtime's existing object model, so field and element access reuses the same
  bounds-checked paths.
* Method values and `reflect.Call` need a generated universal call shim per
  signature.
* **Known gap:** `reflect.StructOf` / `MapOf` and other runtime type
  construction need heap-allocated descriptors; possible, but deferred.

## 7. `unsafe.Pointer` and package `unsafe`

Supported, narrowly:

* `unsafe.Sizeof`/`Alignof`/`Offsetof` — compile-time constants.
* `unsafe.Slice`, `unsafe.String`, `unsafe.SliceData`, `unsafe.StringData` —
  expressible on the object model, so they work.
* Round-tripping a pointer through `uintptr` arithmetic to reach another object,
  or reinterpreting a struct as unrelated bytes, is rejected at compile time
  with a clear error. This breaks some packages; that is the price of a safe
  generated-code guarantee.

## 8. Standard library

* Compile the **real Go standard library** through the same pipeline, with the
  `purego` build tag so assembly-backed packages take their pure-Go paths.
* Reimplement only the bottom: `runtime`, `runtime/internal/*`, `sync/atomic`,
  `syscall`, the `os` and `net` syscall layers, `reflect` internals, and the
  `internal/bytealg`-style packages that assume assembly.
* **Assembly without a `purego` path.** With a real `GOARCH`, packages such as
  `math` and `internal/bytealg` select files that declare bodyless functions,
  whose bodies live in `.s` files. Each one gets a Rust implementation or a
  redirect to the package's generic Go fallback. The same inventory covers the
  stdlib's `go:linkname` references into `runtime`. That contract changes with
  every Go release, so rustygo pins one Go version at a time. The replacement
  packages are substituted through an overlay GOROOT, as TinyGo does.
* `syscall` sits on Rust `std` — which is what makes fullrust and purestd
  reachable without a second port.
* Packages with both a cgo and a pure-Go path (`os/user`, `net`'s resolver)
  take the pure-Go path by default. With cgo enabled (§9a) on a target that
  has a C toolchain, they take their cgo path instead; on the libc-free
  targets that choice does not exist.

## 9. Interop, both directions

**Go calling Rust** — a declaration, no binding generator:

```go
//go:rust crate="purecrypto" path="sha2::sha256"
func sha256(data []byte) [32]byte
```

The emitter produces a wrapper that converts across the bridge type set:
scalars, `[]byte`/`string` (as `&[u8]`/`&str` for the duration of the call),
fixed arrays, and opaque handles for Rust objects (`Gc`-owned, with a Go
finalizer calling Rust's `Drop`).

**Rust calling Go** — the generated crate exports plain Rust items:

```rust
let out = rustygo_std::strconv::quote(rustygo::str("hi"));
```

(`rustygo_std` is the generated, cached stdlib crate from §1, not a published
one.)

Go values stay `Gc`-managed. A Rust caller must hold a runtime handle (which
starts the scheduler and collector), and the API mirrors that: `Runtime::new()`,
then `rt.enter(|| ...)`.

**Bridge rules:** no Go pointer is handed to Rust for longer than a call unless
it is pinned; no Rust reference is stored in a Go value except as a handle. Both
are enforced by the emitter, not by convention.

## 9a. cgo

Go→Rust needs no C, but plenty of Go code calls C on its own, and on a target
with a C toolchain there is no reason to refuse it. `import "C"` is therefore
supported (M4), and unavailable only on the libc-free targets (§10).

* **No second cgo implementation.** With `CGO_ENABLED=1`, `go/packages` already
  hands back the *cgo-processed* package: the `import "C"` file is replaced by
  generated Go in which each C call is an ordinary call to a bodyless
  `_Cfunc_*` function. The front end sees normal Go.
* **The emitter binds those symbols** to Rust `extern "C"` declarations, which
  is the same bodyless-function machinery the stdlib's assembly stubs need
  (§8).
* **The C itself** (the `import "C"` preamble and any `.c` files in the
  package) is compiled by the generated crate's build script, with the
  package's `#cgo CFLAGS`/`LDFLAGS` passed through. Linking C is something
  Cargo does natively.
* **Pointer rules come for free.** Go already forbids passing Go pointers to C
  that point at Go pointers, and forbids C keeping them after the call. Our
  places (§2) hand C a raw address for the duration of a call, which is exactly
  what those rules permit.
* **A C call is a blocking call** (§4): it hands off its scheduler slot, and
  counts as a safe point, so a long or blocking C function stalls neither the
  other goroutines nor a collection.
* **Deferred:** C calling back into Go (`//export`, function pointers into Go),
  which needs a goroutine and stack to run the callback on, so it waits for the
  scheduler (M2).

## 10. Targets

| Target | Status of plan |
|---|---|
| Native Linux, x86-64 + aarch64 | primary |
| Native macOS, x86-64 + aarch64 | M5 (kqueue netpoller, Darwin `syscall`) |
| Native Windows, x86-64 + aarch64 | M5 (IOCP netpoller, Windows x64 context switch with TIB stack bounds, `SyscallN`/DLL `syscall` package, `windows-msvc` with SEH unwinding) |
| [fullrust](https://github.com/KarpelesLab/fullrust) static Linux | opt-in; works (M0 checked `panic=unwind` there). No cgo |
| `no_std` + [purestd](https://github.com/KarpelesLab/purestd) | opt-in subset: no goroutine preemption, single heap, no netpoller, no cgo |

The native targets are the default, and they keep a C toolchain within reach,
which is what makes cgo possible (§9a). The libc-free targets trade that away.

## 11. Performance expectations

Slower than gc Go at first, and honest about why: shadow-stack pushes, fat
pointers, runtime-mediated field access, no assembly in the stdlib. The
recovery path, in order of expected payoff:

1. Escape analysis → plain Rust locals and `[T; N]` arrays, no `Gc`.
2. Bounds-check and safe-point elision on loops `rustc` can already prove.
3. Devirtualization of monomorphic interface calls.
4. Hot stdlib primitives (`memmove`, `IndexByte`, hashing) delegated to Rust
   crates instead of pure-Go loops.

Target to beat before claiming anything: gc Go on the same benchmark set.
There is no slower floor to hide behind — WASM is not a fallback here, so
"within a stated factor of gc Go" is the only measure that counts.

### Measured in M1

With the collector in ([BENCHMARKS.md](BENCHMARKS.md), linux/amd64):

* Code that does not allocate is unchanged: scalar 0.96×, calls 0.58×.
* Pointer chasing is 1.33× gc, and a million-node list costs 63 MiB against
  gc's 21 MiB. Both are the M1 allocator: one `malloc` and one table entry
  per object, against gc's size-classed spans.
* String building is 4.4× gc — 2 million short-lived allocations, each with a
  threshold check, a table push, and a share of the sort every collection
  does. Memory is bounded now, which is the point of M1: 72 MiB against M0's
  850 MiB for the same program.
* The fix for both is the phase-2 allocator (§3), not the code generator. M1's
  bar is correctness under GC torture; M6's is the 2× target.

### Measured in M0

From [BENCHMARKS.md](BENCHMARKS.md) (linux/amd64; regenerate with
`go run ./internal/cmd/bench -o docs/BENCHMARKS.md`):

* **Straight-line code is at parity or better.** Scalar arithmetic runs at
  0.96× gc, field updates through pointers at 0.97×, and recursive calls at
  0.53× (LLVM inlines and unrolls where gc does not). Neither the
  copy-in/copy-out places (§2) nor the thin M0 `Ptr` show a measurable cost
  here.
* **Pointer chasing** runs at 1.07×.
* **Strings** run at 2.2×, and M0's leaking allocator dominates: every
  intermediate string is a fresh allocation that is never freed (850 MiB peak
  against gc's 10 MiB). M1's collector is the fix, not the code generator.
* **Shadow stack: 0–8%.** Built with `RUSTYGO_SHADOWSTACK=1`, each function
  links a frame of root slots into a per-thread chain (`src/gc.rs`), and
  stores a value in its slot when it is defined. Only values that hold a
  reference *and* are live across a safe point (a call, an allocation or a
  loop back-edge) get a slot, which a liveness pass decides
  (`internal/emit/roots.go`). That leaves few: 1 slot in the field loop, 3 in
  the linked-list walk, none in `fib`. Rooting every pointer-typed value
  instead cost 2.3× on the field loop, because each `&p.X` got a slot write.
  References inside struct values are not rooted yet, so this is a lower
  bound.
* **Not yet measured:** a `Ptr` that also carries its object's handle, which
  arrives with the collector in M1.
* **Compile time** is about 275 ms of `cargo build --release` per thousand
  lines of Go, with emitted Rust around 6× the Go line count. Linear
  extrapolation puts the whole standard library near 4 minutes, which is
  fine for a build cached per Go version and target (M3), and nowhere near
  decision gate 3.

Decision gate 1 (an unrecoverable slowdown) is not in sight: nothing here is
beyond 2.2×, and the one outlier has a known cause outside the emitter.

## 12. Correctness strategy

* **Differential testing:** build every test program twice — gc and rustygo —
  and compare stdout, exit status and panic text.
* **Service tests:** the same comparison for programs that serve rather than
  print. The harness starts a server, waits for its port and drives it with a
  scripted client, comparing status codes, headers (minus `Date` and similar)
  and bodies against the gc build.
  * Clients and servers are also mixed across compilers (gc client against
    rustygo server, and the reverse). A symmetric bug, such as a TLS mistake
    made identically on both ends, passes when rustygo talks to itself but not
    when it talks to gc.
  * The driver is built with gc by default, so a failure points at the rustygo
    side.
  * A load variant of each scenario doubles as a scheduler and GC stress test,
    and yields latency numbers against gc for free.
* **Go's own test suite** is the primary measure of correctness: the `test/`
  directory plus `go test std`, for the pinned release. A rustygo-backed `go`
  shim sits first on `PATH`, so tests that build helper programs build them
  with rustygo. Results are compared per test against gc on the same machine.
  Exclusions are allowed only for tests of gc's implementation, not of Go's
  behavior, and each one is listed. See
  [ROADMAP](ROADMAP.md#the-yardstick-gos-own-test-suite).
* **GC torture:** allocation-heavy tests under a debug collector that collects
  at every safe point and poisons freed objects.
* **Race detector:** out of scope; document it as missing.

## 13. Open questions

1. Shadow stack versus stack maps — how much does precise-root bookkeeping
   actually cost in generated code? **M0 answer: 0–8% with liveness-based
   slot assignment** (§11), which is low enough to keep the shadow stack
   and its no-`unsafe`-in-generated-code property. Revisit if rooting struct
   fields, still to come, changes the picture.
2. Can `panic = "unwind"` be relied on across fullrust and `no_std`? If
   not, `defer`/`recover` needs an explicit result-propagation lowering.
   **fullrust: yes** (checked in M0). Generated programs built for
   `x86_64-unknown-linux-fullrust` are static binaries, and Go panics unwind
   through `resume_unwind` → `catch_unwind` with output identical to gc.
   The fullrust toolchain found (1.88) is below the declared MSRV (1.89)
   and needed `--ignore-rust-version`; the MSRV check should include it once
   fullrust ships ≥ 1.89. **`no_std`: still open**, and not urgent — bare-metal
   targets default to `panic = "abort"`, so unwinding there needs an unwinder
   (e.g. the `unwinding` crate). It is an opt-in target, so the answer gates
   nothing before M5.
3. Is a single-threaded mode (no scheduler locks, one heap) worth having for
   embedded targets?
4. How much of `reflect` is enough? `fmt` + `encoding/json` is the practical
   bar; `reflect.StructOf` may never come.
5. Generated-code size: monomorphization plus type descriptors plus the stdlib
   could produce very large crates and slow `rustc` runs. Needs measuring early.
6. Racy heap access (§2): plain loads and stores, or relaxed atomics for every
   heap word? The first is faster and is UB under a Go data race; the second is
   sound. M0 measures both.
7. Atomics on fat pointers: `atomic.Pointer[T]` and `CompareAndSwapPointer`
   operate on `Ptr<T>`, which is two words wide. Options are a double-width CAS
   (`cmpxchg16b`, `casp`) or a thin representation for pointers that are
   accessed atomically. To be decided in M2, before `sync` depends on it.
8. `GoStr` uses `u32` offset and length, which caps strings at 4 GiB where Go
   allows `int`. The same would hold for `Slice<T>` if it follows suit. A
   `mmap`-ed database file is where that bites.
