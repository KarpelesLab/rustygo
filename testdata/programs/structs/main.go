package main

type Point struct{ X, Y int }

type Rect struct {
	Min, Max Point
	Name     string
}

func (p Point) Add(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }

func (p *Point) Scale(k int) {
	p.X *= k
	p.Y *= k
}

func (r Rect) Area() int { return (r.Max.X - r.Min.X) * (r.Max.Y - r.Min.Y) }

func (r *Rect) Grow(d int) {
	r.Min.X -= d
	r.Min.Y -= d
	r.Max.X += d
	r.Max.Y += d
}

func main() {
	p := Point{1, 2}
	q := p
	q.X = 10
	println(p.X, p.Y, q.X, q.Y)

	p.Scale(3)
	println(p.X, p.Y)

	s := p.Add(Point{Y: 5})
	println(s.X, s.Y, s == Point{3, 11}, s != p)

	r := Rect{Min: Point{0, 0}, Max: Point{4, 3}, Name: "box"}
	println(r.Name, r.Area())
	r.Grow(1)
	println(r.Area(), r.Min.X, r.Max.Y)

	rp := &r
	rp.Max.X = 100
	c := *rp
	c.Name = "copy"
	println(r.Max.X, r.Name, c.Name, c.Max.X)

	var z Rect
	println(z.Name == "", z.Min.X, z.Area())
}
