// Program deferpanic is about a frame that was returning normally when one of
// its own deferred calls panicked. Go treats the frame as a panicking one from
// that moment: the rest of its deferred calls run as a panicking frame's do,
// and one of them may recover the panic — after which the frame returns
// normally after all, with whatever its deferred calls made of its results.
package main

// try is Go's own example (test/recover.go): the deferred g may panic, and the
// deferred closure below it recovers and puts the value in the result.
func try(g func(), deflt any) (x any) {
	defer func() {
		if v := recover(); v != nil {
			x = v
		}
	}()
	defer g()
	return deflt
}

// Nothing here recovers, so the panic carries on out.
func unrecovered() {
	defer func() { println("  still runs") }()
	defer func() { panic("from a deferred call") }()
	println("  returning normally")
}

// Two deferred calls panic, one after the other. The last one wins, and every
// deferred call still runs.
func twice() (x string) {
	defer func() { x = "recovered " + text(recover()) }()
	defer func() { panic("second") }()
	defer func() { println("  between the two") }()
	defer func() { panic("first") }()
	return "not this"
}

// A deferred call panics while the frame is already panicking.
func already() (x string) {
	defer func() { x = "recovered " + text(recover()) }()
	defer func() { panic("from the defer") }()
	panic("from the body")
}

// Recovery in the middle: what is left of the list runs on the normal path,
// and a later panic from one of those is a new one again.
func middle() (x string) {
	defer func() { x = "outer got " + text(recover()) }()
	defer func() { panic("again") }()
	defer func() { println("  inner recovered", text(recover())) }()
	defer func() { panic("inner") }()
	return "unset"
}

// A deferred call that panics after the one below it recovered: the frame is
// returning normally again by then, so this is a new panic of its own.
func afterRecovery() (x string) {
	defer func() { x = "then " + text(recover()) }()
	defer func() { panic("and again") }()
	defer func() { println("  recovered", text(recover())) }()
	panic("the body's")
}

func main() {
	println(text(try(func() { panic(5) }, 55)))
	println(text(try(func() {}, "hi")))
	println(twice())
	println(already())
	println(middle())
	println(afterRecovery())

	func() {
		defer func() { println("main recovered", text(recover())) }()
		unrecovered()
	}()

	// And the whole thing again inside a goroutine.
	done := make(chan string)
	go func() {
		defer func() { done <- "goroutine got " + text(recover()) }()
		defer func() { panic("in a goroutine") }()
	}()
	println(<-done)
	println("done")
}

// text renders what recover returned, for the few kinds this program panics
// with. `fmt` is not used, so that this runs wherever goroutines do.
func text(v any) string {
	switch v := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return v
	case int:
		return digits(v)
	}
	return "?"
}

func digits(n int) string {
	if n == 0 {
		return "0"
	}
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return sign + string(b)
}
