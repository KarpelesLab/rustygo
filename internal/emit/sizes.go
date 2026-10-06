package emit

import "go/types"

// How big a value is, and where a struct's fields sit (DESIGN §7).
//
// These are gc's numbers with one correction. rustygo lays every type out as gc
// does except one: a func value is a code address and an environment, two words
// where gc's is one pointer to a funcval. A struct holding a func value
// therefore has a layout of its own, and everything after that field sits eight
// bytes further along than gc would put it.
//
// That matters because a type descriptor's field offsets are what reflection
// reads a field *at*. Given gc's offsets, `Value.Field` on a `crypto/tls.Config`
// — thirty-five fields, eleven of them funcs — reads and writes bytes belonging
// to other fields, and past the end of the object once the drift exceeds what is
// left of it. `Value.Set` copying gc's size for the struct truncates it. So a
// descriptor has to describe the struct that exists rather than the one gc would
// have made, which is what these do.
//
// A type with no func value anywhere inside it is handed straight to gc, so the
// numbers for it are gc's own and cannot drift from them.
//
// `unsafe.Sizeof` and `Offsetof` are not fixed by this, and cannot be: go/types
// folds them to constants with gc's rules while type-checking, before any of this
// runs. So for a struct holding a func value, `unsafe.Sizeof` and
// `reflect.Type.Size` now disagree, where before they agreed with each other and
// neither described the memory. Making them agree again means making a func value
// one word, as gc has it — a pointer to a funcval, with the code address in the
// closure's environment struct so that a closure still allocates once. That is
// the honest fix and a much larger one: it changes every call site, how a func
// value is rooted and traced, and what a closure costs.
type rustSizes struct {
	gc   types.Sizes
	word int64
}

func newRustSizes(gc types.Sizes) rustSizes {
	return rustSizes{gc: gc, word: gc.Sizeof(types.Typ[types.UnsafePointer])}
}

func (s rustSizes) Sizeof(t types.Type) int64 {
	if !holdsFunc(t) {
		return s.gc.Sizeof(t)
	}
	switch u := t.Underlying().(type) {
	case *types.Signature:
		// A code address and an environment.
		return 2 * s.word
	case *types.Struct:
		n := u.NumFields()
		if n == 0 {
			return s.gc.Sizeof(t)
		}
		vars := structFields(u)
		offsets := s.Offsetsof(vars)
		return roundUp(offsets[n-1]+s.Sizeof(vars[n-1].Type()), s.Alignof(t))
	case *types.Array:
		if u.Len() <= 0 {
			return 0
		}
		return u.Len() * s.Sizeof(u.Elem())
	}
	return s.gc.Sizeof(t)
}

func (s rustSizes) Alignof(t types.Type) int64 {
	if !holdsFunc(t) {
		return s.gc.Alignof(t)
	}
	switch u := t.Underlying().(type) {
	case *types.Signature:
		return s.word
	case *types.Struct:
		if u.NumFields() == 0 {
			return s.gc.Alignof(t)
		}
		max := int64(1)
		for i := 0; i < u.NumFields(); i++ {
			if a := s.Alignof(u.Field(i).Type()); a > max {
				max = a
			}
		}
		return max
	case *types.Array:
		return s.Alignof(u.Elem())
	}
	return s.gc.Alignof(t)
}

func (s rustSizes) Offsetsof(fields []*types.Var) []int64 {
	any := false
	for _, f := range fields {
		if holdsFunc(f.Type()) {
			any = true
			break
		}
	}
	if !any {
		return s.gc.Offsetsof(fields)
	}
	// Go's own rule, which is also Rust's for a `#[repr(C)]` struct: each field
	// starts at the next offset its alignment allows.
	offsets := make([]int64, len(fields))
	var at int64
	for i, f := range fields {
		at = roundUp(at, s.Alignof(f.Type()))
		offsets[i] = at
		at += s.Sizeof(f.Type())
	}
	return offsets
}

// holdsFunc reports whether a value of t has a func value in it, which is the
// only thing rustygo lays out differently from gc.
//
// Only what a value holds *in itself* counts: a pointer, slice, map, channel or
// interface is one or more words whatever it refers to, so the walk stops there,
// which is also why it cannot loop on a type that contains itself.
func holdsFunc(t types.Type) bool {
	switch u := t.Underlying().(type) {
	case *types.Signature:
		return true
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if holdsFunc(u.Field(i).Type()) {
				return true
			}
		}
	case *types.Array:
		return holdsFunc(u.Elem())
	case *types.Tuple:
		for i := 0; i < u.Len(); i++ {
			if holdsFunc(u.At(i).Type()) {
				return true
			}
		}
	}
	return false
}

// structFields is a struct's fields as Offsetsof wants them.
func structFields(st *types.Struct) []*types.Var {
	vars := make([]*types.Var, st.NumFields())
	for i := range vars {
		vars[i] = st.Field(i)
	}
	return vars
}

// roundUp is n rounded up to a multiple of a.
func roundUp(n, a int64) int64 {
	if a <= 1 {
		return n
	}
	return (n + a - 1) / a * a
}
