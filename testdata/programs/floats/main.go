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
}
