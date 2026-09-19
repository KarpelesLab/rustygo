package main

func divmod(a, b int) (int, int) { return a / b, a % b }

func minmax(a, b, c int) (lo, hi int) {
	lo, hi = a, a
	for _, v := range [2]int{b, c} {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return
}

func triple() (int, string, bool) { return 1, "two", true }

func main() {
	q, r := divmod(17, 5)
	println(q, r)
	lo, hi := minmax(3, -1, 9)
	println(lo, hi)
	n, s, ok := triple()
	println(n, s, ok)
	_, s2, _ := triple()
	println(s2)
	a, b := 1, 2
	a, b = b, a
	println(a, b)
}
