// Copyright 2026 Karpelès Lab Inc. MIT license.

package reflect

import "unsafe"

// Reaching a field by name, and re-slicing a slice: two ways of getting at
// part of a value that reflect.go's offsets and indices do not already cover.
// `encoding/gob` re-slices, and the `_testmain.go` that `go list -test` writes
// reads testing.M's unexported exitCode field by name.

// FieldByName returns the struct field with the given name, or the zero Value
// if there is none.
//
// Unlike gc's, this does not promote a field of an embedded struct: the
// descriptors say which fields are embedded but the search would have to be
// gc's breadth-first one, with its rule that two fields at the same depth
// cancel each other out, and nothing in the standard library has needed it yet.
func (v Value) FieldByName(name string) Value {
	v.mustBe(Struct, "FieldByName")
	if f, ok := v.Type().FieldByName(name); ok {
		return v.FieldByIndex(f.Index)
	}
	return Value{}
}

// Slice returns v[i:j].
//
// The result is a second slice over the same elements, so it needs three words
// of its own to live in: a zero value of v's type is an object of exactly that
// shape, and one that traces its data pointer, which a header written into
// memory reflect does not own would not.
func (v Value) Slice(i, j int) Value {
	switch v.Kind() {
	case Slice:
		h := (*sliceHeader)(v.p)
		if i < 0 || j < i || j > h.cap {
			panic("reflect.Value.Slice: slice index out of bounds")
		}
		p := descZero(v.d)
		out := (*sliceHeader)(p)
		out.data = unsafe.Add(h.data, uintptr(i)*uintptr(descSize(descElem(v.d))))
		out.len = j - i
		out.cap = h.cap - i
		return Value{v.d, p, false}
	case String:
		s := *(*string)(v.p)
		if i < 0 || j < i || j > len(s) {
			panic("reflect.Value.Slice: string slice index out of bounds")
		}
		p := descZero(v.d)
		*(*string)(p) = s[i:j]
		return Value{v.d, p, false}
	case Array:
		// The result would be a []T, and the descriptor for that slice type is
		// one the emitter only writes for types the program mentions
		// (DESIGN §6); nothing reaches a slice type from an array type.
		panic(unsupported("Value.Slice of an array"))
	}
	panic("reflect.Value.Slice of " + v.Kind().String() + " value")
}

// Slice3 is v[i:j:k], which needs the same three words Slice does and the same
// bounds, with the capacity named outright.
func (v Value) Slice3(i, j, k int) Value {
	v.mustBe(Slice, "Slice3")
	h := (*sliceHeader)(v.p)
	if i < 0 || j < i || k < j || k > h.cap {
		panic("reflect.Value.Slice3: slice index out of bounds")
	}
	p := descZero(v.d)
	out := (*sliceHeader)(p)
	out.data = unsafe.Add(h.data, uintptr(i)*uintptr(descSize(descElem(v.d))))
	out.len = j - i
	out.cap = k - i
	return Value{v.d, p, false}
}

// MakeMapWithSize makes an empty map of a type the program mentions. The size
// is a hint about how much to preallocate, and rustygo's maps do not take one.
func MakeMapWithSize(t Type, n int) Value { return MakeMap(t) }
