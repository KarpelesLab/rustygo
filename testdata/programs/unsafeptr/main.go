package main

import (
	"math"
	"unsafe"
)

type Header struct {
	A int32
	B int32
	C int64
}

func float64bits(f float64) uint64 { return *(*uint64)(unsafe.Pointer(&f)) }

func float64frombits(b uint64) float64 { return *(*float64)(unsafe.Pointer(&b)) }

func main() {
	// Reinterpretation, the way math.Float64bits does it.
	println(float64bits(1.5), float64frombits(0x3ff8000000000000))

	// Field offsets and pointer arithmetic match gc's layout.
	h := Header{A: 1, B: 2, C: 3}
	println(unsafe.Sizeof(h), unsafe.Offsetof(h.B), unsafe.Offsetof(h.C), unsafe.Alignof(h.C))
	pb := (*int32)(unsafe.Add(unsafe.Pointer(&h), unsafe.Offsetof(h.B)))
	*pb = 20
	pc := (*int64)(unsafe.Pointer(uintptr(unsafe.Pointer(&h)) + unsafe.Offsetof(h.C)))
	*pc = 30
	println(h.A, h.B, h.C)

	// unsafe.String / StringData / Slice / SliceData.
	b := []byte("hello, world")
	s := unsafe.String(unsafe.SliceData(b), 5)
	println(s, len(s))
	bs := unsafe.Slice(unsafe.StringData("gopher"), 3)
	println(len(bs), bs[0], bs[2])
	arr := [4]int{10, 20, 30, 40}
	view := unsafe.Slice(&arr[1], 2)
	view[0] = 99
	println(arr[1], len(view), cap(view))

	// Pointer identity through unsafe.Pointer.
	p1 := unsafe.Pointer(&arr[0])
	p2 := unsafe.Pointer(&arr[0])
	var nilp unsafe.Pointer
	println(p1 == p2, nilp == nil, p1 != nilp)
	println(uintptr(p1) != 0)

	// What unsafe.Slice and unsafe.String refuse. The last address is a
	// constant of type unsafe.Pointer, and one byte there is in range while
	// two are not: the elements have to fit before the address space ends.
	last := (*byte)(unsafe.Pointer(^uintptr(0)))
	_ = unsafe.Slice(last, 1)
	_ = unsafe.String(last, 1)
	var neg = -1
	var huge uint64 = math.MaxUint64
	refuses("negative length", func() { _ = unsafe.Slice(new(byte), neg) })
	refuses("length overflows int", func() { _ = unsafe.Slice(new(byte), huge) })
	refuses("elements overflow the space", func() { _ = unsafe.Slice(new(uint64), maxUintptr/8) })
	refuses("slice past the last address", func() { _ = unsafe.Slice(last, 2) })
	refuses("nil with a length", func() { _ = unsafe.Slice((*int)(nil), 1) })
	println(unsafe.Slice((*int)(nil), 0) == nil)
	refuses("string negative length", func() { _ = unsafe.String(new(byte), neg) })
	refuses("string past the last address", func() { _ = unsafe.String(last, 2) })
	refuses("string nil with a length", func() { _ = unsafe.String(nil, 1) })
	println(unsafe.String(nil, 0) == "")
}

const maxUintptr = 1 << (8 * unsafe.Sizeof(uintptr(0)))

// refuses runs f and prints what it panicked with.
func refuses(what string, f func()) {
	defer func() {
		r := recover()
		err, ok := r.(error)
		if !ok {
			println(what, "did not panic with an error")
			return
		}
		println(what+":", err.Error())
	}()
	f()
}
