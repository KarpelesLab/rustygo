package main

// Jumps into the middle of a loop make the control-flow graph irreducible.
func irreducible(n int) int {
	i, acc := 0, 0
	if n%2 == 0 {
		goto middle
	}
top:
	acc += i
middle:
	i++
	if i < n {
		goto top
	}
	return acc
}

func main() {
	for n := 0; n < 6; n++ {
		println(n, irreducible(n))
	}
	i := 0
loop:
	if i < 3 {
		println("goto loop", i)
		i++
		goto loop
	}
}
