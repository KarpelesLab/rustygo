package emit

import (
	"fmt"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// Comparing bytes as a string, without the copy.
//
// `bytes.Equal` is one line — `return string(a) == string(b)` — and gc compares
// the two arrays where they lie. go/ssa spells both conversions out as
// instructions, so the copies are visible here, and a conversion whose only use
// is the comparison does not have to happen at all: the comparison reads the
// bytes instead (`Slice::cmp_bytes`, src/slice.rs).
//
// It is not a small saving. `bytes.Index` calls `Equal` once for every
// candidate position, so searching a long string for a long needle allocated
// twice per position — and `bytes`' own tests assert, through
// `testing.AllocsPerRun`, that searching allocates nothing at all.
//
// `switch string(b) { case "GET": }` is the same shape with a literal on one
// side, and just as common: `net/textproto` and `net/http` read every header
// that way.

// findByteCompares returns the string comparisons to emit over bytes instead,
// and the conversions that leaves with nothing to do.
func (f *fnEmitter) findByteCompares() (map[*ssa.BinOp]bool, map[ssa.Instruction]bool) {
	var cmps map[*ssa.BinOp]bool
	var dead map[ssa.Instruction]bool
	for _, b := range f.fn.Blocks {
		for _, instr := range b.Instrs {
			op, ok := instr.(*ssa.BinOp)
			if !ok || !isComparison(op.Op) || !isString(op.X.Type()) {
				continue
			}
			x, y := elidableBytes(op.X, op), elidableBytes(op.Y, op)
			if x == nil && y == nil {
				continue
			}
			if cmps == nil {
				cmps = map[*ssa.BinOp]bool{}
				dead = map[ssa.Instruction]bool{}
			}
			cmps[op] = true
			for _, c := range []*ssa.Convert{x, y} {
				if c != nil {
					dead[c] = true
				}
			}
		}
	}
	return cmps, dead
}

// byteCompare renders a string comparison over the bytes themselves.
func (f *fnEmitter) byteCompare(v *ssa.BinOp) string {
	ord := orderings[v.Op]
	x, y := elidableBytes(v.X, v), elidableBytes(v.Y, v)
	switch {
	case x != nil && y != nil:
		return fmt.Sprintf("(%s).cmp_bytes(%s).%s()", f.val(x.X), f.val(y.X), ord)
	case x != nil:
		return fmt.Sprintf("(%s).cmp_str(%s).%s()", f.val(x.X), f.val(v.Y), ord)
	default:
		// The slice is the right-hand operand, so it has to be asked first
		// and the answer turned back around.
		return fmt.Sprintf("(%s).cmp_str(%s).reverse().%s()", f.val(y.X), f.val(v.X), ord)
	}
}

// orderings names the method on `core::cmp::Ordering` that each comparison
// asks for. Go orders strings by their bytes and so does Rust, so the two
// agree on every one of them.
var orderings = map[token.Token]string{
	token.EQL: "is_eq",
	token.NEQ: "is_ne",
	token.LSS: "is_lt",
	token.LEQ: "is_le",
	token.GTR: "is_gt",
	token.GEQ: "is_ge",
}

// isComparison reports whether op compares rather than computes.
func isComparison(op token.Token) bool {
	_, ok := orderings[op]
	return ok
}

// elidableBytes returns the conversion v is, when v is `string(b)` for a byte
// slice b and the comparison is the only thing that uses it.
func elidableBytes(v ssa.Value, user ssa.Instruction) *ssa.Convert {
	c, ok := v.(*ssa.Convert)
	if !ok || !isString(c.Type()) {
		return nil
	}
	s, ok := c.X.Type().Underlying().(*types.Slice)
	if !ok {
		return nil
	}
	// `[]rune` to string decodes rather than copies, so only bytes qualify.
	if b := basicInfo(s.Elem()); b == nil || b.Kind() != types.Uint8 {
		return nil
	}
	refs := c.Referrers()
	if refs == nil {
		return nil
	}
	uses := 0
	for _, r := range *refs {
		if _, isDebug := r.(*ssa.DebugRef); isDebug {
			continue
		}
		if r != user {
			return nil
		}
		uses++
	}
	// Twice is `string(a) == string(a)`, which still needs one of them.
	if uses != 1 {
		return nil
	}
	return c
}
