// Struct-field updates through pointers into a global array.
package main

type Particle struct{ X, Y, VX, VY int }

var ps [1024]Particle

func main() {
	for i := range ps {
		ps[i] = Particle{i, -i, i%7 - 3, i%5 - 2}
	}
	for step := 0; step < 200_000; step++ {
		for i := range ps {
			p := &ps[i]
			p.X += p.VX
			p.Y += p.VY
			if p.X < -1000 || p.X > 1000 {
				p.VX = -p.VX
			}
			if p.Y < -1000 || p.Y > 1000 {
				p.VY = -p.VY
			}
		}
	}
	sum := 0
	for i := range ps {
		sum += ps[i].X ^ ps[i].Y
	}
	println(sum)
}
