// Copyright 2026 Karpelès Lab Inc. MIT license.

package runtime

import "unsafe"

// `testing/synctest` runs a group of goroutines in a "bubble" with a fake
// clock, and the runtime is what keeps track of which bubble a goroutine or
// a sync value belongs to. rustygo has no bubbles yet, so every goroutine is
// outside one and nothing is associated with one — which is exactly what the
// standard library expects of an ordinary program. `sync` asks these
// questions on every WaitGroup operation, so they have to answer.
//
// Run and Wait are the entry points a test calls deliberately; those say so
// rather than pretending, because a test that thinks it has a fake clock and
// does not would hang instead of failing.

//go:linkname synctest_isInBubble internal/synctest.IsInBubble
func synctest_isInBubble() bool { return false }

//go:linkname synctest_associate internal/synctest.associate
func synctest_associate(p unsafe.Pointer) int { return 0 }

//go:linkname synctest_disassociate internal/synctest.disassociate
func synctest_disassociate(p unsafe.Pointer) {}

//go:linkname synctest_isAssociated internal/synctest.isAssociated
func synctest_isAssociated(p unsafe.Pointer) bool { return false }

//go:linkname synctest_acquire internal/synctest.acquire
func synctest_acquire() any { return nil }

//go:linkname synctest_release internal/synctest.release
func synctest_release(b any) {}

//go:linkname synctest_inBubble internal/synctest.inBubble
func synctest_inBubble(b any, f func()) { f() }

//go:linkname synctest_run internal/synctest.Run
func synctest_run(f func()) {
	panic("testing/synctest: not supported yet (roadmap M2)")
}

//go:linkname synctest_wait internal/synctest.Wait
func synctest_wait() {
	panic("testing/synctest: not supported yet (roadmap M2)")
}
