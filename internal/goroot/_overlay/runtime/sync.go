// Copyright 2026 Karpelès Lab Inc. MIT license.

package runtime

import "unsafe"

// What package sync and internal/sync ask of the runtime, through
// go:linkname. M1 has one goroutine, so a semaphore that would block can
// never be released: that is gc's deadlock, reported the same way. M2's
// scheduler replaces these.

func semacquire(s *uint32) {
	if *s == 0 {
		fatal("all goroutines are asleep - deadlock!")
	}
	*s--
}

func semrelease(s *uint32) { *s++ }

//go:linkname sync_runtime_Semacquire sync.runtime_Semacquire
func sync_runtime_Semacquire(s *uint32) { semacquire(s) }

//go:linkname sync_runtime_SemacquireWaitGroup sync.runtime_SemacquireWaitGroup
func sync_runtime_SemacquireWaitGroup(s *uint32, synctestDurable bool) { semacquire(s) }

//go:linkname sync_runtime_SemacquireRWMutexR sync.runtime_SemacquireRWMutexR
func sync_runtime_SemacquireRWMutexR(s *uint32, lifo bool, skipframes int) { semacquire(s) }

//go:linkname sync_runtime_SemacquireRWMutex sync.runtime_SemacquireRWMutex
func sync_runtime_SemacquireRWMutex(s *uint32, lifo bool, skipframes int) { semacquire(s) }

//go:linkname sync_runtime_Semrelease sync.runtime_Semrelease
func sync_runtime_Semrelease(s *uint32, handoff bool, skipframes int) { semrelease(s) }

//go:linkname internal_sync_runtime_SemacquireMutex internal/sync.runtime_SemacquireMutex
func internal_sync_runtime_SemacquireMutex(s *uint32, lifo bool, skipframes int) { semacquire(s) }

//go:linkname internal_sync_runtime_Semrelease internal/sync.runtime_Semrelease
func internal_sync_runtime_Semrelease(s *uint32, handoff bool, skipframes int) { semrelease(s) }

//go:linkname internal_sync_runtime_canSpin internal/sync.runtime_canSpin
func internal_sync_runtime_canSpin(i int) bool { return false }

//go:linkname internal_sync_runtime_doSpin internal/sync.runtime_doSpin
func internal_sync_runtime_doSpin() {}

//go:linkname internal_sync_runtime_nanotime internal/sync.runtime_nanotime
func internal_sync_runtime_nanotime() int64 { return nanotime() }

//go:linkname internal_sync_throw internal/sync.throw
func internal_sync_throw(s string) { fatal(s) }

//go:linkname internal_sync_fatal internal/sync.fatal
func internal_sync_fatal(s string) { fatal(s) }

//go:linkname sync_throw sync.throw
func sync_throw(s string) { fatal(s) }

//go:linkname sync_fatal sync.fatal
func sync_fatal(s string) { fatal(s) }

// Condition variables: with one goroutine, a Wait has nobody to wake it.

//go:linkname sync_runtime_notifyListAdd sync.runtime_notifyListAdd
func sync_runtime_notifyListAdd(l unsafe.Pointer) uint32 { return 0 }

//go:linkname sync_runtime_notifyListWait sync.runtime_notifyListWait
func sync_runtime_notifyListWait(l unsafe.Pointer, t uint32) {
	fatal("all goroutines are asleep - deadlock!")
}

//go:linkname sync_runtime_notifyListNotifyAll sync.runtime_notifyListNotifyAll
func sync_runtime_notifyListNotifyAll(l unsafe.Pointer) {}

//go:linkname sync_runtime_notifyListNotifyOne sync.runtime_notifyListNotifyOne
func sync_runtime_notifyListNotifyOne(l unsafe.Pointer) {}

//go:linkname sync_runtime_notifyListCheck sync.runtime_notifyListCheck
func sync_runtime_notifyListCheck(size uintptr) {}

// sync.Pool: one processor, never cleaned (a cleanup may always be skipped).

//go:linkname sync_runtime_registerPoolCleanup sync.runtime_registerPoolCleanup
func sync_runtime_registerPoolCleanup(cleanup func()) {}

//go:linkname sync_runtime_procPin sync.runtime_procPin
func sync_runtime_procPin() int { return 0 }

//go:linkname sync_runtime_procUnpin sync.runtime_procUnpin
func sync_runtime_procUnpin() {}

// randn returns a pseudo-random number in [0, n), for sync.Pool and others.
// gc's is seeded per process; this one is a fixed-seed wyrand, which no
// program may rely on either way.
func randn(n uint32) uint32 {
	randState += 0xa0761d6478bd642f
	hi, lo := mul64(randState, randState^0xe7037ed1a0b428db)
	return uint32((uint64(uint32(hi^lo)) * uint64(n)) >> 32)
}

var randState uint64

func mul64(x, y uint64) (hi, lo uint64) {
	const mask32 = 1<<32 - 1
	x0, x1 := x&mask32, x>>32
	y0, y1 := y&mask32, y>>32
	w0 := x0 * y0
	t := x1*y0 + w0>>32
	w1, w2 := t&mask32, t>>32
	w1 += x0 * y1
	hi = x1*y1 + w2 + w1>>32
	lo = x * y
	return
}

//go:linkname sync_runtime_randn sync.runtime_randn
func sync_runtime_randn(n uint32) uint32 { return randn(n) }

// fatal prints gc's "fatal error: " report and exits with status 2.
func fatal(s string)

// nanotime is the monotonic clock, in nanoseconds.
func nanotime() int64
