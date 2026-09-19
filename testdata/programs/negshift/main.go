package main

func shift(x, s int) int { return x << s }

func main() {
	println(shift(1, 3))
	println(shift(1, -1))
}
