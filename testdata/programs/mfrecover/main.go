// Program mfrecover is about which frames `recover` looks through.
//
// Go's rule, as gc states it in runtime.gorecover, is that there must be exactly
// one *non-wrapper* frame between the panic and the recover: the function the
// defer named. A wrapper does not count — a method value's wrapper does not, and
// neither does the pair `reflect.MakeFunc` interposes between a deferred call
// and the function the program handed it. So `recover` inside that function sees
// the panic, and one call deeper sees nothing.
//
// Each case here says how many frames of its own stand in the way. rustygo marks
// the frames that stand in for a deferred call transparent and looks straight
// through them, which is the same rule by another name; the cases that differ by
// one frame are what says the rule is the same and not merely close.
package main

import "reflect"

var noArgs = reflect.TypeOf((func())(nil))

func report(name string, v any) {
	if v != nil {
		println(name, "recovered", v.(int))
	} else {
		println(name, "recovered nothing")
	}
}

// direct: the made function is the deferred call and `recover` is in the body
// the program handed MakeFunc, which is the one shape that recovers.
func direct() {
	f := reflect.MakeFunc(noArgs, func([]reflect.Value) []reflect.Value {
		report("direct", recover())
		return nil
	}).Interface().(func())
	defer f()
	panic(1)
}

// nested: a closure stands between the defer and the made function, so there are
// two frames that count and nothing recovers.
func nested() {
	f := reflect.MakeFunc(noArgs, func([]reflect.Value) []reflect.Value {
		report("nested", recover())
		return nil
	}).Interface().(func())
	defer func() { f() }()
	panic(2)
}

// helper: `recover` sits one call below the body, which is the second frame that
// counts.
func helper() {
	f := reflect.MakeFunc(noArgs, func([]reflect.Value) []reflect.Value {
		helperRecover()
		return nil
	}).Interface().(func())
	defer f()
	panic(3)
}

func helperRecover() { report("helper", recover()) }

// madeInMade: a made function called from inside another made function's body,
// which is the shape of test16 in Go's own test/recover.go. The inner one must
// find nothing, however many wrappers are above it.
func madeInMade() {
	inner := reflect.MakeFunc(noArgs, func([]reflect.Value) []reflect.Value {
		report("inner", recover())
		return nil
	}).Interface().(func())
	outer := reflect.MakeFunc(noArgs, func([]reflect.Value) []reflect.Value {
		inner()
		return nil
	}).Interface().(func())
	defer outer()
	panic(4)
}

// methodvalue: a method value is a wrapper too, and gc looks through it.
type T struct{}

func (T) M() { report("method", recover()) }

func methodvalue() {
	var t T
	defer t.M()
	panic(5)
}

// bare: `defer recover()` has no frame of its own that counts, so it recovers
// nothing and the panic carries on.
func bare() {
	defer recover()
	panic(6)
}

// twice: a second `recover` in the same body finds the panic already gone.
func twice() {
	f := reflect.MakeFunc(noArgs, func([]reflect.Value) []reflect.Value {
		report("twice first", recover())
		report("twice again", recover())
		return nil
	}).Interface().(func())
	defer f()
	panic(7)
}

// withArgs: the same, through a signature that has something to box, so the
// arguments are what the trampoline is busy with when the panic is in flight.
func withArgs() {
	t := reflect.TypeOf((func(string, []int) int)(nil))
	f := reflect.MakeFunc(t, func(in []reflect.Value) []reflect.Value {
		report("withArgs "+in[0].String(), recover())
		return []reflect.Value{reflect.ValueOf(in[1].Len())}
	}).Interface().(func(string, []int) int)
	defer f("k", []int{1, 2, 3})
	panic(8)
}

// guard runs one case and says whether its panic got out.
func guard(name string, f func()) {
	defer func() {
		if v := recover(); v != nil {
			println(name, "escaped with", v.(int))
		} else {
			println(name, "handled")
		}
	}()
	f()
}

func main() {
	guard("direct", direct)
	guard("nested", nested)
	guard("helper", helper)
	guard("madeInMade", madeInMade)
	guard("methodvalue", methodvalue)
	guard("bare", bare)
	guard("twice", twice)
	guard("withArgs", withArgs)
}
