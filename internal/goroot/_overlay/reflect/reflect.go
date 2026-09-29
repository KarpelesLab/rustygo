// Copyright 2026 Karpelès Lab Inc. MIT license.

// Package reflect is rustygo's stand-in for gc's, which reads gc's type
// descriptors. This one reads rustygo's (DESIGN §6).
//
// Values are read straight out of memory: a place has gc's layout
// (DESIGN §7), so a field is an offset and a slice element is an index, just
// as in gc's reflect. Type structure — kinds, sizes, fields, element and key
// types — comes from the runtime's descriptors, and maps are reached through
// per-map accessors the emitter generates, because a rustygo map is a hash
// table rather than memory Go's layout rules describe.
//
// This is the read-only half of reflection, which is what `fmt` and
// `errors.As` need: inspecting values, walking structs, slices and maps, and
// handing a value back as an interface. Setting values, calling methods and
// constructing types are not here yet and say so when used.
package reflect

import (
	"math"
	"unsafe"
)

// Kind is a type's kind, numbered as gc numbers it.
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

// Ptr is the old name of Pointer.
const Ptr = Pointer

var kindNames = [...]string{
	"invalid", "bool", "int", "int8", "int16", "int32", "int64",
	"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
	"float32", "float64", "complex64", "complex128",
	"array", "chan", "func", "interface", "map", "ptr", "slice",
	"string", "struct", "unsafe.Pointer",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "kind" + itoa(int(k))
}

// Type is a Go type, as reflection describes it.
type Type interface {
	Align() int
	FieldAlign() int
	Name() string
	PkgPath() string
	Size() uintptr
	String() string
	Kind() Kind
	Comparable() bool
	Elem() Type
	Key() Type
	Len() int
	NumField() int
	Field(i int) StructField
	FieldByName(name string) (StructField, bool)
	Implements(u Type) bool
	AssignableTo(u Type) bool
	NumMethod() int
	Method(i int) Method
	MethodByName(name string) (Method, bool)
	Bits() int
	ConvertibleTo(u Type) bool
	NumIn() int
	In(i int) Type
	NumOut() int
	Out(i int) Type
	IsVariadic() bool
	OverflowInt(x int64) bool
	OverflowUint(x uint64) bool
	OverflowFloat(x float64) bool
}

// Method is one method of a type or value.
//
// For a Type, Func is the method expression `T.M`, whose first argument is the
// receiver. For a Value, Func is bound to that value and Name is all that is
// filled in.
type Method struct {
	Name    string
	PkgPath string
	Type    Type
	Func    Value
	Index   int
}

// IsExported reports whether the method is exported. Reflection only ever
// hands out exported ones.
func (m Method) IsExported() bool { return m.PkgPath == "" }

// StructField describes one field of a struct type.
type StructField struct {
	Name      string
	PkgPath   string
	Type      Type
	Tag       StructTag
	Offset    uintptr
	Index     []int
	Anonymous bool
}

// IsExported reports whether the field is exported.
func (f StructField) IsExported() bool { return f.PkgPath == "" }

// StructTag is the tag string of a struct field.
type StructTag string

// Get returns the value of the key in the tag, or "".
func (tag StructTag) Get(key string) string {
	v, _ := tag.Lookup(key)
	return v
}

// Lookup returns the value of the key in the tag, and whether it was found.
// The syntax is gc's: space-separated `key:"value"` pairs.
func (tag StructTag) Lookup(key string) (value string, ok bool) {
	s := string(tag)
	for s != "" {
		for len(s) > 0 && s[0] == ' ' {
			s = s[1:]
		}
		i := 0
		for i < len(s) && s[i] != ':' {
			i++
		}
		if i+1 >= len(s) || s[i] != ':' || s[i+1] != '"' {
			break
		}
		name := s[:i]
		s = s[i+2:]
		// Find the closing quote, honouring backslash escapes.
		j := 0
		for j < len(s) && s[j] != '"' {
			if s[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(s) {
			break
		}
		quoted := s[:j]
		s = s[j+1:]
		if name == key {
			return unquoteTag(quoted), true
		}
	}
	return "", false
}

func unquoteTag(s string) string {
	if !hasBackslash(s) {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		out = append(out, s[i])
	}
	return string(out)
}

func hasBackslash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			return true
		}
	}
	return false
}

// rtype is a type known by its runtime descriptor.
type rtype struct{ d unsafe.Pointer }

// TypeOf returns the dynamic type of i, or nil.
func TypeOf(i any) Type {
	d := ifaceType(i)
	if d == nil {
		return nil
	}
	return rtype{d}
}

func typeAt(d unsafe.Pointer) Type {
	if d == nil {
		return nil
	}
	return rtype{d}
}

func (t rtype) Align() int       { return int(descAlign(t.d)) }
func (t rtype) FieldAlign() int  { return int(descAlign(t.d)) }
func (t rtype) Name() string     { return descName(t.d) }
func (t rtype) PkgPath() string  { return descPkgPath(t.d) }
func (t rtype) Size() uintptr    { return uintptr(descSize(t.d)) }
func (t rtype) String() string   { return descString(t.d) }
func (t rtype) Kind() Kind       { return Kind(descKind(t.d)) }
func (t rtype) Comparable() bool { return descComparable(t.d) }
func (t rtype) Elem() Type       { return typeAt(descElem(t.d)) }
func (t rtype) Key() Type        { return typeAt(descKey(t.d)) }
func (t rtype) NumField() int    { return int(descNumField(t.d)) }
func (t rtype) NumMethod() int   { return int(descNumMethod(t.d)) }

