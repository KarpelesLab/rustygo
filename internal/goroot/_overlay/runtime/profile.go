package runtime

import (
	"internal/profilerecord"
	"unsafe"
)

// The hooks `runtime/pprof`, `runtime/metrics` and `os/signal` expect the
// runtime to provide.
//
// Profiling is a roadmap non-goal for now, and these exist because a test
// binary reaches every one of them whether or not it asks: `testing` imports
// `runtime/pprof` for its `-test.cpuprofile` flags, and a program that wants no
// profile still has to link. Each answers what an empty profile would — no
// samples, no frames — rather than refusing.
//
// Signals are at the bottom of the file and are real.

// Profiles, which the standard library pulls from here by name.

func pprof_goroutineProfileWithLabels(p []profilerecord.StackRecord, labels []unsafe.Pointer) (n int, ok bool) {
	return 0, true
}

func pprof_goroutineLeakProfileWithLabels(p []profilerecord.StackRecord, labels []unsafe.Pointer) (n int, ok bool) {
	return 0, true
}

func pprof_memProfileInternal(p []profilerecord.MemProfileRecord, inuseZero bool) (n int, ok bool) {
	return 0, true
}

func pprof_blockProfileInternal(p []profilerecord.BlockProfileRecord) (n int, ok bool) {
	return 0, true
}

func pprof_mutexProfileInternal(p []profilerecord.BlockProfileRecord) (n int, ok bool) {
	return 0, true
}

func pprof_threadCreateInternal(p []profilerecord.StackRecord) (n int, ok bool) {
	return 0, true
}

func pprof_fpunwindExpand(dst, src []uintptr) int { return 0 }

func pprof_makeProfStack() []uintptr { return nil }

// And the ones the runtime pushes into `runtime/pprof`.

//go:linkname pprof_readProfile runtime/pprof.readProfile
func pprof_readProfile() (data []uint64, tags []unsafe.Pointer, eof bool) { return nil, nil, true }

//go:linkname pprof_cyclesPerSecond runtime/pprof.runtime_cyclesPerSecond
func pprof_cyclesPerSecond() int64 { return 1e9 }

//go:linkname pprof_frameStartLine runtime/pprof.runtime_FrameStartLine
func pprof_frameStartLine(f *Frame) int { return 0 }

//go:linkname pprof_frameSymbolName runtime/pprof.runtime_FrameSymbolName
func pprof_frameSymbolName(f *Frame) string { return f.Function }

//go:linkname pprof_expandFinalInlineFrame runtime/pprof.runtime_expandFinalInlineFrame
func pprof_expandFinalInlineFrame(stk []uintptr) []uintptr { return stk }

//go:linkname pprof_goroutineleakcount runtime/pprof.runtime_goroutineleakcount
func pprof_goroutineleakcount() int { return 0 }

// Profile labels, which `pprof.Do` attaches to the goroutine so that the CPU
// profiler can tag the samples it takes there. gc keeps the set on the g; this
// keeps it in one place, which is the same thing for as long as the labels are
// set and read between two points where the goroutine does not change — which
// is how pprof.Do uses them, saving the old set and putting it back. Two
// goroutines labelling themselves at once would see each other's; nothing
// reads the labels at all, because no profile is taken.

var profLabels unsafe.Pointer

//go:linkname pprof_setProfLabel runtime/pprof.runtime_setProfLabel
func pprof_setProfLabel(labels unsafe.Pointer) { profLabels = labels }

//go:linkname pprof_getProfLabel runtime/pprof.runtime_getProfLabel
func pprof_getProfLabel() unsafe.Pointer { return profLabels }

// blockevent is how the runtime records that a goroutine was blocked for a
// while, which is the block profile's only source. There is no profile to
// record into; pprof's own test reaches it by name to bias the profile it then
// reads back, and reads back nothing.

func blockevent(cycles int64, skip int) {}

// Metrics. runtime/metrics asks the runtime to fill in a Value for each Sample
// it is given, by name; its own Read documents what a name the runtime does not
// implement gets back, which is KindBad.
//
// rustygo answers the ones it can answer truthfully, which are the counters the
// collector already keeps and the few whose honest value is a constant: there
// is no soft memory limit, the heap doubles rather than following a percentage,
// and no finalizer or cleanup is ever queued or run. Everything else in
// metrics.All() — the CPU classes, the size histograms, the scavenger — is
// KindBad, because there is no number behind it and a zero would read as one.
//
// metricsSample is metrics.Sample's layout, because the runtime is handed a
// pointer to an array of them and nothing passes the size across for it to
// check — unlike sync's notifyList, which does. It is pinned to a Go version
// along with everything else here (DESIGN §8).
type metricsSample struct {
	name  string
	value struct {
		kind    int
		scalar  uint64
		pointer unsafe.Pointer
	}
}

