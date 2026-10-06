package emit

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// Interfaces: type descriptors and method ids (DESIGN §6).
//
// Every concrete type that reaches an interface gets a static `TypeDesc` in
// the `ty` module: its name as Go prints it, its method set as (id, wrapper)
// pairs sorted by id, how to compare two values, and how `print` and `panic`
// render one.
//
// A method id numbers a distinct method name and signature across the whole
// program, so a call through an interface looks up the concrete method
// without knowing which interface it came from. Unexported methods include
// their package path, because Go scopes them that way.

// methodID returns the id of a method name and signature.
func (e *emitter) methodID(fn *types.Func) uint32 {
	key := fn.Name() + "|" + signatureKey(fn.Type().(*types.Signature))
	if !fn.Exported() && fn.Pkg() != nil {
		key = fn.Pkg().Path() + "." + key
	}
	if e.methodIDs == nil {
		e.methodIDs = map[string]uint32{}
	}
	id, ok := e.methodIDs[key]
	if !ok {
		id = uint32(len(e.methodIDs))
		e.methodIDs[key] = id
	}
	return id
}

// signatureKey renders a signature by its types alone. Parameter names must
// not take part: an interface may declare `Eval(env *Env) int` while the
// method implementing it is written `Eval(*Env) int`, and the two have to
// come out with the same id.
func signatureKey(sig *types.Signature) string {
	var b strings.Builder
	b.WriteByte('(')
	for i := 0; i < sig.Params().Len(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(types.TypeString(unaliased(sig.Params().At(i).Type()), qualifiedPath))
	}
	if sig.Variadic() {
		b.WriteString("...")
	}
	b.WriteString(")(")
	for i := 0; i < sig.Results().Len(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(types.TypeString(unaliased(sig.Results().At(i).Type()), qualifiedPath))
	}
	b.WriteByte(')')
	return b.String()
}

// unaliased rebuilds t with every alias replaced by what it names, wherever it
// is nested.
//
// An alias is a second spelling of one type, and the two spell differently:
// `os.FileMode` is `io/fs.FileMode` and `any` is `interface{}`. A method id is
// a name and a signature rendered as text, and `os.fileStat.Mode` returns the
// first spelling while `fs.FileInfo.Mode` returns the second — so the method
// and the interface method it implements were getting different ids, and the
// call through the interface could not find it. Recursion stops at a named
// type, which is its own spelling.
func unaliased(t types.Type) types.Type {
	switch t := types.Unalias(t).(type) {
	case *types.Basic:
		// `byte` and `rune` are not aliases in go/types but separate basic
		// types with the same kinds, so that a printer can show the name that
		// was written. Go shows what the type is: `[]uint8`, never `[]byte`.
		return types.Typ[t.Kind()]
	case *types.Pointer:
		return types.NewPointer(unaliased(t.Elem()))
	case *types.Slice:
		return types.NewSlice(unaliased(t.Elem()))
	case *types.Array:
		return types.NewArray(unaliased(t.Elem()), t.Len())
	case *types.Chan:
		return types.NewChan(t.Dir(), unaliased(t.Elem()))
	case *types.Map:
		return types.NewMap(unaliased(t.Key()), unaliased(t.Elem()))
	case *types.Signature:
		return types.NewSignatureType(nil, nil, nil,
			unaliasedTuple(t.Params()), unaliasedTuple(t.Results()), t.Variadic())
	case *types.Struct:
		fields := make([]*types.Var, t.NumFields())
		tags := make([]string, t.NumFields())
		for i := range fields {
			f := t.Field(i)
			fields[i] = types.NewField(f.Pos(), f.Pkg(), f.Name(), unaliased(f.Type()), f.Embedded())
			tags[i] = t.Tag(i)
		}
		return types.NewStruct(fields, tags)
	default:
		return t
	}
}

// unaliasedTuple is a tuple of the same types with their aliases resolved and
// their names dropped, since a signature's text is its types alone.
func unaliasedTuple(tup *types.Tuple) *types.Tuple {
	vars := make([]*types.Var, tup.Len())
	for i := range vars {
		vars[i] = types.NewParam(token.NoPos, nil, "", unaliased(tup.At(i).Type()))
	}
	return types.NewTuple(vars...)
}

// qualifiedPath names packages by import path, so ids are stable and
// distinct across packages.
func qualifiedPath(p *types.Package) string { return p.Path() }

// goName is the type's name as Go prints it: `%T`, a `reflect.Type`'s String,
// and a panic's message all use this spelling.
//
// It is not quite go/types' spelling. Go writes `struct { A int }` and
// `interface {}` where go/types writes `struct{A int}` and `any`, and a type
// is named by what it is rather than by what it was called, so an alias is
// resolved (`[]byte` is `[]uint8`) and a parameter's name is dropped
// (`func(int)`, never `func(d int)`). Packages are named, not pathed, exactly
// as gc's panic messages do it.
func goName(t types.Type) string {
	var b strings.Builder
	writeGoName(&b, t)
	return b.String()
}