// Method returns the i'th exported method, in Go's order, which is by name.
func (t rtype) Method(i int) Method {
	if i < 0 || i >= t.NumMethod() {
		panic("reflect: Method index out of range")
	}
	d := descMethodExprType(t.d, i)
	// An interface type's method has a signature and no func value, exactly as
	// gc's reflect reports it.
	var fn Value
	if p := descMethodExprFunc(t.d, i); p != nil {
		fn = Value{d, p, false}
	}
	return Method{
		Name:    descMethodName(t.d, i),
		PkgPath: descMethodPkgPath(t.d, i),
		Type:    typeAt(d),
		Func:    fn,
		Index:   i,
	}
}

// What a func type is made of.

func (t rtype) NumIn() int {
	t.mustBe(Func, "NumIn")
	return int(descNumIn(t.d))
}

func (t rtype) In(i int) Type {
	t.mustBe(Func, "In")
	return typeAt(descIn(t.d, i))
}

func (t rtype) NumOut() int {
	t.mustBe(Func, "NumOut")
	return int(descNumOut(t.d))
}

func (t rtype) Out(i int) Type {
	t.mustBe(Func, "Out")
	return typeAt(descOut(t.d, i))
}

func (t rtype) IsVariadic() bool {
	t.mustBe(Func, "IsVariadic")
	return descVariadic(t.d)
}

func (t rtype) mustBe(k Kind, method string) {
	if t.Kind() != k {
		panic("reflect: Type." + method + " of " + t.Kind().String() + " type")
	}
}

// Whether a value would be truncated by this type, which is its width alone.

func (t rtype) OverflowInt(x int64) bool {
	bits := uint(t.Bits())
	trunc := (x << (64 - bits)) >> (64 - bits)
	return x != trunc
}

func (t rtype) OverflowUint(x uint64) bool {
	bits := uint(t.Bits())
	trunc := (x << (64 - bits)) >> (64 - bits)
	return x != trunc
}

func (t rtype) OverflowFloat(x float64) bool {
	if t.Kind() == Float32 {
		return overflowFloat32(x)
	}
	if t.Kind() != Float64 {
		panic("reflect: OverflowFloat of " + t.Kind().String() + " type")
	}
	return false
}

func overflowFloat32(x float64) bool {
	if x < 0 {
		x = -x
	}
	return 3.4028234663852886e+38 < x
}

func (t rtype) MethodByName(name string) (Method, bool) {
	for i := 0; i < t.NumMethod(); i++ {
		if descMethodName(t.d, i) == name {
			return t.Method(i), true
		}
	}
	return Method{}, false
}

func (t rtype) Len() int {
	if t.Kind() != Array {
		panic("reflect: Len of non-array type " + t.String())
	}
	return int(descLen(t.d))
}

func (t rtype) Bits() int {
	switch k := t.Kind(); k {
	case Int, Int8, Int16, Int32, Int64, Uint, Uint8, Uint16, Uint32, Uint64,
		Uintptr, Float32, Float64, Complex64, Complex128:
		return int(descSize(t.d)) * 8
	default:
		panic("reflect: Bits of non-arithmetic type " + t.String())
	}
}

func (t rtype) Field(i int) StructField {
	if t.Kind() != Struct {
		panic("reflect: Field of non-struct type " + t.String())
	}
	return StructField{
		Name:      descFieldName(t.d, i),
		PkgPath:   descFieldPkgPath(t.d, i),
		Type:      typeAt(descFieldType(t.d, i)),
		Tag:       StructTag(descFieldTag(t.d, i)),
		Offset:    uintptr(descFieldOffset(t.d, i)),
		Index:     []int{i},
		Anonymous: descFieldEmbedded(t.d, i),
	}
}

func (t rtype) FieldByName(name string) (StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); f.Name == name {
			return f, true
		}
	}
	return StructField{}, false
}

// Implements and AssignableTo cover what errors.As asks: whether a type has
// an interface's methods, and whether a value of one type can be assigned to a
// variable of another.
func (t rtype) Implements(u Type) bool {
	if u == nil || u.Kind() != Interface {
		panic("reflect: non-interface type passed to Type.Implements")
	}
	ru, ok := u.(rtype)
	if !ok {
		return false
	}
	return descImplements(t.d, ru.d)
}

// AssignableTo is the same type, or a type assignable to an interface it
// implements. Go's rule is wider — a named type is assignable to its unnamed
// underlying type, and a bidirectional channel to a directed one — and the
// descriptors do not say enough to see those.
func (t rtype) AssignableTo(u Type) bool {
	o, ok := u.(rtype)
	if !ok {
		return false
	}
	if t.d == o.d {
		return true
	}
	return u.Kind() == Interface && descImplements(t.d, o.d)
}

// Value is a Go value, as reflection sees it: its type, the address of the
// value itself, and whether that address is the value's own storage rather
// than a copy (gc's flagAddr).
type Value struct {
	d    unsafe.Pointer
	p    unsafe.Pointer
	addr bool
}

