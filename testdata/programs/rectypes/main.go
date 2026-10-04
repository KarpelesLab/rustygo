// Program rectypes uses named types that contain themselves with no struct to
// break the cycle: `type R []R` and `type P *P`.
//
// Their Rust value form would unfold for ever — `Slice<Slot<Slice<Slot<…>>>>`
// — so the *place* form is given a name of its own, which closes the cycle the
// way a struct does. `encoding/json`'s own tests declare the first of these to
// check that Marshal reports a cycle.
package main

import "fmt"

type recSlice []recSlice

type selfPtr *selfPtr

type holder struct {
	r recSlice
	p selfPtr
}

func depth(r recSlice) int {
	if len(r) == 0 {
		return 0
	}
	// Only the first element, so a cycle would spin rather than recurse for
	// ever on a tree.
	return 1 + depth(r[0])
}

func main() {
	var r recSlice
	fmt.Println(r == nil, len(r), depth(r))

	r = append(r, nil, recSlice{nil, nil, nil})
	fmt.Println(len(r), len(r[0]), len(r[1]), depth(r))

	// A deep chain, which the collector has to walk element by element: each
	// level is a slice whose one element is the level below.
	deep := recSlice{nil}
	for i := 0; i < 2000; i++ {
		deep = recSlice{deep}
	}
	fmt.Println(depth(deep))

	// Ranging and copying, which go through the place form.
	for i, e := range r {
		fmt.Println(i, len(e))
	}
	dst := make(recSlice, len(r))
	fmt.Println(copy(dst, r), len(dst[1]))

	var p selfPtr
	fmt.Println(p == nil)
	p = &p
	fmt.Println(*p == nil, **p == nil, *p == p)

	h := holder{r: r, p: p}
	fmt.Println(len(h.r), h.p != nil, fmt.Sprintf("%T %T", h.r, h.p))

	// Through an interface, which needs a type descriptor for each of them.
	var any1 any = r
	var any2 any = p
	fmt.Println(len(any1.(recSlice)), any2.(selfPtr) == p)
}
