// Comparing a byte slice as a string: the shape `bytes.Equal` is written in,
// which gc compares in place rather than copying both sides. Every operator,
// both operand positions, and the empty and nil slices, which have no bytes to
// point at.
package main

import "bytes"

func cmp(a, b []byte) {
	println(string(a) == string(b), string(a) != string(b))
	println(string(a) < string(b), string(a) <= string(b))
	println(string(a) > string(b), string(a) >= string(b))
}

func lit(a []byte) {
	println(string(a) == "abc", "abc" == string(a))
	println(string(a) < "abc", "abc" < string(a))
	println(string(a) >= "abc", "abc" >= string(a))
}

// The conversion is used twice, so it has to happen: the result is a string
// that outlives the comparison.
func kept(a []byte) string {
	s := string(a)
	if s == "keep" {
		return s + "!"
	}
	return s
}

func which(b []byte) string {
	switch string(b) {
	case "GET":
		return "get"
	case "POST":
		return "post"
	}
	return "other"
}

func main() {
	cmp([]byte("abc"), []byte("abc"))
	cmp([]byte("abc"), []byte("abd"))
	cmp([]byte("abc"), []byte("ab"))
	cmp([]byte("ab"), []byte("abc"))
	cmp(nil, nil)
	cmp(nil, []byte(""))
	cmp([]byte{}, []byte("a"))
	cmp([]byte{0}, []byte{0, 0})
	cmp([]byte{0xff}, []byte{0x7f})

	lit(nil)
	lit([]byte("abc"))
	lit([]byte("abd"))
	lit([]byte("ab"))

	println(kept([]byte("keep")), kept([]byte("drop")))
	println(which([]byte("GET")), which([]byte("POST")), which([]byte("PUT")))

	// The same value on both sides, which still needs one copy.
	a := []byte("xy")
	println(string(a) == string(a))

	// And what the whole thing is for.
	println(bytes.Equal([]byte("hello"), []byte("hello")))
	println(bytes.Equal([]byte("hello"), []byte("world")))
	println(bytes.Index([]byte("the quick brown fox"), []byte("brown")))
	println(bytes.LastIndex([]byte("abcabcabc"), []byte("abc")))
	println(bytes.Compare([]byte("a"), []byte("b")))
}
