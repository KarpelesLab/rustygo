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

// errorString is a run-time error that is only a message, which is what the
// arithmetic ones are.
type errorString string

func (e errorString) Error() string { return "runtime error: " + string(e) }
func (e errorString) RuntimeError() {}

// The errors the compiler's own arithmetic raises. `math/bits` reaches these
// two by name, through a `go:linkname` on a variable, and panics with them
// from its own division routines.

var divideError error = errorString("integer divide by zero")

var overflowError error = errorString("integer overflow")

// GC runs a garbage collection.
func GC()

// KeepAlive marks its argument reachable until this point.
func KeepAlive(x any) { keepAliveSink = x; keepAliveSink = nil }

var keepAliveSink any

// Goroutines run on GOMAXPROCS worker threads (DESIGN §4). Gosched hands the
// processor to the next goroutine; GOMAXPROCS is the limit on how many run at
// once, and lowering it idles the extra workers rather than stopping them.

func Gosched()

func GOMAXPROCS(n int) int { return gomaxprocs(n) }
func NumCPU() int          { return numCPU() }
func LockOSThread()        { lockOSThread() }
func UnlockOSThread()      { unlockOSThread() }

func gomaxprocs(n int) int
func numCPU() int
func lockOSThread()
func unlockOSThread()

// NumGoroutine counts the goroutines that exist, as the scheduler sees them.
func NumGoroutine() int

// Goexit ends the running goroutine after its deferred calls, and is what
// `testing`'s t.FailNow is written on.
func Goexit() { goexit() }

func goexit()

// Stack traces arrive with M3's Rust-line to Go-position table; until then a
// caller has no name.
//
// It does have to *exist*: `testing` panics outright if `Callers` reports no
// frames at all, so one comes back with nothing in it, and a failure prints
// its position as `:0` rather than the file and line it came from.

func Caller(skip int) (pc uintptr, file string, line int, ok bool) {
	return unknownPC, "", 0, false
}

func Callers(skip int, pc []uintptr) int {
	if len(pc) == 0 {
		return 0
	}
	pc[0] = unknownPC
	return 1
}

// unknownPC stands for a frame rustygo cannot name yet. Not zero: a zero
// program counter means there is no frame there at all.
const unknownPC = 1

type Frame struct {
	PC       uintptr
	Func     *Func
	Function string
	File     string
	Line     int
	Entry    uintptr
}

type Frames struct{ left int }

func CallersFrames(callers []uintptr) *Frames { return &Frames{left: len(callers)} }

func (ci *Frames) Next() (frame Frame, more bool) {
	if ci.left == 0 {
		return Frame{}, false
	}
	ci.left--
	return Frame{PC: unknownPC}, ci.left > 0
}

type Func struct{}

func FuncForPC(pc uintptr) *Func                  { return nil }
func (f *Func) Name() string                      { return "" }
func (f *Func) Entry() uintptr                    { return 0 }
func (f *Func) FileLine(pc uintptr) (string, int) { return "", 0 }
func Stack(buf []byte, all bool) int              { return 0 }

// SetFinalizer is written at the call site, where the emitter can still see
// that `obj` is a *T and `finalizer` a func(*T) — by the time the runtime has
// two `any`s, it cannot make the call (internal/emit/finalizer.go). This body
// is what remains for a call the emitter cannot read that way: it accepts the
// finalizer and never runs it, which Go allows, since a finalizer is only ever
// promised to run at most once.

func SetFinalizer(obj any, finalizer any) {}

// Cleanups are still to come. Like a finalizer that never runs, a cleanup
// that never runs is allowed; the object simply stays alive.

type Cleanup struct{}

func AddCleanup[T, S any](ptr *T, cleanup func(S), arg S) Cleanup { return Cleanup{} }
func (c Cleanup) Stop()                                           {}

// Which makes waiting for the cleanup queue to empty a wait for nothing: the
// queue a cleanup would be put on is always empty, because none is ever
// queued. `sync` and `unique` both wait for it in their tests, before checking
// that what a cleanup should have released has been.
//
// It will therefore go on to find the object still there. That is the honest
// answer to give it — the queue really is empty — and it is better than a wait
// that times out, which would say the queue was busy when nothing has ever
// been in it. (gc answers `unique`'s copy of the same question too; rustygo's
// `unique` is its own package and does not ask.)

//go:linkname sync_test_blockUntilEmptyCleanupQueue sync_test.runtime_blockUntilEmptyCleanupQueue
func sync_test_blockUntilEmptyCleanupQueue(timeout int64) bool { return true }

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

// heapStats is what the collector tracks: live objects and bytes, every
// object and byte ever allocated, and how many collections have run.
func heapStats() (objects, bytes, totalObjects, totalBytes, collections uint64)

// ReadMemStats fills in what rustygo's collector knows and leaves the rest
// zero. There is no separate heap, stack and span accounting to report: one
// heap, one allocation per object (DESIGN §3), so Sys is the live heap and
// the several ways gc splits that number all give the same answer.
// The fields are assigned one by one rather than through a struct literal,
// which go/ssa builds in memory of its own: a program may call this between
// two readings of Mallocs and expect the count not to have moved
// (test/closure.go does), so reporting the numbers must not allocate.
func ReadMemStats(m *MemStats) {
	objects, bytes, totalObjects, totalBytes, collections := heapStats()
	m.Alloc = bytes
	m.TotalAlloc = totalBytes
	m.Sys = bytes
	m.Mallocs = totalObjects
	m.Frees = totalObjects - objects
	m.HeapAlloc = bytes
	m.HeapSys = bytes
	m.HeapInuse = bytes
	m.HeapObjects = objects
	m.NumGC = uint32(collections)
	m.EnableGC = true
}

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

