// Program reflectarrayof asks reflect.ArrayOf for array types.
//
// An array's descriptor depends on its element at every point — how to box, zero,
// compare and hash one, and where the pointers inside one are for the collector —
// so unlike a pointer's there is nothing to derive it from. What rustygo does
// instead is list, on each element type, the arrays of it the program contains,
// and ArrayOf is a lookup among them. The answer is then the program's own type,
// so it is the *same* type: that is what this checks, because a second type equal
// to the first would pass every test but `==`, and then fail assignment.
//
// It is how `testify` asks — `reflect.ArrayOf(v.Len(), t.Elem())` for a value it is
// already holding, so the length is that type's own and the type exists. Asking
// for one the program does not contain says so, and this checks that too.
package main

import (
	"reflect"
	"strings"
)

type point struct {
	X, Y int
}

type withPointers struct {
	Name string
	Next *point
}

func main() {
	// The identity that matters: the same type back, not a second one like it.
	ti := reflect.TypeOf([3]int{})
	got := reflect.ArrayOf(3, reflect.TypeFor[int]())
	println("int3 same", got == ti, got.String(), got.Len(), got.Elem().Kind().String())

	// Of a struct element, and of one holding pointers, which is where a derived
	// descriptor would have had to know where the pointers are.
	tp := reflect.TypeOf([2]point{})
	println("point2 same", reflect.ArrayOf(2, reflect.TypeFor[point]()) == tp)
	tw := reflect.TypeOf([2]withPointers{})
	println("withPointers2 same", reflect.ArrayOf(2, reflect.TypeFor[withPointers]()) == tw)

	// A value made through it is a value of that type: settable, comparable, and
	// it survives being handed out as an interface.
	v := reflect.New(reflect.ArrayOf(2, reflect.TypeFor[point]())).Elem()
	v.Index(0).Field(0).SetInt(1)
	v.Index(1).Field(1).SetInt(2)
	arr := v.Interface().([2]point)
	println("made", arr[0].X, arr[1].Y, "equal", arr == [2]point{{X: 1}, {Y: 2}})

	// The same with pointers inside, which the collector has to trace: fill one
	// in, allocate a great deal, and read it back.
	w := reflect.New(reflect.ArrayOf(2, reflect.TypeFor[withPointers]())).Elem()
	w.Index(0).Field(0).SetString("zero")
	w.Index(1).Field(1).Set(reflect.ValueOf(&point{X: 7, Y: 8}))
	junk := make([]*point, 0, 64)
	for i := 0; i < 1024; i++ {
		junk = append(junk, &point{X: i})
	}
	wa := w.Interface().([2]withPointers)
	println("traced", wa[0].Name, wa[1].Next.X, wa[1].Next.Y, "junk", len(junk))

	// Zero, copying and DeepEqual over an array made this way.
	z := reflect.New(reflect.ArrayOf(3, reflect.TypeFor[int]())).Elem()
	println("fresh zero", z.IsZero(), "len", z.Len())
	src := [3]int{4, 5, 6}
	z.Set(reflect.ValueOf(src))
	println("copied", z.Index(0).Int(), z.Index(2).Int(),
		"deepequal", reflect.DeepEqual(z.Interface(), src))

	// Length zero is a type like any other.
	println("zero length", reflect.ArrayOf(0, reflect.TypeFor[int]()) == reflect.TypeOf([0]int{}))

	// One the program does not contain. gc builds it; rustygo says it cannot,
	// naming the type. Either is a right answer and neither is a wrong one, so
	// what is checked is that: a type of the length asked for, or a panic that
	// says why. The output is the same under both, which is the point — the
	// divergence is in which branch runs, not in what the program observes.
	func() {
		defer func() {
			if v := recover(); v != nil {
				msg, _ := v.(string)
				println("absent handled",
					strings.Contains(msg, "is not a type this program contains"))
			}
		}()
		if t := reflect.ArrayOf(9999, reflect.TypeFor[point]()); t.Len() == 9999 {
			println("absent handled true")
		}
	}()

	// A negative length is Go's own error, not ours.
	func() {
		defer func() { println("negative:", recover().(string)) }()
		_ = reflect.ArrayOf(-1, reflect.TypeFor[int]())
	}()
}
