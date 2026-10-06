// Program structlayout checks that a struct holding a func field has the layout
// reflection says it has.
//
// A descriptor's field offsets are what `Value.Field` reads a field *at*, so they
// have to describe the struct that exists. rustygo lays every type out as gc does
// except one: a func value is a code address and an environment, two words where
// gc's is one (DESIGN §7). So a struct with a func field in it is wider than gc's,
// everything after that field sits further along, and given gc's numbers
// reflection reads and writes the wrong bytes — which is what
// `crypto/tls.Config`, thirty-five fields and eleven of them funcs, was doing.
//
// What this prints is therefore only what must be the same under both compilers:
// what comes back out of a field that was written, whether a fresh value reads
// zero, and the invariants the numbers have to satisfy. The numbers themselves
// are *not* printed, because they honestly differ — `reflect.Type.Size` describes
// the memory and `unsafe.Sizeof` is a constant go/types folded with gc's rules
// before any of rustygo ran, so for these three types the two disagree. Making
// them agree means making a func value one word, which is a much larger change
// (internal/emit/sizes.go says what it would take).
package main

import (
	"reflect"
	"unsafe"
)

type withFunc struct {
	A int64
	F func() int
	B int64
	C string
}

type twoFuncs struct {
	F func()
	G func()
	N int64
}

type nested struct {
	Inner withFunc
	Tail  int64
}

type funcArray struct {
	Head int64
	Fs   [3]func()
	Tail int64
}

// sane reports the invariants a type's own numbers must satisfy whatever the
// layout is: fields in order, each one inside the type, and the type no smaller
// than gc would make it, since only a func value is ever wider here.
func sane(t reflect.Type, gcSize uintptr) bool {
	var at uintptr
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Offset < at || f.Offset+f.Type.Size() > t.Size() {
			return false
		}
		at = f.Offset + f.Type.Size()
	}
	return t.Size() >= gcSize
}

func main() {
	tw := reflect.TypeOf(withFunc{})
	println("withFunc sane", sane(tw, unsafe.Sizeof(withFunc{})))

	// Write each field through reflection and read it back as a field. The two
	// agree only if the offsets describe the struct that exists.
	var w withFunc
	v := reflect.ValueOf(&w).Elem()
	v.Field(0).SetInt(11)
	v.Field(2).SetInt(22)
	v.Field(3).SetString("cc")
	println("withFunc A", w.A, "B", w.B, "C", w.C, "F nil", w.F == nil)

	// And the other way round.
	w2 := withFunc{A: 1, B: 2, C: "three"}
	v2 := reflect.ValueOf(&w2).Elem()
	println("read back A", v2.Field(0).Int(), "B", v2.Field(2).Int(), "C", v2.Field(3).String())
	println("F still nil", w2.F == nil, "reflect says nil", v2.Field(1).IsNil())

	// A func field written through reflection is callable afterwards, and the
	// fields either side of it are untouched.
	v2.Field(1).Set(reflect.ValueOf(func() int { return 7 }))
	println("called", w2.F(), "A", w2.A, "B", w2.B, "C", w2.C)

	// Two func fields drift twice as far.
	tt := reflect.TypeOf(twoFuncs{})
	println("twoFuncs sane", sane(tt, unsafe.Sizeof(twoFuncs{})))
	var two twoFuncs
	reflect.ValueOf(&two).Elem().Field(2).SetInt(33)
	println("twoFuncs N", two.N, "F nil", two.F == nil, "G nil", two.G == nil)

	// A func field inside a nested struct moves the outer struct's tail too.
	tn := reflect.TypeOf(nested{})
	println("nested sane", sane(tn, unsafe.Sizeof(nested{})))
	var n nested
	nv := reflect.ValueOf(&n).Elem()
	nv.Field(1).SetInt(44)
	nv.Field(0).Field(2).SetInt(55)
	nv.Field(0).Field(3).SetString("inner")
	println("nested Tail", n.Tail, "Inner.B", n.Inner.B, "Inner.C", n.Inner.C)

	// An array of func values is three of them, not three words.
	ta := reflect.TypeOf(funcArray{})
	println("funcArray sane", sane(ta, unsafe.Sizeof(funcArray{})))
	var fa funcArray
	av := reflect.ValueOf(&fa).Elem()
	av.Field(0).SetInt(66)
	av.Field(2).SetInt(77)
	av.Field(1).Index(1).Set(reflect.ValueOf(func() {}))
	println("funcArray Head", fa.Head, "Tail", fa.Tail,
		"0 nil", fa.Fs[0] == nil, "1 nil", fa.Fs[1] == nil, "2 nil", fa.Fs[2] == nil)

	// A fresh value reads zero in every field, which is what
	// crypto/tls's TestCloneNonFuncFields asks of a fresh Config.
	for _, t := range []reflect.Type{tw, tt, tn, ta} {
		z := reflect.New(t).Elem()
		allZero := true
		for i := 0; i < t.NumField(); i++ {
			if !z.Field(i).IsZero() {
				allZero = false
				println("not zero:", t.String(), t.Field(i).Name)
			}
		}
		println("fresh", t.String(), "all zero", allZero)
	}

	// And a whole struct copied through reflection comes back with every field,
	// which is the shape of a Clone. A struct holding a func is not comparable,
	// so the fields are read one at a time, as `TestCloneNonFuncFields` does.
	src := withFunc{A: 8, B: 9, C: "nine"}
	dst := reflect.New(tw).Elem()
	dst.Set(reflect.ValueOf(src))
	got := dst.Interface().(withFunc)
	println("copied A", got.A, "B", got.B, "C", got.C, "F nil", got.F == nil)
}
