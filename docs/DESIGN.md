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
  and Rust has no `goto`. `go/ssa` keeps blocks and phis, and a relooper turns
  them back into `loop`/`match` when the CFG is irreducible.
* **Unit of output:** one Rust module per Go package, one crate per build. Go
  import cycles are impossible, so module ordering is a topological sort. Crate
  granularity is a later optimization (incremental builds).

### Tool UX

```sh
rustygo build ./cmd/app          # -> ./out/app (via rustc)
rustygo emit  ./cmd/app          # -> ./out/crate/  (inspectable Rust)
rustygo test  ./...              # go test semantics, run through rustc
```

`GOARCH` stays a real value (64-bit: `amd64`/`arm64` type sizes) so `go/packages`
resolves the standard library normally. Target selection rides on a `rustygo`
build tag, plus `rustygo_wasm` / `rustygo_nostd` for the constrained targets, so
packages can ship `foo_rustygo.go`. This mirrors what TinyGo does and avoids
teaching `go list` a GOARCH it rejects.

## 2. Type mapping

| Go | Rust | Notes |
|---|---|---|
| `bool`, `int8`…`int64`, `uint…`, `float32/64` | `bool`, `i8`…`i64`, `u…`, `f32/f64` | all arithmetic via `wrapping_*`; `/` and `%` check for zero and panic |
| `int`, `uint`, `uintptr` | `i64`, `u64`, `u64` | 64-bit only for now |
| `complex64/128` | runtime `Complex32/64` | |
| `string` | `GoStr { data: Gc<[u8]>, off: u32, len: u32 }` | **not** `String`: Go strings are arbitrary bytes |
| `[]T` | `Slice<T> { arr: Gc<GoArray<T>>, off, len, cap }` | aliasing and `append` semantics preserved |
| `[N]T` | `[T; N]` | value type, copied on assignment |
| `map[K]V` | `Gc<GoMap<K, V>>` | runtime hash map, randomized iteration order |
| `chan T` | `Gc<Chan<T>>` | runtime, integrates with scheduler |
| `func(...)` | `Gc<Closure<Args, Ret>>` | captured env is a traced struct |
| `struct` | `#[derive(Trace)] struct` | field order preserved; `Gc<T>` when heap-allocated |
| `*T` | `Ptr<T>` | see interior pointers |
| `interface{...}` | `Iface { typ: &'static TypeDesc, val: Word }` | vtable hangs off `TypeDesc` |
| `unsafe.Pointer` | `Ptr<Opaque>` | restricted; see §7 |
| generics | monomorphized by `go/ssa` | no Rust generics needed |

### Interior pointers

`&s.Field`, `&arr[i]` and `&slice[i]` are legal Go and must keep the whole
object alive. `Ptr<T>` is therefore a fat pointer:

```rust
pub struct Ptr<T> { base: Gc<Obj>, offset: u32, _t: PhantomData<T> }
```

Loads and stores go through the runtime, which bounds-checks against the
object's layout descriptor. This is the main reason generated code can avoid
`unsafe` — and the main reason it costs more than native Go. Escape analysis
(M6) is what claws that back for locals that never escape.

## 3. Memory and the collector

* **Precise tracing collector**, not reference counting: Go programs make cycles
  routinely, and `Rc` plus a cycle collector was rejected as both slow and
  single-threaded.
* **Roots:** generated functions maintain a shadow stack of `Gc` handles. Cheap
  push/pop, no stack maps, no `unsafe` in generated code. Escape analysis keeps
  non-escaping values out of it entirely.
* **Safe points** at every call and every loop back-edge — enough for a
  stop-the-world collector that never interrupts a thread mid-object.
* **Phase 1:** stop-the-world mark-sweep, one heap, a global lock on allocation.
  Correct and boring.
* **Phase 2:** per-thread allocation buffers, size-class allocator, then
  generational or incremental marking if measurements demand it.
* **Finalizers / weak refs:** `runtime.SetFinalizer`, Go 1.24's `weak`, and
  `unique` ride on the collector's post-mark phase.

## 4. Goroutines and the scheduler

* **Stackful coroutines.** Every Go function may block, so colouring functions
  `async` infects nearly the whole program and forces boxed futures for
  recursion. A context switch (per-architecture assembly in the runtime) keeps
  generated code straight-line and lets any function block anywhere.
* **M:N scheduler** over OS threads, work-stealing run queues, `GOMAXPROCS`
  honored. Preemption is cooperative at safe points; Go's own async preemption
  is out of scope initially, so a tight loop without calls can starve a thread.
