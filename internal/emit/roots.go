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
	cand := map[ssa.Value]bool{}
	for _, p := range f.fn.Params {
		if holdsRef(p.Type()) {
			cand[p] = true
		}
	}
	for _, b := range f.fn.Blocks {
		for _, instr := range b.Instrs {
			if v, ok := instr.(ssa.Value); ok && f.hasVar(v) && holdsRef(v.Type()) {
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
			if safePoint(instr) {
				for v := range after {
					need[v] = true
				}
			}
		})
	}
	// Slots in a stable order: parameters, then instructions.
	for _, p := range f.fn.Params {
		if need[p] {
			f.roots[p] = len(f.roots)
		}
	}
	for _, b := range f.fn.Blocks {
		for _, instr := range b.Instrs {
			if v, ok := instr.(ssa.Value); ok && need[v] {
				f.roots[v] = len(f.roots)
			}
		}
	}
}

// holdsRef reports whether a value of type t holds a GC reference that a
// slot records: pointers and strings. References inside struct and array
// values are not tracked yet.
func holdsRef(t types.Type) bool {
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return true
	case *types.Basic:
		return u.Info()&types.IsString != 0
	}
	return false
}

// safePoint reports whether a collection can happen during instr.
func safePoint(instr ssa.Instruction) bool {
	switch instr := instr.(type) {
	case *ssa.Call:
		_, builtin := instr.Call.Value.(*ssa.Builtin)
		return !builtin
	case *ssa.Alloc:
		return true
	case *ssa.BinOp:
		return instr.Op == token.ADD && isString(instr.X.Type())
	case *ssa.Convert:
		return isString(instr.Type())
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
