// recover's exact rule: a value only for the function a defer invoked, and
// only its own panic. Modelled on Go's own test/recover1.go, which calls
// this territory "here be dragons".
package main

func mustRecover(x int) {
	mustNotRecover() // a sub-call recovers nothing, even from here
	v := recover()
	if v == nil {
		println("BUG: missing recover, want", x)
		return
	}
	if v.(int) != x {
		println("BUG: recovered", v.(int), "want", x)
	}
	// The panic is gone now, however often it is asked for.
	if recover() != nil {
		println("BUG: recovered twice")
	}
}

func mustNotRecover() {
	if recover() != nil {
		println("BUG: spurious recover")
	}
}

// A nested panic: the inner one is recovered, then the outer.
func nested() {
	defer mustRecover(1)
	defer func() {
		defer mustRecover(2)
		panic(2)
	}()
	panic(1)
}

// `defer recover()` inside a function that a panicking frame deferred: it
// runs as if called from that function, so it does recover — unless another
// panic is being handled when it runs, which is the swapped order below.
// These two are Go's test/recover1.go test6 and test7. The program prints
// what happens rather than asserting it, so gc says what is right.
func deferredRecoverLate() (stillPanicking bool) {
	defer func() { stillPanicking = recover() != nil }()
	defer func() {
		defer recover()      // runs second, once mustRecover has stopped the panic
		defer mustRecover(3) // runs first
		panic(3)
	}()
	panic(2)
}

func deferredRecoverEarly() (stillPanicking bool) {
	defer func() { stillPanicking = recover() != nil }()
	defer func() {
		defer mustRecover(3) // runs second
		defer recover()      // runs first, while panic 3 is in flight: a no-op
		panic(3)
	}()
	panic(2)
}

// A recover in a deferred call of a frame that is not panicking.
func notPanicking() {
	defer func() {
		if recover() != nil {
			println("BUG: recovered without a panic")
		}
	}()
}

func main() {
	nested()
	notPanicking()
	println("late defer recover, still panicking:", deferredRecoverLate())
	println("early defer recover, still panicking:", deferredRecoverEarly())
	println("ok")
}
