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
// instructions, so the copies are visible here, and a conversion to string that
// nothing but comparisons use does not have to happen at all: the comparisons
// read the bytes instead (`Slice::cmp_bytes`, src/slice.rs).
//
// It is not a small saving. `bytes.Index` calls `Equal` once for every candidate
// position, so searching a long string for a long needle allocated twice per
// position — and `bytes`' own tests assert, through `testing.AllocsPerRun`, that
// searching allocates nothing at all.
//
// `switch string(b) { case "GET": }` is the same shape and just as common:
// `net/textproto` and `net/http` read every header that way. There one
// conversion serves several comparisons — go/ssa may even turn a long switch
// into a binary search over `<` — so the question is asked of the conversion
// rather than of each comparison: all of its uses have to be comparisons, and
// then every one of them reads bytes.

// findByteCompares records the string comparisons to emit over bytes instead
// and the conversions they stand in for, and returns the latter as the
// instructions the body leaves out.
func (f *fnEmitter) findByteCompares() map[ssa.Instruction]bool {
	var dead map[ssa.Instruction]bool
	for _, b := range f.fn.Blocks {
		for _, instr := range b.Instrs {
			c, ok := instr.(*ssa.Convert)
			if !ok || !bytesToString(c) {
				continue
			}
			users := comparisonUsers(c)
			if len(users) == 0 {
				continue
			}
			if dead == nil {
				dead = map[ssa.Instruction]bool{}
				f.byteCmp = map[*ssa.BinOp]bool{}
				f.byteConv = map[*ssa.Convert]bool{}
			}
			dead[c] = true
			f.byteConv[c] = true
			for _, op := range users {
				f.byteCmp[op] = true
			}
		}
	}
	return dead
}

// byteCompare renders a string comparison over the bytes themselves. At least
// one operand is a conversion findByteCompares decided against making.
func (f *fnEmitter) byteCompare(v *ssa.BinOp) string {
	ord := orderings[v.Op]
	x, y := f.elidedBytes(v.X), f.elidedBytes(v.Y)
	switch {
	case x != nil && y != nil:
		return fmt.Sprintf("(%s).cmp_bytes(%s).%s()", f.val(x), f.val(y), ord)
	case x != nil:
		return fmt.Sprintf("(%s).cmp_str(%s).%s()", f.val(x), f.val(v.Y), ord)
	default:
		// The slice is the right-hand operand, so it has to be asked first and
		// the answer turned back around.
		return fmt.Sprintf("(%s).cmp_str(%s).reverse().%s()", f.val(y), f.val(v.X), ord)
	}
}

// elidedBytes returns the byte slice behind an operand whose conversion to
// string is one of the ones left out, or nil if the operand is a string like
// any other.
func (f *fnEmitter) elidedBytes(v ssa.Value) ssa.Value {
	c, ok := v.(*ssa.Convert)
	if !ok || !f.byteConv[c] {
		return nil
	}
	return c.X
}

// orderings names the method on `core::cmp::Ordering` that each comparison
// asks for. Go orders strings by their bytes and so does Rust, so the two agree
// on every one of them.
var orderings = map[token.Token]string{
	token.EQL: "is_eq",
	token.NEQ: "is_ne",
	token.LSS: "is_lt",
	token.LEQ: "is_le",
	token.GTR: "is_gt",
	token.GEQ: "is_ge",
}

// bytesToString reports whether c is `string(b)` for a byte slice b.
func bytesToString(c *ssa.Convert) bool {
	if !isString(c.Type()) {
		return false
	}
	s, ok := c.X.Type().Underlying().(*types.Slice)
	if !ok {
		return false
	}
	// `[]rune` to string decodes rather than copies, so only bytes qualify.
	b := basicInfo(s.Elem())
	return b != nil && b.Kind() == types.Uint8
}

// comparisonUsers returns the comparisons that use c, or nil if anything else
// does — in which case the string has to be made after all.
func comparisonUsers(c *ssa.Convert) []*ssa.BinOp {
	refs := c.Referrers()
	if refs == nil {
		return nil
	}
	var users []*ssa.BinOp
	for _, r := range *refs {
		if _, isDebug := r.(*ssa.DebugRef); isDebug {
			continue
		}
		op, ok := r.(*ssa.BinOp)
		if !ok || orderings[op.Op] == "" {
			return nil
		}
		users = append(users, op)
	}
	return users
}
