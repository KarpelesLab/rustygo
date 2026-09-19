package main

func main() {
	var a, b int = 7, -3
	println(a+b, a-b, a*b, a/b, a%b, -a/b, -a%b)

	var m, n int64 = -9223372036854775808, -1
	println(m/n, m%n, m*n, m-1)

	var u8 uint8 = 250
	u8 += 10
	var i8 int8 = 127
	i8++
	var u uint
	u--
	println(u8, i8, u)

	println(a&b, a|b, a^b, a&^b, ^a, ^u8)

	var s uint = 70
	println(1<<s, a<<3, a>>1, b>>1, b>>70, uint32(1)<<31)

	var x uint32 = 0xdeadbeef
	println(x>>4, x<<4, int32(x), uint16(x), int8(x), uint64(int8(x)))

	var c int16 = -32768
	println(-c, c/-1, c*c)
}
