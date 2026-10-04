// Program typenames prints types the way Go prints them: `%T`, `%#v`, a
// `reflect.Type`, and the message of a failed type assertion.
//
// Go's spelling is not go/types' spelling, which is what the emitter has to
// work from. Go writes `struct { A int }` and `interface {}` where go/types
// writes `struct{A int}` and `any`, names a type by what it is rather than by
// what it was called — `[]byte` is `[]uint8` — and drops a parameter's name,
// so `func(d int)` is `func(int)`.
package main

import (
	"fmt"
	"reflect"
)

type inner struct{ X int }

type Box[T any] struct{ V T }

type Named interface {
	Zed() int
	Foo(int) string
}

func main() {
	fmt.Printf("%T\n", struct{}{})
	fmt.Printf("%T\n", struct {
		A int
		B string
	}{})
	fmt.Printf("%T\n", struct {
		A int `json:"a,omitempty"`
		b []inner
	}{})
	fmt.Printf("%T\n", struct {
		inner
		Y int
	}{})
	fmt.Printf("%T %T\n", struct{ A, B int }{}, &struct{ P *struct{ Q int } }{})

	fmt.Printf("%T %T %T\n", []any{}, map[any]any{}, [3]*inner{})
	fmt.Printf("%T %T\n", []byte{}, []rune{})
	fmt.Printf("%T %T %T\n", make(chan int), make(chan<- int), make(<-chan struct{}))

	fmt.Printf("%T\n", func(a ...int) {})
	fmt.Printf("%T\n", func(d int, s string) (int, error) { return 0, nil })
	fmt.Printf("%T\n", map[struct{ K int }]func(int) (string, error){})

	fmt.Printf("%T %T\n", Box[int]{}, Box[[]any]{})
	fmt.Println(reflect.TypeFor[Named](), reflect.TypeFor[interface{ M(...int) (a, b error) }]())
	fmt.Println(reflect.TypeFor[interface{}](), reflect.TypeFor[map[Named]Box[string]]())
	fmt.Println(reflect.TypeOf(inner{}), reflect.TypeOf(&inner{}), reflect.TypeOf(uint8(0)))

	fmt.Printf("%#v\n", []any{1, "x"})
	fmt.Printf("%#v\n", map[string]any{"k": 1})
	fmt.Printf("%#v\n", struct {
		A any
		B []byte
	}{A: 1, B: []byte("z")})

	// The name a failed type assertion reports.
	var v any = 3
	_, err := func() (out string, err any) {
		defer func() { err = recover() }()
		out = v.(string)
		return
	}()
	fmt.Println(err)
	var i any = []any{}
	_, err2 := func() (out Named, err any) {
		defer func() { err = recover() }()
		out = i.(Named)
		return
	}()
	fmt.Println(err2)
}
