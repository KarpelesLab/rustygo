// Program recfunc uses a named func type that returns itself, which is the
// state-machine shape `text/template/parse`'s lexer is written around:
//
//	type stateFn func(*lexer) stateFn
//
// The Rust type for it cannot be an expansion — the result of its own code
// pointer is itself — so it is a struct with a name, wrapping the plain func
// value. A func value is a code address and an environment whatever its
// signature, and Rust recurs happily through a function pointer, so the name
// is all it takes.
//
// Crossing between `stateFn` and `func(*lexer) stateFn`, which are now two Rust
// types, is what this exercises: a function returning itself by name, a
// closure assigned to one, a variable whose inferred type is the unnamed
// signature, the zero value, and the type as reflection reports it.
package main

import (
	"fmt"
	"reflect"
)

type lexer struct {
	in  string
	pos int
	out []string
}

// stateFn names the state after this one, and is its own result type.
type stateFn func(*lexer) stateFn

func lexText(l *lexer) stateFn {
	start := l.pos
	for l.pos < len(l.in) && l.in[l.pos] != '{' {
		l.pos++
	}
	if l.pos > start {
		l.out = append(l.out, "text:"+l.in[start:l.pos])
	}
	if l.pos == len(l.in) {
		return nil
	}
	return lexAction
}

func lexAction(l *lexer) stateFn {
	l.pos++ // past the '{'
	start := l.pos
	for l.pos < len(l.in) && l.in[l.pos] != '}' {
		l.pos++
	}
	l.out = append(l.out, "action:"+l.in[start:l.pos])
	if l.pos < len(l.in) {
		l.pos++ // past the '}'
	}
	return lexText
}

// run drives the machine through a variable of the *named* type.
func run(in string) []string {
	l := &lexer{in: in}
	var state stateFn = lexText
	for state != nil {
		state = state(l)
	}
	return l.out
}

// runInferred drives it through one whose type go/types infers as the unnamed
// signature, so every step crosses the boundary the other way.
func runInferred(in string) []string {
	l := &lexer{in: in}
	// The inferred type is `func(*lexer) stateFn`, not `stateFn`.
	state := lexText
	for state != nil {
		state = state(l)
	}
	return l.out
}

func main() {
	fmt.Println(run("hi {name} there {x}"))
	fmt.Println(runInferred("a{b}c"))
	fmt.Println(run(""), len(run("")))

	// A closure of the named type, which keeps naming itself until it stops.
	var count int
	var tick stateFn
	tick = func(l *lexer) stateFn {
		count++
		l.out = append(l.out, fmt.Sprint("tick", count))
		if count == 3 {
			return nil
		}
		return tick
	}
	l := &lexer{}
	for s := tick; s != nil; {
		s = s(l)
	}
	fmt.Println(l.out, count)

	// The zero value, and a comparison with nil either way round.
	var zero stateFn
	fmt.Println(zero == nil, nil == zero, tick == nil)

	// Held in the containers a func value can be held in.
	states := []stateFn{lexText, lexAction, nil}
	byName := map[string]stateFn{"text": lexText, "action": lexAction}
	fmt.Println(len(states), states[2] == nil, byName["text"] == nil, byName["none"] == nil)

	// Through a struct field and an interface, each of which needs the type
	// descriptor as well as the Rust type.
	type machine struct {
		start stateFn
		name  string
	}
	m := machine{start: lexText, name: "lex"}
	fmt.Println(m.name, m.start == nil)
	var any1 any = m.start
	fmt.Println(fmt.Sprintf("%T", any1), any1.(stateFn) != nil)

	// What reflection says: a func of one argument returning its own type.
	t := reflect.TypeOf(zero)
	fmt.Println(t, t.Kind(), t.NumIn(), t.NumOut(), t.In(0), t.Out(0) == t)
	fmt.Println(reflect.ValueOf(lexText).Kind(), reflect.ValueOf(zero).IsNil())
}