// ValueOf returns a Value for the value in i.
func ValueOf(i any) Value {
	d := ifaceType(i)
	if d == nil {
		return Value{}
	}
	// The interface holds a copy, so it is not the variable's own storage.
	return Value{d, ifaceData(i), false}
}

func (v Value) IsValid() bool { return v.d != nil }

func (v Value) Kind() Kind {
	if v.d == nil {
		return Invalid
	}
	return Kind(descKind(v.d))
}

func (v Value) Type() Type {
	if v.d == nil {
		panic("reflect: Type of the zero Value")
	}
	return rtype{v.d}
}

// Interface returns the value as an interface, copying it out.
func (v Value) Interface() any {
	if v.d == nil {
		return nil
	}
	if v.Kind() == Interface {
		return *(*any)(v.p)
	}
	return makeIface(v.d, descBox(v.d, v.p))
}

// CanInterface is true for every Value this package produces: it never hands
// out one obtained through an unexported field.
func (v Value) CanInterface() bool { return v.d != nil }

func (v Value) IsNil() bool {
	switch v.Kind() {
	case Pointer, UnsafePointer, Func, Map:
		return *(*unsafe.Pointer)(v.p) == nil
	case Slice:
		return (*sliceHeader)(v.p).data == nil
	case Interface:
		return *(*any)(v.p) == nil
	case Chan:
		return *(*unsafe.Pointer)(v.p) == nil
	}
	panic("reflect: IsNil of " + v.Kind().String() + " value")
}

func (v Value) IsZero() bool {
	switch v.Kind() {
	case Bool:
		return !v.Bool()
	case Int, Int8, Int16, Int32, Int64:
		return v.Int() == 0
	case Uint, Uint8, Uint16, Uint32, Uint64, Uintptr:
		return v.Uint() == 0
	case Float32, Float64:
		return v.Float() == 0
	case String:
		return v.String() == ""
	case Pointer, UnsafePointer, Func, Map, Slice, Interface, Chan:
		return v.IsNil()
	}
	return false
}

// The numeric and string readers: a place holds exactly the bytes gc would
// give it, so each reads its own width.

func (v Value) Bool() bool {
	v.mustBe(Bool, "Bool")
	return *(*bool)(v.p)
}

func (v Value) Int() int64 {
	switch v.Kind() {
	case Int:
		return int64(*(*int)(v.p))
	case Int8:
		return int64(*(*int8)(v.p))
	case Int16:
		return int64(*(*int16)(v.p))
	case Int32:
		return int64(*(*int32)(v.p))
	case Int64:
		return *(*int64)(v.p)
	}
	panic("reflect: Int of " + v.Kind().String() + " value")
}

func (v Value) Uint() uint64 {
	switch v.Kind() {
	case Uint:
		return uint64(*(*uint)(v.p))
	case Uint8:
		return uint64(*(*uint8)(v.p))
	case Uint16:
		return uint64(*(*uint16)(v.p))
	case Uint32:
		return uint64(*(*uint32)(v.p))
	case Uint64:
		return *(*uint64)(v.p)
	case Uintptr:
		return uint64(*(*uintptr)(v.p))
	}
	panic("reflect: Uint of " + v.Kind().String() + " value")
}

func (v Value) Float() float64 {
	switch v.Kind() {
	case Float32:
		return float64(*(*float32)(v.p))
	case Float64:
		return *(*float64)(v.p)
	}
	panic("reflect: Float of " + v.Kind().String() + " value")
}

func (v Value) Complex() complex128 {
	switch v.Kind() {
	case Complex64:
		return complex128(*(*complex64)(v.p))
	case Complex128:
		return *(*complex128)(v.p)
	}
	panic("reflect: Complex of " + v.Kind().String() + " value")
}

// String never panics, as gc's does not: a non-string value prints as
// "<T Value>".
func (v Value) String() string {
	switch v.Kind() {
	case String:
		return *(*string)(v.p)
	case Invalid:
		return "<invalid Value>"
	}
	return "<" + v.Type().String() + " Value>"
}

func (v Value) Bytes() []byte {
	if v.Kind() == Slice && v.Type().Elem().Kind() == Uint8 {
		return *(*[]byte)(v.p)
	}
	panic("reflect: Bytes of " + v.Kind().String() + " value")
}

func (v Value) Pointer() uintptr {
	switch v.Kind() {
	case Pointer, UnsafePointer, Func, Map, Chan:
		return uintptr(*(*unsafe.Pointer)(v.p))
	case Slice:
		return uintptr((*sliceHeader)(v.p).data)
	}
	panic("reflect: Pointer of " + v.Kind().String() + " value")
}

func (v Value) UnsafePointer() unsafe.Pointer { return unsafe.Pointer(v.Pointer()) }

func (v Value) Len() int {
	switch v.Kind() {
	case Array:
		return int(descLen(v.d))
	case Slice:
		return (*sliceHeader)(v.p).len
	case String:
		return len(*(*string)(v.p))
	case Map:
		return int(mapLen(v.d, v.p))
	}
	panic("reflect: Len of " + v.Kind().String() + " value")
}