// The kinds, as metrics numbers them.
const (
	metricBad = iota
	metricUint64
)

//go:linkname metrics_readMetrics runtime/metrics.runtime_readMetrics
func metrics_readMetrics(p unsafe.Pointer, length, capacity int) {
	if p == nil || length <= 0 {
		return
	}
	objects, _, totalObjects, _, _ := heapStats()
	samples := unsafe.Slice((*metricsSample)(p), length)
	for i := range samples {
		v, ok := uint64(0), true
		switch samples[i].name {
		case "/gc/heap/allocs:objects":
			v = totalObjects
		case "/gc/heap/frees:objects":
			v = totalObjects - objects
		case "/gc/gogc:percent":
			// The collector runs when the live set has doubled (src/heap.rs),
			// which is what a hundred percent means.
			v = 100
		case "/gc/gomemlimit:bytes":
			v = 1<<63 - 1 // no limit, reported as gc reports none
		case "/gc/cleanups/executed:cleanups", "/gc/cleanups/queued:cleanups",
			"/gc/finalizers/executed:finalizers", "/gc/finalizers/queued:finalizers":
			v = 0 // none is ever queued, so none is ever run
		default:
			ok = false
		}
		samples[i].value.pointer = nil
		if ok {
			samples[i].value.kind, samples[i].value.scalar = metricUint64, v
		} else {
			samples[i].value.kind, samples[i].value.scalar = metricBad, 0
		}
	}
}

// The names the runtime implements, which metrics' own test holds against
// metrics.All(); it reports the difference, so the list being short is a
// failure it can describe rather than one it falls over.

//go:linkname metrics_readMetricNames runtime/metrics_test.runtime_readMetricNames
func metrics_readMetricNames() []string {
	return []string{
		"/gc/cleanups/executed:cleanups",
		"/gc/cleanups/queued:cleanups",
		"/gc/finalizers/executed:finalizers",
		"/gc/finalizers/queued:finalizers",
		"/gc/gogc:percent",
		"/gc/gomemlimit:bytes",
		"/gc/heap/allocs:objects",
		"/gc/heap/frees:objects",
	}
}

// Signals.
//
// `os/signal` asks the runtime to start and stop delivering each signal it is
// interested in, and keeps one goroutine inside signal_recv waiting for the
// next one. The runtime installs a handler and queues what arrives (src/rt.rs);
// these are the names the standard library reaches it by.
//
// gc catches nearly every signal from the start and decides what to do with
// one when it comes. rustygo only catches what a program asked for, so a
// program that asks for nothing is still killed by SIGINT the way any other
// process is — which is what gc does too for a signal nobody wanted.

func signalEnable(s uint32)
func signalDisable(s uint32)
func signalIgnore(s uint32)
func signalIgnored(s uint32) bool
func signalRecv() uint32
func signalWaitIdle()

//go:linkname signal_enable os/signal.signal_enable
func signal_enable(s uint32) { signalEnable(s) }

//go:linkname signal_disable os/signal.signal_disable
func signal_disable(s uint32) { signalDisable(s) }

//go:linkname signal_ignore os/signal.signal_ignore
func signal_ignore(s uint32) { signalIgnore(s) }

//go:linkname signal_ignored os/signal.signal_ignored
func signal_ignored(s uint32) bool { return signalIgnored(s) }

//go:linkname signal_recv os/signal.signal_recv
func signal_recv() uint32 { return signalRecv() }

//go:linkname signalWaitUntilIdle os/signal.signalWaitUntilIdle
func signalWaitUntilIdle() { signalWaitIdle() }

// The traceback setting, which only matters to a crash report rustygo writes
// its own way.

//go:linkname debug_SetTraceback runtime/debug.SetTraceback
func debug_SetTraceback(level string) {}

//go:linkname pprof_goroutineLeakGC runtime/pprof.runtime_goroutineLeakGC
func pprof_goroutineLeakGC() {}

// The execution tracer's clock and its buffer flush, which `runtime/trace`
// pulls from here whether or not anything is tracing.

func traceAdvance(stopTrace bool) {}

func traceClockNow() uint64 { return uint64(nanotime()) }

func traceClockUnitsPerSecond() uint64 { return 1e9 }