// StackRecord and BlockProfileRecord are what the other profiles report, and
// every one of them reports nothing.

type StackRecord struct{ Stack0 [32]uintptr }

func (r *StackRecord) Stack() []uintptr { return nil }

type BlockProfileRecord struct {
	Count  int64
	Cycles int64
	StackRecord
}

func ThreadCreateProfile(p []StackRecord) (n int, ok bool) { return 0, true }
func BlockProfile(p []BlockProfileRecord) (n int, ok bool) { return 0, true }
func MutexProfile(p []BlockProfileRecord) (n int, ok bool) { return 0, true }
func GoroutineProfile(p []StackRecord) (n int, ok bool)    { return 0, true }
func SetCPUProfileRate(hz int)                             {}
func CPUProfile() []byte                                   { return nil }

// The execution tracer, which `testing` reaches for when asked to write a
// trace. Starting it reports that there is nothing to start.

func StartTrace() error { return errUnsupported("runtime.StartTrace") }
func StopTrace()        {}
func ReadTrace() []byte { return nil }

type traceUnsupported string

func (e traceUnsupported) Error() string { return string(e) + ": not supported by rustygo yet" }

func errUnsupported(what string) error { return traceUnsupported(what) }

// Breakpoint would trap into a debugger; there is none to trap into.
func Breakpoint() { panic("runtime.Breakpoint: no debugger (rustygo)") }

func SetMutexProfileFraction(rate int) int { return 0 }
func SetBlockProfileRate(rate int)         {}

// What `runtime/debug` asks the runtime for. The collector has no percentage
// to set and no limit to keep — it collects when the live set has doubled
// (src/heap.rs) — and a fault is a fault: rustygo does not catch one and turn
// it into a panic, so `SetPanicOnFault` cannot honestly say it did.

// readGCStats fills in the buffer debug.ReadGCStats hands over: `n` pause
// durations, then `n` moments those pauses ended, then the wall time of the
// last collection, how many there have been, and the total pause.
//
// The collector counts its collections and does not time them, which is what
// ReadMemStats already says — every entry of its PauseNs is zero. So the pauses
// and their end times are reported as zero here as well, one per collection,
// and the two answers agree. Leaving the buffer alone instead, as this used to,
// left ReadGCStats reading whatever happened to be in it.
//
//go:linkname debug_readGCStats runtime/debug.readGCStats
func debug_readGCStats(p *[]int64) {
	_, _, _, _, collections := heapStats()
	n := int(collections)
	if n > pauseRecords {
		n = pauseRecords
	}
	b := (*p)[:2*n+3]
	clear(b[:2*n])
	b[2*n] = 0                    // no wall time kept for the last collection
	b[2*n+1] = int64(collections) //
	b[2*n+2] = 0                  // no pause measured, so none to total
	*p = b
}

// pauseRecords is how many collections the pause history holds, which is
// len(MemStats.PauseNs); debug.ReadGCStats sizes its buffer from the same
// number and will not take more.
const pauseRecords = 256

//go:linkname debug_freeOSMemory runtime/debug.freeOSMemory
func debug_freeOSMemory() { GC() }

//go:linkname debug_setMaxStack runtime/debug.setMaxStack
func debug_setMaxStack(n int) int { return 1 << 20 }

//go:linkname debug_setGCPercent runtime/debug.setGCPercent
func debug_setGCPercent(n int32) int32 { return 100 }

//go:linkname debug_setPanicOnFault runtime/debug.setPanicOnFault
func debug_setPanicOnFault(b bool) bool { return false }

//go:linkname debug_setMaxThreads runtime/debug.setMaxThreads
func debug_setMaxThreads(n int) int { return 10000 }

//go:linkname debug_setMemoryLimit runtime/debug.setMemoryLimit
func debug_setMemoryLimit(n int64) int64 { return 1<<63 - 1 }

// debug.SetCrashOutput asks for the report a fatal panic writes to go to a
// second descriptor as well as to standard error, which is how a program keeps
// its own crash log. The runtime holds the descriptor and writes there too
// (src/rt.rs); ^uintptr(0) back means there was none before, which is what
// tells the caller not to close anything.

func crashFD(fd uintptr) uintptr

// setCrashFD is the name runtime/debug reaches for.
func setCrashFD(fd uintptr) uintptr { return crashFD(fd) }

// debug.WriteHeapDump writes every object in the heap to a descriptor, in the
// format `viewcore` reads: gc walks its spans and emits a record per object,
// per type and per goroutine stack frame.
//
// rustygo's collector could walk the heap — that is what tracing does — but
// none of the rest is there: an object knows how to trace itself and not how
// to describe itself, there are no span boundaries to report, and a goroutine
// stack is a Rust stack with a shadow list of roots beside it. So nothing is
// written, which leaves the caller an empty file rather than a dump in a
// format nothing could read.

//go:linkname debug_WriteHeapDump runtime/debug.WriteHeapDump
func debug_WriteHeapDump(fd uintptr) {}

// PanicNilError is what recover returns after panic(nil).
type PanicNilError struct{ _ [0]*PanicNilError }

func (*PanicNilError) Error() string { return "panic called with nil argument (goexit=false)" }
func (*PanicNilError) RuntimeError() {}