func (v Value) Cap() int {
	switch v.Kind() {
	case Array:
		return int(descLen(v.d))
	case Slice:
		return (*sliceHeader)(v.p).cap
	}
	panic("reflect: Cap of " + v.Kind().String() + " value")
}

// Index returns the i'th element of an array, slice or string.
func (v Value) Index(i int) Value {
	switch v.Kind() {
	case Array:
		elem := descElem(v.d)
		n := int(descLen(v.d))
		if i < 0 || i >= n {
			panic("reflect: array index out of range")
		}
		return Value{elem, unsafe.Add(v.p, uintptr(i)*uintptr(descSize(elem))), v.addr}
	case Slice:
		h := (*sliceHeader)(v.p)
		if i < 0 || i >= h.len {
			panic("reflect: slice index out of range")
		}
		elem := descElem(v.d)
		// A slice's elements are always its own storage.
		return Value{elem, unsafe.Add(h.data, uintptr(i)*uintptr(descSize(elem))), true}
	case String:
		s := *(*string)(v.p)
		if i < 0 || i >= len(s) {
			panic("reflect: string index out of range")
		}
		b := s[i]
		return ValueOf(b)
	}
	panic("reflect: Index of " + v.Kind().String() + " value")
}

// Field returns the i'th field of a struct.
func (v Value) Field(i int) Value {
	if v.Kind() != Struct {
		panic("reflect: Field of " + v.Kind().String() + " value")
	}
	if i < 0 || i >= int(descNumField(v.d)) {
		panic("reflect: struct field index out of range")
	}
	return Value{descFieldType(v.d, i), unsafe.Add(v.p, uintptr(descFieldOffset(v.d, i))), v.addr}
}

// NumMethod returns the number of exported methods of v's type.
func (v Value) NumMethod() int {
	if v.d == nil {
		panic("reflect: NumMethod of the zero Value")
	}
	return int(descNumMethod(v.d))
}

// Method returns the i'th exported method bound to v: a func value with no
// receiver, which carries v with it.
func (v Value) Method(i int) Value {
	if v.d == nil {
		panic("reflect: Method of the zero Value")
	}
	if i < 0 || i >= v.NumMethod() {
		panic("reflect: Method index out of range")
	}
	p := descMethodValueFunc(v.d, i, v.p)
	if p == nil {
		panic("reflect: Method on an interface type's value")
	}
	return Value{descMethodValueType(v.d, i), p, false}
}

// MethodByName returns the named method bound to v, or the zero Value.
func (v Value) MethodByName(name string) Value {
	if v.d == nil {
		panic("reflect: MethodByName of the zero Value")
	}
	for i := 0; i < v.NumMethod(); i++ {
		if descMethodName(v.d, i) == name {
			return v.Method(i)
		}
	}
	return Value{}
}

func (v Value) NumField() int {
	if v.Kind() != Struct {
		panic("reflect: NumField of " + v.Kind().String() + " value")
	}
	return int(descNumField(v.d))
}

// Elem returns the value a pointer points at, or the value in an interface.
func (v Value) Elem() Value {
	switch v.Kind() {
	case Pointer:
		p := *(*unsafe.Pointer)(v.p)
		if p == nil {
			return Value{}
		}
		// What a pointer points at is addressable.
		return Value{descElem(v.d), p, true}
	case Interface:
		i := *(*any)(v.p)
		return ValueOf(i)
	}
	panic("reflect: Elem of " + v.Kind().String() + " value")
}

// MapKeys returns a map's keys, in the map's own order.
func (v Value) MapKeys() []Value {
	if v.Kind() != Map {
		panic("reflect: MapKeys of " + v.Kind().String() + " value")
	}
	keyType := descKey(v.d)
	var keys []Value
	it := mapIter(v.d, v.p)
	for {
		ok, k, _ := mapNext(v.d, it)
		if !ok {
			return keys
		}
		keys = append(keys, Value{keyType, k, false})
	}
}

// MapIndex returns m[key], or the zero Value if the key is absent.
func (v Value) MapIndex(key Value) Value {
	if v.Kind() != Map {
		panic("reflect: MapIndex of " + v.Kind().String() + " value")
	}
	val, ok := mapIndex(v.d, v.p, key.p)
	if !ok {
		return Value{}
	}
	return Value{descElem(v.d), val, false}
}

// MapRange returns an iterator over a map.
func (v Value) MapRange() *MapIter {
	if v.Kind() != Map {
		panic("reflect: MapRange of " + v.Kind().String() + " value")
	}
	return &MapIter{d: v.d, it: mapIter(v.d, v.p)}
}

// MapIter walks a map, one entry at a time.
type MapIter struct {
	d  unsafe.Pointer
	it unsafe.Pointer
	k  unsafe.Pointer
	v  unsafe.Pointer
	ok bool
}

func (it *MapIter) Next() bool {
	it.ok, it.k, it.v = mapNext(it.d, it.it)
	return it.ok
}

func (it *MapIter) Key() Value {
	if !it.ok {
		panic("reflect: MapIter.Key called before Next")
	}
	return Value{descKey(it.d), it.k, false}
}

func (it *MapIter) Value() Value {
	if !it.ok {
		panic("reflect: MapIter.Value called before Next")
	}
	return Value{descElem(it.d), it.v, false}
}

