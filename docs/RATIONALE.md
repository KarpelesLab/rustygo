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

### A. Rust → WASM → run inside Go

Compile the Rust crate to `wasm32`, run it in Go on
[wazero](https://wazero.io) (pure Go), generate bindings both sides.

* **For:** cheap, proven — [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3)
  ships SQLite to Go exactly this way. No cgo, no `unsafe` in Go.
* **Against:** per-call overhead and no SIMD, so codec-grade work suffers; Rust
  objects are reachable only through handles; 4 GB per instance; one instance is
  single-threaded, so parallelism comes from a pool.
* **Verdict:** still the right answer for *chunky* calls (compress this buffer,
  sign this message). Worth building independently of rustygo.

### B. Go → WASM → translate WASM to safe Rust

`GOOS=wasip1 GOARCH=wasm go build`, then translate the module into safe Rust,
implementing the WASI imports on Rust `std`. The mirror image of the `GOARCH=wasm`
port: Go's real runtime, GC, scheduler and `reflect` run unchanged over a
`Vec<u8>` heap.

* **For:** near-complete Go compatibility for a fraction of the effort — months,
  not years. Nothing to maintain across Go releases. Reaches fullrust, purestd
  and kintane. Shares its front half with a WASM → Go translator, so one tool
  serves both directions.
* **Against:** the Go heap is an opaque byte array to Rust. No shared types, no
  direct calls; interop is still handles. Performance is Go-on-WASM, well below
  native Go.
* **Verdict:** the pragmatic path, and the fallback if C turns out to be
  unaffordable. It does not deliver type-level interop, which is the actual goal.

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
  stack switching. That technique is the plan for the WASM target.
* **Go's own `GOARCH=wasm` port** — how to run the real runtime on a target with
  no goto and no native stacks.
* **`ncruces/go-sqlite3`** — evidence for option A's viability.
* **Rust GC experiments** (`gc-arena`, `shredder`, Servo's collector) — for
  shadow-stack versus stack-map tradeoffs and `Trace` derive ergonomics.

## What would make this project wrong

Written down now, to be checked honestly later:

1. **M0 measures a 10×-plus slowdown** that escape analysis clearly cannot
   recover. Then option B delivers the same compatibility for far less work.
2. **`reflect` proves impractical** to the point that `encoding/json` and `fmt`
   do not work. A Go that cannot marshal JSON is not useful for KLB services.
3. **Generated crates are so large** that `rustc` compile times make the
   toolchain unusable in CI.
4. **The duplication it removes turns out to be cheap** — the hand-written pairs
   are already written and passing vectors; the pain is future pairs, not
   existing ones. Worth re-checking before M2.
