package emit

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// fnEmitter writes one function.
type fnEmitter struct {
	e   *emitter
	fn  *ssa.Function
	out strings.Builder
	// loop is set when the body has more than one block and runs as a
	// dispatch loop over block indices.
	loop bool
	// roots maps each value that must stay visible to the collector to its
	// shadow-stack slot; cells marks those held in a `Slot` local because
	// their references sit inside an aggregate.
	roots map[ssa.Value]int
	cells map[ssa.Value]bool
}

func (e *emitter) function(fn *ssa.Function) {
	f := &fnEmitter{e: e, fn: fn}
	name := e.fnPaths[fn]
	name = name[strings.LastIndex(name, "::")+2:]
	m := e.module(fn.Pkg)
	if fn.Pkg == nil && fn.Origin() != nil {
		m = e.module(fn.Origin().Pkg)
	}

	if fn.Pkg != nil && e.res.Std[fn.Pkg.Pkg] {
		e.errorf(fn.Pos(), "package %s: the standard library is not compiled yet (roadmap M3)", fn.Pkg.Pkg.Path())
		return
	}
	if fn.Blocks == nil {
		e.errorf(fn.Pos(), "%s has no Go body (assembly or linkname), not supported yet", fn)
		return
	}

	params := make([]string, 0, len(fn.Params)+1)
	if len(fn.FreeVars) > 0 {
		params = append(params, "__env: Env")
	}
	for i, p := range fn.Params {
		params = append(params, fmt.Sprintf("a%d: %s", i, f.typ(p.Type(), p.Pos())))
	}
	ret := ""
	if res := fn.Signature.Results(); res.Len() == 1 {
		ret = " -> " + f.typ(res.At(0).Type(), fn.Pos())
	} else if res.Len() > 1 {
		ret = " -> " + f.typ(res, fn.Pos())
	}
	fmt.Fprintf(&f.out, "\n// Go: %s\npub fn %s(%s)%s {\n", fn.String(), name, strings.Join(params, ", "), ret)
	f.collectRoots()
	if len(fn.FreeVars) > 0 {
		fmt.Fprintf(&f.out, "    let __e = __env.cast::<%s>();\n", f.envPlace())
	}
	f.declare()
	if len(f.roots) > 0 {
		fmt.Fprintf(&f.out, "    let __roots = rustygo::gc::Frame::<%d>::new();\n", len(f.roots))
		for v, k := range f.rootsInOrder() {
			if f.cells[v] {
				fmt.Fprintf(&f.out, "    __roots.set_local(%d, &%s);\n", k, f.name(v))
			}
		}
		f.out.WriteString("    __roots.scope(|| {\n")
		for _, p := range fn.Params {
			f.out.WriteString(f.rootSet(p, "    "))
		}
	}
	f.body()
	if len(f.roots) > 0 {
		f.out.WriteString("    })\n")
	}
	f.out.WriteString("}\n")
	m.buf.WriteString(f.out.String())
}

// rootSet records v in its shadow-stack slot after v is assigned. Values in
// cells need no such write: their slot tracks the cell itself.
func (f *fnEmitter) rootSet(v ssa.Value, ind string) string {
	k, ok := f.roots[v]
	if !ok || f.cells[v] {
		return ""
	}
	return fmt.Sprintf("%s__roots.set(%d, &%s);\n", ind, k, f.name(v))
}

// name is the Rust expression naming v's local (not its value).
func (f *fnEmitter) name(v ssa.Value) string {
	if p, ok := v.(*ssa.Parameter); ok {
		for i, q := range f.fn.Params {
			if p == q {
				return fmt.Sprintf("a%d", i)
			}
		}
	}
	return v.Name()
}

// rootsInOrder iterates the rooted values by slot, for stable output.
func (f *fnEmitter) rootsInOrder() map[ssa.Value]int {
	return f.roots
}

