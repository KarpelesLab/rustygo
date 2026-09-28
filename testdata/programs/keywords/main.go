// Program keywords uses Go names that are Rust keywords — as types, methods,
// fields, functions, variables and a package — because the emitter has to
// spell each of them in Rust somewhere, and the rules are not all the same.
// Most become raw identifiers (`r#box`); a few cannot, because the name is
// also the stem of another the emitter builds from it.
package main

import kw "github.com/KarpelesLab/rustygo/testdata/programs/keywords/match"

type box struct {
	move   int
	unsafe string
	fn     func(int) int
}

// A named type whose Rust form is not a struct, so it is spelled out wherever
// it is used rather than declared once.
type dyn []box

type trait interface {
	impl() string
}

func (b *box) impl() string { return b.unsafe }

type become struct{ box }

var static = 7
var async = map[string]int{"where": 1, "yield": 2}

func loop(d dyn) int {
	total := 0
	for _, b := range d {
		total += b.move + b.fn(b.move)
	}
	return total
}

func main() {
	b := box{move: 1, unsafe: "one", fn: func(n int) int { return n * 10 }}
	c := box{move: 2, unsafe: "two", fn: func(n int) int { return n * 100 }}
	d := dyn{b, c}

	var t trait = &b
	println(t.impl(), loop(d), static)

	e := become{box{move: 3, unsafe: "three", fn: func(n int) int { return n }}}
	println(e.impl(), e.move)

	println(async["where"], async["yield"], len(async))

	// The same names again, from a package whose own name is a keyword.
	println(kw.Const(), kw.Type{Enum: 5}.Extern())

	defer func() {
		if r := recover(); r != nil {
			println("recovered:", r.(*box).unsafe)
		}
	}()
	panic(&b)
}
