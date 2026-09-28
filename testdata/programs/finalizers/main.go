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

// A box is deliberately larger than a word. gc combines tiny pointer-free
// allocations into one block and finalizes such a block only once every
// object in it has died, so a program that counts finalizers of `new(int32)`s
// is counting gc's allocator, not its collector (Go's own tinyfin.go allows
// for exactly this).
type box struct {
	n   int
	pad [8]int64
}

// set allocates in a frame of its own, so that nothing of the caller's is
// still pointing at the object when the collector looks.
func set(i int, done chan int) {
	b := &box{n: i}
	runtime.SetFinalizer(b, func(p *box) { done <- p.n })
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
	// Most of them, not all: whether the very last object a loop allocated is
	// still reachable is a question about the caller's frame, and the answer
	// is allowed to differ.
	const n, want = 20, 16
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		set(i, done)
	}

	// A finalizer that is removed before it can run.
	gone := &box{n: -1}
	runtime.SetFinalizer(gone, func(*box) { println("BUG: a cleared finalizer ran") })
	runtime.SetFinalizer(gone, nil)

	// An object nothing lets go of.
	keep := &node{n: 7}
	runtime.SetFinalizer(keep, func(*node) { println("BUG: a live object's finalizer ran") })

	order := make(chan string, 2)
	chain(order)

	seen := make([]bool, n)
	count := 0
	for count < want {
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
		case <-time.After(20 * time.Second):
			println("BUG: timed out with", count, "of", n)
			return
		}
	}
	println("most finalized:", count >= want)

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
		case <-time.After(20 * time.Second):
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