func writeGoName(b *strings.Builder, t types.Type) {
	switch t := types.Unalias(t).(type) {
	case *types.Basic:
		// `byte` and `rune` are separate basic types with the same kinds as
		// `uint8` and `int32`, so that a printer can show the name that was
		// written. Go shows what the type is.
		b.WriteString(types.Typ[t.Kind()].Name())
	case *types.Named:
		obj := t.Obj()
		if obj.Pkg() != nil {
			b.WriteString(obj.Pkg().Name())
			b.WriteByte('.')
		}
		b.WriteString(obj.Name())
		if args := t.TypeArgs(); args != nil {
			b.WriteByte('[')
			for i := 0; i < args.Len(); i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				writeGoName(b, args.At(i))
			}
			b.WriteByte(']')
		}
	case *types.Pointer:
		b.WriteByte('*')
		writeGoName(b, t.Elem())
	case *types.Slice:
		b.WriteString("[]")
		writeGoName(b, t.Elem())
	case *types.Array:
		fmt.Fprintf(b, "[%d]", t.Len())
		writeGoName(b, t.Elem())
	case *types.Map:
		b.WriteString("map[")
		writeGoName(b, t.Key())
		b.WriteByte(']')
		writeGoName(b, t.Elem())
	case *types.Chan:
		switch t.Dir() {
		case types.SendOnly:
			b.WriteString("chan<- ")
		case types.RecvOnly:
			b.WriteString("<-chan ")
		default:
			b.WriteString("chan ")
		}
		writeGoName(b, t.Elem())
	case *types.Signature:
		b.WriteString("func")
		writeGoSignature(b, t)
	case *types.Struct:
		if t.NumFields() == 0 {
			b.WriteString("struct {}")
			return
		}
		b.WriteString("struct { ")
		for i := 0; i < t.NumFields(); i++ {
			if i > 0 {
				b.WriteString("; ")
			}
			f := t.Field(i)
			// An embedded field is spelled by its type alone.
			if !f.Embedded() {
				b.WriteString(f.Name())
				b.WriteByte(' ')
			}
			writeGoName(b, f.Type())
			if tag := t.Tag(i); tag != "" {
				b.WriteByte(' ')
				b.WriteString(strconv.Quote(tag))
			}
		}
		b.WriteString(" }")
	case *types.Interface:
		if t.NumMethods() == 0 {
			b.WriteString("interface {}")
			return
		}
		b.WriteString("interface { ")
		methods := make([]*types.Func, t.NumMethods())
		for i := range methods {
			methods[i] = t.Method(i)
		}
		sort.Slice(methods, func(i, j int) bool { return methods[i].Name() < methods[j].Name() })
		for i, m := range methods {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(m.Name())
			writeGoSignature(b, m.Signature())
		}
		b.WriteString(" }")
	case *types.Tuple:
		// Not a type a program can write; the emitter uses one for multiple
		// results.
		b.WriteByte('(')
		for i := 0; i < t.Len(); i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			writeGoName(b, t.At(i).Type())
		}
		b.WriteByte(')')
	default:
		b.WriteString(types.TypeString(t, func(p *types.Package) string { return p.Name() }))
	}
}

// writeGoSignature writes a signature's parameters and results, without the
// `func` and without any parameter's name.
func writeGoSignature(b *strings.Builder, sig *types.Signature) {
	b.WriteByte('(')
	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		if sig.Variadic() && i == params.Len()-1 {
			b.WriteString("...")
			if s, ok := params.At(i).Type().Underlying().(*types.Slice); ok {
				writeGoName(b, s.Elem())
				continue
			}
		}
		writeGoName(b, params.At(i).Type())
	}
	b.WriteByte(')')
	switch res := sig.Results(); res.Len() {
	case 0:
	case 1:
		b.WriteByte(' ')
		writeGoName(b, res.At(0).Type())
	default:
		b.WriteString(" (")
		for i := 0; i < res.Len(); i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			writeGoName(b, res.At(i).Type())
		}
		b.WriteByte(')')
	}
}

// goIfaceName is the static interface type as gc names it in an interface
// conversion panic: a named type by name, and the empty interface as
// `interface {}` rather than go/types' `any`.
func goIfaceName(t types.Type) string {
	if _, named := types.Unalias(t).(*types.Named); named {
		return goName(t)
	}
	if it, ok := t.Underlying().(*types.Interface); ok && it.NumMethods() == 0 {
		return "interface {}"
	}
	return strings.Replace(goName(t), "interface{", "interface {", 1)
}

// typeDesc returns the Rust path of t's type descriptor, emitting it, its
// method wrappers and its comparison and printing functions on first use.
func (e *emitter) typeDesc(t types.Type, pos token.Pos) string {
	// Keyed by type identity, not by spelling: `byte` and `uint8` are one
	// type with two names, and an assertion across the two has to find the
	// same descriptor.
	if p, ok := e.descs.At(t).(string); ok {
		return p
	}
	key := types.TypeString(t, qualifiedPath)
	name := e.types.ns.claim("TD_" + mangle(strings.NewReplacer("*", "ptr_", ".", "_", "/", "_").Replace(key)))
	// A descriptor sits with its type, so that the method wrappers and the
	// element descriptors it points at are already compiled below it.
	band := e.bands.typ(t)
	path := crateName(band) + "::ty::" + name
	e.descs.Set(t, path)
	if _, isPtr := types.Unalias(t).(*types.Pointer); isPtr {
		// What the next pass consults to decide whether `*T` already has a
		// descriptor of its own (pointerDesc).
		e.ptrSeen.Set(t, true)
	}
	if at, isArray := types.Unalias(t).(*types.Array); isArray {
		// And the same for `reflect.ArrayOf`, which an element type answers
		// from the arrays of it that exist (arrayDescs).
		lens, _ := e.arraySeen.At(at.Elem()).([]int64)
		e.arraySeen.Set(at.Elem(), append(lens, at.Len()))
	}

	place := e.types.place(t, e, pos)
	methods := e.methodTable(t, pos)
	ifaceMethods := e.ifaceMethodIDs(t)
	reflectMethods := e.reflectMethods(t, pos)
	numMethods := exportedMethods(t)
	equal, hash := "None", "None"
	if types.Comparable(t) {
		equal = fmt.Sprintf("Some(|a, b| a.cast::<%s>().load() == b.cast::<%s>().load())", place, place)
		hash = fmt.Sprintf("Some(|d| GoKey::go_hash(&d.cast::<%s>().load()))", place)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\npub static %s: TypeDesc = TypeDesc {\n    name: %q,\n    short: %q,\n    pkg_path: %q,\n    kind: %d,\n    size: %d,\n    align: %d,\n    methods: &[%s],\n    equal: %s,\n    hash: %s,\n    print: |d, out| { %s },\n    box_value: |p| Data::of(Ptr::<%s>::alloc(unsafe { p.to_ptr::<%s>() }.load())),\n    zero: || Data::of(Ptr::<%s>::alloc(GoValue::zero())),\n",
		name, goName(t), shortName(t), pkgPath(t), reflectKind(t), e.sizeof(t), e.alignof(t),
		methods, equal, hash, e.printValue(t, place, pos), place, place, place)
	// What reflection needs to walk a value's structure.
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		fmt.Fprintf(&b, "    elem: Some(&%s),\n", e.typeDesc(u.Elem(), pos))
	case *types.Slice:
		// `make` for this slice type, which is what `reflect.MakeSlice` needs:
		// the elements have to be traced as this element type. The slice is
		// rooted across the allocation that boxes it, which nothing else
		// refers to it through yet.
		fmt.Fprintf(&b, "    elem: Some(&%s),\n    make_slice: Some(|__len, __cap| {\n"+
			"        let __s = Slice::<%s>::make(__len, __cap);\n"+
			"        let __roots = rustygo::gc::Frame::<1>::new();\n"+
			"        __roots.scope(|| {\n            __roots.set(0, &__s);\n"+
			"            Data::of(Ptr::<%s>::alloc(__s))\n        })\n    }),\n",
			e.typeDesc(u.Elem(), pos), e.types.place(u.Elem(), e, pos), place)
	case *types.Array:
		fmt.Fprintf(&b, "    elem: Some(&%s),\n    len: %d,\n", e.typeDesc(u.Elem(), pos), u.Len())
	case *types.Chan:
		// `reflect.ChanDir`'s own numbering: receive is 1, send is 2, both is
		// the two together.
		dir := 3
		switch u.Dir() {
		case types.RecvOnly:
			dir = 1
		case types.SendOnly:
			dir = 2
		}
		fmt.Fprintf(&b, "    elem: Some(&%s),\n    chan_dir: %d,\n    chan_ops: Some(&%s),\n",
			e.typeDesc(u.Elem(), pos), dir, e.chanOps(u, pos))
	case *types.Map:
		fmt.Fprintf(&b, "    elem: Some(&%s),\n    key: Some(&%s),\n    map_ops: Some(&%s),\n",
			e.typeDesc(u.Elem(), pos), e.typeDesc(u.Key(), pos), e.mapOps(u, pos))
	case *types.Struct:
		fmt.Fprintf(&b, "    fields: &[%s],\n", e.fieldDescs(t, u, pos))
	case *types.Signature:
		// What reflection asks a func type: its parameters, its results, and
		// how to call it.
		fmt.Fprintf(&b, "    params: &[%s],\n    results: &[%s],\n",
			e.descList(u.Params(), pos), e.descList(u.Results(), pos))
		if u.Variadic() {
			b.WriteString("    variadic: true,\n")
		}
		if shim := e.callShim(u, pos); shim != "" {
			fmt.Fprintf(&b, "    call: Some(%s),\n", shim)
		}
		if shim := e.makeFuncShim(t, u, name, pos); shim != "" {
			fmt.Fprintf(&b, "    make_func: Some(%s),\n", shim)
		}
	}
	if p := e.pointerDesc(t, pos); p != "" {
		fmt.Fprintf(&b, "    ptr: Some(&%s),\n", p)
	}
	if a := e.arraysOf(t, pos); a != "" {
		fmt.Fprintf(&b, "    arrays: &[%s],\n", a)
	}
	if ifaceMethods != "" {
		fmt.Fprintf(&b, "    iface_methods: &[%s],\n", ifaceMethods)
	}
	if numMethods > 0 {
		fmt.Fprintf(&b, "    num_methods: %d,\n", numMethods)
	}
	if reflectMethods != "" {
		fmt.Fprintf(&b, "    reflect_methods: &[%s],\n", reflectMethods)
	}
	b.WriteString("    ..TypeDesc::DEFAULT\n};\n")
	e.types.at(band).WriteString(b.String())
	return path
}