// declare writes the locals: one per SSA value, zero-initialized, because
// rustc cannot prove definite assignment across reconstructed control flow.
// Values that hold references inside an aggregate live in a `Slot`, so the
// collector can trace the local where it sits.
func (f *fnEmitter) declare() {
	for _, b := range f.fn.Blocks {
		for _, instr := range b.Instrs {
			v, ok := instr.(ssa.Value)
			if !ok || !f.hasVar(v) {
				continue
			}
			if f.cells[v] {
				fmt.Fprintf(&f.out, "    let %s: Slot<%s> = Place::new(GoValue::zero());\n", v.Name(), f.valueType(v))
			} else {
				fmt.Fprintf(&f.out, "    let mut %s: %s = GoValue::zero();\n", v.Name(), f.valueType(v))
			}
		}
	}
}

func (f *fnEmitter) typ(t types.Type, pos token.Pos) string {
	return f.e.types.rust(t, f.e, f.pos(pos))
}

func (f *fnEmitter) place(t types.Type, pos token.Pos) string {
	return f.e.types.place(t, f.e, f.pos(pos))
}

// envPlace is the Rust place type of this function's captured environment.
func (f *fnEmitter) envPlace() string {
	st := f.e.envStruct(f.fn)
	return "crate::ty::" + f.e.types.structInfo(st, f.fn.Name()+"$env", f.e, f.fn.Pos()).name + "_P"
}

// pos falls back to the function's position when an instruction has none.
func (f *fnEmitter) pos(pos token.Pos) token.Pos {
	if !pos.IsValid() {
		return f.fn.Pos()
	}
	return pos
}

func (f *fnEmitter) errorf(pos token.Pos, format string, args ...any) {
	f.e.errorf(f.pos(pos), format, args...)
}

func (f *fnEmitter) body() {
	if len(f.fn.Blocks) == 1 {
		f.block(f.fn.Blocks[0], "    ")
		return
	}
	if f.structured() {
		return
	}
	f.loop = true
	f.out.WriteString("    let mut blk: usize = 0;\n    loop {\n        match blk {\n")
	for _, b := range f.fn.Blocks {
		fmt.Fprintf(&f.out, "            %d => {\n", b.Index)
		f.block(b, "                ")
		f.out.WriteString("            }\n")
	}
	f.out.WriteString("            _ => unreachable!(),\n        }\n    }\n")
}

// hasVar reports whether v gets a local.
func (f *fnEmitter) hasVar(v ssa.Value) bool {
	if t, ok := v.Type().(*types.Tuple); ok && t.Len() == 0 {
		return false
	}
	if mi, ok := v.(*ssa.MakeInterface); ok && onlyPanicUses(mi) {
		return false
	}
	return !deadLoad(v)
}

// deadLoad reports an unused load that cannot panic. go/ssa emits one for
// `for i := range arr` over an array variable (`t0 = *arr`), and copying the
// whole array every time the loop starts costs more than the loop. Only loads
// from globals and allocations qualify: those are never nil, so dropping the
// load cannot drop a nil-dereference panic.
func deadLoad(v ssa.Value) bool {
	u, ok := v.(*ssa.UnOp)
	if !ok || u.Op != token.MUL || len(*u.Referrers()) > 0 {
		return false
	}
	switch u.X.(type) {
	case *ssa.Global, *ssa.Alloc:
		return true
	}
	return false
}

// valueType is the Rust type of the local holding v.
func (f *fnEmitter) valueType(v ssa.Value) string {
	switch v := v.(type) {
	case *ssa.Range:
		if !isString(v.X.Type()) {
			f.errorf(v.Pos(), "range over %s is not supported yet (roadmap M1)", v.X.Type())
		}
		return "StrIter"
	case *ssa.Next:
		return "(bool, i64, i32)"
	}
	return f.typ(v.Type(), v.Pos())
}

func (f *fnEmitter) block(b *ssa.BasicBlock, ind string) {
	for _, instr := range b.Instrs {
		switch instr := instr.(type) {
		case *ssa.Phi, *ssa.DebugRef:
			// Phis are assigned on the incoming edges.
		case *ssa.Jump:
			f.out.WriteString(f.edge(b, b.Succs[0], ind))
		case *ssa.If:
			fmt.Fprintf(&f.out, "%sif %s {\n", ind, f.val(instr.Cond))
			f.out.WriteString(f.edge(b, b.Succs[0], ind+"    "))
			fmt.Fprintf(&f.out, "%s} else {\n", ind)
			f.out.WriteString(f.edge(b, b.Succs[1], ind+"    "))
			fmt.Fprintf(&f.out, "%s}\n", ind)
		case *ssa.Return:
			f.out.WriteString(ind + f.ret(instr) + "\n")
		default:
			if s := f.instr(instr); s != "" {
				f.out.WriteString(ind + s + "\n")
			}
		}
	}
}

