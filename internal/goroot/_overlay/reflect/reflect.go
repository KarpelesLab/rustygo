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
// Reading, writing, calling and making values are all here: inspecting a
// value, walking structs, slices and maps, handing one back as an interface,
// setting one, calling a method or a func value, and making a pointer, slice
// or map of a type chosen at run time. Building a *type* out of parts —
// `StructOf`, `SliceOf`, `MapOf` — is not, and says so when used: a pointer's
// descriptor can be derived from the one it points at, because every pointer
// has the same shape, and a struct's cannot.
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
	FieldByNameFunc(match func(string) bool) (StructField, bool)
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
	ChanDir() ChanDir
	CanSeq() bool
	CanSeq2() bool
	FieldByIndex(index []int) StructField
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
		fn = Value{d: d, p: p}
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

// FieldByNameFunc returns the first field whose name satisfies match.
//
// gc searches breadth-first through embedded fields and reports nothing when
// two fields at the same depth both match, because neither is the one the
// selector would mean. Neither this nor FieldByName promotes an embedded
// field at all, so the shallow search is all there is, and declaration order
// decides.
func (t rtype) FieldByNameFunc(match func(string) bool) (StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); match(f.Name) {
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
// value itself, whether that address is the value's own storage rather than a
// copy (gc's flagAddr), and whether it was reached through an unexported
// field.
//
// Go lets reflection read an unexported field and nothing more: such a value
// cannot be set and cannot be handed back as an interface. gc keeps two bits
// for that, and so does this. sticky says the value itself came through an
// unexported field. embed says it *is* an unexported embedded struct, whose
// own fields are ordinary values again — which is how `encoding/json` fills in
// the fields promoted from one while refusing to allocate the struct itself.
type Value struct {
	d      unsafe.Pointer
	p      unsafe.Pointer
	addr   bool
	sticky bool
	embed  bool
}

// ro reports whether the value came through an unexported field, either way.
func (v Value) ro() bool { return v.sticky || v.embed }

// ValueOf returns a Value for the value in i.
func ValueOf(i any) Value {
	d := ifaceType(i)
	if d == nil {
		return Value{}
	}
	// The interface holds a copy, so it is not the variable's own storage.
	return Value{d: d, p: ifaceData(i)}
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
//
// A value read out of an unexported field cannot leave reflection, because
// handing it back as an interface is exactly the access Go refuses.
func (v Value) Interface() any {
	if v.ro() {
		panic("reflect.Value.Interface: cannot return value obtained from unexported field or method")
	}
	return v.iface()
}

// iface boxes the value without that check, which is what comparison needs:
// DeepEqual reads unexported fields, as Go's own `==` does, and only passing
// one out to the program is forbidden.
func (v Value) iface() any {
	if v.d == nil {
		return nil
	}
	if v.Kind() == Interface {
		return *(*any)(v.p)
	}
	return makeIface(v.d, descBox(v.d, v.p))
}

// CanInterface reports whether Interface may be called: not for a value read
// out of an unexported field, which Go does not let out of reflection.
func (v Value) CanInterface() bool { return v.d != nil && !v.ro() }

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
		// `-0.0` is zero, which is what the comparison says and the bytes do
		// not: `omitzero` on a float field turns on this distinction.
		return v.Float() == 0
	case Complex64, Complex128:
		return v.Complex() == 0
	case String:
		return v.String() == ""
	case Pointer, UnsafePointer, Func, Map, Slice, Interface, Chan:
		return v.IsNil()
	case Array:
		for i := 0; i < v.Len(); i++ {
			if !v.Index(i).IsZero() {
				return false
			}
		}
		return true
	case Struct:
		for i := 0; i < v.NumField(); i++ {
			if !v.Field(i).IsZero() {
				return false
			}
		}
		return true
	}
	panic("reflect: IsZero of " + v.Kind().String() + " value")
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

// Bytes returns v's underlying bytes: a []byte as it is, and an addressable
// array of bytes as a slice over it, which is what `fmt` asks of a `[N]byte`.
func (v Value) Bytes() []byte {
	switch v.Kind() {
	case Slice:
		if v.Type().Elem().Kind() != Uint8 {
			break
		}
		return *(*[]byte)(v.p)
	case Array:
		if v.Type().Elem().Kind() != Uint8 {
			break
		}
		if !v.addr {
			panic("reflect.Value.Bytes of unaddressable byte array")
		}
		return unsafe.Slice((*byte)(v.p), v.Len())
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
	case Chan:
		return int(chanLen(v.d, v.p))
	}
	panic("reflect: Len of " + v.Kind().String() + " value")
}

func (v Value) Cap() int {
	switch v.Kind() {
	case Array:
		return int(descLen(v.d))
	case Slice:
		return (*sliceHeader)(v.p).cap
	case Chan:
		return int(chanCap(v.d, v.p))
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
		return Value{d: elem, p: unsafe.Add(v.p, uintptr(i)*uintptr(descSize(elem))),
			addr: v.addr, sticky: v.ro()}
	case Slice:
		h := (*sliceHeader)(v.p)
		if i < 0 || i >= h.len {
			panic("reflect: slice index out of range")
		}
		elem := descElem(v.d)
		// A slice's elements are always its own storage.
		return Value{d: elem, p: unsafe.Add(h.data, uintptr(i)*uintptr(descSize(elem))),
			addr: true, sticky: v.ro()}
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
	f := Value{
		d:    descFieldType(v.d, i),
		p:    unsafe.Add(v.p, uintptr(descFieldOffset(v.d, i))),
		addr: v.addr,
		// Only the sticky bit is inherited: the fields of an unexported embedded
		// struct are ordinary values, which is what lets `encoding/json` fill in
		// the ones promoted out of it.
		sticky: v.sticky,
	}
	if descFieldPkgPath(v.d, i) != "" {
		if descFieldEmbedded(v.d, i) {
			f.embed = true
		} else {
			f.sticky = true
		}
	}
	return f
}

// FieldByIndex returns the nested field of a struct, following the index
// `Type.Field` reports. A pointer to an embedded struct along the way is
// followed, as gc's does.
func (v Value) FieldByIndex(index []int) Value {
	if len(index) == 1 {
		return v.Field(index[0])
	}
	v.mustBe(Struct, "FieldByIndex")
	for i, x := range index {
		if i > 0 && v.Kind() == Pointer && descKind(descElem(v.d)) == uint8(Struct) {
			if v.IsNil() {
				panic("reflect: indirection through nil pointer to embedded struct")
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v
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
	return Value{d: descMethodValueType(v.d, i), p: p, sticky: v.ro()}
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
		// Both bits carry over: what an unexported embedded pointer points at is
		// still that struct, whose own fields are readable.
		return Value{d: descElem(v.d), p: p, addr: true, sticky: v.sticky, embed: v.embed}
	case Interface:
		e := ValueOf(*(*any)(v.p))
		if e.IsValid() {
			// What was in the interface is a copy, so only the read-only bit
			// follows it out.
			e.sticky = v.ro()
		}
		return e
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
		keys = append(keys, Value{d: keyType, p: k, sticky: v.ro()})
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
	return Value{d: descElem(v.d), p: val, sticky: v.ro()}
}

// MapRange returns an iterator over a map.
func (v Value) MapRange() *MapIter {
	if v.Kind() != Map {
		panic("reflect: MapRange of " + v.Kind().String() + " value")
	}
	return &MapIter{d: v.d, it: mapIter(v.d, v.p), ro: v.ro()}
}

// MapIter walks a map, one entry at a time.
type MapIter struct {
	d  unsafe.Pointer
	it unsafe.Pointer
	k  unsafe.Pointer
	v  unsafe.Pointer
	ok bool
	// Whether the map came through an unexported field, which its keys and
	// values inherit.
	ro bool
}

func (it *MapIter) Next() bool {
	it.ok, it.k, it.v = mapNext(it.d, it.it)
	return it.ok
}

func (it *MapIter) Key() Value {
	if !it.ok {
		panic("reflect: MapIter.Key called before Next")
	}
	return Value{d: descKey(it.d), p: it.k, sticky: it.ro}
}

func (it *MapIter) Value() Value {
	if !it.ok {
		panic("reflect: MapIter.Value called before Next")
	}
	return Value{d: descElem(it.d), p: it.v, sticky: it.ro}
}

// The setting half. A value can be written when the Value refers to the
// variable's own storage: what a pointer points at, a slice's elements, and
// fields and elements reached from those. The collector does not move
// objects, so a write is a write.

func (v Value) CanAddr() bool { return v.addr }

func (v Value) CanSet() bool { return v.addr && !v.ro() }

// Addr returns a pointer to v, which must be addressable. The pointer itself
// is not: gc's Addr does not hand back a variable either.
func (v Value) Addr() Value {
	if !v.addr {
		panic("reflect.Value.Addr of unaddressable value")
	}
	d := descPtrTo(v.d)
	return Value{d: d, p: boxPointer(v.p), sticky: v.sticky, embed: v.embed}
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
	// An interface is compared by what it holds, on either side: `decode.go`
	// asks whether the value inside an `any` is the pointer it came from.
	if v.Kind() == Interface {
		v = v.Elem()
	}
	if u.Kind() == Interface {
		u = u.Elem()
	}
	if !v.IsValid() || !u.IsValid() {
		return v.IsValid() == u.IsValid()
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
	if v.ro() {
		panic("reflect: reflect.Value." + method + " using value obtained using unexported field")
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

// Constructing values, and calling a function through reflection rather than
// through a func value.

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
		out[i] = Value{d: descOut(v.d, i), p: results[i]}
	}
	return out
}

// Grow makes room in a slice for n more elements, reallocating its backing
// array if it has to, as `append` would.
func (v Value) Grow(n int) {
	v.mustBeAssignable("Grow")
	v.mustBe(Slice, "Grow")
	if n < 0 {
		panic("reflect.Value.Grow: negative len")
	}
	h := (*sliceHeader)(v.p)
	need := h.len + n
	if need < 0 {
		panic("reflect.Value.Grow: slice overflow")
	}
	if need <= h.cap {
		return
	}
	// Go's growth, the rule the runtime's own append uses (src/slice.rs):
	// double while small, then widen by about a quarter. Growing by exactly
	// what was asked would make `encoding/json`, which calls Grow(1) per
	// element, quadratic.
	newcap := h.cap
	if newcap == 0 {
		newcap = need
	}
	for newcap < need {
		if newcap < 256 {
			newcap *= 2
		} else {
			newcap += newcap / 4
		}
	}
	// The element size is read first: between the new array and the store
	// below there must be no allocation, because until the store nothing the
	// collector can see refers to the new array.
	width := int(descSize(descElem(v.d)))
	grown := (*sliceHeader)(descMakeSlice(v.d, h.len, newcap))
	copyBytes(grown.data, h.data, h.len*width)
	h.data, h.cap = grown.data, grown.cap
}

// MakeSlice makes a slice of a type the program mentions, so the slice's own
// `make` is there to make it with. The result is not addressable, as gc's is
// not; its elements are.
func MakeSlice(typ Type, len, cap int) Value {
	rt, ok := typ.(rtype)
	if !ok || typ.Kind() != Slice {
		panic("reflect.MakeSlice of non-slice type")
	}
	if len < 0 {
		panic("reflect.MakeSlice: negative len")
	}
	if cap < len {
		panic("reflect.MakeSlice: len > cap")
	}
	return Value{d: rt.d, p: descMakeSlice(rt.d, len, cap)}
}

// PointerTo returns the type *t.
//
// The descriptor for `*T` is either one the emitter wrote, because the program
// contains the type, or one the runtime builds and keeps: every pointer has the
// same shape, so there is little in it that depends on T (src/reflect.rs).
func PointerTo(t Type) Type {
	rt, ok := t.(rtype)
	if !ok {
		panic("reflect: PointerTo of a nil Type")
	}
	return rtype{descPtrTo(rt.d)}
}

// PtrTo is the old name of PointerTo.
func PtrTo(t Type) Type { return PointerTo(t) }

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
	return Value{d: rt.d, p: m, addr: true}
}

// New returns a Value holding a pointer to a new zero value of typ.
//
// The pointer is not itself addressable, as gc's is not; what it points at is.
func New(typ Type) Value {
	rt, ok := typ.(rtype)
	if !ok {
		panic("reflect: New(nil)")
	}
	// The type first: deriving a descriptor allocates outside the Go heap, so
	// it cannot collect the value descNew is about to make.
	d := descPtrTo(rt.d)
	return Value{d: d, p: descNew(rt.d)}
}

// NewAt returns a Value representing a pointer to a value of the given type,
// using p as that pointer.
//
// Nothing is allocated and nothing is checked: the caller is asserting that
// there is a value of that type at that address, which is what the
// `unsafe.Pointer` in the signature means.
func NewAt(typ Type, p unsafe.Pointer) Value {
	rt, ok := typ.(rtype)
	if !ok {
		panic("reflect: NewAt(nil)")
	}
	return Value{d: descPtrTo(rt.d), p: boxPointer(p)}
}

// The type constructors, which mint a type the program may never have written.
//
// A descriptor is a Rust static the emitter writes for the types a program
// mentions (DESIGN §6), and most of one cannot be assembled from the outside:
// it carries the functions that box, zero, compare and hash a value of the
// type, and — the part nothing here can supply — the knowledge the collector
// needs to find the pointers inside one. `PointerTo` is the exception, and only
// because every pointer has the same shape, so there is nothing in its
// descriptor that depends on what it points at (src/reflect.rs).
//
// They are declared because a package that merely mentions one of them should
// still compile and still pass every test that does not reach it: nothing in
// the standard library calls these on a path it takes, and `testify` and
// `encoding/gob` each reach one in a single test.

// ArrayOf returns the array type with the given length and element type.
func ArrayOf(length int, elem Type) Type { panic(unsupported("ArrayOf")) }

// SliceOf returns the slice type with element type t.
func SliceOf(t Type) Type { panic(unsupported("SliceOf")) }

// MapOf returns the map type with the given key and element types.
func MapOf(key, elem Type) Type { panic(unsupported("MapOf")) }

// ChanOf returns the channel type with the given direction and element type.
func ChanOf(dir ChanDir, t Type) Type { panic(unsupported("ChanOf")) }

// FuncOf returns the function type with the given argument and result types.
func FuncOf(in, out []Type, variadic bool) Type { panic(unsupported("FuncOf")) }

// StructOf returns the struct type containing fields.
func StructOf(fields []StructField) Type { panic(unsupported("StructOf")) }

// Zero returns the zero value of a type: all-zero bytes, in an object of the
// right size that knows how to trace itself. The result is not addressable,
// as gc's is not.
func Zero(typ Type) Value {
	rt, ok := typ.(rtype)
	if !ok {
		panic("reflect.Zero of a nil Type")
	}
	return Value{d: rt.d, p: descZero(rt.d)}
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
	case Func:
		// Go compares two func values only against nil, and DeepEqual says so:
		// two nil funcs are equal and nothing else is.
		return x.IsNil() && y.IsNil()
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
		return x.iface() == y.iface()
	}
}

func (v Value) mustBe(k Kind, method string) {
	if v.Kind() != k {
		panic(&ValueError{Method: "reflect.Value." + method, Kind: v.Kind()})
	}
}

// A ValueError occurs when a Value method is called on a Value that does not
// support it, which is what every `mustBe` here reports. gc panics with one of
// these rather than with a string, and a program may recover and read it:
// `jmoiron/sqlx` names the type, and `encoding/json` has been known to.
type ValueError struct {
	Method string
	Kind   Kind
}

func (e *ValueError) Error() string {
	if e.Kind == 0 {
		return "reflect: call of " + e.Method + " on zero Value"
	}
	return "reflect: call of " + e.Method + " on " + e.Kind.String() + " Value"
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
func descMakeSlice(d unsafe.Pointer, len, cap int) unsafe.Pointer
func descPtrTo(d unsafe.Pointer) unsafe.Pointer
func descNew(d unsafe.Pointer) unsafe.Pointer
func boxPointer(p unsafe.Pointer) unsafe.Pointer
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

// The rest of reflect needs more than the descriptors carry, and says so
// rather than guessing:
//
//   - Append and AppendSlice have to hand back a slice that shares the
//     original's array whenever there is room in it, as `append` does, which
//     means boxing a slice header the runtime did not just allocate;
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
