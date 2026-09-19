package main

func classify(n int) string {
	switch {
	case n < 0:
		return "negative"
	case n == 0:
		return "zero"
	case n < 10:
		return "small"
	}
	return "large"
}

func grade(score int) string {
	switch score / 10 {
	case 10, 9:
		return "A"
	case 8:
		return "B"
	case 7:
		return "C"
	default:
		return "F"
	}
}

func fall(n int) int {
	r := 0
	switch n {
	case 1:
		r += 1
		fallthrough
	case 2:
		r += 10
		fallthrough
	case 3:
		r += 100
	case 4:
		r += 1000
	}
	return r
}

func main() {
	for _, n := range [4]int{-5, 0, 7, 42} {
		println(n, classify(n))
	}
	println(grade(95), grade(81), grade(70), grade(12))
	println(fall(1), fall(2), fall(3), fall(4), fall(5))
	switch s := "go"; s {
	case "rust":
		println("rust")
	case "go":
		println("go!")
	}
}