// edge assigns the phis of succ for the edge from pred, then transfers
// control to succ.
func (f *fnEmitter) edge(pred, succ *ssa.BasicBlock, ind string) string {
	return f.phiMoves(pred, succ, ind) + fmt.Sprintf("%sblk = %d;\n%scontinue;\n", ind, succ.Index, ind)
}

func (f *fnEmitter) ret(r *ssa.Return) string {
	switch len(r.Results) {
	case 0:
		return "return;"
	case 1:
		return "return " + f.val(r.Results[0]) + ";"
	}
	parts := make([]string, len(r.Results))
	for i, v := range r.Results {
		parts[i] = f.val(v)
	}
	return "return (" + strings.Join(parts, ", ") + ");"
}

// instr returns the statement for a non-terminator instruction.
func (f *fnEmitter) instr(instr ssa.Instruction) string {
	if v, ok := instr.(ssa.Value); ok {
		if deadLoad(v) {
			return ""
		}
		expr := f.expr(v)
		if expr == "" {
			return ""
		}
		if !f.hasVar(v) {
			return expr + ";"
		}
		return f.assign(v, expr)
	}
	switch instr := instr.(type) {
	case *ssa.Store:
		return fmt.Sprintf("(%s).store(%s);", f.val(instr.Addr), f.val(instr.Val))
	case *ssa.Panic:
		return f.panic(instr)
	case *ssa.Go:
		f.errorf(instr.Pos(), "go statements are not supported yet (roadmap M2)")
	case *ssa.Defer, *ssa.RunDefers:
		f.errorf(instr.Pos(), "defer is not supported yet (roadmap M1)")
	case *ssa.Send:
		f.errorf(instr.Pos(), "channels are not supported yet (roadmap M2)")
	case *ssa.MapUpdate:
		f.errorf(instr.Pos(), "maps are not supported yet (roadmap M1)")
	default:
		f.errorf(instr.Pos(), "instruction %T is not supported yet", instr)
	}
	return ""
}

// assign writes v's local, and records it as a root if it has a slot.
func (f *fnEmitter) assign(v ssa.Value, expr string) string {
	if f.cells[v] {
		return fmt.Sprintf("%s.store(%s);", v.Name(), expr)
	}
	return v.Name() + " = " + expr + ";" + strings.TrimSuffix(f.rootSet(v, " "), "\n")
}

