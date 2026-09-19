package main

type Grid [3][3]int

func sum(a [5]int) int {
	a[0] = 1000 // the caller's copy is unaffected
	t := 0
	for _, v := range a {
		t += v
	}
	return t
}

func main() {
	a := [5]int{1, 2, 3, 4, 5}
	println(sum(a), a[0])

	b := a
	b[1] = 20
	println(a[1], b[1], a == b, a != b)

	var g Grid
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			g[i][j] = i*3 + j
		}
	}
	row := g[1]
	row[0] = 99
	println(g[1][0], row[0], g[2][2])

	type P struct{ X, Y int }
	ps := [2]P{{1, 2}, {3, 4}}
	ps[1].Y = 40
	pp := &ps[0]
	pp.X = 10
	println(ps[0].X, ps[1].Y)

	var names [3]string
	names[1] = "one"
	println(names[0] == "", names[1], len(names))
}