// The setting half. A value can be written when the Value refers to the
// variable's own storage: what a pointer points at, a slice's elements, and
// fields and elements reached from those. The collector does not move
// objects, so a write is a write.

func (v Value) CanAddr() bool { return v.addr }

func (v Value) CanSet() bool { return v.addr }

func (v Value) Addr() Value {
	if !v.addr {
		panic("reflect.Value.Addr of unaddressable value")
	}
	panic(unsupported("Value.Addr"))
}

// Set assigns x to v, which must have the same type, or be assignable to an
// interface variable.
func (v Value) Set(x Value) {
	v.mustBeAssignable("Set")
	if !x.IsValid() {
		panic("reflect: Set with the zero Value")
	}
	if v.d != x.d {
		if v.Kind() == Interface {
			*(*any)(v.p) = x.Interface()
			return
		}
		panic("reflect.Set: value of type " + x.Type().String() +
			" is not assignable to type " + v.Type().String())
	}
	copyBytes(v.p, x.p, int(descSize(v.d)))
}

func (v Value) SetBool(x bool) {
	v.mustBeAssignable("SetBool")
	v.mustBe(Bool, "SetBool")
	*(*bool)(v.p) = x
}

func (v Value) SetInt(x int64) {
	v.mustBeAssignable("SetInt")
	switch v.Kind() {
	case Int:
		*(*int)(v.p) = int(x)
	case Int8:
		*(*int8)(v.p) = int8(x)
	case Int16:
		*(*int16)(v.p) = int16(x)
	case Int32:
		*(*int32)(v.p) = int32(x)
	case Int64:
		*(*int64)(v.p) = x
	default:
		panic("reflect: SetInt of " + v.Kind().String() + " value")
	}
}

func (v Value) SetUint(x uint64) {
	v.mustBeAssignable("SetUint")
	switch v.Kind() {
	case Uint:
		*(*uint)(v.p) = uint(x)
	case Uint8:
		*(*uint8)(v.p) = uint8(x)
	case Uint16:
		*(*uint16)(v.p) = uint16(x)
	case Uint32:
		*(*uint32)(v.p) = uint32(x)
	case Uint64:
		*(*uint64)(v.p) = x
	case Uintptr:
		*(*uintptr)(v.p) = uintptr(x)
	default:
		panic("reflect: SetUint of " + v.Kind().String() + " value")
	}
}

func (v Value) SetFloat(x float64) {
	v.mustBeAssignable("SetFloat")
	switch v.Kind() {
	case Float32:
		*(*float32)(v.p) = float32(x)
	case Float64:
		*(*float64)(v.p) = x
	default:
		panic("reflect: SetFloat of " + v.Kind().String() + " value")
	}
}

func (v Value) SetComplex(x complex128) {
	v.mustBeAssignable("SetComplex")
	switch v.Kind() {
	case Complex64:
		*(*complex64)(v.p) = complex64(x)
	case Complex128:
		*(*complex128)(v.p) = x
	default:
		panic("reflect: SetComplex of " + v.Kind().String() + " value")
	}
}

func (v Value) SetString(x string) {
	v.mustBeAssignable("SetString")
	v.mustBe(String, "SetString")
	*(*string)(v.p) = x
}

// Equal reports whether v and u are equal, which for two values of one
// comparable type is what `==` would say.
func (v Value) Equal(u Value) bool {
	if !v.IsValid() || !u.IsValid() {
		return v.IsValid() == u.IsValid()
	}
	if v.Kind() == Interface {
		return *(*any)(v.p) == *(*any)(u.p)
	}
	if v.d != u.d {
		return false
	}
	if !v.Comparable() {
		panic("reflect.Value.Equal: values of type " + v.Type().String() + " are not comparable")
	}
	return descEqual(v.d, v.p, u.p)
}

// Comparable reports whether Equal may be called on v.
func (v Value) Comparable() bool {
	if !v.IsValid() {
		return true
	}
	if v.Kind() == Interface {
		return true
	}
	return v.Type().Comparable()
}

// SetZero sets v to the zero value of its type, which for every Go type is
// all-zero bytes.
func (v Value) SetZero() {
	v.mustBeAssignable("SetZero")
	n := int(descSize(v.d))
	b := unsafe.Slice((*byte)(v.p), n)
	for i := range b {
		b[i] = 0
	}
}

// SetLen sets a slice's length, which must not exceed its capacity.
func (v Value) SetLen(n int) {
	v.mustBeAssignable("SetLen")
	v.mustBe(Slice, "SetLen")
	h := (*sliceHeader)(v.p)
	if n < 0 || n > h.cap {
		panic("reflect: SetLen out of range")
	}
	h.len = n
}

// SetCap sets a slice's capacity, which must be between its length and its
// current capacity.
func (v Value) SetCap(n int) {
	v.mustBeAssignable("SetCap")
	v.mustBe(Slice, "SetCap")
	h := (*sliceHeader)(v.p)
	if n < h.len || n > h.cap {
		panic("reflect: SetCap out of range")
	}
	h.cap = n
}

// SetBytes sets v, which must be a []byte, to x.
func (v Value) SetBytes(x []byte) {
	v.mustBeAssignable("SetBytes")
	v.mustBe(Slice, "SetBytes")
	if descKind(descElem(v.d)) != uint8(Uint8) {
		panic("reflect.Value.SetBytes of " + v.Type().String())
	}
	*(*[]byte)(v.p) = x
}

