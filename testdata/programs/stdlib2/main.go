package main

import (
	"fmt"
	"os"
	"strings"
)

type Point struct{ X, Y int }

func main() {
	fmt.Println("hello from rustygo")
	fmt.Printf("%d + %d = %d\n", 2, 3, 2+3)
	fmt.Println(Point{1, 2}, []string{"a", "b"}, map[string]int{"k": 7})
	fmt.Fprintln(os.Stderr, "this goes to stderr")
	fmt.Fprintf(os.Stdout, "%s|%v|%T\n", strings.ToUpper("mixed"), 3.25, Point{})
	if len(os.Args) > 0 {
		fmt.Println("args:", len(os.Args) > 0)
	}
	fmt.Println("env has PATH:", os.Getenv("PATH") != "")
}
