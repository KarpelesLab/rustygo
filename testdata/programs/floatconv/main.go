package main

// Out-of-range and NaN float-to-integer conversions are implementation-
// specific in Go; rustygo matches gc on each architecture.
func main() {
	var zero float64
	vals := [10]float64{1e300, -1e300, zero / zero, 1 / zero, -1 / zero, 3e9, -3e9, 1.8446744073709552e19, 9.3e18, -0.5}
	for _, v := range vals {
		println(v, int64(v), int32(v), int16(v), int8(v), int(v))
		println("  u", uint64(v), uint32(v), uint16(v), uint8(v), uint(v), uintptr(v))
	}
	var f32 float32 = 3e9
	println(int32(f32), uint32(f32), int64(f32), uint64(f32))
}
