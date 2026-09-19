package main

func main() {
	sum := 0
	for i := 1; i <= 100; i++ {
		sum += i
	}
	println("sum", sum)

	for i := 1; i <= 15; i++ {
		switch {
		case i%15 == 0:
			println("FizzBuzz")
		case i%3 == 0:
			println("Fizz")
		case i%5 == 0:
			println("Buzz")
		default:
			println(i)
		}
	}

	count := 0
outer:
	for i := 0; i < 10; i++ {
		for j := 0; j < 10; j++ {
			if j > i {
				continue outer
			}
			if i*j > 20 {
				break outer
			}
			count++
		}
	}
	println("count", count)

	n, steps := 27, 0
	for n != 1 {
		if n%2 == 0 {
			n /= 2
		} else {
			n = 3*n + 1
		}
		steps++
	}
	println("collatz", steps)
}
