package main

func fib(n int) int {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

func even(n uint) bool {
	if n == 0 {
		return true
	}
	return odd(n - 1)
}

func odd(n uint) bool {
	if n == 0 {
		return false
	}
	return even(n - 1)
}

func ackermann(m, n int) int {
	switch {
	case m == 0:
		return n + 1
	case n == 0:
		return ackermann(m-1, 1)
	}
	return ackermann(m-1, ackermann(m, n-1))
}

func main() {
	println(fib(25), even(10), odd(7), even(7))
	println(ackermann(2, 3))
}
