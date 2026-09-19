// Short string building and indexing.
package main

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
	total := 0
	for i := 0; i < 2_000_000; i++ {
		s := "item-" + itoa(i) + "!"
		total += len(s) + int(s[len(s)/2])
	}
	println(total)
}