* **Growable stacks:** coroutine stacks start small and grow by allocating a
  larger stack and switching — the runtime can do this precisely because
  generated code holds Go pointers only in the shadow stack and Rust locals of
  known layout.
* **Channels and `select`** live in the runtime: same FIFO fairness, same
  blocking semantics, same random choice among ready cases.
* **Netpoller:** `epoll`/`kqueue` (or Rust `std` blocking threads on
  constrained targets) driving the same parking primitives as channels.
* **WASM:** no stack switching, so that target falls back to whole-program
  blocking analysis (GopherJS's approach): functions that can block become state
  machines, the rest stay plain calls.

## 5. Control flow, `defer`, `panic`, `recover`

* `defer` → a per-frame defer list in the runtime; frames that own defers are
  wrapped so the list runs on both normal return and unwinding.
* `panic` → a Rust panic carrying a `GoPanic` payload; `recover` inspects the
  payload from inside a deferred call. Requires `panic = "unwind"`.
* Runtime panics (nil dereference, index out of range, divide by zero, failed
  type assertion, closed-channel send) map to the same payload with Go's
  messages, because programs match on them.
* `goto` and labeled `break`/`continue` come out of `go/ssa` as ordinary CFG
  edges and are reconstructed by the relooper.

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
* `syscall` sits on Rust `std` — which is what makes fullrust, purestd and
  kintane reachable without a second port.
* Anything requiring cgo (`os/user` in cgo mode, `net` in cgo resolver mode) is
  unavailable by construction; the pure-Go paths are the only paths.

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

Go values stay `Gc`-managed. A Rust caller must hold a runtime handle (which
starts the scheduler and collector), and the API mirrors that: `Runtime::new()`,
then `rt.enter(|| ...)`.

**Bridge rules:** no Go pointer is handed to Rust for longer than a call unless
it is pinned; no Rust reference is stored in a Go value except as a handle. Both
are enforced by the emitter, not by convention.

## 10. Targets

| Target | Status of plan |
|---|---|
| Native (Linux/macOS/Windows, x86-64 + aarch64) | primary |
| [fullrust](https://github.com/KarpelesLab/fullrust) static Linux | expected to work once `syscall` sits on Rust `std`; needs `panic=unwind` on that toolchain |
| `wasm32-unknown-unknown` / WASI | needs the blocking-analysis path instead of coroutines |
| `no_std` + [purestd](https://github.com/KarpelesLab/purestd) / [kintane](https://github.com/KarpelesLab/kintane) | subset: no goroutine preemption, single heap, no netpoller |

## 11. Performance expectations

Slower than gc Go at first, and honest about why: shadow-stack pushes, fat
pointers, runtime-mediated field access, no assembly in the stdlib. The
recovery path, in order of expected payoff:

1. Escape analysis → plain Rust locals and `[T; N]` arrays, no `Gc`.
2. Bounds-check and safe-point elision on loops `rustc` can already prove.
3. Devirtualization of monomorphic interface calls.
4. Hot stdlib primitives (`memmove`, `IndexByte`, hashing) delegated to Rust
   crates instead of pure-Go loops.

Target to beat before claiming anything: gc Go on the same benchmark set, and
Go-on-WASM as the floor.

## 12. Correctness strategy

* **Differential testing:** build every test program twice — gc and rustygo —
  and compare stdout, exit status and panic text.
* **Go's own test suite:** the `test/` directory of the Go distribution plus
  standard-library tests, tracked as a published pass rate from M1 onward.
* **GC torture:** allocation-heavy tests under a debug collector that collects
  at every safe point and poisons freed objects.
* **Race detector:** out of scope; document it as missing.

## 13. Open questions

1. Shadow stack versus stack maps — how much does precise-root bookkeeping
   actually cost in generated code? (M0 measures it.)
2. Can `panic = "unwind"` be relied on across fullrust, WASM and `no_std`? If
   not, `defer`/`recover` needs an explicit result-propagation lowering.
3. Is a single-threaded mode (`Rc` + no scheduler locks) worth having for WASM
   and embedded?
4. How much of `reflect` is enough? `fmt` + `encoding/json` is the practical
   bar; `reflect.StructOf` may never come.
5. Generated-code size: monomorphization plus type descriptors plus the stdlib
   could produce very large crates and slow `rustc` runs. Needs measuring early.
