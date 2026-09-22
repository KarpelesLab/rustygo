// What a recovered runtime error is: gc's message, and a value that
// satisfies both error and runtime.Error.
package main

import (
	"runtime"
	"strings"
)

func check(name string, want string, f func()) {
	defer func() {
		v := recover()
		if v == nil {
			println("BUG:", name, "did not panic")
			return
		}
		err, ok := v.(runtime.Error)
		if !ok {
			println("BUG:", name, "panicked, but not with a runtime.Error")
			return
		}
		if !strings.Contains(err.Error(), want) {
			println("BUG:", name, "said", err.Error(), "want", want)
		}
	}()
	f()
}

type stringer interface{ String() string }

func main() {
	var p *int
	var m map[string]int
	var i interface{} = 1
	zero := 0
	n := -1

	check("nil deref", "invalid memory address or nil pointer dereference", func() { println(*p) })
	check("divide", "integer divide by zero", func() { println(1 / zero) })
	check("index", "index out of range [5] with length 3", func() {
		s := []int{1, 2, 3}
		println(s[zero+5])
	})
	check("nil map write", "assignment to entry in nil map", func() { m["k"] = 1 })
	check("type assert", "interface conversion: interface {} is int, not string", func() {
		println(i.(string))
	})
	check("missing method", "missing method String", func() { println(i.(stringer).String()) })

	// make's complaint names the length when the length alone is wrong, and
	// the capacity when it was written down separately.
	check("make len", "len out of range", func() { println(len(make([]int, n))) })
	check("make cap", "cap out of range", func() { println(len(make([]int, 0, n))) })

	// A struct's blank fields take part in no comparison, and a zero-sized
	// element type has a backing array the collector must still not trip on.
	type blank struct{ _, _, _ int }
	type nothing struct{}
	a, b := blank{}, blank{}
	if a != b {
		println("BUG: blank fields compared unequal")
	}
	seen := map[blank]int{a: 1}
	seen[b] = 2
	if len(seen) != 1 || seen[a] != 2 {
		println("BUG: blank fields hashed apart")
	}
	var zs []nothing
	for range 3 {
		zs = append(zs, nothing{})
	}
	runtime.GC()
	println("ok", len(zs))
}
