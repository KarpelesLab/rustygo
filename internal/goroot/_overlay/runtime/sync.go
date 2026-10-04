// Copyright 2026 Karpelès Lab Inc. MIT license.

package runtime

import "unsafe"

// What package sync and internal/sync ask of the runtime, through
// go:linkname. A semaphore that cannot be taken parks the goroutine on the
// address of its counter, and releasing it wakes the one that has waited
// longest (DESIGN §4).
//
// The whole of acquire and release is the runtime's, in Rust. It used to be the
// loop below, written in Go; with goroutines on more than one thread that no
// longer works, because the test of the counter and the park have to agree
// about what happened between them, and only the side that owns the wait queue
// can make them. The counter is also read and written atomically there, so two
// goroutines cannot take the same unit.

func semacquire(s *uint32)
func semrelease(s *uint32)

// semapark parks this goroutine on an address, and semawake readies the
// goroutine that has waited longest on it. What is left of the pair that
// semacquire used to be built from: a wait whose condition is the wake itself.
func semapark(addr uintptr)
func semawake(addr uintptr)

// The atomics the runtime package needs for its own bookkeeping. sync/atomic is
// not reachable from here, so the runtime answers them itself.
func atomicLoad32(p *uint32) uint32
func atomicStore32(p *uint32, v uint32)
func atomicAdd32(p *uint32, d uint32) uint32

// procPin keeps a goroutine on the processor it is running on, and reports that
// processor's number, which is below GOMAXPROCS. sync shards caches by it.
func procPin() int
func procUnpin()

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

// sync/atomic and sync.Pool pin a goroutine to its processor while they work on
// that processor's own data, and index arrays by the number they are given. The
// runtime answers with a processor slot below GOMAXPROCS that no other running
// goroutine holds, and keeps the goroutine where it is until it unpins — which
// is what makes a per-processor shard safe to touch without a lock.

//go:linkname atomic_runtime_procPin sync/atomic.runtime_procPin
func atomic_runtime_procPin() int { return procPin() }

//go:linkname atomic_runtime_procUnpin sync/atomic.runtime_procUnpin
func atomic_runtime_procUnpin() { procUnpin() }

// A weak pointer is a strong one here. rustygo's collector has no way yet to
// clear a reference when its object dies (that is the same machinery as
// runtime.SetFinalizer, still to come), so a weak pointer keeps its object
// alive instead of losing it. The cost is that what only a weak pointer
// refers to is never collected — `unique.Make`'s interning table, which is
// what net/netip uses, therefore never shrinks.

//go:linkname weak_registerWeakPointer weak.runtime_registerWeakPointer
func weak_registerWeakPointer(p unsafe.Pointer) unsafe.Pointer { return p }

//go:linkname weak_makeStrongFromWeak weak.runtime_makeStrongFromWeak
func weak_makeStrongFromWeak(p unsafe.Pointer) unsafe.Pointer { return p }

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

// sync.Cond's ticket list. The layout is sync's own, because the runtime is
// handed a pointer to it; notifyListCheck is how sync asks whether the two
// agree.
//
// A waiter takes a ticket, and waits until `notify` passes it. Waking is by
// address, all at once, each waiter looking at its own ticket again — the
// same shape as a channel's waiters, and for the same reason: waking one and
// choosing wrongly loses a wakeup, while waking all cannot.
type notifyList struct {
	wait   uint32
	notify uint32
	lock   uintptr
	head   unsafe.Pointer
	tail   unsafe.Pointer
}

// notified reports whether ticket t has been called, counting the wraparound
// the way gc's `less` does.
func notified(t, notify uint32) bool {
	return int32(t-notify) < 0
}

// The two counters are read and written atomically: sync.Cond documents that
// Signal and Broadcast may be called without holding the lock a Wait holds, so
// a ticket taken on one thread and a notification sent from another meet here
// and nowhere else.

//go:linkname sync_runtime_notifyListAdd sync.runtime_notifyListAdd
func sync_runtime_notifyListAdd(l *notifyList) uint32 {
	return atomicAdd32(&l.wait, 1) - 1
}

//go:linkname sync_runtime_notifyListWait sync.runtime_notifyListWait
func sync_runtime_notifyListWait(l *notifyList, t uint32) {
	for !notified(t, atomicLoad32(&l.notify)) {
		semaparkall(uintptr(unsafe.Pointer(l)))
	}
}

//go:linkname sync_runtime_notifyListNotifyAll sync.runtime_notifyListNotifyAll
func sync_runtime_notifyListNotifyAll(l *notifyList) {
	wait := atomicLoad32(&l.wait)
	if atomicLoad32(&l.notify) == wait {
		return
	}
	atomicStore32(&l.notify, wait)
	semawakeall(uintptr(unsafe.Pointer(l)))
}

//go:linkname sync_runtime_notifyListNotifyOne sync.runtime_notifyListNotifyOne
func sync_runtime_notifyListNotifyOne(l *notifyList) {
	if atomicLoad32(&l.notify) == atomicLoad32(&l.wait) {
		return
	}
	atomicAdd32(&l.notify, 1)
	semawakeall(uintptr(unsafe.Pointer(l)))
}

//go:linkname sync_runtime_notifyListCheck sync.runtime_notifyListCheck
func sync_runtime_notifyListCheck(size uintptr) {
	if size != unsafe.Sizeof(notifyList{}) {
		fatal("runtime: bad notifyList size")
	}
}