// expr returns the right-hand side for a value-producing instruction.
func (f *fnEmitter) expr(v ssa.Value) string {
	switch v := v.(type) {
	case *ssa.Alloc:
		return fmt.Sprintf("Ptr::<%s>::alloc(GoValue::zero())", f.place(v.Type().(*types.Pointer).Elem(), v.Pos()))
	case *ssa.BinOp:
		return f.binop(v)
	case *ssa.UnOp:
		return f.unop(v)
	case *ssa.Call:
		return f.call(v)
	case *ssa.ChangeType:
		return f.val(v.X)
	case *ssa.Convert:
		return f.convert(v)
	case *ssa.Extract:
		return fmt.Sprintf("%s.%d", f.val(v.Tuple), v.Index)
	case *ssa.Field:
		return fmt.Sprintf("(%s).%s", f.val(v.X), f.e.types.field(v.X.Type(), v.Field, f.e, v.Pos()))
	case *ssa.FieldAddr:
		st := v.X.Type().Underlying().(*types.Pointer).Elem()
		return fmt.Sprintf("(%s).project(|p| &p.%s)", f.val(v.X), f.e.types.field(st, v.Field, f.e, v.Pos()))
	case *ssa.IndexAddr:
		switch t := v.X.Type().Underlying().(type) {
		case *types.Pointer:
			if arr, ok := t.Elem().Underlying().(*types.Array); ok {
				return fmt.Sprintf("(%s).project(|a| &a[%s])", f.val(v.X), f.index(v.Index, arr.Len()))
			}
		case *types.Slice:
			at := "at"
			if isUnsigned(v.Index.Type()) {
				at = "at_u"
			}
			return fmt.Sprintf("(%s).%s(%s as %s)", f.val(v.X), at, f.val(v.Index), indexCast(v.Index.Type()))
		}
		f.errorf(v.Pos(), "indexing %s is not supported yet (roadmap M1)", v.X.Type())
	case *ssa.Index:
		switch t := v.X.Type().Underlying().(type) {
		case *types.Array:
			return fmt.Sprintf("(%s)[%s]", f.val(v.X), f.index(v.Index, t.Len()))
		case *types.Basic:
			if t.Info()&types.IsString != 0 {
				if isUnsigned(v.Index.Type()) {
					return fmt.Sprintf("(%s).at_u(%s as u64)", f.val(v.X), f.val(v.Index))
				}
				return fmt.Sprintf("(%s).at(%s as i64)", f.val(v.X), f.val(v.Index))
			}
		}
		f.errorf(v.Pos(), "indexing %s is not supported yet (roadmap M1)", v.X.Type())
	case *ssa.Slice:
		lo := "0i64"
		if v.Low != nil {
			lo = f.val(v.Low) + " as i64"
		}
		hi := "None"
		if v.High != nil {
			hi = "Some(" + f.val(v.High) + " as i64)"
		}
		if isString(v.X.Type()) {
			return fmt.Sprintf("(%s).slice(%s, %s)", f.val(v.X), lo, hi)
		}
		max := "None"
		if v.Max != nil {
			max = "Some(" + f.val(v.Max) + " as i64)"
		}
		x := f.val(v.X)
		if _, ok := v.X.Type().Underlying().(*types.Pointer); ok {
			x = "(" + x + ").to_slice()" // a[:] on a pointer to an array
		}
		return fmt.Sprintf("(%s).slice(%s, %s, %s)", x, lo, hi, max)
	case *ssa.MakeSlice:
		elem := v.Type().Underlying().(*types.Slice).Elem()
		return fmt.Sprintf("Slice::<%s>::make(%s as i64, %s as i64)",
			f.place(elem, v.Pos()), f.val(v.Len), f.val(v.Cap))
	case *ssa.Range:
		return fmt.Sprintf("(%s).iter()", f.val(v.X))
	case *ssa.Next:
		if !v.IsString {
			f.errorf(v.Pos(), "range over maps is not supported yet (roadmap M1)")
			return ""
		}
		it := v.Iter.Name()
		if f.cells[v.Iter] {
			// The iterator lives in a cell so the collector can trace the
			// string it holds; step it and put it back.
			return fmt.Sprintf("{ let mut it = %s.load(); let r = it.advance(); %s.store(it); r }", it, it)
		}
		return it + ".advance()"
	case *ssa.MakeInterface:
		// Only reachable as a panic operand, which panic() handles itself.
		if onlyPanicUses(v) {
			return ""
		}
		f.errorf(v.Pos(), "interfaces are not supported yet (roadmap M1)")
	case *ssa.MakeClosure:
		fn := v.Fn.(*ssa.Function)
		st := f.e.envStruct(fn)
		info := f.e.types.structInfo(st, fn.Name()+"$env", f.e, v.Pos())
		fields := make([]string, len(v.Bindings))
		for i, b := range v.Bindings {
			fields[i] = fmt.Sprintf("%s: %s", info.fields[i], f.val(b))
		}
		env := fmt.Sprintf("Ptr::<crate::ty::%s_P>::alloc(crate::ty::%s { %s })",
			info.name, info.name, strings.Join(fields, ", "))
		return fmt.Sprintf("Func::new(%s as %s, Env::of(%s))",
			f.e.fnPath(fn), f.e.types.fnPtr(fn.Signature, f.e, v.Pos()), env)
	case *ssa.SliceToArrayPointer:
		f.errorf(v.Pos(), "slice-to-array-pointer conversion is not supported yet")
	case *ssa.MakeMap, *ssa.Lookup:
		f.errorf(v.Pos(), "maps are not supported yet (roadmap M1)")
	case *ssa.MakeChan, *ssa.Select:
		f.errorf(v.Pos(), "channels are not supported yet (roadmap M2)")
	case *ssa.TypeAssert, *ssa.ChangeInterface:
		f.errorf(v.Pos(), "interfaces are not supported yet (roadmap M1)")
	default:
		f.errorf(v.Pos(), "instruction %T is not supported yet", v)
	}
	return ""
}

