# rustygo

[![CI](https://github.com/KarpelesLab/rustygo/actions/workflows/ci.yml/badge.svg)](https://github.com/KarpelesLab/rustygo/actions/workflows/ci.yml)
[![crates.io](https://img.shields.io/crates/v/rustygo.svg)](https://crates.io/crates/rustygo)
[![docs.rs](https://img.shields.io/docsrs/rustygo)](https://docs.rs/rustygo)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**A Go → Rust compiler.** Compile Go packages into Rust source, link them with
a Rust runtime, and end up with Go and Rust code in *one* binary that share
real types — no FFI between them, no `unsafe` in anything you write, and no
cgo needed to get there.

> **Status: M0 and M1 are done; M2 (goroutines) and M3 (the standard library)
> are in progress.** Go programs compile to Rust and run under a precise
> mark-sweep collector, and their output, panics and exit codes are identical to
> gc's — 55 differential programs check that on every commit, every one of them
> also under GC torture, which collects at every allocation. Go's own `test/`
> directory is at [123 of 141](docs/GOTEST.md), and every failure is listed
> there with the reason. Standard-library packages are measured by the tests Go
> ships for them, which is [docs/STDTEST.md](docs/STDTEST.md).
>
> Working: the language, near enough — slices with aliasing, `append` and the
> string/`[]byte`/`[]rune` conversions, maps, closures and func values,
> interfaces with dynamic dispatch and type switches, generics, `defer`,
> `panic`/`recover` down to the cases Go's own tests call "here be dragons",
> and `runtime.SetFinalizer`. Goroutines are real stacks with a context switch
> written for the purpose, with channels, `select`, `sync`, timers and a
> netpoller under them. The standard library is the Go distribution's own
> source, compiled: `fmt`, `strings`, `sort`, `os`, `reflect`, `time`, `net`,
> `os/exec`, and `net/http` as a server *and* a client in one process.
>
> Not yet: threads — the scheduler runs every goroutine on one of them — and a
> file and socket layer for anything but Linux, so macOS and Windows compile the
> language but not the packages that reach the kernel. Nor `encoding/json`,
> HTTPS end to end, or `rustygo test`. The plan is
> [docs/DESIGN.md](docs/DESIGN.md) for the architecture,
> [docs/RATIONALE.md](docs/RATIONALE.md) for why this shape and not another,
> and [docs/ROADMAP.md](docs/ROADMAP.md) for the milestones.

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

It also puts Go where only Rust currently goes: [purestd](https://github.com/KarpelesLab/purestd)
libc-free userland, [fullrust](https://github.com/KarpelesLab/fullrust) static
binaries, `no_std` targets. Those are options, not the default: an ordinary
build targets the platform's usual Rust target, where a C toolchain is
available and cgo works.

## What it is not

- **Not a source-to-source beautifier.** The output is Rust that `rustc`
  compiles, not Rust a human would enjoy reading.
- **Not dependent on cgo.** Go reaches Rust directly, so no C sits between
  them. C itself is not excluded: on targets with a C toolchain, Go packages
  that `import "C"` are supported (roadmap M4), because plenty of real code
  needs them. The libc-free targets are where that trade is made, and there
  cgo is unavailable by construction.
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
             generated Rust crate(s)                     `rustygo` crate (runtime)
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
#[derive(Default)]
pub struct Point { pub x: i64, pub y: i64 }

impl Trace for Point { fn trace(&self, _: &mut Tracer) {} }   // no pointers inside

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
| **Goroutines** — can block anywhere, so `async` would colour nearly every function | Stackful coroutines in the runtime (per-arch context switch), M:N scheduler, lazily committed fixed stacks |
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

## Repository layout

One repository and one version number, with two halves:

| Path | What | Language |
|---|---|---|
| `cmd/rustygo`, `internal/` | the compiler: `rustygo build` / `emit` / `test` / `ssa` | Go (it runs on `go/ssa`) |
| `Cargo.toml`, `src/` | the runtime: the `rustygo` crate generated code links against | Rust |
| `docs/` | design, rationale, roadmap | |

The runtime is a single crate. Its optional parts are cargo features (`std`,
`gc-torture`, and more to come), not separate crates. Tracing code is written
by the emitter, not by a derive macro, so no proc-macro crate is needed.

```sh
go run ./cmd/rustygo build -o hello ./testdata/programs/hello   # needs cargo
go run ./cmd/rustygo emit -o out ./testdata/programs/hello      # inspect the Rust
go run ./cmd/rustygo test errors -test.v                        # a package's own tests, through rustygo
go run ./cmd/rustygo ssa ./testdata/programs/hello              # the SSA it came from
go test -timeout 40m ./...                                      # includes the gc-vs-rustygo harness, which takes minutes
cargo test                                                      # runtime unit tests
```

Two harnesses measure rustygo against Go rather than against itself, and both
write what they found:

```sh
go run ./internal/cmd/gotest  -o docs/GOTEST.md    # Go's test/ directory, gc vs rustygo
go run ./internal/cmd/stdtest -o docs/STDTEST.md   # the standard library's own tests
```

Rust MSRV is 1.89 (edition 2024). The compiler targets the Go release pinned in
`go.mod`.

## License

MIT. Copyright © 2026 Karpelès Lab Inc.
