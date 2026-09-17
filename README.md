# rustygo

**A Go → Rust compiler.** Compile Go packages into Rust source, link them with
a Rust runtime, and end up with Go and Rust code in *one* binary that share
real types — no cgo, no FFI, no `unsafe` in anything you write.

> **Status: design only.** There is no code in this repository yet. This is the
> written-down plan: [docs/DESIGN.md](docs/DESIGN.md) for the architecture,
> [docs/RATIONALE.md](docs/RATIONALE.md) for why this shape and not another,
> [docs/ROADMAP.md](docs/ROADMAP.md) for the milestones. Comments and holes
> poked in it are welcome before the first line lands.

## Why

Two stacks that need each other:

- A large body of **Go** — cloud infrastructure, services, protocol libraries.
- A fast-growing body of **pure Rust** — [purecrypto](https://github.com/KarpelesLab/purecrypto),
  [compcol](https://github.com/KarpelesLab/compcol), [graphitesql](https://github.com/KarpelesLab/graphitesql),
  [OxideAV](https://github.com/OxideAV).

Today they are bridged by hand: `tss-lib` / `tsslib-rs`, `outscript` /
`outscript-rs`, `spotlib` / `spotlib-rs`, `strtotime` / `strtotime-rs`, `gotz` /
`timezone-data-rs`. Every pair is two implementations of one idea, kept
wire-compatible by discipline and test vectors.

rustygo attacks that from the other side: if Go *compiles to* Rust, a Go package
can call a Rust crate directly, a Rust program can call a Go package directly,
and there is one implementation again.

It also puts Go where only Rust currently goes: [fullrust](https://github.com/KarpelesLab/fullrust)
static binaries, [purestd](https://github.com/KarpelesLab/purestd) libc-free
userland, [kintane](https://github.com/KarpelesLab/kintane), `no_std` targets.

## What it is not

- **Not a source-to-source beautifier.** The output is Rust that `rustc`
  compiles, not Rust a human would enjoy reading.
- **Not a cgo replacement.** Programs that need C are out of scope; that is the
  point.
- **Not a Go implementation in Rust.** The front end is Go's own
  [`go/ssa`](https://pkg.go.dev/golang.org/x/tools/go/ssa); the Go compiler is
  not forked.
- **Not `GOARCH=rust` in the gc toolchain.** See
  [docs/RATIONALE.md](docs/RATIONALE.md#why-not-a-real-goarch).

## Shape

```
  Go packages ──► go/packages + go/types ──► go/ssa
                                               │
                                     rustygo code generator
                                               │
                        ┌──────────────────────┴──────────────────────┐
                        ▼                                             ▼
             generated Rust crate(s)                        rustygo-runtime crate
             (safe Rust, no unsafe)                  GC · scheduler · chan · map ·
                        │                            reflect · syscall shims
                        └──────────────────┬──────────────────────────┘
                                           ▼
                                         rustc
                                           │
                    native · fullrust · purestd · no_std subset
```

Generated code stays safe Rust. The `unsafe` lives in the runtime crate — the
garbage collector and the coroutine context switch — the same way Rust's own
`std` confines it.

```go
type Point struct{ X, Y int }

func (p *Point) Scale(k int) { p.X *= k; p.Y *= k }
```

```rust
#[derive(Default, Trace)]
pub struct Point { pub x: i64, pub y: i64 }

impl Point {
    pub fn scale(this: &Gc<Point>, k: i64) {
        this.with_mut(|p| {
            p.x = p.x.wrapping_mul(k);   // Go integers wrap
            p.y = p.y.wrapping_mul(k);
        });
    }
}
```

## The four hard parts

| Problem | Plan |
|---|---|
| **Garbage collection** — Go has cycles, interior pointers (`&s.f`, `&a[i]`), slices sharing a backing array | Precise tracing collector in the runtime; `Gc<T>` handles; generated trace impls; safe points at calls and loop back-edges; fat pointers for interior pointers |
| **Goroutines** — can block anywhere, so `async` would colour nearly every function | Stackful coroutines in the runtime (per-arch context switch), M:N scheduler, growable stacks |
| **`reflect` / `unsafe.Pointer`** — `encoding/json`, `fmt`, much of the stdlib | Emit full type descriptors; map `unsafe.Pointer` onto the runtime object model; document the patterns that will not be supported |
| **Standard library** — assembly, `go:linkname`, `runtime` internals | Compile the real stdlib with the `purego` build tag; reimplement only `runtime`, `syscall`, `os` bottom, `reflect` internals, `sync/atomic` on Rust std |

Details and the smaller traps (non-UTF-8 strings, randomized map order, nil and
divide-by-zero panics, `defer`/`recover` on Rust unwinding) are in
[docs/DESIGN.md](docs/DESIGN.md).

## Honest cost

This is a TinyGo-sized project. TinyGo took years and several people, and still
does not cover all of `reflect` or the standard library. rustygo carries the
same weight plus a garbage collector Rust does not give it for free.

There is no cheaper path to the same goal. The WASM routes — running Rust
inside Go on a WASM runtime, or compiling Go to WASM and translating that to
Rust — are ruled out: they cost too much performance, and they cut the code off
from native resources (threads, syscalls, devices, hardware acceleration) that
both stacks exist to use. Native compilation is the requirement, so the runtime
work below is the price of entry. [docs/RATIONALE.md](docs/RATIONALE.md) records
the comparison.

## License

MIT. Copyright © 2026 Karpelès Lab Inc.
