package main

type Name string

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	s := ""
	for n != 0 {
		d := n % 10
		if d < 0 {
			d = -d
		}
		s = string(rune('0'+d)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

func main() {
	s := "hello"
	t := s + ", " + "world"
	println(t, len(t), t[0], t[len(t)-1])
	println(t[7:], t[:5], t[3:8], t[5:5] == "")
	println("abc" < "abd", "ab" < "abc", "b" > "abc", s == "hello", s != t)

	for i, r := range "aé€😀\xff!" {
		println(i, r)
	}
	println(string(rune(65)), string(rune(0x20AC)), string(rune(-1)), len(string(rune(0xD800))))

	var n Name = "gopher"
	println(n, len(n), string(n)+"s")
	println(itoa(0), itoa(12345), itoa(-987), itoa(-9223372036854775808))

	x := ""
	for i := 0; i < 5; i++ {
		x += itoa(i)
	}
	println(x)
	println("multi\nline", "tab\tand \"quotes\"")
}
