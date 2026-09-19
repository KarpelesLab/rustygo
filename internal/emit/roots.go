package emit

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// Shadow-stack roots: which values the collector must be able to find.
//
// A value needs a slot only if it holds a GC reference and is live across a
// safe point, where a collection can happen:
//   - a call (the callee may allocate),
//   - an allocation (new object, string concatenation, string(rune)),
//   - a loop back-edge (cooperative preemption from M2 on).
//
// Anything else lives and dies between safe points and needs no slot: an
// interior pointer such as `&p.X` that is loaded from immediately, for
// instance. The analysis is plain backward liveness over SSA values, with
// phi operands used at the end of the incoming edge.

// collectRoots assigns shadow-stack slots.
func (f *fnEmitter) collectRoots() {
	f.roots = map[ssa.Value]int{}
	f.cells = map[ssa.Value]bool{}
	cand := map[ssa.Value]bool{}
	for _, p := range f.fn.Params {
		if containsRef(p.Type()) {
			cand[p] = true
		}
	}
	for _, b := range f.fn.Blocks {
		for _, instr := range b.Instrs {
			v, ok := instr.(ssa.Value)
			if !ok || !f.hasVar(v) {
				continue
			}
			// A string iterator holds the string it walks.
			if _, isRange := v.(*ssa.Range); isRange || containsRef(v.Type()) {
				cand[v] = true
			}
		}
	}
	if len(cand) == 0 {
		return
	}

	rpo := reversePostorder(f.fn)
	liveIn := map[*ssa.BasicBlock]map[ssa.Value]bool{}
	liveOut := func(b *ssa.BasicBlock) map[ssa.Value]bool {
		out := map[ssa.Value]bool{}
		for _, s := range b.Succs {
			for v := range liveIn[s] {
				out[v] = true
			}
			idx := predIndex(s, b)
			for _, instr := range s.Instrs {
				phi, ok := instr.(*ssa.Phi)
				if !ok {
					break
				}
				if e := phi.Edges[idx]; cand[e] {
					out[e] = true
				}
			}
		}
		return out
	}
	// transfer walks b backward from live-out; visit sees each instruction
	// with the set live right after it, minus what it defines.
	transfer := func(b *ssa.BasicBlock, live map[ssa.Value]bool, visit func(ssa.Instruction, map[ssa.Value]bool)) {
		var ops []*ssa.Value
		for i := len(b.Instrs) - 1; i >= 0; i-- {
			instr := b.Instrs[i]
			if v, ok := instr.(ssa.Value); ok {
				delete(live, v)
			}
			if visit != nil {
				visit(instr, live)
			}
			if _, isPhi := instr.(*ssa.Phi); isPhi {
				continue
			}
			for _, op := range instr.Operands(ops[:0]) {
				if *op != nil && cand[*op] {
					live[*op] = true
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for i := len(f.fn.Blocks) - 1; i >= 0; i-- {
			b := f.fn.Blocks[i]
			live := liveOut(b)
			transfer(b, live, nil)
			if len(live) != len(liveIn[b]) {
				liveIn[b], changed = live, true
			}
		}
	}

	need := map[ssa.Value]bool{}
	for _, b := range f.fn.Blocks {
		live := liveOut(b)
		for _, s := range b.Succs {
			if rpo[s] <= rpo[b] { // back-edge: a preemption point
				for v := range live {
					need[v] = true
				}
			}
		}
		transfer(b, live, func(instr ssa.Instruction, after map[ssa.Value]bool) {
			if !safePoint(instr) {
				return
			}
			// Live across the safe point, and also what it is handed: a
			// collection can run while the callee holds those values.
			for v := range after {
				need[v] = true
			}
			var ops []*ssa.Value
			for _, op := range instr.Operands(ops) {
				if *op != nil && cand[*op] {
					need[*op] = true
				}
			}
		})
	}
	// Slots in a stable order: parameters, then instructions. A value whose
	// references sit inside an aggregate lives in a cell, so the collector
	// can trace the local itself.
	assign := func(v ssa.Value) {
		if !need[v] {
			return
		}
		f.roots[v] = len(f.roots)
		if _, isRange := v.(*ssa.Range); isRange || !holdsRef(v.Type()) {
			f.cells[v] = true
		}
	}
	for _, p := range f.fn.Params {
		assign(p)
	}
	for _, b := range f.fn.Blocks {
		for _, instr := range b.Instrs {
			if v, ok := instr.(ssa.Value); ok {
				assign(v)
			}
		}
	}
}

// holdsRef reports whether a value of type t *is* a GC reference: a pointer
// or a string. Such a value goes in a plain root slot.
func holdsRef(t types.Type) bool {
	switch u := t.Underlying().(type) {
	case *types.Pointer, *types.Slice:
		return true
	case *types.Basic:
		return u.Info()&types.IsString != 0
	}
	return false
}

// containsRef reports whether a value of type t holds any GC reference,
// including inside a struct, array or tuple. Such a value must be traced.
func containsRef(t types.Type) bool {
	if holdsRef(t) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if containsRef(u.Field(i).Type()) {
				return true
			}
		}
	case *types.Array:
		return containsRef(u.Elem())
	case *types.Tuple:
		for i := 0; i < u.Len(); i++ {
			if containsRef(u.At(i).Type()) {
				return true
			}
		}
	}
	return false
}

// safePoint reports whether a collection can happen during instr.
func safePoint(instr ssa.Instruction) bool {
	switch instr := instr.(type) {
	case *ssa.Call:
		b, builtin := instr.Call.Value.(*ssa.Builtin)
		if !builtin {
			return true // the callee may allocate
		}
		switch b.Name() {
		case "append":
			return true // grows by allocating a new array
		case "copy":
			return true // copy([]byte, string) allocates
		}
		return false
	case *ssa.Alloc:
		return true
	case *ssa.BinOp:
		return instr.Op == token.ADD && isString(instr.X.Type())
	case *ssa.Convert:
		// string <-> []byte and []rune both copy into a new object.
		return isString(instr.Type()) || isString(instr.X.Type())
	case *ssa.MakeSlice:
		return true
	}
	return false
}

func predIndex(b, pred *ssa.BasicBlock) int {
	for i, p := range b.Preds {
		if p == pred {
			return i
		}
	}
	return -1
}

// reversePostorder numbers the blocks reachable from the entry.
func reversePostorder(fn *ssa.Function) map[*ssa.BasicBlock]int {
	var post []*ssa.BasicBlock
	seen := map[*ssa.BasicBlock]bool{}
	var dfs func(b *ssa.BasicBlock)
	dfs = func(b *ssa.BasicBlock) {
		seen[b] = true
		for _, s := range b.Succs {
			if !seen[s] {
				dfs(s)
			}
		}
		post = append(post, b)
	}
	dfs(fn.Blocks[0])
	rpo := make(map[*ssa.BasicBlock]int, len(post))
	for i, b := range post {
		rpo[b] = len(post) - 1 - i
	}
	return rpo
}
