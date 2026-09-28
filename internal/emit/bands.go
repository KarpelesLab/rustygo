package emit

import (
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// Bands: which crate each piece of the program is emitted into.
//
// A program that reaches `net/http` emits 855,000 lines of Rust, and `rustc`
// cannot compile that as one crate — it was killed twice trying, once past
// 6.5 GB. Splitting is not an optimization for such a program, it is what
// makes it buildable at all, and it is also what lets the standard library
// be compiled once and reused (roadmap M3).
//
// The cut is the import graph's own shape. A package's *band* is how deep it
// sits: a package that imports nothing is band 0, and a package is one deeper
// than the deepest thing it imports. Two packages in the same band therefore
// never import each other, so a crate per band has dependencies that only
// ever point downwards — a DAG, which is what Rust requires of crates.
//
// Everything else follows from that. A function goes in its package's band,
// except a generic instantiation: `maps.Clone[map[string]http.RoundTripper]`
// lives in `maps` but mentions a type from far above it, so it goes as deep
// as its type arguments do. Nothing can be referenced from below, because a
// package can only mention what it imports, and what it imports is shallower.
// Imports alone are not the whole story: a `go:linkname` reaches from a
// package that imports nothing into the runtime, which imports plenty, and a
// caller must be at least as deep as what it calls. So the first pass records
// who calls whom, and [bands.settle] lifts each function to the deepest thing
// it reaches before the second pass places anything. Lifting a function lifts
// its callers in turn, which is what the fixed point is for.
type bands struct {
	// byPackage is the memoized depth of each package.
	byPackage map[*types.Package]int
	// calls is the call graph the first pass saw, and lifted the band each
	// function settled at.
	calls  map[*ssa.Function][]*ssa.Function
	lifted map[*ssa.Function]int
	// deepest is the highest band any item was placed in.
	deepest int
}

func newBands() *bands {
	return &bands{
		byPackage: map[*types.Package]int{},
		calls:     map[*ssa.Function][]*ssa.Function{},
	}
}

// called records that `from` mentions `to`, so that `from` cannot be placed
// in a crate that `to`'s crate cannot be seen from.
func (b *bands) called(from, to *ssa.Function) {
	if from == nil || to == nil || from == to || b.lifted != nil {
		return
	}
	b.calls[from] = append(b.calls[from], to)
}

// settle computes each function's final band: its own depth, and the depth of
// everything it reaches. Repeated until nothing moves, which terminates
// because every step raises a band and no band exceeds the deepest package.
func (b *bands) settle() {
	lifted := map[*ssa.Function]int{}
	for f := range b.calls {
		lifted[f] = b.fn(f)
	}
	for _, callees := range b.calls {
		for _, c := range callees {
			if _, ok := lifted[c]; !ok {
				lifted[c] = b.fn(c)
			}
		}
	}
	for moved := true; moved; {
		moved = false
		for f, callees := range b.calls {
			for _, c := range callees {
				if lifted[c] > lifted[f] {
					lifted[f] = lifted[c]
					moved = true
				}
			}
		}
	}
	for _, d := range lifted {
		b.note(d)
	}
	b.lifted = lifted
}

// pkg returns a package's band: one deeper than the deepest it imports.
func (b *bands) pkg(p *types.Package) int {
	if p == nil {
		return 0
	}
	if d, ok := b.byPackage[p]; ok {
		return d
	}
	// Guard against a cycle, which Go's import graph cannot have but a
	// malformed one should not hang on.
	b.byPackage[p] = 0
	depth := 0
	for _, imp := range p.Imports() {
		if d := b.pkg(imp) + 1; d > depth {
			depth = d
		}
	}
	b.byPackage[p] = depth
	b.note(depth)
	return depth
}

// typ returns the band a type belongs in: as deep as the deepest package any
// named type inside it comes from. An anonymous struct of `http.Request`s
// belongs with `http`.
func (b *bands) typ(t types.Type) int {
	return b.typeDepth(t, map[types.Type]bool{})
}

func (b *bands) typeDepth(t types.Type, seen map[types.Type]bool) int {
	if t == nil || seen[t] {
		return 0
	}
	seen[t] = true
	depth := 0
	deeper := func(d int) {
		if d > depth {
			depth = d
		}
	}
	switch t := types.Unalias(t).(type) {
	case *types.Named:
		if obj := t.Obj(); obj != nil {
			deeper(b.pkg(obj.Pkg()))
		}
		if args := t.TypeArgs(); args != nil {
			for i := 0; i < args.Len(); i++ {
				deeper(b.typeDepth(args.At(i), seen))
			}
		}
		deeper(b.typeDepth(t.Underlying(), seen))
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			deeper(b.typeDepth(t.Field(i).Type(), seen))
		}
	case *types.Pointer:
		deeper(b.typeDepth(t.Elem(), seen))
	case *types.Slice:
		deeper(b.typeDepth(t.Elem(), seen))
	case *types.Array:
		deeper(b.typeDepth(t.Elem(), seen))
	case *types.Chan:
		deeper(b.typeDepth(t.Elem(), seen))
	case *types.Map:
		deeper(b.typeDepth(t.Key(), seen))
		deeper(b.typeDepth(t.Elem(), seen))
	case *types.Signature:
		deeper(b.typeDepth(t.Params(), seen))
		deeper(b.typeDepth(t.Results(), seen))
		if recv := t.Recv(); recv != nil {
			deeper(b.typeDepth(recv.Type(), seen))
		}
	case *types.Tuple:
		for i := 0; i < t.Len(); i++ {
			deeper(b.typeDepth(t.At(i).Type(), seen))
		}
	case *types.Interface:
		for i := 0; i < t.NumMethods(); i++ {
			deeper(b.typeDepth(t.Method(i).Type(), seen))
		}
	}
	b.note(depth)
	return depth
}

// fn returns the band a function is emitted in.
func (b *bands) fn(f *ssa.Function) int {
	if d, ok := b.lifted[f]; ok {
		return d
	}
	pkg := f.Pkg
	if pkg == nil && f.Origin() != nil {
		pkg = f.Origin().Pkg
	}
	depth := 0
	if pkg != nil {
		depth = b.pkg(pkg.Pkg)
	}
	// An instantiation goes as deep as the types it was instantiated with:
	// its body mentions them, and its own package may be far below them.
	if args := f.TypeArgs(); args != nil {
		for _, a := range args {
			if d := b.typ(a); d > depth {
				depth = d
			}
		}
	}
	// Everything the function mentions in its own signature, receiver
	// included. This is what places the wrappers go/ssa synthesizes, which
	// belong to no package at all: a wrapper around a method of a type from
	// deep in the standard library goes as deep as that type.
	if d := b.typ(f.Signature); d > depth {
		depth = d
	}
	// And what a closure captured, which its body reads without ever naming
	// it in the signature.
	for _, fv := range f.FreeVars {
		if d := b.typ(fv.Type()); d > depth {
			depth = d
		}
	}
	b.note(depth)
	return depth
}

func (b *bands) note(depth int) {
	if depth > b.deepest {
		b.deepest = depth
	}
}
