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
* **Constant tables become static data.** Go writes its big tables as array
  literals — `unicode/norm` has one of 19,426 bytes — and go/ssa spells every
  element out as an address and a store. Emitted literally, that package's
  initializer is one Rust function of 72,613 lines, which `rustc` compiles in
  31 GB. A run of constant stores into a fresh array is recognized and emitted
  as a `static` instead (`internal/emit/tables.go`), which is what gc does
  with the same input: 206 of them in an HTTP server, and a third less Rust
  overall.
* **Not the gc compiler's SSA.** By that point structured control flow is gone,
  and Rust has no `goto`. `go/ssa` keeps blocks and phis, and the emitter
  rebuilds them into labeled blocks and loops (`break 'b`, `continue 'l`),
  following Ramsey's "Beyond Relooper". Only irreducible CFGs (a `goto` into
  a loop body) fall back to a loop around a `match` on the block index. M0
  measured why structuring is mandatory: LLVM compiles that fallback to an
  indirect jump per basic block, which cost 6× on a field-update loop.
* **Unit of output:** one Rust module per Go package, and **one crate per band**
  — a band being how deep a package sits in the import graph, so that two
  packages in the same band never import each other and a crate's
  dependencies only ever point downwards (`internal/emit/bands.go`). Splitting
  is not tidiness: a program reaching `net/http` emits hundreds of thousands
  of lines of Rust, and `rustc` needs tens of gigabytes for one crate that
  size. It is also what lets the lower crates, which are the standard
  library, be compiled once and reused. Go
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
| `map[K]V` | `GoMap<K, V>` | handle to a heap object holding an open-addressing table; keys hash by Go's rules; iteration starts at a random bucket |
| `chan T` | `Chan<T>` | handle to a heap object holding the buffer and the state blocked goroutines test |
| `func(...)` | `Func<fn(Env, ..) -> ..>` | code pointer + environment; the captured env is a traced place struct |
| `struct` | `struct` + emitted `impl Trace` | field order preserved; `Gc<T>` when heap-allocated |
| `*T` | `Ptr<T>` | see interior pointers |
| `interface{...}` | `Iface { desc: &'static TypeDesc, data: Data }` | the concrete value is boxed; `TypeDesc` carries the method table |
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
  collection ([§11](#11-performance-expectations)). From M2 every thread
  allocates from that one heap under one lock, which is the simplest thing
  that is correct; per-thread allocation caches over size classes are M6's
  job, and what this costs is the number to measure before writing them.
* **What the collector reports.** `runtime.ReadMemStats` gives the numbers the
  collector actually keeps — live objects and bytes, everything ever
  allocated, and the collection count — and leaves the rest zero. There is no
  separate heap, stack and span accounting to report: one heap, one
  allocation per object, so the several ways gc splits that number all give
  the same answer here.
* **GC torture** (`RUSTYGO_GCTORTURE=1`, the runtime's `gc-torture` feature)
  collects at every allocation, and never reuses what it collects: freed
  objects are poisoned and quarantined, and every dereference checks the
  quarantine. A missing root is then an immediate panic naming the address
  ("use of collected object … (a missing GC root)") rather than a read of
  someone else's data. The whole differential suite passes under it, which is
  what checks that the emitted roots are complete.
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

**As built (M2).** Goroutines run on worker threads, as many of them at a time
as `GOMAXPROCS` allows — which defaults to two rather than to the number of
CPUs, because three or more still corrupt a goroutine's saved context and that
is not yet understood. A goroutine is a stack (`src/stack.rs`: a reservation
with a 64 KiB guard below it) and a saved context (`src/context.rs`: the
callee-saved registers and the stack pointer, in naked assembly for x86-64
and aarch64). `go` hands the scheduler the same thunk-and-environment a
`defer` builds, so the two share their machinery.

A worker stands on its thread's own stack between goroutines, and a goroutine
that blocks switches back to that stack rather than straight into the next
goroutine. That is not a detail: a goroutine may be offered to another thread
only once nothing is running on its stack, and the only place from which that
is true is the stack it has just left. So the worker, not the goroutine, is
what puts a yielding goroutine back on the run queue, marks a blocking one as
waiting, and frees a finished one's stack — and for the same reason the program
itself runs on a goroutine with a stack of its own, because the first worker
needs its native stack to stand on.

One run queue, shared, under one lock. `GOMAXPROCS` is a count of processor
slots a running goroutine holds one of, so a worker that cannot get one idles
rather than exits and lowering the limit and raising it again costs nothing —
which is the shape Go's own tests use. `runtime.LockOSThread` and the
processor pinning `sync.Pool` and `sync/atomic` ask for are the same
mechanism: a pinned goroutine keeps its slot and goes back on its own worker's
queue, never the shared one, so a per-processor shard is really per-processor.
Work stealing, a queue per worker, and a monitor thread that takes a slot back
from a goroutine blocked in a system call are still to come, and with them the
measurement that says whether the shared queue's lock was ever the problem.

**Blocking is no longer decided by inspection.** On one thread, a goroutine
that found a channel empty could park knowing nothing had run in between. Now
something can, so a goroutine registers itself as a waiter *while it still
holds whatever guards the condition it is waiting for* — the channel's own
lock, the word a semaphore counts down — and parks only once that is released,
because a park switches stacks. A wake that lands in the window between the
two is recorded on the goroutine and consumed by the park, which then comes
straight back; every caller re-tests what it was waiting for, because a park
may return with nothing having happened. Go's semaphores moved into the
runtime for the same reason: a test written in Go and a park in the runtime
cannot agree about what happened between them.

The state that belongs to a goroutine rather than to the thread — its shadow
stack of GC roots, and the panics it is handling — moves with it on every
switch, which is what makes a panic on one goroutine invisible to another and
lets the collector trace the roots of a goroutine that is not running.
`src/tls.rs` is where every piece of the runtime's state says which of the two
it is: per thread, or shared behind a lock. Blocking is a park: a channel that
is not ready, a semaphore that is held, `runtime.Gosched`. When nothing can run
anywhere and nothing is being waited for, that is Go's
`all goroutines are asleep - deadlock!`, reported the same way.

**A collection stops the world.** The collector has to see every goroutine's
roots and may not read a stack that is changing, so before marking it asks
every thread that is running Go code to stop. A thread notices at its next
safe point — every loop back-edge, and the top of every allocation — puts its
goroutine's roots where the collector looks for a parked one's, and waits. A
thread between goroutines, or asleep in the netpoller, has nothing to stop and
nothing to show. The world is stopped *before* the heap is locked and not
after: a thread that has not reached a safe point yet may be inside a heap
critical section of its own, and waiting for it with that lock held would be
waiting for a lock the collector is holding.

Channels are heap objects like maps, with a lock each. Every goroutine blocked
on one parks on the channel's address and re-tests its condition when woken,
and every operation that changes a channel wakes all of them: more wakeups than
Go's queues of waiters need, but it cannot lose one. `select` takes a case in
one step under that lock rather than asking whether an operation would block
and then performing it — in between, another goroutine could take the value,
and the operation would block, which no select case may do. It polls its cases
from a random start, so a ready case cannot be starved, and registers on every
channel before it tests any of them.

**The netpoller, and timers.** A goroutine that waits on a socket parks on
the descriptor, and one worker with nothing to run sleeps in `epoll_wait`
until one moves (`src/netpoll.rs`). One worker, because one `epoll_wait`
reports every descriptor at once; and because that sleep may last until a
socket moves, the poller holds a descriptor of its own that the scheduler
writes to when something else becomes runnable. It answers the nine
questions `internal/poll` asks the runtime, level-triggered with the interest
mask following the waiters: a descriptor is asked about readability only
while a goroutine is waiting to read it. gc polls edge-triggered with
readiness latches, which costs fewer system calls and hangs the program if a
latch is ever wrong.

Deadlines are the same machinery as `time.Sleep`: a goroutine may park until
a moment on the monotonic clock, and the earliest such moment is how long the
scheduler's next wait may last. Timers are a list and a goroutine of their
own in the runtime overlay — it sleeps until the earliest deadline, fires
what is due, and parks on a semaphore when nothing is left, which is what
keeps a program whose only remaining goroutine is the timer loop from
counting as busy. What a timer does when it fires is Go's own code: the time
package hands the runtime a function and an argument, and firing calls it.

Deadlines are real: a descriptor carries a moment for reads and one for
writes, a wait that starts after one has passed fails at once, and the
scheduler's next sleep is bounded by the earliest of them. `net/http` cannot
work without this — it aborts a read already in progress by giving it a
deadline in the past and waiting for the reader to come back.

A descriptor is in the poller only while a goroutine waits on it. Errors and
hangups are reported for a registered descriptor whatever its interest mask
says, so one that nobody waits on is taken out entirely; leaving it in means a
hung-up socket is reported for ever and the scheduler spins.

`RUSTYGO_TRACE=sched` reports what the scheduler and the poller are doing —
which descriptors were opened, who waited and what woke them, each deadline,
and every time nothing was runnable. It is how a program that stops making
progress is diagnosed at all, until goroutine stacks can be printed.

Still to come in M2: work stealing with a run queue per worker, Go's `sysmon`
taking a processor slot back from a goroutine that blocks in a system call
without announcing it, time-sliced preemption at the safe points that now
exist, pooling the stacks of goroutines that have exited, and
`testing/synctest`.
* **Netpoller:** `epoll`/`kqueue`/IOCP (or Rust `std` blocking threads on
  constrained targets) driving the same parking primitives as channels.

## 5. Control flow, `defer`, `panic`, `recover`

* `defer` → a per-function list of thunks in the runtime: a code pointer plus
  the environment holding the arguments, which Go evaluates at the `defer`
  statement. The environment is an ordinary heap place struct, so deferred
  arguments stay reachable. The list runs where Go runs it, before a named
  result is read back, and a function that defers wraps its body in
  `catch_unwind` so the list also runs while unwinding — and so a deferred
  call may itself panic without aborting.
* `panic` → a Rust panic carrying a `GoPanic` payload; `recover` inspects the
  payload from inside a deferred call. Requires `panic = "unwind"`.
* Runtime panics (nil dereference, index out of range, divide by zero, failed
  type assertion, closed-channel send) map to the same payload with Go's
  messages, because programs match on them. The payload also carries the
  value `recover` returns, which for these is gc's: an `error` that is also a
  `runtime.Error`.
* **`recover`'s rule, exactly** (Go's own tests call this territory "here be
  dragons", and they are part of rustygo's measured set). A value comes back
  only when `recover` is called *directly* by a function that a defer
  invoked, and only for that frame's own panic. Panics nest — one raised
  while another is being handled — so the runtime keeps a stack of them, not
  one. The frames the collector already tracks answer "directly": the chain
  of linked frames is the Go call stack, so a deferred call's frame sits on
  the one the defer machinery linked, while anything it calls has a frame of
  its own in between. To keep the chain complete, a function that can reach a
  `recover` through any number of calls links a frame even with nothing to
  root; a program with no `recover` anywhere links no extra frames and pays
  nothing (`internal/emit/recover.go`). When a deferred call does recover,
  the frame resumes and runs the rest of its defer list on its normal path,
  which is what makes `defer recover()` recover in one order and do nothing
  in the other.
* **Frames that stand in for a deferred call do not count.** gc's rule is more
  precisely that there must be exactly one *non-wrapper* frame between the panic
  and the `recover` (`runtime.gorecover`), and the pair `reflect.MakeFunc`
  interposes — the trampoline and the dispatcher that turns boxed arguments into
  `[]reflect.Value` — are wrappers in that sense. Each marks its frame
  transparent, and the walk outward from `recover`'s caller looks straight
  through them (`src/gc.rs`). The mark lives in the frame header, so it goes away
  with the frame and costs nothing in size. One closure too many between the
  defer and the made function, or one call too deep inside it, lands on a frame
  that does count and recovers nothing — which is gc's answer too, and is what
  `testdata/programs/mfrecover` measures case by case.
* `goto` and labeled `break`/`continue` come out of `go/ssa` as ordinary CFG
  edges and are reconstructed by the structuring pass (§1).

## 6. `reflect` and type descriptors

`reflect` is not optional: `fmt` and `encoding/json` are in every program.

* Emit a `TypeDesc` for every type the program instantiates: kind, size, field
  names and offsets, tags, method table, element/key types. **M1 emits one for
  every concrete type that reaches an interface**: its Go name, its method set
  as (id, wrapper) pairs, how to compare two values, and how `print` and
  `panic` render one. A method id numbers a distinct name and signature across
  the program, so a call through an interface finds the concrete method
  without knowing which interface it came from — an itab per (interface,
  type) pair, and devirtualization, are M6.
* `reflect.Value` becomes `(&'static TypeDesc, Ptr<Opaque>)` — exactly the
  runtime's existing object model, so field and element access reuses the same
  bounds-checked paths.
* Method values and `reflect.Call` need a generated universal call shim per
  signature.
* **Known gap:** `reflect.StructOf` / `MapOf` and other runtime type
  construction need heap-allocated descriptors; possible, but deferred.
  `PointerTo` is the exception that turned out to be easy — see below — and
  `ArrayOf` is answered without building anything: the element's descriptor lists
  the arrays of it the program contains, so asking for one that exists hands back
  the program's own type. An array's descriptor depends on its element at every
  point, the collector's knowledge of where the pointers inside one are included,
  so there is nothing to derive it from; deriving one would mean giving every
  descriptor's `equal`, `hash`, `print`, `box_value` and `zero` the descriptor
  itself as an argument, so they could loop over elements, and giving the element a
  typed-run allocator so the collector stays precise. Worth doing when something
  needs it: the only caller in reach, `testify`, asks for `[n]T` of a value it is
  already holding, so the type exists, and `encoding/gob`'s one caller goes on to
  call `StructOf` and would still stop there.

**As built (M3).** `reflect` is rustygo's own package, swapped in through the
overlay (§8), and it is ordinary Go: a `Type` is a descriptor address, a
`Value` is `{d, p unsafe.Pointer}`, and reading a field or an element is a
pointer computed from the descriptor's offsets and dereferenced. The runtime
answers 25 questions about a descriptor (`src/reflect.rs`) and the emitter
fills them in:

* Kind, size, align, name, string, package path, element and key types, and
  whether the type is comparable.
* Fields: name, package path, type, **offset**, tag and embeddedness. The
  offsets are the ones go/types computes with gc's own `types.Sizes`, so a
  struct reflects identically under both compilers.
* Boxing: `box_value` copies the bytes at an address into a fresh object, so
  `Value.Interface()` hands back a real interface value.
* Maps, which have no fixed layout to walk, get a per-map-type `MapOps`
  (`len`, `iter`, `next`, `index`) generated beside the descriptor. Keys and
  values come back as addresses of freshly boxed copies, which is what lets
  Go-side reflection treat a map like any other value.

That is enough for `fmt` to compile and run unmodified — the whole of
`Printf`'s `%v`/`%+v`/`%#v`/`%T`, `Stringer`, `error` and `%w` wrapping — and
for `reflect.DeepEqual` and `Swapper`. `Zero`, `MakeSlice`, `MakeMap`,
`Type.Method`/`Implements` and `Value.Call` followed, each one a question the
descriptor answers or a closure generated per type.

**Pointer types made at run time.** `reflect.New`, `reflect.PointerTo` and
`Value.Addr` all hand back a `*T`, and the emitter writes descriptors only for
the types a program mentions — so the one for `*T` may not exist. A pointer's
descriptor hardly depends on what it points at, though: it is one word, it is
compared and hashed as an address, and boxing or zeroing it moves that one
word. So the runtime builds it from the element's descriptor and leaks it,
keyed by the element so that a type is built once.

Two things it cannot derive, and both come from the emitter. `*T`'s **method
set**, which is what `PointerTo(t).Implements(json.Marshaler)` asks of every
type `encoding/json` encodes. And **type identity**: Go says a type is one
type, and identity here is the descriptor's *address*, so a value `New` made
has to carry the same descriptor a `*T` variable in the program does. A
descriptor therefore names its own `*T` (`TypeDesc::ptr`) wherever the program
already contains that type or `*T` has methods, and only where neither holds —
a pointer type nothing in the program writes, whose method set is empty — does
the runtime derive one.

**Reading an unexported field is not writing it.** A `Value` carries gc's two
read-only bits as well: `sticky`, which says the value came through an
unexported field, and `embed`, which says it *is* an unexported embedded
struct, whose own fields are ordinary values again. `CanSet` and `CanInterface`
are what they are made of, and `encoding/json` depends on both — it reads the
fields promoted out of an unexported embedded struct and refuses to allocate a
pointer to one.

That is enough for `encoding/json`: all 104 of its tests and their 592 subtests
pass, `TestSynctestMarshal` aside, which needs `testing/synctest`.

## 7. `unsafe.Pointer` and package `unsafe`

Supported, as gc supports it (revised in M1; the first draft rejected
reinterpretation):

* **Places have gc's bytes, with one exception.** Generated structs are
  `#[repr(C)]` — Go's struct layout — in both their value and place forms, and a
  `Slot` is transparent over its value. Pointers, strings, slices, maps and
  interfaces have gc's sizes. A **func value does not**: it is a code address and
  an environment, two words where gc's is one pointer to a funcval. So a struct
  holding a func value is wider than gc's, and everything after that field sits
  eight bytes further along.
* **What that costs, and who pays it.** `unsafe.Sizeof`/`Alignof`/`Offsetof` are
  constants go/types folds with gc's rules while type-checking, so for such a
  struct they describe a layout that does not exist. Reflection must not: a
  descriptor's field offsets are what `Value.Field` reads a field *at*, so they
  are computed with rustygo's own arithmetic — gc's, corrected for the width of a
  func value (`internal/emit/sizes.go`). A type with no func value inside it is
  handed straight to gc and its numbers cannot drift.

  So `unsafe.Sizeof` and `reflect.Type.Size` disagree for a struct holding a func
  value. That is the one divergence left, and it is narrower than the alternative:
  before, both reported gc's number and *neither* described the memory, so
  reflecting over `crypto/tls.Config` — thirty-five fields, eleven of them funcs —
  read and wrote bytes belonging to other fields. Making the two agree again means
  making a func value one word as gc has it, with the code address in the
  closure's environment struct so a closure still allocates once; that changes
  every call site, how a func value is rooted and traced, and what a closure
  costs, so it is written down rather than done.
* **`unsafe.Pointer` is an address** (`UPtr`). Converting it back to a typed
  pointer reinterprets memory as gc does — `math.Float64bits` is
  `*(*uint64)(unsafe.Pointer(&f))` — and pointer arithmetic through `uintptr`
  works, the collector being non-moving. The collector traces an
  `unsafe.Pointer` like any pointer; a `uintptr` keeps nothing alive, as Go's
  rules say.
* `unsafe.Add`, `Slice`, `SliceData`, `String` and `StringData` work.
* **The honest cost:** generated code contains `unsafe` exactly where the Go
  source imports package `unsafe`, and nowhere else. Such code is as safe as
  Go's own `unsafe` contract makes it, under rustygo as under gc.

## 8. Standard library

* Compile the **real Go standard library** through the same pipeline, with the
  `purego` build tag so assembly-backed packages take their pure-Go paths.
* Reimplement only the bottom: `runtime`, `runtime/internal/*`, `sync/atomic`,
  `syscall`, the `os` and `net` syscall layers, `reflect` internals, and the
  `internal/bytealg`-style packages that assume assembly.
* **Assembly without a `purego` path.** With a real `GOARCH`, packages such as
  `math` and `internal/bytealg` select files that declare bodyless functions,
  whose bodies live in `.s` files. Each one gets a Rust implementation or a
  redirect to the package's generic Go fallback — `internal/emit/fallbacks.go`
  maps `math/big.addVV` to the `addVV_g` sitting next to it, and `math.archLog`
  to `math.log`. A function that exists only for a processor feature rustygo
  never reports, such as `crc32`'s SSE 4.2 kernels, gets a body that says so
  instead of a checksum that is wrong. The same inventory covers the
  stdlib's `go:linkname` references into `runtime`. That contract changes with
  every Go release, so rustygo pins one Go version at a time. The replacement
  packages are substituted through an overlay GOROOT, as TinyGo does.
* `syscall` sits on Rust `std` — which is what makes fullrust and purestd
  reachable without a second port.
* **The file layer, as built (M3).** The standard library's whole `syscall`
  package funnels into one function that gc writes in assembly,
  `internal/runtime/syscall/linux.Syscall6`. rustygo provides it as a raw
  system call (`src/syscall.rs`: the `syscall` instruction on x86-64, `svc #0`
  on aarch64) returning gc's `(r1, r2, errno)`. Above it, `syscall`, `os`,
  `io/fs`, `time` and `internal/poll` are the real packages, compiled
  unmodified, so `os.Stdout`, `os.Args`, `os.Getenv`, `os.Exit` and file I/O
  are Go code reaching the kernel the way gc's does. The pieces gc puts in
  its runtime instead of in `syscall` — `exit`, `nanosleep`, `walltime`,
  `nanotime`, `fcntl`, `args`, `envs`, the `entersyscall`/`exitsyscall` pair,
  and the exit hooks `os.Exit` runs — are rustygo's runtime overlay, each one
  a `go:linkname` push to the name the standard library expects. A blocking
  call blocks the thread, which with one goroutine is the whole program; the
  netpoller is M2/M3 work.
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