// SetMapIndex sets m[key] = elem, or deletes m[key] if elem is the zero Value.
func (v Value) SetMapIndex(key, elem Value) {
	v.mustBe(Map, "SetMapIndex")
	if v.IsNil() {
		panic("reflect: SetMapIndex on a nil map")
	}
	if key.d != descKey(v.d) {
		panic("reflect: SetMapIndex with a key of type " + key.Type().String())
	}
	if !elem.IsValid() {
		mapDelete(v.d, v.p, key.p)
		return
	}
	if elem.d != descElem(v.d) {
		panic("reflect: SetMapIndex with a value of type " + elem.Type().String())
	}
	mapSet(v.d, v.p, key.p, elem.p)
}

func (v Value) mustBeAssignable(method string) {
	if !v.IsValid() {
		panic("reflect: " + method + " on the zero Value")
	}
	if !v.addr {
		panic("reflect: reflect.Value." + method + " using unaddressable value")
	}
}

// copyBytes moves a value's bytes; both addresses hold a value of the same
// type, so the ranges are the same length and do not overlap.
func copyBytes(dst, src unsafe.Pointer, n int) {
	copy(unsafe.Slice((*byte)(dst), n), unsafe.Slice((*byte)(src), n))
}

// Swapper returns a function that swaps two elements of a slice, which is
// what sort.Slice uses.
func Swapper(slice any) func(i, j int) {
	v := ValueOf(slice)
	if v.Kind() != Slice {
		panic("reflect: Swapper of non-slice " + v.Type().String())
	}
	h := (*sliceHeader)(v.p)
	size := int(descSize(descElem(v.d)))
	n, data := h.len, h.data
	if n < 2 {
		return func(i, j int) {
			if i != 0 || j != 0 || n == 0 {
				panic("reflect: slice index out of range")
			}
		}
	}
	tmp := make([]byte, size)
	return func(i, j int) {
		if uint(i) >= uint(n) || uint(j) >= uint(n) {
			panic("reflect: slice index out of range")
		}
		pi := unsafe.Add(data, uintptr(i)*uintptr(size))
		pj := unsafe.Add(data, uintptr(j)*uintptr(size))
		copy(tmp, unsafe.Slice((*byte)(pi), size))
		copyBytes(pi, pj, size)
		copy(unsafe.Slice((*byte)(pj), size), tmp)
	}
}

// Constructing types and values, and calling a method through reflection
// rather than through a func value, are not here yet.

// Call calls the function v with the arguments in, and returns its results.
//
// The emitter generates the call itself, one closure per func type, because
// only it knows the signature (DESIGN §6). Arguments must have exactly the
// parameters' types: gc's reflect also accepts an assignable one and boxes it,
// which would mean building an interface value from here.
func (v Value) Call(in []Value) []Value {
	if v.Kind() != Func {
		panic("reflect: Call of " + v.Kind().String() + " Value")
	}
	if v.IsNil() {
		panic("reflect: Call of nil func")
	}
	t := v.Type()
	if t.IsVariadic() {
		panic(unsupported("Value.Call on a variadic function"))
	}
	if len(in) != t.NumIn() {
		panic("reflect: Call with the wrong number of input parameters")
	}
	args := make([]unsafe.Pointer, len(in))
	for i, a := range in {
		if !a.IsValid() {
			panic("reflect: Call using the zero Value as an argument")
		}
		if a.Type() != t.In(i) {
			panic("reflect: Call using " + a.Type().String() + " as type " + t.In(i).String())
		}
		args[i] = a.p
	}
	results := descCall(v.d, v.p, args)
	out := make([]Value, len(results))
	for i := range results {
		out[i] = Value{descOut(v.d, i), results[i], false}
	}
	return out
}
func (v Value) Grow(n int)                   { panic(unsupported("Value.Grow")) }
func MakeSlice(typ Type, len, cap int) Value { panic(unsupported("MakeSlice")) }

// PointerTo would need a type descriptor rustygo's emitter did not write, the
// program never having mentioned the type (DESIGN §6).
func PointerTo(t Type) Type { panic(unsupported("PointerTo")) }

// MakeMap makes an empty map of a type the program does mention, so the map's
// own accessors are there to make it with.
func MakeMap(t Type) Value {
	rt, ok := t.(rtype)
	if !ok || t.Kind() != Map {
		panic("reflect.MakeMap of non-map type")
	}
	m := mapMake(rt.d)
	// The Value refers to a box holding the map, which is the map's own
	// storage as far as anything here is concerned.
	return Value{rt.d, m, true}
}

// New would hand back a `*T`, and the descriptor for that pointer type is one
// the program never mentioned, so the emitter never wrote it (DESIGN §6).
func New(typ Type) Value { panic(unsupported("New")) }

// Zero returns the zero value of a type: all-zero bytes, in an object of the
// right size that knows how to trace itself. The result is not addressable,
// as gc's is not.
func Zero(typ Type) Value {
	rt, ok := typ.(rtype)
	if !ok {
		panic("reflect.Zero of a nil Type")
	}
	return Value{rt.d, descZero(rt.d), false}
}

