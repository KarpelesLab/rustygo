// Program goexit ends goroutines with runtime.Goexit, which is what
// `testing`'s t.Skip and t.FailNow are written on.
//
// Goexit is not a panic: nothing recovers it and nothing stops it, but every
// frame's deferred calls run on the way out — including the deferred call that
// tells another goroutine the first one is done, which is the whole reason
// `testing` can use it.
package main

import (
	"runtime"
	"sync"
)

// runner is the shape testing uses: a named function started with `go`, whose
// deferred function signals a channel, with the body leaving through Goexit
// from a nested call.
func runner(signal chan<- bool, body func()) {
	defer func() {
		r := recover()
		println("  deferred, recover was nil:", r == nil)
		signal <- true
	}()
	body()
	println("  body returned")
}

func main() {
	sig := make(chan bool)
	go runner(sig, func() { println("  body running"); skip() })
	println("got", <-sig)

	// A goroutine that ends with Goexit, with two defers on the way out.
	done := make(chan string, 4)
	go func() {
		defer func() { done <- "outer defer" }()
		defer func() { done <- "inner defer" }()
		runtime.Goexit()
		done <- "not reached"
	}()
	println(<-done, "|", <-done)

	// A WaitGroup still gets its Done, and a nested goroutine still reports.
	var wg sync.WaitGroup
	wg.Add(1)
	inner := make(chan bool)
	go func() {
		defer wg.Done()
		go runner(inner, func() { skip() })
		println("inner got", <-inner)
	}()
	wg.Wait()

	// Every frame's defers run, innermost first.
	go func() {
		defer func() { done <- "frame 1" }()
		func() {
			defer func() { done <- "frame 2" }()
			runtime.Goexit()
		}()
		done <- "not reached either"
	}()
	println(<-done, "|", <-done)
	println("done")
}

func skip() {
	println("  skipping")
	runtime.Goexit()
}
