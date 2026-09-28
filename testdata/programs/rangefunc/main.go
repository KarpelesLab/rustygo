// Program rangefunc iterates over functions: `for x := range f` where f is a
// `func(yield func(T) bool)`. go/ssa turns the loop body into a closure the
// iterator calls, so `break`, `continue`, `return` and `defer` inside the body
// all have to mean what they would in a loop — a `defer` in particular belongs
// to the function containing the `range`, not to the closure.
package main

func two(yield func(int) bool) {
	for i := 1; i <= 4; i++ {
		if !yield(i) {
			println("  iterator stopped at", i)
			return
		}
	}
	println("  iterator ran out")
}

func pairs(yield func(string, int) bool) {
	for i, s := range []string{"a", "b", "c"} {
		if !yield(s, i) {
			return
		}
	}
}

func plain() {
	for x := range two {
		print(x, " ")
	}
	println()
	for k, v := range pairs {
		print(k, "=", v, " ")
	}
	println()
}

func breaks() {
	for x := range two {
		if x == 3 {
			break
		}
		if x == 1 {
			continue
		}
		print(x, " ")
	}
	println()
}

func returns() int {
	for x := range two {
		if x == 2 {
			return x * 10
		}
	}
	return -1
}

// defers pushes onto its own defer list from inside a loop body, so all of
// them run here, after the loop and in reverse.
func defers() {
	defer println("  defers: last")
	for x := range two {
		defer println("  defers: body", x)
		if x == 2 {
			break
		}
	}
	defer println("  defers: after the loop")
	println("  defers: returning")
}

// A named result a body assigns, and a defer that changes it afterwards.
func result() (n int) {
	defer func() { n *= 2 }()
	for x := range two {
		n += x
		defer func() { n++ }()
	}
	return n
}

func recovers() (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = "recovered " + r.(string)
		}
	}()
	for x := range two {
		defer println("  recovers: deferred", x)
		if x == 2 {
			panic("from the body")
		}
	}
	return "no panic"
}

func main() {
	plain()
	breaks()
	println(returns())
	defers()
	println(result())
	println(recovers())
}
