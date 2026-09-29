// Copyright 2026 Karpelès Lab Inc. MIT license.

// Package reflectlite is rustygo's stand-in for gc's.
//
// gc has a second, smaller reflect because `errors` and `sort` are below
// `reflect` in its import graph and it will not have a cycle. rustygo's
// `reflect` is its own package written over the runtime's type descriptors
// (DESIGN §6) and imports almost nothing, so there is no cycle to avoid: this
// is the same package under its other name, and the two cannot disagree.
package reflectlite

import (
	"reflect"
	"unsafe"
)

// Kind is a type's kind, numbered as in package reflect.
type Kind = reflect.Kind

const (
	Invalid       = reflect.Invalid
	Bool          = reflect.Bool
	Int           = reflect.Int
	Int8          = reflect.Int8
	Int16         = reflect.Int16
	Int32         = reflect.Int32
	Int64         = reflect.Int64
	Uint          = reflect.Uint
	Uint8         = reflect.Uint8
	Uint16        = reflect.Uint16
	Uint32        = reflect.Uint32
	Uint64        = reflect.Uint64
	Uintptr       = reflect.Uintptr
	Float32       = reflect.Float32
	Float64       = reflect.Float64
	Complex64     = reflect.Complex64
	Complex128    = reflect.Complex128
	Array         = reflect.Array
	Chan          = reflect.Chan
	Func          = reflect.Func
	Interface     = reflect.Interface
	Map           = reflect.Map
	Pointer       = reflect.Pointer
	Ptr           = reflect.Pointer
	Slice         = reflect.Slice
	String        = reflect.String
	Struct        = reflect.Struct
	UnsafePointer = reflect.UnsafePointer
)

// Type and Value are reflect's own. gc's reflectlite.Type is a subset of
// reflect.Type's method set, so code written against either compiles.
type Type = reflect.Type

type Value = reflect.Value

func TypeOf(i any) Type                      { return reflect.TypeOf(i) }
func ValueOf(i any) Value                    { return reflect.ValueOf(i) }
func Swapper(slice any) func(i, j int)       { return reflect.Swapper(slice) }
func TypeFor[T any]() Type                   { return reflect.TypeFor[T]() }
func DeepEqual(x, y any) bool                { return reflect.DeepEqual(x, y) }
func PtrTo(t Type) Type                      { return reflect.PointerTo(t) }
func Zero(t Type) Value                      { return reflect.Zero(t) }
func unused(p unsafe.Pointer) unsafe.Pointer { return p }

// A ValueError occurs when a Value method is invoked on a Value that does not
// support it.
type ValueError struct {
	Method string
	Kind   Kind
}

func (e *ValueError) Error() string { return "reflect: call of " + e.Method + " on invalid Value" }
