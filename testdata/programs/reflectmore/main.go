// Program reflectmore is the part of reflection that answers questions about a
// type rather than about a value: what a func type is made of, whether one type
// implements another, what a type's zero value is, and the writes that reach
// into a slice or a map.
package main

import (
	"fmt"
	"io"
	"reflect"
	"sort"
)

type shape interface {
	Area() float64
	Name() string
}

type square struct{ side float64 }

func (s square) Area() float64 { return s.side * s.side }
func (s square) Name() string  { return "square" }

type dot struct{}

func (dot) Name() string { return "dot" }

func main() {
	// What a func type is made of.
	ft := reflect.TypeOf(func(a int, b string, c ...byte) (bool, error) { return false, nil })
	fmt.Println(ft.Kind(), ft.NumIn(), ft.NumOut(), ft.IsVariadic())
	for i := 0; i < ft.NumIn(); i++ {
		fmt.Print(ft.In(i), " ")
	}
	for i := 0; i < ft.NumOut(); i++ {
		fmt.Print(ft.Out(i), " ")
	}
	fmt.Println()
	fmt.Println(reflect.TypeOf(func() {}).NumIn(), reflect.TypeOf(func() {}).IsVariadic())

	// Whether a type implements an interface.
	shapeType := reflect.TypeFor[shape]()
	errType := reflect.TypeFor[error]()
	stringerType := reflect.TypeFor[fmt.Stringer]()
	fmt.Println(shapeType.Kind(), shapeType.NumMethod(), errType.NumMethod())
	fmt.Println(reflect.TypeOf(square{}).Implements(shapeType),
		reflect.TypeOf(dot{}).Implements(shapeType),
		reflect.TypeOf(io.EOF).Implements(errType),
		reflect.TypeOf(square{}).Implements(stringerType))
	fmt.Println(reflect.TypeOf(square{}).AssignableTo(shapeType),
		reflect.TypeOf(square{}).AssignableTo(reflect.TypeOf(square{})),
		reflect.TypeOf(dot{}).AssignableTo(shapeType))

	// Zero values.
	fmt.Printf("%v %q %v %v\n",
		reflect.Zero(reflect.TypeOf(0)).Interface(),
		reflect.Zero(reflect.TypeOf("")).Interface(),
		reflect.Zero(reflect.TypeOf(square{})).Interface(),
		reflect.Zero(reflect.TypeOf([]int(nil))).IsNil())

	// What a type would truncate.
	i8, u8, f32 := reflect.TypeOf(int8(0)), reflect.TypeOf(uint8(0)), reflect.TypeOf(float32(0))
	fmt.Println(i8.OverflowInt(127), i8.OverflowInt(128), u8.OverflowUint(255), u8.OverflowUint(256),
		f32.OverflowFloat(1e38), f32.OverflowFloat(1e39))

	// Equality, through the descriptor's own comparison.
	a, b := square{2}, square{2}
	fmt.Println(reflect.ValueOf(a).Equal(reflect.ValueOf(b)),
		reflect.ValueOf(a).Equal(reflect.ValueOf(square{3})),
		reflect.ValueOf(1).Equal(reflect.ValueOf("1")),
		reflect.ValueOf([]int{1}).Comparable())

	// Writes into a value's own storage.
	nums := []int{1, 2, 3, 4, 5}
	v := reflect.ValueOf(&nums).Elem()
	v.Index(0).SetInt(10)
	v.SetLen(3)
	fmt.Println(nums, len(nums), cap(nums))
	v.SetLen(5)
	v.Index(4).SetZero()
	fmt.Println(nums)

	s := square{7}
	sv := reflect.ValueOf(&s).Elem()
	sv.SetZero()
	fmt.Println(s)

	bs := []byte("before")
	bv := reflect.ValueOf(&bs).Elem()
	bv.SetBytes([]byte("after"))
	fmt.Println(string(bs))

	// A map made and written through reflection.
	mt := reflect.TypeOf(map[string]int(nil))
	m := reflect.MakeMap(mt)
	m.SetMapIndex(reflect.ValueOf("one"), reflect.ValueOf(1))
	m.SetMapIndex(reflect.ValueOf("two"), reflect.ValueOf(2))
	m.SetMapIndex(reflect.ValueOf("gone"), reflect.ValueOf(3))
	m.SetMapIndex(reflect.ValueOf("gone"), reflect.Value{})
	fmt.Println(m.Len(), m.MapIndex(reflect.ValueOf("two")).Int(),
		m.MapIndex(reflect.ValueOf("gone")).IsValid())
	keys := make([]string, 0, m.Len())
	for _, k := range m.MapKeys() {
		keys = append(keys, k.String())
	}
	sort.Strings(keys)
	fmt.Println(keys)

	// And the same map, put back into an interface and printed by fmt.
	fmt.Println(m.Interface())
}
