package geom

// Created counts points built through New; set in init.
var Created int

type Point struct{ X, Y int }

func init() { Created = 100 }

func (p *Point) Move(dx, dy int) {
	p.X += dx
	p.Y += dy
	Created++
}

func Dist2(a, b Point) int {
	dx, dy := b.X-a.X, b.Y-a.Y
	return dx*dx + dy*dy
}
