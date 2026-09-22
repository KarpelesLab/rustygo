package main

import "fmt"

type Point struct {
	X, Y int
	Name string
}

type Temp float64

func (t Temp) String() string { return fmt.Sprintf("%.1f°C", float64(t)) }

func main() {
	println(fmt.Sprintf("%d %s %v %t", 42, "hi", 3.5, true))
	p := Point{1, 2, "origin"}
	println(fmt.Sprintf("%v | %+v | %#v | %T", p, p, p, p))
	println(fmt.Sprint("a", 1, 2, "b"), fmt.Sprintln("x", 9))
	println(fmt.Sprintf("%v %v", []int{1, 2, 3}, map[string]int{"k": 1}))
	println(fmt.Sprintf("%s %q %x %08.3f %-5d|", "s", "q", 255, 3.14159, 7))
	println(fmt.Sprintf("%v", Temp(21.5)), fmt.Sprintf("%v", &p) != "")
	err := fmt.Errorf("wrapped: %w", fmt.Errorf("inner"))
	println(err.Error())
}
