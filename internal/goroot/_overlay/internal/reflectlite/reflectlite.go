// Copyright 2026 Karpelès Lab Inc. MIT license.

// Package reflectlite is rustygo's stand-in for gc's, which reads gc's type
// descriptors. This one answers from rustygo's own (the runtime's TypeDesc,
// DESIGN §6).
//
// M1 covers what errors.Is needs: a type's name and whether it is
// comparable. The rest of the API is present so its importers compile, and
// panics with a clear message when used, until reflection lands (the
// descriptors do not carry kinds, element types or sizes yet).
package reflectlite

import "unsafe"

// Kind is a type's kind, numbered as in package reflect.
type Kind uint

const (
	Invalid Kind = iota
	Bool
	Int
	Int8
	Int16
	Int32
	Int64
	Uint
	Uint8
	Uint16
	Uint32
	Uint64
	Uintptr
	Float32
	Float64
	Complex64
	Complex128
	Array
	Chan
	Func
	Interface
	Map
	Pointer
	Slice
	String
	Struct
	UnsafePointer
)

const Ptr = Pointer

// Type is the subset of reflect.Type the standard library's low levels use.
type Type interface {
	Name() string
	PkgPath() string
	Size() uintptr
	Kind() Kind
	Implements(u Type) bool
	AssignableTo(u Type) bool
	Comparable() bool
	String() string
	Elem() Type
}

// rtype is a type known by its runtime descriptor; a nil descriptor is a
// type the M1 descriptors cannot describe yet (an element type, say).
type rtype struct{ desc unsafe.Pointer }

// TypeOf returns the dynamic type of i, or nil for a nil interface.
func TypeOf(i any) Type {
	d := typeOf(i)
	if d == nil {
		return nil
	}
	return rtype{d}
}

func (t rtype) Comparable() bool {
	t.need("Comparable")
	return descComparable(t.desc)
}

func (t rtype) String() string {
	if t.desc == nil {
		return "<unknown type>"
	}
	return descName(t.desc)
}

// Elem cannot be answered yet, but errors computes one at init, so it returns
// an unknown type rather than panicking.
func (t rtype) Elem() Type { return rtype{} }

func (t rtype) Name() string             { panic(unsupported("Type.Name")) }
func (t rtype) PkgPath() string          { panic(unsupported("Type.PkgPath")) }
func (t rtype) Size() uintptr            { panic(unsupported("Type.Size")) }
func (t rtype) Kind() Kind               { panic(unsupported("Type.Kind")) }
func (t rtype) Implements(u Type) bool   { panic(unsupported("Type.Implements")) }
func (t rtype) AssignableTo(u Type) bool { panic(unsupported("Type.AssignableTo")) }

func (t rtype) need(method string) {
	if t.desc == nil {
		panic(unsupported(method + " of an element type"))
	}
}

// Value is present for its importers; M1 cannot build one.
type Value struct{ _ unsafe.Pointer }

func ValueOf(i any) Value              { panic(unsupported("ValueOf")) }
func (v Value) Type() Type             { panic(unsupported("Value.Type")) }
func (v Value) Kind() Kind             { panic(unsupported("Value.Kind")) }
func (v Value) IsNil() bool            { panic(unsupported("Value.IsNil")) }
func (v Value) Elem() Value            { panic(unsupported("Value.Elem")) }
func (v Value) Set(x Value)            { panic(unsupported("Value.Set")) }
func (v Value) Len() int               { panic(unsupported("Value.Len")) }
func Swapper(slice any) func(i, j int) { panic(unsupported("Swapper")) }

// A ValueError occurs when a Value method is invoked on a Value that does
// not support it.
type ValueError struct {
	Method string
	Kind   Kind
}

func (e *ValueError) Error() string { return "reflect: call of " + e.Method + " on invalid Value" }

func unsupported(what string) string {
	return "internal/reflectlite." + what + ": not supported by rustygo yet (DESIGN §6)"
}

// Answered by the runtime from its type descriptors.
func typeOf(i any) unsafe.Pointer
func descComparable(d unsafe.Pointer) bool
func descName(d unsafe.Pointer) string
