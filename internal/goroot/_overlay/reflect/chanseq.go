// Copyright 2026 Karpelès Lab Inc. MIT license.

package reflect

import (
	"iter"
	"unsafe"
)

// A channel type's direction, and the ranges a type can be ranged over. Both
// are questions about a type rather than about a value, and the standard
// library asks them while registering types: `encoding/gob` and `net/rpc` ask
// which way a channel points before refusing to encode one, `text/template`
// asks whether a value can be ranged over before it writes a `range` block, and
// `go-cmp` asks both.

// ChanDir is a channel type's direction.
type ChanDir int

const (
	RecvDir ChanDir             = 1 << iota // <-chan
	SendDir                                 // chan<-
	BothDir = RecvDir | SendDir             // chan
)

func (d ChanDir) String() string {
	switch d {
	case RecvDir:
		return "<-chan"
	case SendDir:
		return "chan<-"
	case BothDir:
		return "chan"
	}
	return "ChanDir(" + itoa(int(d)) + ")"
}

// ChanDir returns a channel type's direction.
func (t rtype) ChanDir() ChanDir {
	if t.Kind() != Chan {
		panic("reflect: ChanDir of non-chan type " + t.String())
	}
	return ChanDir(descChanDir(t.d))
}

// CanSeq reports whether `for range v` is allowed over a value of this type —
// Go's one-variable form, which counts over an integer and iterates over
// everything else that has elements.
func (t rtype) CanSeq() bool {
	switch t.Kind() {
	case Int, Int8, Int16, Int32, Int64, Uint, Uint8, Uint16, Uint32, Uint64, Uintptr,
		Array, Slice, Chan, String, Map:
		return true
	case Func:
		return canRangeFunc(t)
	case Pointer:
		return t.Elem().Kind() == Array
	}
	return false
}

// CanSeq2 reports whether `for k, v := range` is allowed — the two-variable
// form, which an integer and a channel do not have.
func (t rtype) CanSeq2() bool {
	switch t.Kind() {
	case Array, Slice, String, Map:
		return true
	case Func:
		return canRangeFunc2(t)
	case Pointer:
		return t.Elem().Kind() == Array
	}
	return false
}

// canRangeFunc is Go's rule for a func a `range` may call: it takes one
// argument, that argument is a `func(V) bool`, and it returns nothing.
func canRangeFunc(t Type) bool {
	if t.NumIn() != 1 || t.NumOut() != 0 {
		return false
	}
	yield := t.In(0)
	return yield.Kind() == Func && yield.NumIn() == 1 && yield.NumOut() == 1 &&
		yield.Out(0).Kind() == Bool
}

// canRangeFunc2 is the same for the two-variable form, where the yield takes
// two arguments.
func canRangeFunc2(t Type) bool {
	if t.NumIn() != 1 || t.NumOut() != 0 {
		return false
	}
	yield := t.In(0)
	return yield.Kind() == Func && yield.NumIn() == 2 && yield.NumOut() == 1 &&
		yield.Out(0).Kind() == Bool
}

// Seq returns an iterator over v, which is `for range v` as a value.
//
// An `iter.Seq` is a func taking a yield, so producing one needs no coroutine
// and nothing from `iter` but the name of the type.
func (v Value) Seq() iter.Seq[Value] {
	if !v.Type().CanSeq() {
		panic("reflect: " + v.Type().String() + " cannot be ranged over")
	}
	switch v.Kind() {
	case Int, Int8, Int16, Int32, Int64:
		// Each count is a value of v's own type, which is what `New` of that
		// type and a write into it produces.
		return func(yield func(Value) bool) {
			for i := int64(0); i < v.Int(); i++ {
				x := New(v.Type()).Elem()
				x.SetInt(i)
				if !yield(x) {
					return
				}
			}
		}
	case Uint, Uint8, Uint16, Uint32, Uint64, Uintptr:
		return func(yield func(Value) bool) {
			for i := uint64(0); i < v.Uint(); i++ {
				x := New(v.Type()).Elem()
				x.SetUint(i)
				if !yield(x) {
					return
				}
			}
		}
	case Chan:
		return func(yield func(Value) bool) {
			for {
				x, ok := v.Recv()
				if !ok || !yield(x) {
					return
				}
			}
		}
	case Map:
		return func(yield func(Value) bool) {
			for _, k := range v.MapKeys() {
				if !yield(k) {
					return
				}
			}
		}
	case String:
		return func(yield func(Value) bool) {
			for _, r := range v.String() {
				if !yield(ValueOf(r)) {
					return
				}
			}
		}
	case Func:
		return func(yield func(Value) bool) {
			v.Call([]Value{ValueOf(yield)})
		}
	}
	// An array, a slice, or a pointer to an array.
	return func(yield func(Value) bool) {
		s := v
		if v.Kind() == Pointer {
			s = v.Elem()
		}
		for i := 0; i < s.Len(); i++ {
			if !yield(ValueOf(i)) {
				return
			}
		}
	}
}