// pointerDesc names the descriptor of `*t` for t's own descriptor to point at,
// or "" to leave the runtime to derive one.
//
// `reflect.New`, `reflect.PointerTo` and `Value.Addr` all hand back a `*T`, and
// the runtime can build most of a pointer's descriptor itself: every pointer is
// one word, compared and boxed the same way (src/reflect.rs). Two things it
// cannot. One is `*T`'s method set, which is what decides whether
// `PointerTo(T).Implements(json.Marshaler)` — the question `encoding/json` asks
// of every type it encodes. The other is Go's promise that a type has one
// descriptor: a value `New` made has to be assignable to a `*T` variable, and
// that test compares descriptor addresses. So the emitted descriptor is named
// here whenever `*T` has methods, or whenever the program already contains
// `*T` — which only a whole pass knows, so the previous one is asked.
//
// The previous pass is not quite the whole answer. Naming `*T` here generates
// its method wrappers, which pull in method bodies this pass is the first to
// emit, and those bodies can mention a pointer type the previous pass never
// saw. If reflection then asks for that one, it gets a derived descriptor
// beside the emitted one and the two are not the same type. Closing that would
// take iterating to a fixed point, which costs a pass for every program that
// uses `reflect.New`; the shape it needs is unusual enough to wait for a
// program that hits it.
func (e *emitter) pointerDesc(t types.Type, pos token.Pos) string {
	if !e.needsReflectPointer {
		return ""
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		// Not a type a program can write: the emitter uses one for multiple
		// results, and nothing points at it.
		return ""
	}
	pt := types.NewPointer(t)
	seen := e.descs.At(pt) != nil || (e.ptrDescs != nil && e.ptrDescs.At(pt) != nil)
	if !seen && types.NewMethodSet(pt).Len() == 0 {
		return ""
	}
	return e.typeDesc(pt, pos)
}

