package emit

import (
	"fmt"
	"go/constant"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// Constant tables.
//
// Go's generated tables are written as array literals — `unicode/norm` has one
// of 19,426 bytes — and go/ssa spells every literal out: allocate the array,
// take the address of element 0, store a constant, take the address of element
// 1, store a constant, and so on. Emitted as written, that package's
// initializer is one Rust function of 72,613 lines, and `rustc` needs 31 GB to
// compile it. gc has the same input and emits static data.
//
// So does rustygo, when it can see that is what the code is: a run of stores
// of constants, at constant indices, into an array that has just been
// allocated and not yet used for anything else. The run becomes a `static`
// array in the module and one allocation initialized from it. Anything the run
// does not cover stays zero, and any store after the run is emitted normally
// and overwrites what the table holds.

// table is a constant array recognized in a function's body.
type table struct {
	// name is the static the elements were written into.
	name string
	// skip holds the instructions the static replaces.
	skip map[ssa.Instruction]bool
}

// findTables recognizes the constant tables in one function, returning the
// static to initialize each allocation from and the instructions that are no
// longer needed.
func (f *fnEmitter) findTables() (map[*ssa.Alloc]string, map[ssa.Instruction]bool) {
	var tables map[*ssa.Alloc]string
	var skip map[ssa.Instruction]bool
	for _, b := range f.fn.Blocks {
		for i := 0; i < len(b.Instrs); i++ {
			alloc, ok := b.Instrs[i].(*ssa.Alloc)
			if !ok {
				continue
			}
			t, next := f.readTable(b, i)
			if t == nil {
				continue
			}
			if tables == nil {
				tables = map[*ssa.Alloc]string{}
				skip = map[ssa.Instruction]bool{}
			}
			tables[alloc] = t.name
			for instr := range t.skip {
				skip[instr] = true
			}
			i = next - 1
		}
	}
	return tables, skip
}

// The number of elements below which writing them out is no worse than a
// static: the point of this is the tables with thousands of entries.
const minTableElements = 16

// readTable reads the run of constant stores that follows the allocation at
// b.Instrs[i], and returns the table it makes, with the index just past the
// run.
func (f *fnEmitter) readTable(b *ssa.BasicBlock, i int) (*table, int) {
	alloc := b.Instrs[i].(*ssa.Alloc)
	arr, ok := alloc.Type().(*types.Pointer).Elem().Underlying().(*types.Array)
	if !ok {
		return nil, i
	}
	elem, ok := arr.Elem().Underlying().(*types.Basic)
	if !ok || elem.Info()&(types.IsInteger|types.IsBoolean|types.IsFloat) == 0 {
		// Only the element types a Rust `static` can hold directly. A table
		// of strings or structs would need the heap.
		return nil, i
	}
	values := make([]string, arr.Len())
	zero := "0"
	if elem.Info()&types.IsBoolean != 0 {
		zero = "false"
	}
	for j := range values {
		values[j] = zero
	}
	skip := map[ssa.Instruction]bool{}
	// go/ssa writes a literal as every element's address first and then every
	// store, so both kinds are taken in whatever order they come, until
	// something else turns up.
	at := map[ssa.Value]int64{}
	n := 0
	j := i + 1
scan:
	for ; j < len(b.Instrs); j++ {
		switch x := b.Instrs[j].(type) {
		case *ssa.IndexAddr:
			if x.X != alloc {
				break scan
			}
			idx, ok := x.Index.(*ssa.Const)
			if !ok {
				break scan
			}
			k, exact := constant.Int64Val(constant.ToInt(idx.Value))
			if !exact || k < 0 || k >= arr.Len() {
				break scan
			}
			// The address must be used for nothing but its own store.
			if refs := x.Referrers(); refs == nil || len(*refs) != 1 {
				break scan
			}
			at[x] = k
			skip[x] = true
		case *ssa.Store:
			k, ok := at[x.Addr]
			if !ok {
				break scan
			}
			val, isConst := x.Val.(*ssa.Const)
			if !isConst || val.Value == nil {
				break scan
			}
			lit := f.constant(val)
			if strings.Contains(lit, "(") {
				// Not something a static can hold.
				break scan
			}
			values[k] = lit
			skip[x] = true
			n++
		default:
			break scan
		}
	}
	// Any element whose address was taken but never stored is not a table
	// after all: something else was going to write it.
	if n != len(at) {
		return nil, i
	}
	if n < minTableElements {
		return nil, i
	}
	name := f.e.module(f.fn.Pkg).ns.claim(mangle(f.fn.Name() + "$table"))
	rust := f.typ(arr.Elem(), alloc.Pos())
	fmt.Fprintf(f.e.module(f.fn.Pkg).at(f.e.bands.fn(f.fn)),
		"\n// %d constants, from a table literal in %s\nstatic %s: [%s; %d] = [%s];\n",
		n, f.fn, name, rust, arr.Len(), strings.Join(values, ", "))
	return &table{name: name, skip: skip}, j
}