// DeepEqual compares two values the way gc's does, for the kinds this
// package can read.
func DeepEqual(x, y any) bool {
	if x == nil || y == nil {
		return x == y
	}
	vx, vy := ValueOf(x), ValueOf(y)
	if vx.Type() != vy.Type() {
		return false
	}
	return deepValueEqual(vx, vy)
}

func deepValueEqual(x, y Value) bool {
	if !x.IsValid() || !y.IsValid() {
		return x.IsValid() == y.IsValid()
	}
	switch x.Kind() {
	case Array:
		for i := 0; i < x.Len(); i++ {
			if !deepValueEqual(x.Index(i), y.Index(i)) {
				return false
			}
		}
		return true
	case Slice:
		if x.IsNil() != y.IsNil() || x.Len() != y.Len() {
			return false
		}
		for i := 0; i < x.Len(); i++ {
			if !deepValueEqual(x.Index(i), y.Index(i)) {
				return false
			}
		}
		return true
	case Interface:
		if x.IsNil() || y.IsNil() {
			return x.IsNil() == y.IsNil()
		}
		return deepValueEqual(x.Elem(), y.Elem())
	case Pointer:
		if x.Pointer() == y.Pointer() {
			return true
		}
		return deepValueEqual(x.Elem(), y.Elem())
	case Struct:
		for i := 0; i < x.NumField(); i++ {
			if !deepValueEqual(x.Field(i), y.Field(i)) {
				return false
			}
		}
		return true
	case Map:
		if x.IsNil() != y.IsNil() || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.MapKeys() {
			vx, vy := x.MapIndex(k), y.MapIndex(k)
			if !vx.IsValid() || !vy.IsValid() || !deepValueEqual(vx, vy) {
				return false
			}
		}
		return true
	default:
		if !x.Type().Comparable() {
			return false
		}
		return x.Interface() == y.Interface()
	}
}

func (v Value) mustBe(k Kind, method string) {
	if v.Kind() != k {
		panic("reflect: " + method + " of " + v.Kind().String() + " value")
	}
}

