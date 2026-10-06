package runtime

import (
	"internal/profilerecord"
	"unsafe"
)

// The hooks `runtime/pprof`, `os/signal` and `runtime/debug` expect the runtime
// to provide.
//
// Profiling and signal delivery are not here (roadmap: profiling is a non-goal
// for now, signals are M3's `os/signal`). These exist because a test binary
// reaches every one of them: `testing` imports `runtime/pprof` for its
// `-test.cpuprofile` flags and `os/signal` for its timeout, and a program that
// asks for neither still has to link.
//
// Each one answers what an empty profile and a quiet process would: no samples,
// no frames, no signals.

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
// it is given, by name, and its own Read documents what happens to a name the
// runtime does not implement: the Value comes back as KindBad. rustygo
// implements none of them, so that is what every one of them gets.
//
// The collector does know some of what the list asks about — live bytes and
// objects, and how many collections have run, which ReadMemStats already
// reports — so this could answer a handful of the hundred names in
// metrics.All(). It does not, because a caller cannot tell a metric that is
// missing from one that is zero except by the kind, and a partial answer is
// the one shape that makes the distinction useless. The mirror of Sample below
// is what a real implementation would write through.
//
// That mirror is metrics.Sample's layout, because the runtime is handed a
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

//go:linkname metrics_readMetrics runtime/metrics.runtime_readMetrics
func metrics_readMetrics(p unsafe.Pointer, length, capacity int) {
	if p == nil || length <= 0 {
		return
	}
	samples := unsafe.Slice((*metricsSample)(p), length)
	for i := range samples {
		samples[i].value.kind = 0 // metrics.KindBad
		samples[i].value.scalar = 0
		samples[i].value.pointer = nil
	}
}

// The names the runtime implements, which metrics' own test holds against
// metrics.All(). None, so far.

//go:linkname metrics_readMetricNames runtime/metrics_test.runtime_readMetricNames
func metrics_readMetricNames() []string { return nil }

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
