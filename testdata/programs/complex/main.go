package main

type Signal complex128

func mag2(z complex128) float64 { return real(z)*real(z) + imag(z)*imag(z) }

func mandelbrotSteps(c complex128, limit int) int {
	z := complex(0, 0)
	for i := 0; i < limit; i++ {
		z = z*z + c
		if mag2(z) > 4 {
			return i
		}
	}
	return limit
}

func main() {
	a := complex(1, 2)
	b := 3 + 4i
	println(real(a), imag(a), real(b), imag(b))
	println(real(a+b), imag(a+b), real(a-b), imag(a-b))
	println(real(a*b), imag(a*b))
	q := a / b
	println(real(q), imag(q))
	println(a == complex(1, 2), a == b, a != b)

	// The whole value, printed as gc prints it.
	println(a, b, a*b, -a)

	// complex64 keeps float32 precision.
	var c64 complex64 = complex(0.1, 0.2)
	println(c64, real(c64), complex128(c64) == complex(float64(float32(0.1)), float64(float32(0.2))))
	println(complex64(b) == complex64(complex(3, 4)))

	// Division edge cases: gc's algorithm, not the naive formula.
	var zero float64
	inf := 1 / zero
	cz := complex(zero, zero)
	println(real(complex(1, 1) / cz))
	println(real(complex(1, 1)/complex(inf, 0)), imag(complex(1, 1)/complex(inf, 0)))
	nan := zero / zero
	println(real(complex(nan, nan) / complex(1, 1)))

	// Named complex types and conversions.
	var s Signal = complex(2, -1)
	println(real(complex128(s)), imag(complex128(s)))

	// Something that actually computes.
	steps := 0
	for i := -8; i < 8; i++ {
		for j := -8; j < 8; j++ {
			steps += mandelbrotSteps(complex(float64(i)/8, float64(j)/8), 20)
		}
	}
	println("mandelbrot", steps)

	// Complex values in slices, maps and interfaces.
	xs := []complex128{a, b, q}
	println(len(xs), real(xs[2]))
	m := map[complex128]string{a: "a", b: "b"}
	println(m[a], m[complex(3, 4)], len(m))
	var any1 any = a
	z, ok := any1.(complex128)
	println(ok, real(z))
}
