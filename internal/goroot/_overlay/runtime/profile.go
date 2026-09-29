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

// Signals. Nothing delivers one, so nothing is ever received: the goroutine
// waiting for one waits for ever, which is what it does under gc in a process
// that is sent no signals.

//go:linkname signal_enable os/signal.signal_enable
func signal_enable(s uint32) {}

//go:linkname signal_disable os/signal.signal_disable
func signal_disable(s uint32) {}

//go:linkname signal_ignore os/signal.signal_ignore
func signal_ignore(s uint32) {}

//go:linkname signal_ignored os/signal.signal_ignored
func signal_ignored(s uint32) bool { return true }

//go:linkname signal_recv os/signal.signal_recv
func signal_recv() uint32 {
	for {
		semapark(0)
	}
}

//go:linkname signalWaitUntilIdle os/signal.signalWaitUntilIdle
func signalWaitUntilIdle() {}

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