func onlyPanicUses(v ssa.Value) bool {
	for _, r := range *v.Referrers() {
		if _, ok := r.(*ssa.Panic); !ok {
			return false
		}
	}
	return true
}

// index returns the checked usize index expression for i against length n.
func (f *fnEmitter) index(i ssa.Value, n int64) string {
	if isUnsigned(i.Type()) {
		return fmt.Sprintf("rustygo::ops::index_u(%s as u64, %d)", f.val(i), n)
	}
	return fmt.Sprintf("rustygo::ops::index(%s as i64, %d)", f.val(i), n)
}

func (f *fnEmitter) binop(v *ssa.BinOp) string {
	x, y := f.val(v.X), f.val(v.Y)
	b := basicInfo(v.X.Type())
	isInt := b != nil && b.Info()&types.IsInteger != 0
	switch v.Op {
	case token.EQL, token.NEQ:
		// The only comparison Go allows on a slice is against nil.
		if _, ok := v.X.Type().Underlying().(*types.Slice); ok {
			return sliceNilTest(x, v.Op)
		}
		if _, ok := v.Y.Type().Underlying().(*types.Slice); ok {
			return sliceNilTest(y, v.Op)
		}
		return fmt.Sprintf("(%s %s %s)", x, v.Op, y)
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		return fmt.Sprintf("(%s %s %s)", x, v.Op, y)
	case token.SHL, token.SHR:
		var count string
		if isUnsigned(v.Y.Type()) {
			count = y + " as u64"
		} else {
			count = "rustygo::ops::shift_count(" + y + " as i64)"
		}
		method := "go_shl"
		if v.Op == token.SHR {
			method = "go_shr"
		}
		return fmt.Sprintf("(%s).%s(%s)", x, method, count)
	}
	if b == nil {
		f.errorf(v.Pos(), "operator %s on %s is not supported", v.Op, v.X.Type())
		return ""
	}
	if b.Info()&types.IsString != 0 && v.Op == token.ADD {
		return fmt.Sprintf("(%s).concat(%s)", x, y)
	}
	if b.Info()&types.IsComplex != 0 {
		f.errorf(v.Pos(), "complex numbers are not supported yet (roadmap M1)")
		return ""
	}
	if isInt {
		switch v.Op {
		case token.ADD:
			return fmt.Sprintf("(%s).wrapping_add(%s)", x, y)
		case token.SUB:
			return fmt.Sprintf("(%s).wrapping_sub(%s)", x, y)
		case token.MUL:
			return fmt.Sprintf("(%s).wrapping_mul(%s)", x, y)
		case token.QUO:
			return fmt.Sprintf("(%s).go_div(%s)", x, y)
		case token.REM:
			return fmt.Sprintf("(%s).go_rem(%s)", x, y)
		case token.AND_NOT:
			return fmt.Sprintf("(%s & !%s)", x, y)
		}
	}
	switch v.Op {
	case token.ADD, token.SUB, token.MUL, token.QUO, token.AND, token.OR, token.XOR:
		return fmt.Sprintf("(%s %s %s)", x, v.Op, y)
	}
	f.errorf(v.Pos(), "operator %s on %s is not supported", v.Op, v.X.Type())
	return ""
}

