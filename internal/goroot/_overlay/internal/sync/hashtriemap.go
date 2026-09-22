// Copyright 2026 Karpelès Lab Inc. MIT license.

package sync

// HashTrieMap is rustygo's stand-in for gc's, which is a lock-free hash trie
// built on gc's own type descriptors (it reads a map type's hasher and
// equality straight out of `abi.Type`). rustygo's descriptors are its own, so
// that cannot work here; this is a map behind the package's Mutex, which has
// the same behaviour for one goroutine and is correct once M2 makes Mutex
// block.
//
// It backs sync.Map, sync.WaitGroup's bubbles and unique.Handle.
type HashTrieMap[K comparable, V any] struct {
	mu Mutex
	m  map[K]V
}

func (ht *HashTrieMap[K, V]) init() {
	if ht.m == nil {
		ht.m = make(map[K]V)
	}
}

func (ht *HashTrieMap[K, V]) Load(key K) (value V, ok bool) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	value, ok = ht.m[key]
	return
}

func (ht *HashTrieMap[K, V]) LoadOrStore(key K, value V) (result V, loaded bool) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.init()
	if old, ok := ht.m[key]; ok {
		return old, true
	}
	ht.m[key] = value
	return value, false
}

func (ht *HashTrieMap[K, V]) Store(key K, new V) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.init()
	ht.m[key] = new
}

func (ht *HashTrieMap[K, V]) Swap(key K, new V) (previous V, loaded bool) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.init()
	previous, loaded = ht.m[key]
	ht.m[key] = new
	return
}

func (ht *HashTrieMap[K, V]) CompareAndSwap(key K, old, new V) (swapped bool) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.init()
	cur, ok := ht.m[key]
	if !ok || !equalValues(cur, old) {
		return false
	}
	ht.m[key] = new
	return true
}

func (ht *HashTrieMap[K, V]) LoadAndDelete(key K) (value V, loaded bool) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	value, loaded = ht.m[key]
	if loaded {
		delete(ht.m, key)
	}
	return
}

func (ht *HashTrieMap[K, V]) Delete(key K) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	delete(ht.m, key)
}

func (ht *HashTrieMap[K, V]) CompareAndDelete(key K, old V) (deleted bool) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	cur, ok := ht.m[key]
	if !ok || !equalValues(cur, old) {
		return false
	}
	delete(ht.m, key)
	return true
}

func (ht *HashTrieMap[K, V]) All() func(yield func(K, V) bool) {
	return ht.Range
}

// Range visits a snapshot, so a yield that stores or deletes cannot disturb
// the iteration.
func (ht *HashTrieMap[K, V]) Range(yield func(K, V) bool) {
	ht.mu.Lock()
	keys := make([]K, 0, len(ht.m))
	vals := make([]V, 0, len(ht.m))
	for k, v := range ht.m {
		keys = append(keys, k)
		vals = append(vals, v)
	}
	ht.mu.Unlock()
	for i, k := range keys {
		if !yield(k, vals[i]) {
			return
		}
	}
}

func (ht *HashTrieMap[K, V]) Clear() {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.m = nil
}

// equalValues compares two values of a type that need not be comparable:
// gc's version consults the type's equality function, and this asks the
// values themselves, which is only possible through an interface.
func equalValues[V any](a, b V) bool {
	return any(a) == any(b)
}
