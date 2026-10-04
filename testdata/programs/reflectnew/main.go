// Program reflectnew makes values whose type is only known at run time:
// reflect.New, reflect.PointerTo and Value.Addr all hand back a `*T`, and the
// emitter writes descriptors only for the types a program mentions.
//
// So the descriptor for `*T` is sometimes one the emitter wrote and sometimes
// one the runtime derives from the element's. Both have to be the *same*
// descriptor every time, because Go says a type is one type: this program
// checks it two ways, by comparing Types and by asserting a value New made
// back to the pointer type the program itself wrote.
//
// The method set is the part a derived descriptor cannot invent, which is what
// `PointerTo(T).Implements(...)` asks and what `encoding/json` decides how to
// encode a value by.
package main

import (
	"fmt"
	"reflect"
	"strings"
)

type point struct {
	X, Y int
	Name string
}

// A pointer method, so the method set of *point is not the method set of point.
func (p *point) Scale(n int) { p.X *= n; p.Y *= n }

func (p point) String() string { return fmt.Sprintf("(%d,%d)", p.X, p.Y) }

type stringer interface{ String() string }

type scaler interface{ Scale(int) }

func main() {
	// New, then write through the pointer and read it back as the type the
	// program wrote, which only works if the two descriptors are one.
	v := reflect.New(reflect.TypeFor[point]())
	fmt.Println(v.Type(), v.Kind(), v.CanAddr(), v.Elem().CanAddr())
	e := v.Elem()
	e.Field(0).SetInt(3)
	e.Field(1).SetInt(4)
	e.Field(2).SetString("origin")
	p := v.Interface().(*point)
	fmt.Println(*p, p.String())

	// Addr is New's mirror: a pointer to storage that already exists.
	var q point
	a := reflect.ValueOf(&q).Elem().Addr()
	fmt.Println(a.Type(), a.CanAddr())
	a.Interface().(*point).X = 7
	fmt.Println(q.X)

	// The method set of *T, which is what a derived descriptor cannot have.
	pt := reflect.PointerTo(reflect.TypeFor[point]())
	fmt.Println(pt, pt.Kind(), pt.Elem(), pt.NumMethod())
	fmt.Println(pt.Implements(reflect.TypeFor[scaler]()),
		pt.Implements(reflect.TypeFor[stringer]()),
		reflect.TypeFor[point]().Implements(reflect.TypeFor[scaler]()))
	v.Interface().(scaler).Scale(10)
	fmt.Println(*p)

	// Type identity, which is the descriptor's address: the same type however
	// it was reached.
	tp := reflect.TypeFor[point]()
	fmt.Println(reflect.PointerTo(tp) == reflect.PointerTo(tp),
		reflect.PointerTo(tp) == reflect.TypeOf(&point{}),
		reflect.New(tp).Type() == reflect.TypeOf(&point{}),
		reflect.PointerTo(reflect.PointerTo(tp)) == reflect.TypeOf(new(*point)))

	// A pointer type nothing in the program mentions, so the runtime derives
	// its descriptor. It has no methods, no name and no package.
	ds := reflect.PointerTo(reflect.TypeFor[[]strings.Builder]())
	fmt.Println(ds, ds.Name() == "", ds.PkgPath() == "", ds.NumMethod(),
		ds.Size(), ds.Elem().Kind(), ds == reflect.PointerTo(ds.Elem()))

	// Through a pointer-to-pointer, which is the shape `json.Unmarshal` walks
	// into a **T field.
	pp := reflect.New(reflect.PointerTo(tp))
	pp.Elem().Set(reflect.New(tp))
	pp.Elem().Elem().Field(0).SetInt(9)
	fmt.Println((**pp.Interface().(**point)).X)

	// Grow makes room the way append does, and the elements it leaves beyond
	// the length are zero.
	s := reflect.ValueOf(&[]point{{X: 1}}).Elem()
	s.Grow(3)
	fmt.Println(s.Len(), s.Cap() >= 4, s.Index(0).Field(0).Int())
	s.SetLen(4)
	fmt.Println(s.Len(), s.Index(3).Field(0).Int(), s.Index(3).Field(2).String() == "")
	for i := 0; i < 40; i++ {
		s.Grow(1)
		s.SetLen(s.Len() + 1)
		s.Index(s.Len() - 1).Field(0).SetInt(int64(i))
	}
	fmt.Println(s.Len(), s.Index(43).Field(0).Int())

	// A nested field, reached by the index Type.Field reports, through a
	// pointer to an embedded struct.
	type inner struct{ N int }
	type outer struct {
		*inner
		M int
	}
	o := reflect.ValueOf(&outer{inner: &inner{N: 5}, M: 6}).Elem()
	fmt.Println(o.FieldByIndex([]int{0, 0}).Int(), o.FieldByIndex([]int{1}).Int())

	// Zero and SetZero over a pointer type.
	z := reflect.Zero(pt)
	fmt.Println(z.IsNil(), z.IsZero(), z.Type())
	a2 := reflect.New(pt).Elem()
	a2.Set(reflect.ValueOf(&q))
	fmt.Println(a2.IsNil())
	a2.SetZero()
	fmt.Println(a2.IsNil())
}