func (f *fnEmitter) unop(v *ssa.UnOp) string {
	x := f.val(v.X)
	switch v.Op {
	case token.MUL:
		return fmt.Sprintf("(%s).load()", x)
	case token.NOT, token.XOR:
		return fmt.Sprintf("(!%s)", x)
	case token.SUB:
		if b := basicInfo(v.X.Type()); b != nil && b.Info()&types.IsInteger != 0 {
			return fmt.Sprintf("(%s).wrapping_neg()", x)
		}
		return fmt.Sprintf("(-%s)", x)
	case token.ARROW:
		f.errorf(v.Pos(), "channels are not supported yet (roadmap M2)")
		return ""
	}
	f.errorf(v.Pos(), "unary %s is not supported", v.Op)
	return ""
}

func (f *fnEmitter) convert(v *ssa.Convert) string {
	from, to := basicInfo(v.X.Type()), basicInfo(v.Type())
	x := f.val(v.X)
	// string <-> []byte and []rune.
	if s, ok := v.Type().Underlying().(*types.Slice); ok && isString(v.X.Type()) {
		if eb := basicInfo(s.Elem()); eb != nil {
			switch eb.Kind() {
			case types.Uint8:
				return fmt.Sprintf("Slice::<Slot<u8>>::of_str(%s)", x)
			case types.Int32:
				return fmt.Sprintf("Slice::<Slot<i32>>::of_str(%s)", x)
			}
		}
	}
	if s, ok := v.X.Type().Underlying().(*types.Slice); ok && isString(v.Type()) {
		if eb := basicInfo(s.Elem()); eb != nil {
			switch eb.Kind() {
			case types.Uint8, types.Int32:
				return fmt.Sprintf("(%s).to_str()", x)
			}
		}
	}
	if from != nil && to != nil {
		fi, ti := from.Info(), to.Info()
		switch {
		case fi&types.IsInteger != 0 && ti&types.IsString != 0:
			return fmt.Sprintf("GoStr::from_rune(%s as i64)", x)
		case fi&types.IsFloat != 0 && ti&types.IsInteger != 0:
			return floatToInt(x, to.Kind())
		case fi&(types.IsInteger|types.IsFloat) != 0 && ti&(types.IsInteger|types.IsFloat) != 0:
			return fmt.Sprintf("(%s as %s)", x, basicTypes[to.Kind()])
		}
	}
	f.errorf(v.Pos(), "conversion from %s to %s is not supported yet", v.X.Type(), v.Type())
	return ""
}

// sliceNilTest compares a slice against nil.
func sliceNilTest(x string, op token.Token) string {
	if op == token.EQL {
		return fmt.Sprintf("(%s).is_nil()", x)
	}
	return fmt.Sprintf("(!(%s).is_nil())", x)
}

// indexCast is the Rust type an index widens to before a bounds check.
func indexCast(t types.Type) string {
	if isUnsigned(t) {
		return "u64"
	}
	return "i64"
}

// floatToInt converts a float expression to an integer kind the way gc
// does on the target architecture (rustygo::ops::float).
func floatToInt(x string, to types.BasicKind) string {
	wide := "(" + x + " as f64)"
	switch to {
	case types.Int, types.Int64:
		return "rustygo::ops::float::to_i64" + wide
	case types.Int32:
		return "rustygo::ops::float::to_i32" + wide
	case types.Uint, types.Uint64, types.Uintptr:
		return "rustygo::ops::float::to_u64" + wide
	case types.Uint32:
		return "rustygo::ops::float::to_u32" + wide
	}
	return fmt.Sprintf("(rustygo::ops::float::to_i32%s as %s)", wide, basicTypes[to])
}

func (f *fnEmitter) call(v *ssa.Call) string {
	c := v.Common()
	if c.IsInvoke() {
		f.errorf(v.Pos(), "interface method calls are not supported yet (roadmap M1)")
		return ""
	}
	args := make([]string, len(c.Args))
	for i, a := range c.Args {
		args[i] = f.val(a)
	}
	if b, ok := c.Value.(*ssa.Builtin); ok {
		return f.builtin(v, b, args)
	}
	if callee := c.StaticCallee(); callee != nil && c.Value == callee {
		return fmt.Sprintf("%s(%s)", f.e.fnPath(callee), strings.Join(args, ", "))
	}
	// A call through a func value: code and environment come from the value.
	fv := f.val(c.Value)
	call := append([]string{"__f.env()"}, args...)
	return fmt.Sprintf("{ let __f = %s; (__f.code())(%s) }", fv, strings.Join(call, ", "))
}

