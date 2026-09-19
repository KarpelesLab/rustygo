package main

import "github.com/KarpelesLab/rustygo/testdata/programs/multipkg/geom"

var origin = geom.Point{}

func main() {
	a := geom.Point{X: 3, Y: 4}
	println(geom.Dist2(origin, a), geom.Created)
	a.Move(1, 1)
	println(a.X, a.Y, geom.Created)
}
