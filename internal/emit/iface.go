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
	sig := fn.Type().(*types.Signature)
	key := fn.Name() + "|" + types.TypeString(types.NewSignatureType(nil, nil, nil, sig.Params(), sig.Results(), sig.Variadic()), qualifiedPath)
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

// qualifiedPath names packages by import path, so ids are stable and
// distinct across packages.
func qualifiedPath(p *types.Package) string { return p.Path() }

// goName is the type's name as Go prints it: `main.Point`, `*main.Node`,
// `int`. Packages are named, not pathed, exactly as gc's panic messages do.
func goName(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
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
	if e.descs == nil {
		e.descs = map[string]string{}
	}
	key := types.TypeString(t, qualifiedPath)
	if p, ok := e.descs[key]; ok {
		return p
	}
	name := e.types.ns.claim("TD_" + mangle(strings.NewReplacer("*", "ptr_", ".", "_", "/", "_").Replace(key)))
	path := "crate::ty::" + name
	e.descs[key] = path

	place := e.types.place(t, e, pos)
	methods := e.methodTable(t, pos)
	equal := "None"
	if types.Comparable(t) {
		equal = fmt.Sprintf("Some(|a, b| a.cast::<%s>().load() == b.cast::<%s>().load())", place, place)
	}
	fmt.Fprintf(&e.types.buf, "\npub static %s: TypeDesc = TypeDesc {\n    name: %q,\n    methods: &[%s],\n    equal: %s,\n    print: |d, out| { %s },\n};\n",
		name, goName(t), methods, equal, e.printValue(t, place, pos))
	return path
}

// methodTable renders t's method set as (id, wrapper) pairs sorted by id.
func (e *emitter) methodTable(t types.Type, pos token.Pos) string {
	type entry struct {
		id   uint32
		path string
	}
	var entries []entry
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
	path := "crate::" + m.name + "::" + name
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
	fmt.Fprintf(&m.buf, "\n// %s, called through an interface\npub fn %s(%s)%s {\n    %s(%s)\n}\n",
		key, name, strings.Join(params, ", "), ret, e.fnPath(fn), strings.Join(args, ", "))
	return path
}

// printValue renders the body of a descriptor's print function, following
// gc's `printpanicval`: an error prints its Error(), a Stringer its String(),
// a predeclared basic type its value, a named basic type `main.T(v)`, and
// anything else `(main.T) 0xaddr`.
func (e *emitter) printValue(t types.Type, place string, pos token.Pos) string {
	load := fmt.Sprintf("d.cast::<%s>().load()", place)
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
	m := c.Method
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
	recv := f.val(c.Value)
	call := append([]string{"__i.data()"}, args...)
	return fmt.Sprintf("{ let __i = %s; (__i.method::<fn(%s)%s>(%d))(%s) }",
		recv, strings.Join(params, ", "), ret, id, strings.Join(call, ", "))
}
