// Copyright 2026 Karpelès Lab Inc. MIT license.

// rustygo's bodies for sync/atomic's functions, which gc writes in assembly.
// The package's types (Int64, Value, Pointer[T], ...) stay gc's, and call
// these.
//
// M1 runs one goroutine, so plain loads and stores are atomic. M2, which
// brings threads, replaces these with the runtime's atomics
// (DESIGN §13, questions 6 and 7).

package atomic

import "unsafe"

var _ unsafe.Pointer

func SwapInt32(addr *int32, new int32) (old int32) {
	old = *addr
	*addr = new
	return
}

func SwapUint32(addr *uint32, new uint32) (old uint32) {
	old = *addr
	*addr = new
	return
}

func SwapUintptr(addr *uintptr, new uintptr) (old uintptr) {
	old = *addr
	*addr = new
	return
}

func SwapPointer(addr *unsafe.Pointer, new unsafe.Pointer) (old unsafe.Pointer) {
	old = *addr
	*addr = new
	return
}

func CompareAndSwapInt32(addr *int32, old, new int32) (swapped bool) {
	if *addr == old {
		*addr = new
		return true
	}
	return false
}

func CompareAndSwapUint32(addr *uint32, old, new uint32) (swapped bool) {
	if *addr == old {
		*addr = new
		return true
	}
	return false
}

func CompareAndSwapUintptr(addr *uintptr, old, new uintptr) (swapped bool) {
	if *addr == old {
		*addr = new
		return true
	}
	return false
}

func CompareAndSwapPointer(addr *unsafe.Pointer, old, new unsafe.Pointer) (swapped bool) {
	if *addr == old {
		*addr = new
		return true
	}
	return false
}

func AddInt32(addr *int32, delta int32) (new int32) {
	*addr += delta
	return *addr
}

func AddUint32(addr *uint32, delta uint32) (new uint32) {
	*addr += delta
	return *addr
}

func AddUintptr(addr *uintptr, delta uintptr) (new uintptr) {
	*addr += delta
	return *addr
}

func AndInt32(addr *int32, mask int32) (old int32) {
	old = *addr
	*addr &= mask
	return
}

func AndUint32(addr *uint32, mask uint32) (old uint32) {
	old = *addr
	*addr &= mask
	return
}

func AndUintptr(addr *uintptr, mask uintptr) (old uintptr) {
	old = *addr
	*addr &= mask
	return
}

func OrInt32(addr *int32, mask int32) (old int32) {
	old = *addr
	*addr |= mask
	return
}

func OrUint32(addr *uint32, mask uint32) (old uint32) {
	old = *addr
	*addr |= mask
	return
}

func OrUintptr(addr *uintptr, mask uintptr) (old uintptr) {
	old = *addr
	*addr |= mask
	return
}

func LoadInt32(addr *int32) (val int32) {
	return *addr
}

func LoadUint32(addr *uint32) (val uint32) {
	return *addr
}

func LoadUintptr(addr *uintptr) (val uintptr) {
	return *addr
}

func LoadPointer(addr *unsafe.Pointer) (val unsafe.Pointer) {
	return *addr
}

func StoreInt32(addr *int32, val int32) {
	*addr = val
}

func StoreUint32(addr *uint32, val uint32) {
	*addr = val
}

func StoreUintptr(addr *uintptr, val uintptr) {
	*addr = val
}

func StorePointer(addr *unsafe.Pointer, val unsafe.Pointer) {
	*addr = val
}

func SwapInt64(addr *int64, new int64) (old int64) {
	old = *addr
	*addr = new
	return
}

func SwapUint64(addr *uint64, new uint64) (old uint64) {
	old = *addr
	*addr = new
	return
}

func CompareAndSwapInt64(addr *int64, old, new int64) (swapped bool) {
	if *addr == old {
		*addr = new
		return true
	}
	return false
}

func CompareAndSwapUint64(addr *uint64, old, new uint64) (swapped bool) {
	if *addr == old {
		*addr = new
		return true
	}
	return false
}

func AddInt64(addr *int64, delta int64) (new int64) {
	*addr += delta
	return *addr
}

func AddUint64(addr *uint64, delta uint64) (new uint64) {
	*addr += delta
	return *addr
}

func AndInt64(addr *int64, mask int64) (old int64) {
	old = *addr
	*addr &= mask
	return
}

func AndUint64(addr *uint64, mask uint64) (old uint64) {
	old = *addr
	*addr &= mask
	return
}

func OrInt64(addr *int64, mask int64) (old int64) {
	old = *addr
	*addr |= mask
	return
}

func OrUint64(addr *uint64, mask uint64) (old uint64) {
	old = *addr
	*addr |= mask
	return
}

func LoadInt64(addr *int64) (val int64) {
	return *addr
}

func LoadUint64(addr *uint64) (val uint64) {
	return *addr
}

func StoreInt64(addr *int64, val int64) {
	*addr = val
}

func StoreUint64(addr *uint64, val uint64) {
	*addr = val
}
