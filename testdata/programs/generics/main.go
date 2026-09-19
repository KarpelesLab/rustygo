package main

type Number interface {
	~int | ~int64 | ~float64
}

func Max[T Number](a, b T) T {
	if a > b {
		return a
	}
	return b
}

func Sum[T Number](xs [4]T) T {
	var s T
	for _, x := range xs {
		s += x
	}
	return s
}

type Pair[A, B any] struct {
	First  A
	Second B
}

func MakePair[A, B any](a A, b B) Pair[A, B] { return Pair[A, B]{a, b} }

type Meters int

func main() {
	println(Max(3, 7), Max(2.5, -1.0), Max(Meters(4), Meters(9)))
	println(Sum([4]int{1, 2, 3, 4}), Sum([4]float64{0.5, 0.25, 0.125, 0.125}))
	p := MakePair("answer", 42)
	println(p.First, p.Second)
	q := MakePair(true, 1.5)
	println(q.First, q.Second)
}
