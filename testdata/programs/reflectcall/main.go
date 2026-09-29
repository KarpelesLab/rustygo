// Program reflectcall calls functions through reflection: a plain function, a
// method value, a method expression whose first argument is the receiver, and a
// closure over something the collector has to keep alive across the call.
//
// The call itself is generated per func type, because only the emitter knows a
// signature; the arguments arrive boxed and the results go back boxed, and the
// boxes for the results are made before the call so that the call's own
// allocations cannot collect them.
package main

import (
	"fmt"
	"reflect"
	"strings"
)

type acc struct{ n int }

func (a *acc) Add(d int) int { a.n += d; return a.n }

func two(a int, b string) (string, int) { return strings.Repeat(b, a), a * 2 }

func none() {}

func one(s string) string { return s + "!" }

func main() {
	fmt.Println(reflect.ValueOf(two).Call([]reflect.Value{
		reflect.ValueOf(3), reflect.ValueOf("ab"),
	}))
	fmt.Println(len(reflect.ValueOf(none).Call(nil)))
	fmt.Println(reflect.ValueOf(one).Call([]reflect.Value{reflect.ValueOf("hi")})[0].String())

	// A method value, called through reflection.
	a := &acc{}
	m := reflect.ValueOf(a).MethodByName("Add")
	fmt.Println(m.Call([]reflect.Value{reflect.ValueOf(2)})[0].Int(),
		m.Call([]reflect.Value{reflect.ValueOf(5)})[0].Int(), a.n)

	// A method expression, whose first argument is the receiver.
	me := reflect.TypeOf(a).Method(0).Func
	fmt.Println(me.Call([]reflect.Value{reflect.ValueOf(a), reflect.ValueOf(10)})[0].Int())

	// A closure over something the collector has to keep.
	prefix := strings.Repeat("x", 3)
	f := func(s string) string { return prefix + s }
	fmt.Println(reflect.ValueOf(f).Call([]reflect.Value{reflect.ValueOf("y")})[0].String())

	// What it refuses.
	for _, bad := range []func(){
		func() { reflect.ValueOf(one).Call(nil) },
		func() { reflect.ValueOf(one).Call([]reflect.Value{reflect.ValueOf(1)}) },
		func() { reflect.ValueOf(3).Call(nil) },
	} {
		func() {
			defer func() { fmt.Println("refused:", recover() != nil) }()
			bad()
		}()
	}
}
