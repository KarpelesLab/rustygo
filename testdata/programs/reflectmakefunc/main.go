// Program reflectmakefunc builds func values at run time with reflect.MakeFunc
// and calls them as ordinary functions.
//
// MakeFunc is Value.Call run backwards. Call hands boxed arguments to generated
// code that knows the signature; MakeFunc has generated code — a trampoline per
// func type, closing over the function the program supplied — hand boxed
// arguments out to reflect, which turns them into []Value, calls that function,
// and reads the results back. Every boxing allocates, so what this program is
// really checking is that nothing an argument refers to is lost on the way in.
package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

type pair struct {
	name string
	vals []int
}

// sum is the shape test/recover.go uses: a function made at run time, called by
// name, which may panic on the way through.
func makeSum() func(int, int) int {
	fn := func(in []reflect.Value) []reflect.Value {
		a, b := in[0].Int(), in[1].Int()
		if a < 0 {
			panic("negative")
		}
		return []reflect.Value{reflect.ValueOf(int(a + b))}
	}
	return reflect.MakeFunc(reflect.TypeFor[func(int, int) int](), fn).Interface().(func(int, int) int)
}

// makeJoin allocates inside the made function, so a collection can happen with
// the arguments only half unpacked.
func makeJoin() func(pair, string) (string, error) {
	fn := func(in []reflect.Value) []reflect.Value {
		p := in[0].Interface().(pair)
		sep := in[1].String()
		parts := make([]string, 0, len(p.vals))
		for _, v := range p.vals {
			parts = append(parts, fmt.Sprint(v))
		}
		s := p.name + ":" + strings.Join(parts, sep)
		var err error
		if len(p.vals) == 0 {
			err = errors.New("empty")
		}
		out := reflect.New(reflect.TypeFor[error]()).Elem()
		if err != nil {
			out.Set(reflect.ValueOf(err))
		}
		return []reflect.Value{reflect.ValueOf(s), out}
	}
	t := reflect.TypeFor[func(pair, string) (string, error)]()
	return reflect.MakeFunc(t, fn).Interface().(func(pair, string) (string, error))
}

func main() {
	add := makeSum()
	fmt.Println(add(2, 3), add(10, -0), add(0, 0))

	// A panic from inside the made function unwinds through the trampoline and
	// is recovered by the caller, which is what test/recover.go asks for.
	func() {
		defer func() { fmt.Println("recovered:", recover()) }()
		fmt.Println(add(-1, 1))
	}()

	join := makeJoin()
	s, err := join(pair{name: "xs", vals: []int{1, 2, 3}}, "-")
	fmt.Println(s, err)
	s, err = join(pair{name: "none"}, ",")
	fmt.Println(s, err)

	// No arguments and no results, so the trampoline has only its environment.
	var ran int
	noop := reflect.MakeFunc(reflect.TypeFor[func()](), func([]reflect.Value) []reflect.Value {
		ran++
		return nil
	}).Interface().(func())
	noop()
	noop()
	fmt.Println(ran)

	// A variadic signature, whose last argument arrives as one slice Value.
	count := reflect.MakeFunc(reflect.TypeFor[func(string, ...int) string](),
		func(in []reflect.Value) []reflect.Value {
			rest := in[1]
			total := 0
			for i := 0; i < rest.Len(); i++ {
				total += int(rest.Index(i).Int())
			}
			return []reflect.Value{reflect.ValueOf(fmt.Sprint(in[0].String(), "=", total))}
		}).Interface().(func(string, ...int) string)
	fmt.Println(count("sum", 1, 2, 3, 4), count("none"))

	// Through the Value rather than the Go func value, and what the type says.
	v := reflect.MakeFunc(reflect.TypeFor[func(int) int](), func(in []reflect.Value) []reflect.Value {
		return []reflect.Value{reflect.ValueOf(int(in[0].Int() * 2))}
	})
	fmt.Println(v.Kind(), v.Type(), v.IsNil(), v.Type().NumIn(), v.Type().NumOut())
	fmt.Println(v.Call([]reflect.Value{reflect.ValueOf(21)})[0].Int())

	// Many calls with allocation on both sides, to give the collector something
	// to do while an argument is only half boxed.
	total := 0
	for i := 0; i < 200; i++ {
		s, _ := join(pair{name: fmt.Sprint("p", i), vals: []int{i, i + 1}}, "+")
		total += len(s)
	}
	fmt.Println(total)
}