// arraysOf renders the arrays of t the program contains, by length and
// sorted, which is what `reflect.ArrayOf` hands back; "" when the program never
// calls ArrayOf or there are none.
//
// An array cannot be derived the way a pointer can. Its descriptor depends on
// its element at every point — how to box, zero, compare and hash one, and where
// the pointers inside one are for the collector — and none of that follows from
// the element's own descriptor. So this lists the ones that exist, which is also
// what makes the answer the program's own type rather than a second type equal to
// it. In practice it is every array ArrayOf is asked for: `testify` asks for
// `[n]T` of a value it is already holding, so `[n]T` is a type the program has.
func (e *emitter) arraysOf(t types.Type, pos token.Pos) string {
	if !e.needsReflectArrayOf || e.arrayDescs == nil {
		return ""
	}
	lens, _ := e.arrayDescs.At(t).([]int64)
	if len(lens) == 0 {
		return ""
	}
	sorted := append([]int64(nil), lens...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts := make([]string, 0, len(sorted))
	var last int64 = -1
	for _, n := range sorted {
		if n == last {
			continue
		}
		last = n
		parts = append(parts, fmt.Sprintf("(%d, &%s)", n, e.typeDesc(types.NewArray(t, n), pos)))
	}
	return strings.Join(parts, ", ")
}

// descList renders a tuple as a list of descriptor references.
func (e *emitter) descList(tup *types.Tuple, pos token.Pos) string {
	parts := make([]string, tup.Len())
	for i := range parts {
		parts[i] = "&" + e.typeDesc(tup.At(i).Type(), pos)
	}
	return strings.Join(parts, ", ")
}

// shortName is the type's declared name alone, or "" if it has none.
func shortName(t types.Type) string {
	if n, ok := types.Unalias(t).(*types.Named); ok {
		return n.Obj().Name()
	}
	if b, ok := t.(*types.Basic); ok {
		return types.Typ[b.Kind()].Name()
	}
	return ""
}

// pkgPath is the import path of the package defining a named type.
func pkgPath(t types.Type) string {
	if n, ok := types.Unalias(t).(*types.Named); ok && n.Obj().Pkg() != nil {
		return n.Obj().Pkg().Path()
	}
	return ""
}

// reflectKind numbers a type as reflect.Kind does.
func reflectKind(t types.Type) int {
	switch u := t.Underlying().(type) {
	case *types.Basic:
		if k, ok := basicKinds[u.Kind()]; ok {
			return k
		}
	case *types.Array:
		return 17
	case *types.Chan:
		return 18
	case *types.Signature:
		return 19
	case *types.Interface:
		return 20
	case *types.Map:
		return 21
	case *types.Pointer:
		return 22
	case *types.Slice:
		return 23
	case *types.Struct:
		return 25
	}
	return 0 // reflect.Invalid
}

// basicKinds maps the predeclared types to reflect.Kind's numbering.
var basicKinds = map[types.BasicKind]int{
	types.Bool: 1, types.Int: 2, types.Int8: 3, types.Int16: 4, types.Int32: 5,
	types.Int64: 6, types.Uint: 7, types.Uint8: 8, types.Uint16: 9, types.Uint32: 10,
	types.Uint64: 11, types.Uintptr: 12, types.Float32: 13, types.Float64: 14,
	types.Complex64: 15, types.Complex128: 16, types.String: 24, types.UnsafePointer: 26,
}

// sizeof and alignof are gc's, which is how rustygo lays types out too.
func (e *emitter) sizeof(t types.Type) int64  { return e.sizes.Sizeof(t) }
func (e *emitter) alignof(t types.Type) int64 { return e.sizes.Alignof(t) }

// fieldDescs renders a struct's fields for reflection, with gc's offsets.
func (e *emitter) fieldDescs(named types.Type, st *types.Struct, pos token.Pos) string {
	vars := make([]*types.Var, st.NumFields())
	for i := range vars {
		vars[i] = st.Field(i)
	}
	offsets := e.sizes.Offsetsof(vars)
	parts := make([]string, st.NumFields())
	for i, f := range vars {
		pkg := ""
		if !f.Exported() && f.Pkg() != nil {
			pkg = f.Pkg().Path()
		}
		parts[i] = fmt.Sprintf("FieldDesc { name: %q, pkg_path: %q, typ: &%s, offset: %d, tag: %q, embedded: %v }",
			f.Name(), pkg, e.typeDesc(f.Type(), pos), offsets[i], st.Tag(i), f.Embedded())
	}
	return strings.Join(parts, ", ")
}

// mapOps emits the accessors reflection uses on a map of this type, and
// returns the static's path. Our maps are hash tables, so unlike other types
// they cannot be read by address arithmetic.
func (e *emitter) mapOps(mt *types.Map, pos token.Pos) string {
	key := "mapops:" + types.TypeString(mt, qualifiedPath)
	if p, ok := e.wrappers[key]; ok {
		return p
	}
	name := e.types.ns.claim("MO_" + mangle(strings.NewReplacer("*", "ptr_", ".", "_", "/", "_", "[", "_", "]", "_", " ", "_").Replace(types.TypeString(mt, qualifiedPath))))
	if e.wrappers == nil {
		e.wrappers = map[string]string{}
	}
	band := e.bands.typ(mt)
	e.wrappers[key] = crateName(band) + "::ty::" + name

	kPlace, vPlace := e.types.place(mt.Key(), e, pos), e.types.place(mt.Elem(), e, pos)
	kRust, vRust := e.types.rust(mt.Key(), e, pos), e.types.rust(mt.Elem(), e, pos)
	mapType := fmt.Sprintf("GoMap<%s, %s>", kRust, vRust)
	iterType := fmt.Sprintf("MapIter<%s, %s>", kRust, vRust)
	fmt.Fprintf(e.types.at(band), `
pub static %s: MapOps = MapOps {
    len: |d| d.cast::<Slot<%s>>().load().len(),
    iter: |d| Data::of(Ptr::<Slot<%s>>::alloc(d.cast::<Slot<%s>>().load().iter())),
    next: |d| {
        let it = d.cast::<Slot<%s>>();
        let mut cur = it.load();
        let (ok, k, v) = cur.advance();
        it.store(cur);
        // Boxing the value allocates, which can collect the key's box before
        // the caller ever sees it, so the key is rooted across it.
        let key = Ptr::<%s>::alloc(k);
        let __roots = rustygo::gc::Frame::<1>::new();
        __roots.scope(|| {
            __roots.set(0, &key);
            (ok, Data::of(key), Data::of(Ptr::<%s>::alloc(v)))
        })
    },
    index: |d, k| {
        let (v, ok) = d
            .cast::<Slot<%s>>()
            .load()
            .get_ok(k.cast::<%s>().load());
        (Data::of(Ptr::<%s>::alloc(v)), ok)
    },
    clone: |d| {
        // maps.Clone: a map of the same type holding the same entries. The
        // source is rooted while the copy is built, because every insertion
        // may collect — and so is the copy, including across the allocation
        // that boxes it, which nothing else refers to it through yet.
        let src = d.cast::<Slot<%s>>().load();
        let out = GoMap::make(src.len());
        let __roots = rustygo::gc::Frame::<2>::new();
        __roots.scope(|| {
            __roots.set(0, &src);
            __roots.set(1, &out);
            let mut it = src.iter();
            while let (true, k, v) = it.advance() {
                out.set(k, v);
            }
            Data::of(Ptr::<Slot<%s>>::alloc(out))
        })
    },
    make: || {
        // Two allocations: the map, and the box holding it. The second can
        // collect the first, which nothing else refers to yet.
        let m = GoMap::make(0);
        let __roots = rustygo::gc::Frame::<1>::new();
        __roots.scope(|| {
            __roots.set(0, &m);
            Data::of(Ptr::<Slot<%s>>::alloc(m))
        })
    },
    set: |d, k, v| {
        d.cast::<Slot<%s>>()
            .load()
            .set(k.cast::<%s>().load(), v.cast::<%s>().load())
    },
    delete: |d, k| {
        d.cast::<Slot<%s>>()
            .load()
            .delete(k.cast::<%s>().load())
    },
};
`, name, mapType, iterType, mapType, iterType, kPlace, vPlace, mapType, kPlace, vPlace, mapType, mapType,
		mapType, mapType, kPlace, vPlace, mapType, kPlace)
	return crateName(band) + "::ty::" + name
}

// chanOps renders the accessors reflection reaches a channel of this type
// through, the way mapOps does for a map.
//
// A channel is a runtime object rather than memory laid out by Go's rules, so
// nothing can read one by address; and every one of these may block, which is
// the point — `reflect.Value.Recv` parks the goroutine until a value arrives.
// The box for a received value is allocated and rooted *before* the receive, so
// that the allocation cannot collect a value the channel has already handed
// over.
func (e *emitter) chanOps(ct *types.Chan, pos token.Pos) string {
	key := "chanops:" + types.TypeString(ct, qualifiedPath)
	if p, ok := e.wrappers[key]; ok {
		return p
	}
	name := e.types.ns.claim("CO_" + mangle(strings.NewReplacer("*", "ptr_", ".", "_", "/", "_", "[", "_", "]", "_", " ", "_", "<", "_", "-", "_").Replace(types.TypeString(ct, qualifiedPath))))
	if e.wrappers == nil {
		e.wrappers = map[string]string{}
	}
	band := e.bands.typ(ct)
	e.wrappers[key] = crateName(band) + "::ty::" + name

	chanType := fmt.Sprintf("Chan<%s>", e.types.rust(ct.Elem(), e, pos))
	ePlace := e.types.place(ct.Elem(), e, pos)
	fmt.Fprintf(e.types.at(band), `
pub static %s: ChanOps = ChanOps {
    recv: |d| {
        let __b = Ptr::<%s>::alloc(GoValue::zero());
        let __roots = rustygo::gc::Frame::<1>::new();
        __roots.scope(|| {
            __roots.set(0, &__b);
            let (v, ok) = d.cast::<Slot<%s>>().load().recv();
            __b.store(v);
            (Data::of(__b), ok)
        })
    },
    try_recv: |d| {
        let ch = d.cast::<Slot<%s>>().load();
        if !ch.can_recv() {
            return (Data::NONE, false);
        }
        let __b = Ptr::<%s>::alloc(GoValue::zero());
        let __roots = rustygo::gc::Frame::<1>::new();
        __roots.scope(|| {
            __roots.set(0, &__b);
            let (v, ok) = ch.recv();
            __b.store(v);
            (Data::of(__b), ok)
        })
    },
    send: |d, v| d.cast::<Slot<%s>>().load().send(v.cast::<%s>().load()),
    try_send: |d, v| {
        let ch = d.cast::<Slot<%s>>().load();
        if !ch.can_send() {
            return false;
        }
        ch.send(v.cast::<%s>().load());
        true
    },
    len: |d| d.cast::<Slot<%s>>().load().len(),
    cap: |d| d.cast::<Slot<%s>>().load().cap(),
    close: |d| d.cast::<Slot<%s>>().load().close(),
};
`, name, ePlace, chanType, chanType, ePlace, chanType, ePlace, chanType, ePlace,
		chanType, chanType, chanType)
	return crateName(band) + "::ty::" + name
}

// methodTable renders t's method set as (id, wrapper) pairs sorted by id.
func (e *emitter) methodTable(t types.Type, pos token.Pos) string {
	type entry struct {
		id   uint32
		path string
	}
	var entries []entry
	if types.IsInterface(t) {
		// An interface value's methods come from its dynamic type, so the
		// interface type itself has no table.
		return ""
	}
	mset := types.NewMethodSet(t)
	for i := 0; i < mset.Len(); i++ {
		sel := mset.At(i)
		fn, _ := sel.Obj().(*types.Func)
		if fn == nil {
			continue
		}
		entries = append(entries, entry{e.methodID(fn), e.methodWrapper(t, sel, pos)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })
	parts := make([]string, len(entries))
	for i, en := range entries {
		parts[i] = fmt.Sprintf("(%d, ErasedFn::new(%s as *const ()))", en.id, en.path)
	}
	return strings.Join(parts, ", ")
}

// methodWrapper emits a function with the shape interface calls use — the
// data word, then the method's parameters — that unboxes the receiver and
// calls the method.
func (e *emitter) methodWrapper(recvType types.Type, sel *types.Selection, pos token.Pos) string {
	fn := e.res.Prog.MethodValue(sel)
	if fn == nil {
		e.errorf(pos, "method %s of %s has no body", sel.Obj().Name(), recvType)
		return "|| ()"
	}
	key := types.TypeString(recvType, qualifiedPath) + "." + sel.Obj().Name()
	if e.wrappers == nil {
		e.wrappers = map[string]string{}
	}
	if p, ok := e.wrappers[key]; ok {
		return p
	}
	m := e.module(fn.Pkg)
	if fn.Pkg == nil {
		m = e.module(nil)
	}
	name := m.ns.claim(mangle(goName(recvType) + "$" + sel.Obj().Name() + "$iface"))
	// The wrapper sits with the type whose method it calls.
	band := e.bands.typ(recvType)
	path := crateName(band) + "::" + m.name + "::" + name
	e.wrappers[key] = path

	sig := sel.Obj().Type().(*types.Signature)
	place := e.types.place(recvType, e, pos)
	params := []string{"__d: Data"}
	args := []string{fmt.Sprintf("__d.cast::<%s>().load()", place)}
	for i := 0; i < sig.Params().Len(); i++ {
		params = append(params, fmt.Sprintf("a%d: %s", i, e.types.rust(sig.Params().At(i).Type(), e, pos)))
		args = append(args, fmt.Sprintf("a%d", i))
	}
	ret := ""
	switch res := sig.Results(); res.Len() {
	case 0:
	case 1:
		ret = " -> " + e.types.rust(res.At(0).Type(), e, pos)
	default:
		ret = " -> " + e.types.rust(res, e, pos)
	}
	fmt.Fprintf(m.at(band), "\n// %s, called through an interface\npub fn %s(%s)%s {\n    %s(%s)\n}\n",
		key, name, strings.Join(params, ", "), ret, e.fnPath(fn), strings.Join(args, ", "))
	return path
}

// printValue renders the body of a descriptor's print function, following
// gc's `printpanicval`: an error prints its Error(), a Stringer its String(),
// a predeclared basic type its value, a named basic type `main.T(v)`, and
// anything else `(main.T) 0xaddr`.
func (e *emitter) printValue(t types.Type, place string, pos token.Pos) string {
	load := fmt.Sprintf("d.cast::<%s>().load()", place)
	if types.IsInterface(t) {
		// Whatever it holds knows how to print itself.
		return fmt.Sprintf("(%s).print_to(out);", load)
	}
	if m := implementsStringMethod(t, "Error"); m != nil {
		return fmt.Sprintf("let s = %s(d); out.extend_from_slice(s.bytes());", e.methodWrapper(t, m, pos))
	}
	if m := implementsStringMethod(t, "String"); m != nil {
		return fmt.Sprintf("let s = %s(d); out.extend_from_slice(s.bytes());", e.methodWrapper(t, m, pos))
	}
	if b := basicInfo(t); b != nil {
		arg := e.printArgOf(t, load, pos)
		if _, named := types.Unalias(t).(*types.Named); !named {
			return fmt.Sprintf("rustygo::print::format(out, &[%s], false);", arg)
		}
		// A named basic type prints as `main.T(v)`, strings quoted.
		if b.Info()&types.IsString != 0 {
			return fmt.Sprintf("out.extend_from_slice(%s); rustygo::print::format(out, &[%s], false); out.extend_from_slice(b\"\\\")\");",
				byteString(goName(t)+"(\""), arg)
		}
		return fmt.Sprintf("out.extend_from_slice(%s); rustygo::print::format(out, &[%s], false); out.push(b')');",
			byteString(goName(t)+"("), arg)
	}
	// The name may hold a quote of its own — a struct tag inside an anonymous
	// struct type — so it is written as a literal rather than pasted in.
	return fmt.Sprintf("out.extend_from_slice(%s); rustygo::print::format(out, &[rustygo::print::Arg::Pointer(d.addr())], false);",
		byteString("("+goName(t)+") "))
}

// implementsStringMethod returns t's zero-argument, string-returning method
// with this name, if it has one.
func implementsStringMethod(t types.Type, name string) *types.Selection {
	mset := types.NewMethodSet(t)
	for i := 0; i < mset.Len(); i++ {
		sel := mset.At(i)
		if sel.Obj().Name() != name {
			continue
		}
		sig, ok := sel.Obj().Type().(*types.Signature)
		if !ok || sig.Params().Len() != 0 || sig.Results().Len() != 1 {
			continue
		}
		if b, ok := sig.Results().At(0).Type().Underlying().(*types.Basic); ok && b.Info()&types.IsString != 0 {
			return sel
		}
	}
	return nil
}

// ifaceMethods renders the ids and names of an interface's methods, for a
// type assertion to that interface.
func (e *emitter) ifaceMethods(it *types.Interface) (ids, names string) {
	var idParts, nameParts []string
	for i := 0; i < it.NumMethods(); i++ {
		m := it.Method(i)
		idParts = append(idParts, fmt.Sprintf("%d", e.methodID(m)))
		nameParts = append(nameParts, fmt.Sprintf("%q", m.Name()))
	}
	return "&[" + strings.Join(idParts, ", ") + "]", "&[" + strings.Join(nameParts, ", ") + "]"
}

// invoke renders a call through an interface: look the method up by id, then
// call it with the data word.
func (f *fnEmitter) invoke(c *ssa.CallCommon, args []string, pos token.Pos) string {
	return f.invokeOn(c.Method, f.val(c.Value), args, pos)
}

// invokeOn is an interface method call on a receiver the caller spells: the
// call site's operand, or a receiver a deferred call stored away earlier.
func (f *fnEmitter) invokeOn(m *types.Func, recv string, args []string, pos token.Pos) string {
	id := f.e.methodID(m)
	sig := m.Type().(*types.Signature)
	params := []string{"Data"}
	for i := 0; i < sig.Params().Len(); i++ {
		params = append(params, f.typ(sig.Params().At(i).Type(), pos))
	}
	ret := ""
	switch res := sig.Results(); res.Len() {
	case 0:
	case 1:
		ret = " -> " + f.typ(res.At(0).Type(), pos)
	default:
		ret = " -> " + f.typ(res, pos)
	}
	call := append([]string{"__i.data()"}, args...)
	return fmt.Sprintf("{ let __i = %s; (__i.method::<fn(%s)%s>(%d))(%s) }",
		recv, strings.Join(params, ", "), ret, id, strings.Join(call, ", "))
}

// Methods, as reflection hands them out.
//
// `reflect.Type.Method(i).Func` is the method expression `T.M`: a func value
// whose first parameter is the receiver. `reflect.Value.Method(i)` is the
// method value `x.M`: a func value without the receiver, carrying it along.
// Neither is the wrapper interface dispatch uses, which takes the receiver
// boxed and has no environment, so each method needs a pair of its own — and
// the descriptor of each one's type, so that `.Interface()` on either can be
// asserted back to the func type the program wrote.
//
// Only exported methods, which is all `reflect.Type.Method` reports for a
// non-interface type, and only when the program reaches this part of
// reflection: it is a lot of code to generate for a program that never asks.

// callShim renders the closure `reflect.Value.Call` calls a func value of this
// type through, or "" when the program never asks.
//
// The arguments arrive boxed and the results go back boxed, which is how a
// `reflect.Value` holds a value either way. The boxes for the results are
// allocated *before* the call and rooted across each other, so that neither the
// call's own allocations nor the next box's can collect one already made; the
// results are stored into them afterwards, with nothing allocating in between.
func (e *emitter) callShim(sig *types.Signature, pos token.Pos) string {
	if !e.needsReflectCall || sig.Variadic() {
		// A variadic call would have to gather its extra arguments into a
		// slice of a type the program may never have mentioned.
		return ""
	}
	res := sig.Results()
	slots := res.Len()
	if slots == 0 {
		slots = 1 // Frame::<0> would be a frame with nothing in it.
	}
	var b strings.Builder
	fmt.Fprintf(&b, "|__f, __args, __out| {\n        let __roots = rustygo::gc::Frame::<%d>::new();\n        __roots.scope(|| {\n", slots)
	for i := 0; i < res.Len(); i++ {
		fmt.Fprintf(&b, "            let __b%d = Ptr::<%s>::alloc(GoValue::zero());\n            __roots.set(%d, &__b%d);\n",
			i, e.types.place(res.At(i).Type(), e, pos), i, i)
	}
	fmt.Fprintf(&b, "            let __fv = __f.cast::<Slot<Func<%s>>>().load();\n", e.types.fnPtr(sig, e, pos))
	args := []string{"__fv.env()"}
	for i := 0; i < sig.Params().Len(); i++ {
		args = append(args, fmt.Sprintf("__args[%d].cast::<%s>().load()",
			i, e.types.place(sig.Params().At(i).Type(), e, pos)))
	}
	call := fmt.Sprintf("(__fv.code())(%s)", strings.Join(args, ", "))
	switch res.Len() {
	case 0:
		fmt.Fprintf(&b, "            %s;\n", call)
	case 1:
		fmt.Fprintf(&b, "            let __r = %s;\n            __b0.store(__r);\n            __out[0] = Data::of(__b0);\n", call)
	default:
		fmt.Fprintf(&b, "            let __r = %s;\n", call)
		for i := 0; i < res.Len(); i++ {
			fmt.Fprintf(&b, "            __b%d.store(__r.%d);\n            __out[%d] = Data::of(__b%d);\n", i, i, i, i)
		}
	}
	b.WriteString("        })\n    }")
	return b.String()
}

// makeFuncShim writes the trampoline `reflect.MakeFunc` hands back for this
// func type and returns the closure the descriptor's `make_func` holds, or ""
// when the program never asks for MakeFunc.
//
// It is `callShim` run backwards. The trampoline has the signature the program
// wrote, so it can be called as an ordinary func value; its *environment* is the
// boxed `func([]Value) []Value` the program gave MakeFunc. It boxes each
// argument the way an interface conversion does, hands the boxes to reflect's
// own dispatcher — through the runtime, because a trampoline sits with its func
// type and that crate need not be able to name `reflect` (src/reflect.rs) — and
// reads the results back out of the boxes that come back.
//
// Every boxing is a safe point, so an argument that holds references is rooted
// before any of them, and each box is stored into the rooted argument slice
// before the next one is made. That is the whole of the GC rule here.
func (e *emitter) makeFuncShim(t types.Type, sig *types.Signature, desc string, pos token.Pos) string {
	if !e.needsReflectMakeFunc {
		return ""
	}
	band := e.bands.typ(t)
	name := e.types.ns.claim("MF_" + mangle(goName(t)))
	params, res := sig.Params(), sig.Results()
	// One slot for each argument that holds a reference, one for the boxed
	// function and one for the slice the boxes go into.
	rooted := make([]int, 0, params.Len())
	for i := 0; i < params.Len(); i++ {
		if containsRef(params.At(i).Type()) {
			rooted = append(rooted, i)
		}
	}
	var b strings.Builder
	decl := make([]string, 0, params.Len()+1)
	decl = append(decl, "__env: Env")
	for i := 0; i < params.Len(); i++ {
		decl = append(decl, fmt.Sprintf("a%d: %s", i, e.types.rust(params.At(i).Type(), e, pos)))
	}
	ret := ""
	switch res.Len() {
	case 0:
	case 1:
		ret = " -> " + e.types.rust(res.At(0).Type(), e, pos)
	default:
		ret = " -> " + e.types.rust(res, e, pos)
	}
	fmt.Fprintf(&b, "\n// %s, as reflect.MakeFunc hands it back: its environment is the\n// boxed func([]reflect.Value) []reflect.Value the program gave MakeFunc.\npub fn %s(%s)%s {\n",
		goName(t), name, strings.Join(decl, ", "), ret)
	fmt.Fprintf(&b, "    let __roots = rustygo::gc::Frame::<%d>::new();\n", len(rooted)+2)
	for k, i := range rooted {
		fmt.Fprintf(&b, "    __roots.set_local(%d, &a%d);\n", k, i)
	}
	b.WriteString("    __roots.scope(|| {\n")
	// The trampoline stands in for the deferred call rather than being it, so
	// `recover` in what the program handed MakeFunc has to see past this frame
	// (src/gc.rs). gc calls such a frame a wrapper and does not count it.
	b.WriteString("        rustygo::gc::mark_transparent();\n")
	fmt.Fprintf(&b, "        __roots.set(%d, &__env);\n", len(rooted))
	fmt.Fprintf(&b, "        let __args = Slice::<Slot<UPtr>>::make(%d, %d);\n", params.Len(), params.Len())
	fmt.Fprintf(&b, "        __roots.set(%d, &__args);\n", len(rooted)+1)
	for i := 0; i < params.Len(); i++ {
		fmt.Fprintf(&b, "        __args.at(%d).store(UPtr::from_ptr(Ptr::<%s>::alloc(a%d)));\n",
			i, e.types.place(params.At(i).Type(), e, pos), i)
	}
	fmt.Fprintf(&b, "        let __out = rustygo::reflect::make_func_call(UPtr::from_addr(__env.addr()), UPtr::from_addr(&%s as *const TypeDesc as usize as u64), __args);\n", desc)
	load := func(i int) string {
		return fmt.Sprintf("unsafe { __out.at(%d).load().to_ptr::<%s>() }.load()",
			i, e.types.place(res.At(i).Type(), e, pos))
	}
	switch res.Len() {
	case 0:
		b.WriteString("        let _ = __out;\n")
	case 1:
		fmt.Fprintf(&b, "        %s\n", load(0))
	default:
		parts := make([]string, res.Len())
		for i := range parts {
			parts[i] = load(i)
		}
		fmt.Fprintf(&b, "        (%s)\n", strings.Join(parts, ", "))
	}
	b.WriteString("    })\n}\n")
	e.types.at(band).WriteString(b.String())
	// The descriptor's side: a func value over the trampoline, closing over the
	// boxed function, boxed in its turn. The func value is rooted across that
	// second allocation, which nothing else refers to it through yet.
	place := e.types.place(t, e, pos)
	// A func type that contains itself has a Rust type of its own, and the func
	// value has to be put into it (types.go).
	value := "__f"
	if n := e.types.selfValue(t, e, pos); n != "" {
		value = n + "(__f)"
	}
	return fmt.Sprintf("|__u| {\n        let __f = Func::new(%s as %s, Env::of(__u.cast::<Slot<u8>>()));\n"+
		"        let __roots = rustygo::gc::Frame::<1>::new();\n        __roots.scope(|| {\n"+
		"            __roots.set(0, &__f);\n            Data::of(Ptr::<%s>::alloc(%s))\n        })\n    }",
		name, e.types.fnPtr(sig, e, pos), place, value)
}

// ifaceMethodIDs renders an interface type's own method ids, sorted, which is
// what says whether another type implements it.
func (e *emitter) ifaceMethodIDs(t types.Type) string {
	it, ok := t.Underlying().(*types.Interface)
	if !ok {
		return ""
	}
	ids := make([]string, 0, it.NumMethods())
	nums := make([]uint32, 0, it.NumMethods())
	for i := 0; i < it.NumMethods(); i++ {
		nums = append(nums, e.methodID(it.Method(i)))
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	for _, n := range nums {
		ids = append(ids, fmt.Sprint(n))
	}
	return strings.Join(ids, ", ")
}

// exportedMethods counts the methods `reflect.Type.NumMethod` reports, which
// is always in the descriptor: asking how many is not asking which.
func exportedMethods(t types.Type) int {
	n := 0
	mset := types.NewMethodSet(t)
	for i := 0; i < mset.Len(); i++ {
		if fn, ok := mset.At(i).Obj().(*types.Func); ok && fn.Exported() {
			n++
		}
	}
	return n
}

// reflectMethods renders the `reflect_methods` table of t's descriptor, or ""
// when the program has no use for one.
func (e *emitter) reflectMethods(t types.Type, pos token.Pos) string {
	if !e.needsReflectMethods {
		return ""
	}
	var parts []string
	mset := types.NewMethodSet(t)
	for i := 0; i < mset.Len(); i++ {
		sel := mset.At(i)
		fn, _ := sel.Obj().(*types.Func)
		if fn == nil || !fn.Exported() {
			continue
		}
		sig := fn.Type().(*types.Signature)
		if types.IsInterface(t) {
			// An interface type's methods have no bodies to point at. Go says
			// so too: it reports the signature without a receiver, and no
			// func value at all.
			bare := e.typeDesc(withoutRecv(sig), pos)
			parts = append(parts, fmt.Sprintf(
				"MethodDesc { name: %q, pkg_path: \"\", expr_type: &%s, expr: ErasedFn::NONE, value_type: &%s, value: ErasedFn::NONE }",
				fn.Name(), bare, bare))
			continue
		}
		expr, value := e.methodFuncs(t, sel, pos)
		parts = append(parts, fmt.Sprintf(
			"MethodDesc { name: %q, pkg_path: \"\", expr_type: &%s, expr: ErasedFn::new(%s as *const ()), value_type: &%s, value: ErasedFn::new(%s as *const ()) }",
			fn.Name(), e.typeDesc(methodExprType(t, sig), pos), expr,
			e.typeDesc(withoutRecv(sig), pos), value))
	}
	// Go reports a type's methods in lexicographic order, which is the order
	// types.NewMethodSet is in already; the filtering above keeps it.
	return strings.Join(parts, ", ")
}

// methodExprType is the type of the method expression `T.M`: the receiver,
// then the method's own parameters.
func methodExprType(recv types.Type, sig *types.Signature) types.Type {
	params := []*types.Var{types.NewParam(token.NoPos, nil, "", recv)}
	anon := unaliasedTuple(sig.Params())
	for i := 0; i < anon.Len(); i++ {
		params = append(params, anon.At(i))
	}
	return types.NewSignatureType(nil, nil, nil, types.NewTuple(params...), unaliasedTuple(sig.Results()), sig.Variadic())
}

// withoutRecv is the type of a method value `x.M`: the method's own signature,
// with no receiver attached to it.
func withoutRecv(sig *types.Signature) types.Type {
	return types.NewSignatureType(nil, nil, nil, unaliasedTuple(sig.Params()), unaliasedTuple(sig.Results()), sig.Variadic())
}

// methodFuncs emits the two func bodies reflection needs for one method and
// returns their paths.
func (e *emitter) methodFuncs(recvType types.Type, sel *types.Selection, pos token.Pos) (string, string) {
	fn := e.res.Prog.MethodValue(sel)
	if fn == nil {
		e.errorf(pos, "method %s of %s has no body", sel.Obj().Name(), recvType)
		return "|| ()", "|| ()"
	}
	key := types.TypeString(recvType, qualifiedPath) + "." + sel.Obj().Name()
	if e.reflectFuncs == nil {
		e.reflectFuncs = map[string][2]string{}
	}
	if p, ok := e.reflectFuncs[key]; ok {
		return p[0], p[1]
	}
	m := e.module(fn.Pkg)
	if fn.Pkg == nil {
		m = e.module(nil)
	}
	base := goName(recvType) + "$" + sel.Obj().Name()
	exprName := m.ns.claim(mangle(base + "$expr"))
	valueName := m.ns.claim(mangle(base + "$bound"))
	// Beside the wrapper, with the type whose method they call.
	band := e.bands.typ(recvType)
	exprPath := crateName(band) + "::" + m.name + "::" + exprName
	valuePath := crateName(band) + "::" + m.name + "::" + valueName
	e.reflectFuncs[key] = [2]string{exprPath, valuePath}

	sig := sel.Obj().Type().(*types.Signature)
	place := e.types.place(recvType, e, pos)
	var params []string
	args := []string{"__r"}
	for i := 0; i < sig.Params().Len(); i++ {
		params = append(params, fmt.Sprintf("a%d: %s", i, e.types.rust(sig.Params().At(i).Type(), e, pos)))
		args = append(args, fmt.Sprintf("a%d", i))
	}
	ret := ""
	switch res := sig.Results(); res.Len() {
	case 0:
	case 1:
		ret = " -> " + e.types.rust(res.At(0).Type(), e, pos)
	default:
		ret = " -> " + e.types.rust(res, e, pos)
	}
	target := e.fnPath(fn)
	out := m.at(band)
	fmt.Fprintf(out, "\n// %s as reflection's method expression `T.M`\npub fn %s(__env: Env, __r: %s%s)%s {\n    let _ = __env;\n    %s(%s)\n}\n",
		key, exprName, e.types.rust(recvType, e, pos), commaBefore(params), ret,
		target, strings.Join(args, ", "))
	fmt.Fprintf(out, "\n// %s as reflection's method value `x.M`, over the receiver in its environment\npub fn %s(__env: Env%s)%s {\n    let __r = __env.cast::<%s>().load();\n    %s(%s)\n}\n",
		key, valueName, commaBefore(params), ret, place,
		target, strings.Join(args, ", "))
	return exprPath, valuePath
}

// commaBefore joins parameters after one that is already there.
func commaBefore(params []string) string {
	if len(params) == 0 {
		return ""
	}
	return ", " + strings.Join(params, ", ")
}
