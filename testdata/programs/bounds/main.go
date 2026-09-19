package main

func get(a [3]int, i int) int { return a[i] }

func main() {
	a := [3]int{1, 2, 3}
	println(get(a, 2))
	println(get(a, 5))
}
