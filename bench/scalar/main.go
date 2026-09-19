// Integer arithmetic in a tight loop: xorshift plus a modulus.
package main

func main() {
	var x uint64 = 88172645463325252
	var acc uint64
	for i := 0; i < 400_000_000; i++ {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		acc += x % 1000
	}
	println(acc)
}