// semaparkall parks on an address that semawakeall wakes, which is every
// waiter at once rather than the longest-waiting one.
func semaparkall(addr uintptr)
func semawakeall(addr uintptr)

// sync.Pool: never cleaned (a cleanup may always be skipped). It shards its
// cache by processor, so a pinned goroutine really has to stay where it is.

//go:linkname sync_runtime_registerPoolCleanup sync.runtime_registerPoolCleanup
func sync_runtime_registerPoolCleanup(cleanup func()) {}

//go:linkname sync_runtime_procPin sync.runtime_procPin
func sync_runtime_procPin() int { return procPin() }

//go:linkname sync_runtime_procUnpin sync.runtime_procUnpin
func sync_runtime_procUnpin() { procUnpin() }

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

// $GODEBUG is in os.go, with the rest of the environment.

// write is what internal/godebug's `//go:linkname write runtime.write`
// names: standard error, whatever the descriptor says.
func write(fd uintptr, p unsafe.Pointer, n int32) int32 {
	writeErr(p, n)
	return n
}

// rand and randn are the runtime's randomness, which sync and others pull.
func rand() uint64 {
	randState += 0xa0761d6478bd642f
	hi, lo := mul64(randState, randState^0xe7037ed1a0b428db)
	return hi ^ lo
}

// The scheduler bookkeeping around a system call, which M2 will make real:
// with one goroutine there is nothing to hand off to.

//go:linkname syscall_runtime_entersyscall syscall.runtime_entersyscall
func syscall_runtime_entersyscall() {}

//go:linkname syscall_runtime_exitsyscall syscall.runtime_exitsyscall
func syscall_runtime_exitsyscall() {}

// internal/poll's semaphores and netpoller. A socket that would block parks
// its goroutine on the descriptor, and the scheduler sleeps in epoll until
// one moves (src/netpoll.rs). A regular file is opened unpollable and never
// reaches here.

//go:linkname poll_runtime_Semacquire internal/poll.runtime_Semacquire
func poll_runtime_Semacquire(s *uint32) { semacquire(s) }

//go:linkname poll_runtime_Semrelease internal/poll.runtime_Semrelease
func poll_runtime_Semrelease(s *uint32) { semrelease(s) }

// The poller is created when the first descriptor is registered, so there is
// nothing to start here.

//go:linkname poll_runtime_pollServerInit internal/poll.runtime_pollServerInit
func poll_runtime_pollServerInit() {}

// pollOpen and friends are the runtime's, in Rust.
func pollOpen(fd uintptr) (uintptr, int)
func pollClose(ctx uintptr)
func pollReset(ctx uintptr, mode int) int
func pollWait(ctx uintptr, mode int) int
func pollUnblock(ctx uintptr)

//go:linkname poll_runtime_pollOpen internal/poll.runtime_pollOpen
func poll_runtime_pollOpen(fd uintptr) (uintptr, int) { return pollOpen(fd) }

//go:linkname poll_runtime_pollClose internal/poll.runtime_pollClose
func poll_runtime_pollClose(ctx uintptr) { pollClose(ctx) }

//go:linkname poll_runtime_pollReset internal/poll.runtime_pollReset
func poll_runtime_pollReset(ctx uintptr, mode int) int { return pollReset(ctx, mode) }

//go:linkname poll_runtime_pollWait internal/poll.runtime_pollWait
func poll_runtime_pollWait(ctx uintptr, mode int) int { return pollWait(ctx, mode) }

// A cancelled wait is one whose goroutine is going away regardless of what
// the descriptor does; there is nothing to undo, because a wait registers
// itself and clears itself.

//go:linkname poll_runtime_pollWaitCanceled internal/poll.runtime_pollWaitCanceled
func poll_runtime_pollWaitCanceled(ctx uintptr, mode int) {}

// A deadline is a moment on the monotonic clock after which reads or writes
// on this descriptor fail instead of waiting. net/http cannot work without
// them: it aborts a read that is already in progress by giving it a deadline
// in the past and waiting for it to come back.
func pollSetDeadline(ctx uintptr, at int64, mode int)

//go:linkname poll_runtime_pollSetDeadline internal/poll.runtime_pollSetDeadline
func poll_runtime_pollSetDeadline(ctx uintptr, d int64, mode int) {
	// What internal/poll hands over is how long from now, not when: zero for
	// no deadline at all, and a negative value for one that has already
	// passed. The poller wants the moment, so the sum is made here, as gc's
	// runtime makes it. Getting this wrong is not obvious from a test that
	// only asks whether the deadline fired — it did, immediately, because a
	// duration read as a moment is a moment just after the process started.
	at := d
	if d > 0 {
		if at = nanotime() + d; at <= 0 {
			at = 1<<63 - 1 // so far off it never arrives
		}
	}
	pollSetDeadline(ctx, at, mode)
}

//go:linkname poll_runtime_pollUnblock internal/poll.runtime_pollUnblock
func poll_runtime_pollUnblock(ctx uintptr) { pollUnblock(ctx) }

//go:linkname poll_runtime_isPollServerDescriptor internal/poll.runtime_isPollServerDescriptor
func poll_runtime_isPollServerDescriptor(fd uintptr) bool { return false }

// writeErr writes n bytes to standard error.
func writeErr(p unsafe.Pointer, n int32)
