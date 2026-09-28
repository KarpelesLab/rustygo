// Program finalizers exercises runtime.SetFinalizer: an object that dies and
// is handed to its finalizer, one whose finalizer is taken away again, one
// that stays reachable, and two where one points at the other, which is what
// orders their finalizers.
//
// Nothing printed may depend on when a collection happens.
package main

import (
	"runtime"
	"time"
)

type node struct {
	next *node
	n    int
}

// set allocates in a frame of its own, so that nothing of the caller's is
// still pointing at the object when the collector looks.
func set(i int, done chan int) {
	x := new(int32)
	*x = int32(i)
	runtime.SetFinalizer(x, func(p *int32) { done <- int(*p) })
}

// chain allocates a pair, the first pointing at the second. Go finalizes the
// first and only then the second, because while the first is waiting it is
// still a reason to keep the second alive.
func chain(order chan string) {
	b := &node{n: 2}
	a := &node{next: b, n: 1}
	runtime.SetFinalizer(b, func(*node) { order <- "b" })
	runtime.SetFinalizer(a, func(*node) { order <- "a" })
}

func main() {
	const n = 20
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		set(i, done)
	}

	// A finalizer that is removed before it can run.
	gone := new(int32)
	runtime.SetFinalizer(gone, func(*int32) { println("BUG: a cleared finalizer ran") })
	runtime.SetFinalizer(gone, nil)

	// An object nothing lets go of.
	keep := &node{n: 7}
	runtime.SetFinalizer(keep, func(*node) { println("BUG: a live object's finalizer ran") })

	order := make(chan string, 2)
	chain(order)

	seen := make([]bool, n)
	count := 0
	for count < n {
		// Nothing else allocates enough to bring the collector along.
		runtime.GC()
		select {
		case v := <-done:
			if v < 0 || v >= n || seen[v] {
				println("BUG: got", v, "twice or out of range")
				return
			}
			seen[v] = true
			count++
		case <-time.After(10 * time.Second):
			println("BUG: timed out with", count, "of", n)
			return
		}
	}
	println("all finalized:", count == n)

	first, second := "", ""
	for i := 0; i < 2; i++ {
		runtime.GC()
		select {
		case s := <-order:
			if first == "" {
				first = s
			} else {
				second = s
			}
		case <-time.After(10 * time.Second):
			println("BUG: the chain timed out")
			return
		}
	}
	println("chain order:", first+second)
	println("kept:", keep.n)
	runtime.GC()
	runtime.KeepAlive(keep)
	println("done")
}