func (f *fnEmitter) builtin(v *ssa.Call, b *ssa.Builtin, args []string) string {
	c := v.Common()
	switch b.Name() {
	case "print", "println":
		parts := make([]string, len(c.Args))
		for i, a := range c.Args {
			parts[i] = f.printArg(a)
		}
		return fmt.Sprintf("rustygo::print::%s(&[%s])", b.Name(), strings.Join(parts, ", "))
	case "len", "cap":
		switch c.Args[0].Type().Underlying().(type) {
		case *types.Slice:
			return fmt.Sprintf("(%s).%s()", args[0], b.Name())
		case *types.Basic:
			if b.Name() == "len" && isString(c.Args[0].Type()) {
				return fmt.Sprintf("(%s).len()", args[0])
			}
		}
	case "append":
		// go/ssa always passes the elements as one slice (or a string, for
		// append([]byte, string...)).
		if isString(c.Args[1].Type()) {
			return fmt.Sprintf("(%s).append_str(%s)", args[0], args[1])
		}
		return fmt.Sprintf("(%s).append_slice(%s)", args[0], args[1])
	case "copy":
		if isString(c.Args[1].Type()) {
			return fmt.Sprintf("(%s).copy_from(Slice::of_str(%s))", args[0], args[1])
		}
		return fmt.Sprintf("(%s).copy_from(%s)", args[0], args[1])
	case "min", "max":
		if bi := basicInfo(v.Type()); bi != nil && bi.Info()&(types.IsInteger|types.IsString) != 0 {
			expr := args[0]
			for _, a := range args[1:] {
				expr = fmt.Sprintf("core::cmp::%s(%s, %s)", b.Name(), expr, a)
			}
			return expr
		}
	}
	f.errorf(v.Pos(), "builtin %s on these operands is not supported yet", b.Name())
	return ""
}

// printArg classifies an operand of print/println (and of panic).
func (f *fnEmitter) printArg(a ssa.Value) string {
	if b := basicInfo(a.Type()); b != nil && b.Kind() == types.UntypedNil {
		return "rustygo::print::Arg::Nil"
	}
	x := f.val(a)
	if _, ok := a.Type().Underlying().(*types.Pointer); ok {
		return fmt.Sprintf("rustygo::print::Arg::Pointer((%s).addr())", x)
	}
	if _, ok := a.Type().Underlying().(*types.Signature); ok {
		return fmt.Sprintf("rustygo::print::Arg::Pointer((%s).addr())", x)
	}
	if _, ok := a.Type().Underlying().(*types.Slice); ok {
		return fmt.Sprintf("rustygo::print::Arg::Slice { len: (%s).len(), cap: (%s).cap(), addr: (%s).addr() }", x, x, x)
	}
	b := basicInfo(a.Type())
	if b != nil {
		info := b.Info()
		switch {
		case info&types.IsBoolean != 0:
			return fmt.Sprintf("rustygo::print::Arg::Bool(%s)", x)
		case info&types.IsString != 0:
			return fmt.Sprintf("rustygo::print::Arg::Str((%s).bytes())", x)
		case info&types.IsUnsigned != 0:
			return fmt.Sprintf("rustygo::print::Arg::Uint(%s as u64)", x)
		case info&types.IsInteger != 0:
			return fmt.Sprintf("rustygo::print::Arg::Int(%s as i64)", x)
		case b.Kind() == types.Float32:
			return fmt.Sprintf("rustygo::print::Arg::Float32(%s)", x)
		case info&types.IsFloat != 0:
			return fmt.Sprintf("rustygo::print::Arg::Float64(%s)", x)
		}
	}
	f.errorf(a.Pos(), "printing a %s is not supported yet", a.Type())
	return "rustygo::print::Arg::Nil"
}

