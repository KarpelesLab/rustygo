// Copyright 2026 Karpelès Lab Inc. MIT license.

// Package runtime is rustygo's runtime package, standing in for gc's at the
// same import path (internal/goroot). It is the Go-facing side of the
// runtime: what the standard library and programs call, implemented in Go
// where Go suffices and, where it does not, declared without a body and
// resolved by the emitter to the Rust runtime (internal/emit/intrinsics.go).
//
// It imports only packages that are pure Go constants, so replacing gc's
// runtime cuts the standard library off from gc's internals entirely.
package runtime

import (
	"internal/goarch"
	"internal/goos"
)

// GOOS and GOARCH are the target's, as gc defines them.
const (
	GOOS   = goos.GOOS
	GOARCH = goarch.GOARCH
)

// Compiler is "gc": the standard library switches on it for layout-dependent
// code, and rustygo keeps gc's layouts (DESIGN §7).
const Compiler = "gc"

// Version reports the Go release rustygo compiles against, as gc's does.
func Version() string { return "go1.26" }

// GOROOT is not meaningful inside a compiled program.
func GOROOT() string { return "" }

// Error identifies a run-time error, as in gc.
type Error interface {
	error
	RuntimeError()
}

// GC runs a garbage collection.
func GC()

// KeepAlive marks its argument reachable until this point.
func KeepAlive(x any) { keepAliveSink = x; keepAliveSink = nil }

var keepAliveSink any

// Goroutines run on one OS thread for now (DESIGN §4), cooperatively:
// Gosched hands the processor to the next one, and there is no second
// thread to report.

func Gosched()
func GOMAXPROCS(n int) int { return 1 }
func NumCPU() int          { return 1 }
func LockOSThread()        {}
func UnlockOSThread()      {}

// NumGoroutine counts the goroutines that exist, as the scheduler sees them.
func NumGoroutine() int

// Goexit is not supported before goroutines exist.
func Goexit() { panic("runtime.Goexit: not supported yet (roadmap M2)") }

// Stack traces arrive in M3 (a Rust-line to Go-position table); until then
// callers are unknown.

func Caller(skip int) (pc uintptr, file string, line int, ok bool) { return 0, "", 0, false }
func Callers(skip int, pc []uintptr) int                           { return 0 }

type Frame struct {
	PC       uintptr
	Func     *Func
	Function string
	File     string
	Line     int
	Entry    uintptr
}

type Frames struct{}

func CallersFrames(callers []uintptr) *Frames     { return &Frames{} }
func (ci *Frames) Next() (frame Frame, more bool) { return Frame{}, false }

type Func struct{}

func FuncForPC(pc uintptr) *Func                  { return nil }
func (f *Func) Name() string                      { return "" }
func (f *Func) Entry() uintptr                    { return 0 }
func (f *Func) FileLine(pc uintptr) (string, int) { return "", 0 }
func Stack(buf []byte, all bool) int              { return 0 }

// Finalizers and cleanups are an M1 item still to come; until then they are
// accepted and never run, which Go allows (a finalizer need not run).

func SetFinalizer(obj any, finalizer any) {}

type Cleanup struct{}

func AddCleanup[T, S any](ptr *T, cleanup func(S), arg S) Cleanup { return Cleanup{} }
func (c Cleanup) Stop()                                           {}

// MemStats has gc's fields, so programs that read it compile; the collector
// fills in what it tracks, and the rest stay zero.
type MemStats struct {
	Alloc, TotalAlloc, Sys, Lookups, Mallocs, Frees                    uint64
	HeapAlloc, HeapSys, HeapIdle, HeapInuse, HeapReleased, HeapObjects uint64
	StackInuse, StackSys, MSpanInuse, MSpanSys, MCacheInuse, MCacheSys uint64
	BuckHashSys, GCSys, OtherSys, NextGC, LastGC, PauseTotalNs         uint64
	PauseNs, PauseEnd                                                  [256]uint64
	NumGC, NumForcedGC                                                 uint32
	GCCPUFraction                                                      float64
	EnableGC, DebugGC                                                  bool
}

func ReadMemStats(m *MemStats) { *m = MemStats{} }

// Profiling is out of scope (roadmap non-goals); these keep programs that
// touch it compiling, and report no samples.

var MemProfileRate int = 512 * 1024

type MemProfileRecord struct {
	AllocBytes, FreeBytes     int64
	AllocObjects, FreeObjects int64
	Stack0                    [32]uintptr
}

func (r *MemProfileRecord) InUseBytes() int64   { return r.AllocBytes - r.FreeBytes }
func (r *MemProfileRecord) InUseObjects() int64 { return r.AllocObjects - r.FreeObjects }
func (r *MemProfileRecord) Stack() []uintptr    { return nil }

func MemProfile(p []MemProfileRecord, inuseZero bool) (n int, ok bool) { return 0, true }

// Breakpoint would trap into a debugger; there is none to trap into.
func Breakpoint() { panic("runtime.Breakpoint: no debugger (rustygo)") }

func SetMutexProfileFraction(rate int) int { return 0 }
func SetBlockProfileRate(rate int)         {}

// PanicNilError is what recover returns after panic(nil).
type PanicNilError struct{ _ [0]*PanicNilError }

func (*PanicNilError) Error() string { return "panic called with nil argument (goexit=false)" }
func (*PanicNilError) RuntimeError() {}
