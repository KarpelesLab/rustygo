package main

type Counter struct{ n int }

func adder() func(int) int {
	sum := 0
	return func(x int) int {
		sum += x
		return sum
	}
}

func apply(xs []int, f func(int) int) []int {
	out := make([]int, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func double(x int) int { return x * 2 }

func compose(f, g func(int) int) func(int) int {
	return func(x int) int { return f(g(x)) }
}

func (c *Counter) Inc() { c.n++ }

func makeBoth() (func(), func() int) {
	// Two closures over one variable see each other's writes.
	n := 0
	return func() { n += 10 }, func() int { return n }
}

func fibGen() func() int {
	a, b := 0, 1
	return func() int {
		r := a
		a, b = b, a+b
		return r
	}
}

func main() {
	// A closure capturing a local by reference.
	next := adder()
	println(next(1), next(2), next(3))
	other := adder()
	println(other(100), next(4))

	// Functions as values, passed and stored.
	println(sumOf(apply([]int{1, 2, 3}, double)))
	triple := func(x int) int { return x * 3 }
	println(sumOf(apply([]int{1, 2, 3}, triple)))

	// Composition: closures capturing closures.
	f := compose(double, triple)
	println(f(2), f(5))

	// Shared capture.
	bump, read := makeBoth()
	bump()
	bump()
	println(read())

	// Captured loop variables are per-iteration in Go 1.22+.
	var fs []func() int
	for i := 0; i < 3; i++ {
		fs = append(fs, func() int { return i })
	}
	println(fs[0](), fs[1](), fs[2]())

	// Generators keep several variables alive.
	g := fibGen()
	total := 0
	for i := 0; i < 10; i++ {
		total += g()
	}
	println("fib", total)

	// A method value carries its receiver.
	c := &Counter{}
	inc := c.Inc
	inc()
	inc()
	println(c.n)

	// nil func values.
	var nilf func(int) int
	println(nilf == nil, f != nil)

	// Recursion through a variable.
	var fact func(int) int
	fact = func(n int) int {
		if n <= 1 {
			return 1
		}
		return n * fact(n-1)
	}
	println(fact(10))

	// A closure capturing a struct field pointer.
	cc := Counter{n: 5}
	p := &cc.n
	addTo := func(d int) { *p += d }
	addTo(3)
	addTo(4)
	println(cc.n)
}

func sumOf(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}
