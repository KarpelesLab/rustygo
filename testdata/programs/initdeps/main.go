// Package-level variables read only by other initializers: the emitter skips
// building variables nothing reads, and must not skip these.
package main

import "github.com/KarpelesLab/rustygo/testdata/programs/initdeps/dep"

func nanValue() float64 {
	var zero float64
	return zero / zero
}

// nan is read only by the table's initializer below.
var nan = nanValue()

// Read by main.
var table = []bool{nan == nan, nan != nan, nan < 1}

// Reads another package's variable, in an initializer only.
var derived = dep.Base + 2

// Read by main, built from derived.
var answer = [2]int{derived, derived * 2}

func main() {
	println(table[0], table[1], table[2])
	println(answer[0], answer[1])
}
