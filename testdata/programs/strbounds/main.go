package main

func slice(s string, lo, hi int) string { return s[lo:hi] }

func main() {
	println(slice("hello", 1, 3))
	println(slice("hello", 3, 2))
}
