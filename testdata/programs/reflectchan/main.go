// Program reflectchan reaches a channel through reflection, and asks a type the
// questions a `range` asks of it.
//
// A channel is a runtime object rather than memory laid out by Go's rules, so
// reflection cannot read one by address the way it reads a struct field: its
// accessors are generated for its own type, as a map's are.
package main

import (
	"fmt"
	"reflect"
	"sort"
)

func main() {
	ch := make(chan int, 4)
	v := reflect.ValueOf(ch)
	t := v.Type()
	fmt.Println(t, t.Kind(), t.ChanDir(), t.Elem(), t.ChanDir() == reflect.BothDir)
	fmt.Println(reflect.TypeOf(make(<-chan string)).ChanDir(),
		reflect.TypeOf(make(chan<- string)).ChanDir())

	// Sending and receiving, with and without blocking.
	fmt.Println(v.TrySend(reflect.ValueOf(7)), v.Len(), v.Cap())
	v.Send(reflect.ValueOf(8))
	fmt.Println(v.Len())
	x, ok := v.Recv()
	fmt.Println(x.Int(), ok)
	y, ok := v.TryRecv()
	fmt.Println(y.Int(), ok)
	z, ok := v.TryRecv()
	fmt.Println(z.IsValid(), ok)

	// A closed channel drains and then says so.
	v.Send(reflect.ValueOf(9))
	v.Close()
	a, ok := v.Recv()
	fmt.Println(a.Int(), ok)
	b, ok := v.Recv()
	fmt.Println(b.IsValid(), ok)

	// What a type says about being ranged over.
	for _, x := range []any{0, "s", []int{}, map[int]int{}, make(chan int), func(func(int) bool) {}, 1.5} {
		rt := reflect.TypeOf(x)
		fmt.Printf("%v:%v,%v ", rt, rt.CanSeq(), rt.CanSeq2())
	}
	fmt.Println()

	// And ranging, as a value.
	var got []int
	for e := range reflect.ValueOf([]int{3, 1, 2}).Seq2() {
		got = append(got, int(e.Int()))
	}
	sort.Ints(got)
	fmt.Println(got)

	var idx []int
	for i := range reflect.ValueOf(4).Seq() {
		idx = append(idx, int(i.Int()))
	}
	fmt.Println(idx)

	keys := []string{}
	for k, e := range reflect.ValueOf(map[string]int{"a": 1, "b": 2}).Seq2() {
		keys = append(keys, fmt.Sprint(k.String(), e.Int()))
	}
	sort.Strings(keys)
	fmt.Println(keys)

	// An index path, and the error form.
	type inner struct{ N int }
	type outer struct {
		I inner
		P *inner
	}
	o := outer{I: inner{5}}
	ot := reflect.TypeOf(o)
	fmt.Println(ot.FieldByIndex([]int{0, 0}).Name, reflect.ValueOf(o).FieldByIndex([]int{0, 0}).Int())
	_, err := reflect.ValueOf(o).FieldByIndexErr([]int{1, 0})
	fmt.Println(err != nil)
	fmt.Println(reflect.ValueOf(&o).Elem().UnsafeAddr() != 0)
}
