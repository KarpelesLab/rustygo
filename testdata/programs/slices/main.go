package main

type Point struct {
	X, Y int
	Tag  string
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

func fill(xs []int, v int) {
	for i := range xs {
		xs[i] = v
	}
}

func main() {
	// Literals, indexing, len/cap, range.
	xs := []int{1, 2, 3, 4, 5}
	println(len(xs), cap(xs), xs[0], xs[4], sum(xs))
	for i, v := range xs {
		print(i, ":", v, " ")
	}
	println()

	// Slicing shares the array; writes show through both views.
	mid := xs[1:4]
	println(len(mid), cap(mid), mid[0])
	mid[0] = 20
	println(xs[1], mid[0])
	fill(mid, 7)
	println(xs[0], xs[1], xs[2], xs[3], xs[4])

	// Three-index slicing caps the capacity.
	short := xs[1:2:3]
	println(len(short), cap(short))

	// append: in place while there is room, copying when there is not.
	a := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		a = append(a, i*i)
	}
	b := append(a, 99) // fits? cap is 4 and len is 4, so this copies
	a = append(a, -1)
	println(len(a), cap(a) >= 5, a[4], b[4], sum(a), sum(b))

	// append of a whole slice, and of a string to []byte.
	c := append([]int{1, 2}, xs...)
	println(len(c), sum(c))
	bs := append([]byte("go"), "pher"...)
	println(string(bs), len(bs))

	// copy, including overlapping ranges.
	dst := make([]int, 3)
	n := copy(dst, c)
	println(n, dst[0], dst[1], dst[2])
	ov := []int{1, 2, 3, 4, 5}
	copy(ov[1:], ov[:4])
	println(ov[0], ov[1], ov[2], ov[3], ov[4])

	// nil slices behave like empty ones, except they are nil.
	var nils []int
	println(nils == nil, len(nils), cap(nils), sum(nils))
	nils = append(nils, 1)
	println(nils == nil, len(nils), nils[0])

	// make with length and capacity; zero values.
	m := make([]string, 2, 8)
	println(len(m), cap(m), m[0] == "", m[1] == "")
	m[1] = "set"
	println(m[1])

	// Slices of structs: elements are addressable through the slice.
	ps := make([]Point, 3)
	for i := range ps {
		ps[i] = Point{X: i, Y: -i, Tag: "p"}
	}
	ps[1].X = 42
	p := &ps[2]
	p.Tag = "third"
	println(ps[1].X, ps[2].Tag, len(ps))

	// Slices of slices.
	grid := make([][]int, 3)
	for i := range grid {
		grid[i] = make([]int, 3)
		for j := range grid[i] {
			grid[i][j] = i * j
		}
	}
	println(grid[2][2], len(grid), len(grid[0]))

	// string <-> []byte <-> []rune.
	s := "héllo"
	bytes := []byte(s)
	runes := []rune(s)
	println(len(s), len(bytes), len(runes), string(bytes) == s, string(runes) == s)
	println(runes[1], bytes[1], string(runes[1:3]))

	// A slice of an array keeps the array alive and aliases it.
	arr := [4]int{9, 8, 7, 6}
	as := arr[1:3]
	as[0] = 88
	println(arr[1], len(as), cap(as))
}
