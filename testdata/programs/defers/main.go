package main

type Res struct {
	Name   string
	Closed bool
}

func (r *Res) Close() { r.Closed = true; println("closed", r.Name) }

func order() {
	for i := 0; i < 3; i++ {
		defer println("defer", i)
	}
	println("body done")
}

func args() {
	x := 1
	defer println("captured at defer time:", x) // prints 1, not 2
	x = 2
	println("x is now", x)
}

func nested(r *Res) string {
	defer r.Close()
	return "used " + r.Name
}

func viaValue() {
	f := func(tag string, n int) { println("closure defer", tag, n) }
	defer f("first", 1)
	defer f("second", 2)
	println("viaValue body")
}

func panics() {
	defer println("defer runs while panicking")
	defer func() { println("second defer too") }()
	panic("boom")
}

func loopAlloc() int {
	// Deferred arguments must survive collections until the call runs.
	total := 0
	for i := 0; i < 3; i++ {
		s := "item-" + itoa(i)
		defer func(name string) { total += len(name) }(s)
	}
	for i := 0; i < 1000; i++ {
		_ = make([]byte, 64)
	}
	return total
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func main() {
	order()
	args()
	r := &Res{Name: "file"}
	println(nested(r), r.Closed)
	viaValue()
	println("total", loopAlloc())
	println(doubled(), addsOnReturn())
	s, n := bothResults()
	println(s, n)
	panics()
}

// Deferred calls run before the caller sees a named result, so they can
// change it.
func doubled() (result int) {
	defer func() { result *= 2 }()
	result = 5
	return
}

func addsOnReturn() (n int) {
	defer func() { n++ }()
	return 10
}

func bothResults() (a string, b int) {
	defer func() { a, b = a+"!", b*3 }()
	return "hi", 2
}