// Seq2 is the two-variable form: the index or key, and the element.
func (v Value) Seq2() iter.Seq2[Value, Value] {
	if !v.Type().CanSeq2() {
		panic("reflect: " + v.Type().String() + " cannot be ranged over")
	}
	switch v.Kind() {
	case Map:
		return func(yield func(Value, Value) bool) {
			it := v.MapRange()
			for it.Next() {
				if !yield(it.Key(), it.Value()) {
					return
				}
			}
		}
	case String:
		return func(yield func(Value, Value) bool) {
			for i, r := range v.String() {
				if !yield(ValueOf(i), ValueOf(r)) {
					return
				}
			}
		}
	case Func:
		return func(yield func(Value, Value) bool) {
			v.Call([]Value{ValueOf(yield)})
		}
	}
	// An array, a slice, or a pointer to an array.
	return func(yield func(Value, Value) bool) {
		s := v
		if v.Kind() == Pointer {
			s = v.Elem()
		}
		for i := 0; i < s.Len(); i++ {
			if !yield(ValueOf(i), s.Index(i)) {
				return
			}
		}
	}
}

// Answered by the runtime from its type descriptors.
func descChanDir(d unsafe.Pointer) int64

// Receiving from and sending to a channel through reflection. A channel's
// accessors are generated for its own type, like a map's, because a channel is a
// runtime object and not memory reflection can read.

// Recv receives from the channel v, blocking until a value arrives. `ok` is
// false once a closed channel has been drained.
func (v Value) Recv() (Value, bool) {
	v.mustBe(Chan, "Recv")
	if v.Type().ChanDir() == SendDir {
		panic("reflect: Recv on a send-only channel")
	}
	return v.recv(true)
}

// TryRecv receives without blocking. It returns the zero Value and false when
// nothing was ready, and the zero Value and true when the channel is closed.
func (v Value) TryRecv() (Value, bool) {
	v.mustBe(Chan, "TryRecv")
	if v.Type().ChanDir() == SendDir {
		panic("reflect: TryRecv on a send-only channel")
	}
	return v.recv(false)
}

func (v Value) recv(block bool) (Value, bool) {
	if v.IsNil() && block {
		// A receive on a nil channel blocks for ever, which this reports the
		// way the runtime does rather than pretending to.
		panic("reflect: Recv on a nil channel")
	}
	if v.IsNil() {
		return Value{}, false
	}
	p, ok := chanRecv(v.d, v.p, block)
	if p == nil {
		return Value{}, false
	}
	return Value{d: descElem(v.d), p: p, sticky: v.ro()}, ok
}

// Send sends x on the channel v, blocking until it is taken.
func (v Value) Send(x Value) {
	v.mustBe(Chan, "Send")
	if v.Type().ChanDir() == RecvDir {
		panic("reflect: Send on a receive-only channel")
	}
	if x.d != descElem(v.d) {
		panic("reflect: Send of a " + x.Type().String() + " on " + v.Type().String())
	}
	chanSend(v.d, v.p, x.p, true)
}

// TrySend sends without blocking, reporting whether it went.
func (v Value) TrySend(x Value) bool {
	v.mustBe(Chan, "TrySend")
	if v.Type().ChanDir() == RecvDir {
		panic("reflect: TrySend on a receive-only channel")
	}
	if x.d != descElem(v.d) {
		panic("reflect: TrySend of a " + x.Type().String() + " on " + v.Type().String())
	}
	if v.IsNil() {
		return false
	}
	return chanSend(v.d, v.p, x.p, false)
}

// Close closes the channel v.
func (v Value) Close() {
	v.mustBe(Chan, "Close")
	if v.Type().ChanDir() == RecvDir {
		panic("reflect: Close of a receive-only channel")
	}
	chanClose(v.d, v.p)
}

func chanRecv(d, ch unsafe.Pointer, block bool) (unsafe.Pointer, bool)
func chanSend(d, ch, v unsafe.Pointer, block bool) bool
func chanLen(d, ch unsafe.Pointer) int64
func chanCap(d, ch unsafe.Pointer) int64
func chanClose(d, ch unsafe.Pointer)
