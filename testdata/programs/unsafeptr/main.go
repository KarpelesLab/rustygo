package main

import "unsafe"

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
}
