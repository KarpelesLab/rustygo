// The first standard-library packages compiled from the real GOROOT:
// strings, strconv, sort, errors, unicode and the generic ones — slices, maps
// and cmp — unmodified (DESIGN §8).
package main

import (
	"cmp"
	"errors"
	"maps"
	"slices"
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

	// The generic ones. `slices` measures its element type with
	// `unsafe.Sizeof`, which go/types cannot fold inside a generic function,
	// so an instantiation asks the emitter for it.
	ns := []int{3, 1, 2, 1}
	slices.Sort(ns)
	println(len(ns), ns[0], ns[3], slices.Contains(ns, 2), slices.Index(ns, 3), slices.Max(ns), slices.Min(ns))
	ys := slices.Clone(ns)
	println(slices.Equal(ns, ys), slices.IsSorted(ys))
	ys = slices.Compact(ys)
	println(len(ys), ys[0], ys[2])
	ys = slices.Insert(ys, 1, 9)
	println(len(ys), ys[1], len(slices.Delete(slices.Clone(ys), 0, 2)))
	slices.Reverse(ys)
	println(ys[0], slices.IsSortedFunc(ys, func(a, b int) int { return cmp.Compare(b, a) }))
	type pair struct{ a, b int64 }
	ps := slices.Clone(make([]pair, 3))
	println(len(ps), ps[0].a == 0)

	m := map[string]int{"a": 1, "b": 2, "c": 3}
	keys := slices.Sorted(maps.Keys(m))
	vals := slices.Sorted(maps.Values(m))
	println(len(keys), keys[0], keys[2], len(vals), vals[0], vals[2])
	m2 := maps.Clone(m)
	m2["d"] = 4
	println(len(m), len(m2), maps.Equal(m, m2), maps.Equal(m, maps.Clone(m)))
	println(cmp.Compare(1, 2), cmp.Compare(2, 2), cmp.Less("a", "b"), cmp.Or(0, 0, 7))
}
