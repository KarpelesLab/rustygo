// The first standard-library packages compiled from the real GOROOT:
// strings, strconv, sort, errors and unicode, unmodified (DESIGN §8).
package main

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

func main() {
	s := strings.ToUpper(strings.Join([]string{"go", "rust"}, "+"))
	println(s, strings.Contains(s, "RU"), strings.Index(s, "+"))
	println(strings.Repeat("ab", 3), strings.TrimSpace("  x  "), strings.Count("cheese", "e"))
	fields := strings.Fields(" a b  c ")
	println(len(fields), fields[2], strings.HasPrefix("golang", "go"), strings.LastIndex("go gopher", "go"))
	println(strings.Replace("oink oink oink", "k", "ky", 2), strings.Title("hello"))
	parts := strings.Split("a,b,c", ",")
	println(len(parts), parts[1], strings.EqualFold("Go", "GO"))

	var b strings.Builder
	for i := 0; i < 3; i++ {
		b.WriteString("x")
		b.WriteByte('-')
	}
	println(b.String(), b.Len())

	xs := []int{5, 2, 9, 1}
	sort.Ints(xs)
	println(xs[0], xs[3], strconv.Itoa(xs[2]), strconv.Quote("a\nb"))
	names := []string{"cy", "ann", "bob"}
	sort.Strings(names)
	println(names[0], names[1], names[2], sort.SearchInts(xs, 5))

	n, err := strconv.Atoi("123")
	println(n, err == nil)
	_, err2 := strconv.Atoi("nope")
	println(err2 != nil, errors.Is(err2, strconv.ErrSyntax), err2.Error())
	f, _ := strconv.ParseFloat("2.5e3", 64)
	println(f, strconv.FormatInt(-255, 16), strconv.FormatFloat(3.14159, 'f', 2, 64))
	bv, _ := strconv.ParseBool("true")
	println(bv, strconv.Quote("héllo\x00"), strconv.QuoteToASCII("héllo"))

	e1 := errors.New("base")
	e2 := errors.Join(e1, errors.New("other"))
	println(errors.Is(e2, e1), e2.Error() == "base\nother", errors.Unwrap(e1) == nil)

	println(unicode.IsLetter('é'), unicode.ToUpper('a') == 'A', unicode.IsDigit('7'), unicode.IsSpace('\t'))
}
