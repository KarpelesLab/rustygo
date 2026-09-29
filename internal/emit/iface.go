package emit

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
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

// goName is the type's name as Go prints it: `main.Point`, `*main.Node`,
// `int`. Packages are named, not pathed, exactly as gc's panic messages do,
// and an alias prints as the type it names (`byte` is `uint8`).
func goName(t types.Type) string {
	if b, ok := types.Unalias(t).(*types.Basic); ok {
		return types.Typ[b.Kind()].Name()
	}
	// Aliases and parameter names both go: Go names a type by what it is, so
	// `[]byte` prints as `[]uint8` and `func(d int)` as `func(int)`, which is
	// what [unaliased] leaves behind.
	return types.TypeString(unaliased(t), func(p *types.Package) string { return p.Name() })
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
		fmt.Fprintf(&b, "    elem: Some(&%s),\n", e.typeDesc(u.Elem(), pos))
	case *types.Array:
		fmt.Fprintf(&b, "    elem: Some(&%s),\n    len: %d,\n", e.typeDesc(u.Elem(), pos), u.Len())
	case *types.Map:
		fmt.Fprintf(&b, "    elem: Some(&%s),\n    key: Some(&%s),\n    map_ops: Some(&%s),\n",
			e.typeDesc(u.Elem(), pos), e.typeDesc(u.Key(), pos), e.mapOps(u, pos))
	case *types.Struct:
		fmt.Fprintf(&b, "    fields: &[%s],\n", e.fieldDescs(t, u, pos))
	case *types.Signature:
		// What reflection asks a func type: its parameters and its results.
		// `Value.Call` will want them too.
		fmt.Fprintf(&b, "    params: &[%s],\n    results: &[%s],\n",
			e.descList(u.Params(), pos), e.descList(u.Results(), pos))
		if u.Variadic() {
			b.WriteString("    variadic: true,\n")
		}
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
        // may collect.
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
        });
        Data::of(Ptr::<Slot<%s>>::alloc(out))
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
			return fmt.Sprintf("out.extend_from_slice(b\"%s(\\\"\"); rustygo::print::format(out, &[%s], false); out.extend_from_slice(b\"\\\")\");", goName(t), arg)
		}
		return fmt.Sprintf("out.extend_from_slice(b\"%s(\"); rustygo::print::format(out, &[%s], false); out.push(b')');", goName(t), arg)
	}
	return fmt.Sprintf("out.extend_from_slice(b\"(%s) \"); rustygo::print::format(out, &[rustygo::print::Arg::Pointer(d.addr())], false);", goName(t))
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
