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
// rather than of each comparison: all of its uses have to read the string and
// not keep it, and then none of them needs it to exist.
//
// `m[string(b)]` is the third shape. A lookup only reads its key, so the bytes
// hash and compare exactly as the string would, and `net/textproto` looks up
// every header name that way before it decides whether to canonicalize it.
// Storing is different — `m[string(b)] = v` has to keep the key — so a map
// update is a use that makes the string real.

// findByteCompares records the string comparisons and map lookups to do over
// bytes instead and the conversions they stand in for, and returns the latter
// as the instructions the body leaves out.
func (f *fnEmitter) findByteCompares() map[ssa.Instruction]bool {
	var dead map[ssa.Instruction]bool
	for _, b := range f.fn.Blocks {
		for _, instr := range b.Instrs {
			c, ok := instr.(*ssa.Convert)
			if !ok || !bytesToString(c) {
				continue
			}
			users := readOnlyUsers(c)
			if len(users) == 0 {
				continue
			}
			if dead == nil {
				dead = map[ssa.Instruction]bool{}
				f.byteCmp = map[*ssa.BinOp]bool{}
				f.byteConv = map[*ssa.Convert]bool{}
				f.byteLookup = map[*ssa.Lookup]bool{}
			}
			dead[c] = true
			f.byteConv[c] = true
			for _, u := range users {
				switch u := u.(type) {
				case *ssa.BinOp:
					f.byteCmp[u] = true
				case *ssa.Lookup:
					f.byteLookup[u] = true
				}
			}
		}
	}
	return dead
}

// byteLookupExpr renders `m[string(b)]` over the slice's own bytes.
func (f *fnEmitter) byteLookupExpr(v *ssa.Lookup) string {
	get := "get_bytes"
	if v.CommaOk {
		get = "get_bytes_ok"
	}
	return fmt.Sprintf("(%s).%s(%s)", f.val(v.X), get, f.val(f.elidedBytes(v.Index)))
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

// readOnlyUsers returns the instructions that use c and only read it — a
// comparison, or a map lookup keyed on it — or nil if anything else does, in
// which case the string has to be made after all.
func readOnlyUsers(c *ssa.Convert) []ssa.Instruction {
	refs := c.Referrers()
	if refs == nil {
		return nil
	}
	var users []ssa.Instruction
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.DebugRef:
			continue
		case *ssa.BinOp:
			if orderings[r.Op] == "" {
				return nil
			}
		case *ssa.Lookup:
			// The key, not the thing being indexed: `string(b)[i]` reads the
			// string itself, and only a map's key is answerable from bytes.
			if r.Index != c {
				return nil
			}
			if _, isMap := r.X.Type().Underlying().(*types.Map); !isMap {
				return nil
			}
		default:
			return nil
		}
		users = append(users, r)
	}
	return users
}