// sliceHeader is a slice's three words, which rustygo lays out as gc does.
type sliceHeader struct {
	data unsafe.Pointer
	len  int
	cap  int
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func unsupported(what string) string {
	return "reflect." + what + ": not supported by rustygo yet (DESIGN §6)"
}

// Answered by the runtime: descriptors are Rust statics, and a map is a hash
// table rather than memory Go's layout rules describe (src/reflect.rs).

func ifaceType(i any) unsafe.Pointer
func ifaceData(i any) unsafe.Pointer
func makeIface(d, data unsafe.Pointer) any
func descKind(d unsafe.Pointer) uint8
func descSize(d unsafe.Pointer) uint64
func descAlign(d unsafe.Pointer) uint64
func descName(d unsafe.Pointer) string
func descString(d unsafe.Pointer) string
func descPkgPath(d unsafe.Pointer) string
func descElem(d unsafe.Pointer) unsafe.Pointer
func descKey(d unsafe.Pointer) unsafe.Pointer
func descLen(d unsafe.Pointer) int64
func descComparable(d unsafe.Pointer) bool
func descNumField(d unsafe.Pointer) int64
func descFieldName(d unsafe.Pointer, i int) string
func descFieldPkgPath(d unsafe.Pointer, i int) string
func descFieldType(d unsafe.Pointer, i int) unsafe.Pointer
func descFieldOffset(d unsafe.Pointer, i int) uint64
func descFieldTag(d unsafe.Pointer, i int) string
func descFieldEmbedded(d unsafe.Pointer, i int) bool
func descBox(d, addr unsafe.Pointer) unsafe.Pointer
func descCall(d, f unsafe.Pointer, args []unsafe.Pointer) []unsafe.Pointer
func descImplements(d, iface unsafe.Pointer) bool
func descZero(d unsafe.Pointer) unsafe.Pointer
func descNumIn(d unsafe.Pointer) int64
func descIn(d unsafe.Pointer, i int) unsafe.Pointer
func descNumOut(d unsafe.Pointer) int64
func descOut(d unsafe.Pointer, i int) unsafe.Pointer
func descVariadic(d unsafe.Pointer) bool
func descEqual(d, a, b unsafe.Pointer) bool
func mapMake(d unsafe.Pointer) unsafe.Pointer
func mapSet(d, m, k, v unsafe.Pointer)
func mapDelete(d, m, k unsafe.Pointer)
func descNumMethod(d unsafe.Pointer) int64
func descMethodName(d unsafe.Pointer, i int) string
func descMethodPkgPath(d unsafe.Pointer, i int) string
func descMethodExprType(d unsafe.Pointer, i int) unsafe.Pointer
func descMethodExprFunc(d unsafe.Pointer, i int) unsafe.Pointer
func descMethodValueType(d unsafe.Pointer, i int) unsafe.Pointer
func descMethodValueFunc(d unsafe.Pointer, i int, recv unsafe.Pointer) unsafe.Pointer
func mapLen(d, m unsafe.Pointer) int64
func mapIter(d, m unsafe.Pointer) unsafe.Pointer
func mapNext(d, it unsafe.Pointer) (bool, unsafe.Pointer, unsafe.Pointer)
func mapIndex(d, m, k unsafe.Pointer) (unsafe.Pointer, bool)

// TypeFor returns the Type that represents the type argument T.
//
// A concrete T has a descriptor of its own, which boxing a zero value of it
// produces. An interface T does not box that way — the zero value of an
// interface is nil and carries no type — so it is reached the way gc's reflect
// reaches it, through the pointer type `*T`, whose descriptor names T as the
// type it points at.
func TypeFor[T any]() Type {
	var zero T
	if t := TypeOf(any(zero)); t != nil {
		return t
	}
	return TypeOf((*T)(nil)).Elem()
}

// TypeAssert is x.(T), for a Value.
func TypeAssert[T any](v Value) (T, bool) {
	x, ok := v.Interface().(T)
	return x, ok
}

// Indirect returns the value v points to. If v is a nil pointer, it returns
// the zero Value. If v is not a pointer, it returns v.
func Indirect(v Value) Value {
	if v.Kind() != Pointer {
		return v
	}
	return v.Elem()
}

// Copy copies the contents of src into dst until either dst has been filled
// or src has been exhausted, and returns the number of elements copied.
func Copy(dst, src Value) int {
	dk, sk := dst.Kind(), src.Kind()
	if dk != Slice && dk != Array {
		panic("reflect.Copy: destination is " + dk.String())
	}
	if sk != Slice && sk != Array && !(sk == String && dst.Type().Elem().Kind() == Uint8) {
		panic("reflect.Copy: source is " + sk.String())
	}
	if dk == Array && !dst.addr {
		panic("reflect: reflect.Value.Copy using unaddressable value")
	}
	if sk == String {
		b := []byte(src.String())
		n := min(dst.Len(), len(b))
		for i := range n {
			dst.Index(i).SetUint(uint64(b[i]))
		}
		return n
	}
	if dst.Type().Elem() != src.Type().Elem() {
		panic("reflect.Copy: element types differ")
	}
	n := min(dst.Len(), src.Len())
	for i := range n {
		dst.Index(i).Set(src.Index(i))
	}
	return n
}

// OverflowInt reports whether x cannot be represented by v's type.
func (v Value) OverflowInt(x int64) bool {
	switch v.Kind() {
	case Int, Int8, Int16, Int32, Int64:
		bits := descSize(v.d) * 8
		trunc := (x << (64 - bits)) >> (64 - bits)
		return x != trunc
	}
	panic("reflect: OverflowInt of " + v.Kind().String() + " value")
}

// OverflowUint reports whether x cannot be represented by v's type.
func (v Value) OverflowUint(x uint64) bool {
	switch v.Kind() {
	case Uint, Uintptr, Uint8, Uint16, Uint32, Uint64:
		bits := descSize(v.d) * 8
		trunc := (x << (64 - bits)) >> (64 - bits)
		return x != trunc
	}
	panic("reflect: OverflowUint of " + v.Kind().String() + " value")
}

// OverflowFloat reports whether x cannot be represented by v's type.
func (v Value) OverflowFloat(x float64) bool {
	switch v.Kind() {
	case Float32:
		if x < 0 {
			x = -x
		}
		return math.MaxFloat32 < x && x <= math.MaxFloat64
	case Float64:
		return false
	}
	panic("reflect: OverflowFloat of " + v.Kind().String() + " value")
}

// The rest of reflect needs type information rustygo does not have yet, or a
// call shim generated per signature, and says so rather than guessing:
//
//   - Append and MakeSlice must allocate a slice of a type chosen at run
//     time, which needs the descriptor to carry how to make one;
//   - Convert and ConvertibleTo need every conversion Go's rules allow,
//     between types the program may never have converted itself;
//   - MakeFunc needs a trampoline for a signature the program may not
//     contain (DESIGN §6).

// Append appends the values x to a slice s and returns the resulting slice.
func Append(s Value, x ...Value) Value {
	panic(unsupported("Append"))
}

// AppendSlice appends a slice t to a slice s and returns the resulting slice.
func AppendSlice(s, t Value) Value {
	panic(unsupported("AppendSlice"))
}

// Convert returns the value v converted to type t.
func (v Value) Convert(t Type) Value {
	panic(unsupported("Value.Convert"))
}

// CanConvert reports whether the value v can be converted to type t.
func (v Value) CanConvert(t Type) bool {
	return false
}

// ConvertibleTo reports whether a value of the type is convertible to type u.
func (t rtype) ConvertibleTo(u Type) bool {
	panic(unsupported("Type.ConvertibleTo"))
}

// MakeFunc returns a new function of the given Type that wraps the function
// fn.
func MakeFunc(typ Type, fn func(args []Value) (results []Value)) Value {
	panic(unsupported("MakeFunc"))
}

// StringHeader is the runtime representation of a string.
//
// Deprecated: Use unsafe.String or unsafe.StringData instead.
//
// rustygo's strings have gc's layout (DESIGN §7), so reading one through this
// sees what gc would: the bytes' address, then the length.
type StringHeader struct {
	Data uintptr
	Len  int
}

// SliceHeader is the runtime representation of a slice.
//
// Deprecated: Use unsafe.Slice or unsafe.SliceData instead.
type SliceHeader struct {
	Data uintptr
	Len  int
	Cap  int
}