func (f *fnEmitter) panic(p *ssa.Panic) string {
	mi, ok := p.X.(*ssa.MakeInterface)
	if !ok {
		if c, ok := p.X.(*ssa.Const); ok && c.IsNil() {
			return "rustygo::panic::panic_nil();"
		}
		f.errorf(p.Pos(), "panic with a %s value is not supported yet (roadmap M1)", p.X.Type())
		return ""
	}
	t := mi.X.Type()
	if basicInfo(t) == nil {
		f.errorf(p.Pos(), "panic with a %s value is not supported yet (roadmap M1)", t)
		return ""
	}
	arg := f.printArg(mi.X)
	if n, ok := types.Unalias(t).(*types.Named); ok {
		name := n.Obj().Name()
		if pkg := n.Obj().Pkg(); pkg != nil {
			name = pkg.Name() + "." + name
		}
		return fmt.Sprintf("rustygo::panic::panic_custom(%q, %s);", name, arg)
	}
	return fmt.Sprintf("rustygo::panic::panic_value(%s);", arg)
}

// val returns the expression for an operand.
func (f *fnEmitter) val(val ssa.Value) string {
	switch v := val.(type) {
	case *ssa.Const:
		return f.constant(v)
	case *ssa.Parameter:
		for i, p := range f.fn.Params {
			if p == v {
				return fmt.Sprintf("a%d", i)
			}
		}
	case *ssa.Global:
		return f.e.globalPath(v) + "()"
	case *ssa.Function:
		// A plain function used as a value: its shim takes an environment.
		return fmt.Sprintf("Func::new(%s as %s, Env::NONE)",
			f.e.shimPath(v), f.e.types.fnPtr(v.Signature, f.e, v.Pos()))
	case *ssa.FreeVar:
		for i, fv := range f.fn.FreeVars {
			if fv == v {
				st := f.e.envStruct(f.fn)
				name := f.e.types.structInfo(st, f.fn.Name()+"$env", f.e, v.Pos()).fields[i]
				return fmt.Sprintf("(__e).project(|e| &e.%s).load()", name)
			}
		}
	case ssa.Instruction:
		if f.cells[val] {
			return val.Name() + ".load()"
		}
		return val.Name()
	}
	f.errorf(val.Pos(), "operand %T is not supported", val)
	return "()"
}

func (f *fnEmitter) constant(c *ssa.Const) string {
	if c.Value == nil {
		return fmt.Sprintf("<%s as GoValue>::zero()", f.typ(c.Type(), c.Pos()))
	}
	b := basicInfo(c.Type())
	if b == nil {
		f.errorf(c.Pos(), "constant of type %s is not supported", c.Type())
		return "()"
	}
	rt := basicTypes[b.Kind()]
	switch {
	case b.Info()&types.IsBoolean != 0:
		return strconv.FormatBool(constant.BoolVal(c.Value))
	case b.Info()&types.IsString != 0:
		return "GoStr::lit(" + byteString(constant.StringVal(c.Value)) + ")"
	case b.Info()&types.IsUnsigned != 0:
		u, _ := constant.Uint64Val(c.Value)
		return fmt.Sprintf("%d%s", u, rt)
	case b.Info()&types.IsInteger != 0:
		i, _ := constant.Int64Val(c.Value)
		if i == math.MinInt64 {
			return "i64::MIN"
		}
		if i < 0 {
			return fmt.Sprintf("(%d%s)", i, rt)
		}
		return fmt.Sprintf("%d%s", i, rt)
	case b.Kind() == types.Float32:
		x, _ := constant.Float32Val(c.Value)
		return fmt.Sprintf("f32::from_bits(%#x)", math.Float32bits(x))
	case b.Info()&types.IsFloat != 0:
		x, _ := constant.Float64Val(c.Value)
		return fmt.Sprintf("f64::from_bits(%#x)", math.Float64bits(x))
	}
	f.errorf(c.Pos(), "constant of type %s is not supported yet", c.Type())
	return "()"
}

// byteString renders s as a Rust byte-string literal.
func byteString(s string) string {
	var b strings.Builder
	b.WriteString(`b"`)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func isString(t types.Type) bool {
	b := basicInfo(t)
	return b != nil && b.Info()&types.IsString != 0
}

func isUnsigned(t types.Type) bool {
	b := basicInfo(t)
	return b != nil && b.Info()&types.IsUnsigned != 0
}
