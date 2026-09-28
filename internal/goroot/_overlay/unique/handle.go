// Copyright 2026 Karpelès Lab Inc. MIT license.

// Package unique provides facilities for canonicalizing ("interning")
// comparable values.
//
// rustygo's replacement. gc's version reads the type's own descriptor through
// internal/abi — its size, and the bitmap saying which words are pointers, so
// that a value holding a string can be cloned into memory the map owns. Those
// descriptors are gc's, and rustygo has its own shape for type information
// (DESIGN §6), so the interning table here is an ordinary map from the value,
// boxed, to the one canonical copy of it.
//
// What is lost is collection: gc holds its canonical values weakly and drops
// the ones nothing refers to any more. rustygo's collector cannot yet clear a
// reference when its object dies, so a value interned here is kept for the
// life of the program. Programs intern a bounded set of values — net/netip
// interns address zones — so this is a bounded cost, not a leak that grows
// with the work done.
package unique

import "sync"

// A Handle is a globally unique identity for some value of type T.
//
// Two handles compare equal exactly if the values used to create them would
// have compared equal. The zero Handle is not a valid handle.
//
// The layout is gc's: a pointer to the canonical value.
type Handle[T comparable] struct {
	value *T
}

// Value returns a shallow copy of the value pointed to by the handle.
func (h Handle[T]) Value() T {
	return *h.value
}

var (
	mu        sync.Mutex
	canonical = map[any]any{}
)

// Make returns a globally unique handle for a value of type T. Handles are
// equal if and only if the values used to produce them are equal.
func Make[T comparable](value T) Handle[T] {
	mu.Lock()
	defer mu.Unlock()
	// Boxing the value makes one map serve every instantiation: two equal
	// values of the same type box to equal interface values, and values of
	// different types are never equal.
	if p, ok := canonical[value]; ok {
		return Handle[T]{p.(*T)}
	}
	p := new(T)
	*p = value
	canonical[value] = p
	return Handle[T]{p}
}
