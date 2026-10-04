// Copyright 2026 Karpelès Lab Inc. MIT license.

//go:build purego

package subtle

// XOR over three byte slices, replacing gc's generic version because of
// alignment and nothing else.
//
// gc xors a word at a time, viewing the byte slices as []uintptr at whatever
// address they happen to start, and it is allowed to: on the architectures that
// tolerate unaligned access the loads it emits do not care, and it checks the
// addresses only on the ones that do. rustygo emits Rust, where a load carries
// its type's alignment, so reading a uintptr through an address that is not a
// multiple of eight is undefined whichever instruction the compiler ends up
// choosing — and the compiler is entitled to choose one that faults. The
// misaligned case is the ordinary one here rather than a corner: crypto/tls
// xors a record's payload, which begins five bytes into the buffer the record
// was read into, and crypto/cipher's counter modes inherit that offset.
//
// So the word loop runs only where the three pointers really are word-aligned —
// which is the case for the freshly allocated buffers crypto/rand's DRBG xors,
// and not for a TLS record — and the rest goes a byte at a time. That is slower
// than gc on exactly the misaligned case. A `xor` in the runtime, reached
// through internal/emit/overrides.go, is what would give the speed back
// (roadmap M6), and it would be faster than gc rather than equal to it.
//
// The build tag is the half of gc's that rustygo ever selects: every load sets
// `purego`, and the package's assembly declaration takes the other half, so the
// two together leave the package exactly as consistent as gc's own.

import "unsafe"

const wordSize = int(unsafe.Sizeof(uintptr(0)))

func xorBytes(dstb, xb, yb *byte, n int) {
	done := 0
	// A word at a time while that is defined, which needs both a whole word to
	// read and an address it may be read through.
	if words := n / wordSize; words > 0 && wordAligned(dstb) && wordAligned(xb) && wordAligned(yb) {
		dst := unsafe.Slice((*uintptr)(unsafe.Pointer(dstb)), words)
		x := unsafe.Slice((*uintptr)(unsafe.Pointer(xb)), words)
		y := unsafe.Slice((*uintptr)(unsafe.Pointer(yb)), words)
		for i := range dst {
			dst[i] = x[i] ^ y[i]
		}
		done = words * wordSize
	}
	if done == n {
		return
	}
	dst := unsafe.Slice(dstb, n)
	x := unsafe.Slice(xb, n)
	y := unsafe.Slice(yb, n)
	for i := done; i < n; i++ {
		dst[i] = x[i] ^ y[i]
	}
}

// wordAligned reports whether a uintptr may be read through p.
func wordAligned(p *byte) bool {
	return uintptr(unsafe.Pointer(p))&uintptr(wordSize-1) == 0
}
