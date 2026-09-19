package emit

import (
	"fmt"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// Control-flow structuring: Rust has no goto, so a function's blocks are
// rebuilt into nested labeled blocks and loops, following Norman Ramsey,
// "Beyond Relooper: Recursive Translation of Unstructured Control Flow to
// Structured Control Flow" (ICFP 2022).
//
// Walking the dominator tree:
//   - a loop header x becomes `'lx: loop { ... }`, and a back edge to it is
//     `continue 'lx`;
//   - a merge node y (two or more forward predecessors) is placed right after
//     a labeled block `'by: { ... }` that encloses its idom's code, and a
//     forward edge to it is `break 'by`;
//   - any other successor has exactly one forward predecessor and is inlined
//     where the edge is taken.
//
// The recipe needs a reducible CFG. Go produces an irreducible one only for a
// goto into a loop body; such functions keep the dispatch-loop form, which is
// correct for any CFG.

type structurer struct {
	f   *fnEmitter
	rpo map[*ssa.BasicBlock]int
}

// structured writes the body in structured form, or reports false (writing
// nothing) if the CFG is irreducible.
func (f *fnEmitter) structured() bool {
	return f.structuredFrom(f.fn.Blocks[0])
}

// structuredFrom does the same from any entry block, which is how the
// recover block's region is emitted.
func (f *fnEmitter) structuredFrom(entry *ssa.BasicBlock) bool {
	s := &structurer{f: f, rpo: reversePostorderFrom(entry)}
	// Reducible iff every retreating edge targets a dominator of its source.
	for b := range s.rpo {
		for _, succ := range b.Succs {
			if s.rpo[succ] <= s.rpo[b] && !succ.Dominates(b) {
				return false
			}
		}
	}
	s.tree(entry, nil, "    ")
	return true
}

type ctxEntry struct {
	loop bool // a loop headed by blk; otherwise a block followed by blk
	blk  *ssa.BasicBlock
}

func (s *structurer) forwardPreds(b *ssa.BasicBlock) int {
	n := 0
	for _, p := range b.Preds {
		if r, ok := s.rpo[p]; ok && r < s.rpo[b] {
			n++
		}
	}
	return n
}

func (s *structurer) isMerge(b *ssa.BasicBlock) bool { return s.forwardPreds(b) >= 2 }

func (s *structurer) isLoopHeader(b *ssa.BasicBlock) bool {
	for _, p := range b.Preds {
		if r, ok := s.rpo[p]; ok && r >= s.rpo[b] {
			return true
		}
	}
	return false
}

func (s *structurer) tree(x *ssa.BasicBlock, ctx []ctxEntry, ind string) {
	var merges []*ssa.BasicBlock
	for _, c := range x.Dominees() {
		if _, reachable := s.rpo[c]; reachable && s.isMerge(c) {
			merges = append(merges, c)
		}
	}
	// Highest reverse-postorder number first: the earliest merge node ends
	// up in the innermost block, right after x's own code.
	slices.SortFunc(merges, func(a, b *ssa.BasicBlock) int { return s.rpo[b] - s.rpo[a] })

	out := &s.f.out
	if s.isLoopHeader(x) {
		fmt.Fprintf(out, "%s'l%d: loop {\n", ind, x.Index)
		s.within(x, merges, append(ctx, ctxEntry{loop: true, blk: x}), ind+"    ")
		fmt.Fprintf(out, "%s}\n", ind)
		return
	}
	s.within(x, merges, ctx, ind)
}

func (s *structurer) within(x *ssa.BasicBlock, merges []*ssa.BasicBlock, ctx []ctxEntry, ind string) {
	out := &s.f.out
	if len(merges) == 0 {
		s.code(x, ctx, ind)
		return
	}
	y := merges[0]
	fmt.Fprintf(out, "%s'b%d: {\n", ind, y.Index)
	s.within(x, merges[1:], append(ctx, ctxEntry{blk: y}), ind+"    ")
	fmt.Fprintf(out, "%s}\n", ind)
	s.tree(y, ctx, ind)
}

// code writes x's instructions and its terminator.
func (s *structurer) code(x *ssa.BasicBlock, ctx []ctxEntry, ind string) {
	f := s.f
	for _, instr := range x.Instrs {
		switch instr := instr.(type) {
		case *ssa.Phi, *ssa.DebugRef:
		case *ssa.Jump:
			s.branch(x, x.Succs[0], ctx, ind)
		case *ssa.If:
			fmt.Fprintf(&f.out, "%sif %s {\n", ind, f.val(instr.Cond))
			s.branch(x, x.Succs[0], ctx, ind+"    ")
			fmt.Fprintf(&f.out, "%s} else {\n", ind)
			s.branch(x, x.Succs[1], ctx, ind+"    ")
			fmt.Fprintf(&f.out, "%s}\n", ind)
		case *ssa.Return:
			f.out.WriteString(ind + f.ret(instr) + "\n")
		default:
			if st := f.instr(instr); st != "" {
				f.out.WriteString(ind + st + "\n")
			}
		}
	}
}

func (s *structurer) branch(src, dst *ssa.BasicBlock, ctx []ctxEntry, ind string) {
	s.f.out.WriteString(s.f.phiMoves(src, dst, ind))
	switch {
	case s.rpo[dst] <= s.rpo[src]:
		fmt.Fprintf(&s.f.out, "%scontinue 'l%d;\n", ind, dst.Index)
	case s.isMerge(dst):
		fmt.Fprintf(&s.f.out, "%sbreak 'b%d;\n", ind, dst.Index)
	default:
		s.tree(dst, ctx, ind)
	}
}

// phiMoves assigns dst's phis for the edge from src, as a parallel copy.
func (f *fnEmitter) phiMoves(src, dst *ssa.BasicBlock, ind string) string {
	var phis []*ssa.Phi
	for _, instr := range dst.Instrs {
		if phi, ok := instr.(*ssa.Phi); ok {
			phis = append(phis, phi)
		}
	}
	if len(phis) == 0 {
		return ""
	}
	idx := predIndex(dst, src)
	var b strings.Builder
	if len(phis) == 1 {
		fmt.Fprintf(&b, "%s%s\n", ind, f.assign(phis[0], f.val(phis[0].Edges[idx])))
		return b.String()
	}
	for i, phi := range phis {
		fmt.Fprintf(&b, "%slet e%d = %s;\n", ind, i, f.val(phi.Edges[idx]))
	}
	for i, phi := range phis {
		fmt.Fprintf(&b, "%s%s\n", ind, f.assign(phi, fmt.Sprintf("e%d", i)))
	}
	return b.String()
}
