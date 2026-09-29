package main

func main() {
	x, y := 1.5, 0.25
	println(x+y, x-y, x*y, x/y)

	var zero float64
	println(1/zero, -1/zero, zero/zero == zero/zero, -zero)
	nan := zero / zero
	println(nan < 1, nan > 1, nan != nan)

	var f32 float32 = 0.1
	println(f32, float64(f32), f32*3)

	f1, f2 := 3.99, -3.99
	println(int(f1), int(f2), int64(1e18), float64(1<<53+1))
	i := 7
	println(float64(i)/2, float32(i)/3)

	sum := 0.0
	for k := 0; k < 10; k++ {
		sum += 0.1
	}
	println(sum, sum == 1.0)
	println(1e21, 1e20, 123456.0, 1234567.0, 0.0001, 0.00001, 5e-324)

	// `min` and `max` on floats are not Rust's: a NaN operand wins, and
	// between the two zeros `min` gives the negative one.
	inf := 1 / zero
	// A real negative zero: the constant -0.0 is +0 in Go, so it has to be
	// computed.
	a, b, neg := 1.5, 2.5, -zero
	println(min(a, b), max(a, b), min(a, b, -3.0), max(a, b, 9.5))
	println(min(nan, a) == min(nan, a), max(a, nan) == max(a, nan), min(a, inf), max(a, inf))
	println(min(zero, neg), max(zero, neg), 1/min(zero, neg), 1/max(zero, neg))
	var f32a, f32b float32 = 1.5, 0.5
	println(min(f32a, f32b), max(f32a, f32b))
	type celsius float64
	println(float64(min(celsius(3), celsius(1))), float64(max(celsius(3), celsius(1))))
}
