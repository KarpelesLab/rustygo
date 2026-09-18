# Why this shape

Observations recorded at the start of the project, so the reasoning can be
argued with later.

## The problem being solved

Karpelès Lab runs two large codebases that need each other. The Go side carries
the deployed services and protocol libraries; the Rust side carries the newer
building blocks — [purecrypto](https://github.com/KarpelesLab/purecrypto),
[compcol](https://github.com/KarpelesLab/compcol),
[graphitesql](https://github.com/KarpelesLab/graphitesql),
[OxideAV](https://github.com/OxideAV).

The current bridge is duplication: `tss-lib` / `tsslib-rs`, `outscript` /
`outscript-rs`, `spotlib` / `spotlib-rs`, `strtotime` / `strtotime-rs`, `gotz` /
`timezone-data-rs`, `klbfw` / `klbfw-rs`. Each pair is two implementations of one
specification, kept compatible by test vectors and care. That cost grows with
every new pair.

cgo would bridge them, and is rejected: it reintroduces a C toolchain, breaks
cross-compilation, breaks the pure-Rust and pure-Go guarantees both sides were
built for, and leaves `unsafe` on the boundary.

## Options considered

### A. Rust → WASM → run inside Go — **rejected**

Compile the Rust crate to `wasm32`, run it in Go on
[wazero](https://wazero.io) (pure Go), generate bindings both sides. Cheap and
proven: [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) ships SQLite
to Go exactly this way.

Rejected on two grounds that no amount of engineering fixes:

* **Performance.** Per-call overhead on every crossing, no SIMD, and every
  buffer copied in and out of linear memory. Codec-, crypto- and
  database-grade work is precisely what would be crossing the boundary.
* **No native resources.** Inside the sandbox there are no threads, no
  syscalls, no file descriptors, no devices, no GPU, no `AF_PACKET`, no
  hardware codecs — the things purecrypto, pktkit, OxideAV and the fleet code
  exist to reach. A bridge that cannot see them bridges nothing useful.

Also inherent: 4 GB per instance, and one instance is single-threaded, so
parallelism means a pool.

### B. Go → WASM → translate WASM to safe Rust — **rejected**

`GOOS=wasip1 GOARCH=wasm go build`, then translate the module into safe Rust,
implementing the WASI imports on Rust `std`. The mirror image of the
`GOARCH=wasm` port: Go's real runtime, GC, scheduler and `reflect` run unchanged
over a `Vec<u8>` heap. It would have been months of work instead of years, with
near-complete Go compatibility.

Rejected for the same two reasons, which survive the translation to Rust:

* **Performance.** Go-on-WASM is the starting point, and translating the module
  to Rust does not recover what the WASM port already gave up: its own
  emulated stack, its own resumable-function scheme, no assembly anywhere in
  the standard library.
* **No native resources.** Everything the program can reach is whatever the
  WASI host chooses to expose. Threads, raw sockets, devices and hardware
  acceleration stay out of reach, which defeats the point for the services this
  is meant to run.

And even setting those aside, the Go heap stays an opaque byte array to Rust:
no shared types, no direct calls, interop still by handles — so it never
delivered the actual goal either.

### C. Go → Rust with a Rust runtime (this repository)

Compile Go through `go/ssa` into Rust source, and supply the runtime Rust does
not have: garbage collector, scheduler, channels, maps, `reflect`.

* **For:** Go structs become Rust structs. A Go package can call a Rust crate
  and vice versa in one binary, with one implementation of each library instead
  of a hand-maintained pair. `rustc` optimizes the result with real types, and
  Go reaches every Rust target.
* **Against:** TinyGo-sized effort, plus a garbage collector Rust does not
  provide. `reflect` and `unsafe.Pointer` will have gaps. Slower than gc Go
  until escape analysis lands.
* **Verdict:** chosen, with eyes open.

## Why not a real `GOARCH`

`GOARCH=rust` inside the gc toolchain was considered and rejected:

* A new architecture touches `cmd/compile`, `cmd/internal/obj`, `cmd/link` and
  the per-arch assembly in `runtime` — then needs rebasing every six months.
* After the gc compiler's SSA lowering, structured control flow is gone, and
  Rust has no `goto`; the output would be one giant loop-and-match per function,
  losing the readable-types advantage that motivated option C.
* `go list` rejects GOOS/GOARCH pairs it does not know, so the pretty
  `GOARCH=rust go build` does not actually work without patching the toolchain
  that resolves packages too.

TinyGo, GopherJS and llgo all sit outside the gc toolchain and use `go/ssa`.
rustygo follows them: a separate `rustygo build` command, a `rustygo` build tag
for target-specific files, and a real 64-bit `GOARCH` underneath so the standard
library resolves.

## Native compilation is a requirement, not a preference

Both rejected options route through WASM, and WASM is ruled out for this project:
the performance loss is unacceptable for crypto, codec and database workloads,
and the sandbox cannot see the native resources — threads, syscalls, devices,
hardware acceleration — that the Go services and the Rust crates both depend on.
That constraint is what makes option C's runtime work unavoidable rather than
merely ambitious: a garbage collector, a scheduler and channels all have to be
built natively, because there is no host runtime to borrow them from.

## On fullrust

[fullrust](https://github.com/KarpelesLab/fullrust) is listed as a target, not a
motivation. Go already produces static libc-free Linux binaries with
`CGO_ENABLED=0`, so fullrust adds nothing *there*. What it adds is the places Go
cannot currently go at all: purestd's syscall-only userland, kintane, `no_std`
boards. Those are reachable because rustygo's `syscall` layer sits on Rust `std`
rather than on a per-OS port.

## Prior art worth reading before writing code

* **TinyGo** — same front end (`go/ssa`), own runtime, own GC, own scheduler.
  The closest existing thing; its `reflect` and stdlib gaps are the realistic
  forecast for rustygo's first years.
* **GopherJS** — whole-program blocking analysis to emulate goroutines without
  stack switching. Not the plan here (stackful coroutines are), but the clearest
  write-up of what Go's concurrency semantics actually demand of a back end.
* **Go's own `GOARCH=wasm` port** — how to run the real runtime on a target with
  no goto and no native stacks; read for the relooper and stack-emulation
  techniques, not as a target.
* **Rust GC experiments** (`gc-arena`, `shredder`, Servo's collector) — for
  shadow-stack versus stack-map tradeoffs and `Trace` implementation ergonomics.

## What would make this project wrong

Written down now, to be checked honestly later, at the decision gates fixed in
[ROADMAP.md](ROADMAP.md#decision-gates):

1. **M0 measures a 10×-plus slowdown** that escape analysis and bounds-check
   elision clearly cannot recover. With the WASM routes off the table there is
   no cheaper fallback, so the honest conclusion would be to keep maintaining
   hand-written pairs instead.
2. **`reflect` proves impractical** to the point that `encoding/json` and `fmt`
   do not work. A Go that cannot marshal JSON is not useful for KLB services.
3. **Generated crates are so large** that `rustc` compile times make the
   toolchain unusable in CI.
4. **The duplication it removes turns out to be cheap** — the hand-written pairs
   are already written and passing vectors; the pain is future pairs, not
   existing ones. Worth re-checking before M2.
