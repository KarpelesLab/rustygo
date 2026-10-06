// Program recvalues uses the named types whose cycle runs through their value
// form rather than through storage:
//
//	type stateFn func(*lexer) stateFn          // text/template/parse's lexer
//	type recursiveMap map[string]recursiveMap  // encoding/gob's depth limit
//	type loop chan loop
//
// None of them has a Rust type that can be an expansion, so each gets a struct
// with a name of its own, wrapping the plain func value, map or channel. That
// works because none of those three has a layout that depends on what is inside
// it: a func value is a code address and an environment, and a map or a channel
// is one pointer to its object.
//
// What this exercises is the boundary. `stateFn` and `func(*lexer) stateFn` are
// two Rust types now, and so are `recursiveMap` and `map[string]recursiveMap`,
// so a value crossing between them is wrapped or unwrapped: a function
// returning itself by name, a closure assigned to one, a variable whose
// inferred type is the unnamed signature, a map made under its own name and
// reached through the unnamed one, the zero values, and what reflection says
// about each.
package main

import (
	"fmt"
	"reflect"
	"sort"
)

type lexer struct {
	in  string
	pos int
	out []string
}

// stateFn names the state after this one, and is its own result type.
type stateFn func(*lexer) stateFn

// recursiveMap is its own element type.
type recursiveMap map[string]recursiveMap

// loop is its own element type too, through a channel.
type loop chan loop

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

func keys(m recursiveMap) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// nest builds n levels, each a map holding the one below it under "k".
func nest(n int) recursiveMap {
	m := make(recursiveMap)
	for i := 0; i < n; i++ {
		m = recursiveMap{"k": m}
	}
	return m
}

func depthOf(m recursiveMap) int {
	d := 0
	for m != nil {
		m = m["k"]
		d++
	}
	return d
}

func lookup(m recursiveMap, path ...string) bool {
	for _, p := range path {
		v, ok := m[p]
		if !ok {
			return false
		}
		m = v
	}
	return true
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

	// A map whose element type is itself, which is what `encoding/gob` nests to
	// check the decoder's depth limit.
	tree := make(recursiveMap)
	tree["a"] = recursiveMap{"b": nil, "c": make(recursiveMap)}
	tree["a"]["c"]["d"] = nil
	fmt.Println(len(tree), len(tree["a"]), len(tree["a"]["c"]), keys(tree["a"]))
	fmt.Println(depthOf(nest(40)), lookup(tree, "a", "c", "d"), lookup(tree, "a", "z"))

	// Reached through the unnamed type, so the crossing goes the other way.
	var plain map[string]recursiveMap = tree
	plain["e"] = nil
	fmt.Println(len(tree), tree["e"] == nil, keys(tree))
	delete(tree, "e")

	var nilMap recursiveMap
	fmt.Println(nilMap == nil, len(nilMap), nilMap["x"] == nil)

	mt := reflect.TypeOf(tree)
	fmt.Println(mt, mt.Kind(), mt.Key(), mt.Elem() == mt, reflect.ValueOf(tree).Len())
	var anyMap any = tree
	fmt.Println(fmt.Sprintf("%T", anyMap), len(anyMap.(recursiveMap)))

	// A channel of itself, which a channel's one word closes the same way.
	ch := make(loop, 1)
	inner := make(loop, 1)
	ch <- inner
	got := <-ch
	fmt.Println(got == inner, got != ch, len(ch), cap(ch))
	var nilCh loop
	fmt.Println(nilCh == nil)
	ct := reflect.TypeOf(ch)
	fmt.Println(ct, ct.Kind(), ct.Elem() == ct, ct.ChanDir())
}
