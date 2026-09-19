package main

type Weekday int

const (
	Sunday Weekday = iota
	Monday
	Tuesday
)

const (
	_  = iota
	KB = 1 << (10 * iota)
	MB
	GB
)

const big = 1 << 100
const small = big >> 98

type Flags uint8

const (
	FlagA Flags = 1 << iota
	FlagB
	FlagC
)

func main() {
	println(Sunday, Monday, Tuesday)
	println(KB, MB, GB, small)
	f := FlagA | FlagC
	println(f, f&FlagB == 0, f&^FlagA)
	const s = "const" + "ant"
	println(s, len(s))
	const r = 'x'
	println(r, string(r))
}
