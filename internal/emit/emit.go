// Package emit turns a built SSA program into a Rust crate that links against
// the rustygo runtime crate.
//
// Layout of the generated crate:
//
//	Cargo.toml     depends on the runtime crate by path
//	src/main.rs    module declarations and `fn main`
//	src/ty.rs      one struct (and its place struct) per Go struct type
//	src/<pkg>.rs   one module per Go package: functions and globals
//
// Functions are emitted on demand, starting from the main package's `init`
// and `main` and following static calls, so unreachable code and
// uninstantiated generic bodies never reach rustc.
//
// This is the roadmap's M0 subset: constructs outside it (slices, maps,
// interfaces, closures, goroutines, defer, the standard library) are rejected
// with a positioned error rather than miscompiled.
package emit

import (
	"bytes"
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KarpelesLab/rustygo/internal/load"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/types/typeutil"
)

// Options controls where and how the crate is written.
type Options struct {
	// OutDir receives the generated crate (Cargo.toml and src/).
	OutDir string
	// RuntimePath is the directory of the runtime crate, used as a path
	// dependency.
	RuntimePath string
	// BinName names the binary cargo produces.
	BinName string
	// GcTorture builds the runtime with its debug collector: collect at
	// every allocation, which is what proves the emitted roots are right.
	GcTorture bool
}

// Crate writes the Rust crate for the single main package in res.
//
// It runs the emitter twice: the first run finds which package-level
// variables anything outside initialization reads, so the second can skip
// building the rest (deadinit.go).
func Crate(res *load.Result, opt Options) error {
	first, err := run(res, nil)
	if err != nil {
		return err
	}
	live := liveInitGlobals(first.inits, first.readGlobals)
	e, err := run(res, live)
	if err != nil {
		return err
	}
	return e.write(opt, e.initPath, e.mainPath)
}

// run emits the whole program into memory. liveGlobals, when set, lets
// package initializers skip variables nothing else reads.
func run(res *load.Result, liveGlobals map[*ssa.Global]bool) (*emitter, error) {
	var mainPkg *ssa.Package
	for _, p := range res.Pkgs {
		if p != nil && p.Pkg.Name() == "main" {
			if mainPkg != nil {
				return nil, errors.New("more than one main package")
			}
			mainPkg = p
		}
	}
	if mainPkg == nil {
		return nil, errors.New("no main package")
	}

	e := &emitter{
		res:     res,
		fset:    res.Prog.Fset,
		modules: map[*ssa.Package]*module{},
		fnPaths: map[*ssa.Function]string{},
		shims:   map[*ssa.Function]string{},
		globals: map[*ssa.Global]string{},
		envs:    map[*ssa.Function]*types.Struct{},
		types:   newTypeReg(),

		liveGlobals: liveGlobals,
		readGlobals: map[*ssa.Global]bool{},
		calledFrom:  map[*ssa.Function]*ssa.Function{},
	}
	e.initPath = e.fnPath(mainPkg.Func("init"))
	mainFn := mainPkg.Func("main")
	if mainFn == nil {
		return nil, errors.New("main package has no func main")
	}
	e.mainPath = e.fnPath(mainFn)
	for len(e.queue) > 0 {
		fn := e.queue[0]
		e.queue = e.queue[1:]
		e.emitFunction(fn)
	}
	if len(e.errs) > 0 {
		return nil, e.err()
	}
	return e, nil
}

// emitFunction emits fn, turning a crash into an ordinary error naming the
// function: a bug in the emitter should report where it was, not panic.
func (e *emitter) emitFunction(fn *ssa.Function) {
	e.current = fn
	defer func() {
		if r := recover(); r != nil {
			e.errorf(fn.Pos(), "internal error compiling %s: %v", fn, r)
		}
	}()
	e.function(fn)
}

type emitter struct {
	initPath, mainPath string
	// liveGlobals is the previous run's readGlobals, if any: the globals
	// package initializers must still build. readGlobals collects, during
	// this run, the globals read outside package initializers.
	liveGlobals map[*ssa.Global]bool
	readGlobals map[*ssa.Global]bool
	// inits are the package initializers this run emitted.
	inits []*ssa.Function
	// current is the function being emitted, and calledFrom remembers which
	// function first asked for each other one, so an unsupported callee can
	// name its caller.
	current    *ssa.Function
	calledFrom map[*ssa.Function]*ssa.Function

	opt     Options
	res     *load.Result
	fset    *token.FileSet
	modules map[*ssa.Package]*module
	modList []*module
	modNS   namespace
	fnPaths map[*ssa.Function]string
	shims   map[*ssa.Function]string
	globals map[*ssa.Global]string
	envs    map[*ssa.Function]*types.Struct
	// methodIDs numbers each distinct method name and signature; descs and
	// wrappers memoize the per-type descriptors and their method wrappers.
	methodIDs map[string]uint32
	descs     typeutil.Map // types.Type -> string, the descriptor's path
	wrappers  map[string]string
	queue     []*ssa.Function
	types     *typeReg
	errs      []diag
}

// diag is one unsupported construct.
type diag struct {
	pos token.Pos
	msg string
}

// module is one generated Rust module (one Go package).
type module struct {
	name string
	ns   namespace
	buf  bytes.Buffer
}

func (e *emitter) module(pkg *ssa.Package) *module {
	if m, ok := e.modules[pkg]; ok {
		return m
	}
	if e.modNS == nil {
		// `ty` and `main` are taken by the crate's own layout.
		e.modNS = namespace{"ty": true, "main": true}
	}
	path := "synthetic"
	if pkg != nil {
		path = pkg.Pkg.Path()
	}
	name := strings.ReplaceAll(path, "/", "$")
	m := &module{name: e.modNS.claim(mangle(name)), ns: namespace{}}
	e.modules[pkg] = m
	e.modList = append(e.modList, m)
	return m
}

// errorf records an unsupported construct at pos.
func (e *emitter) errorf(pos token.Pos, format string, args ...any) {
	e.errs = append(e.errs, diag{pos, fmt.Sprintf(format, args...)})
}

// caller names the function that first reached fn, for a diagnostic.
func (e *emitter) caller(fn *ssa.Function) string {
	if from := e.calledFrom[fn]; from != nil {
		return ", reached from " + from.String()
	}
	return ""
}

// err reports each distinct message once, at its first known position, in
// source order.
func (e *emitter) err() error {
	first := map[string]int{}
	var uniq []diag
	for _, d := range e.errs {
		i, seen := first[d.msg]
		switch {
		case !seen:
			first[d.msg] = len(uniq)
			uniq = append(uniq, d)
		case !uniq[i].pos.IsValid() && d.pos.IsValid():
			uniq[i].pos = d.pos
		}
	}
	sort.SliceStable(uniq, func(i, j int) bool {
		pi, pj := e.fset.Position(uniq[i].pos), e.fset.Position(uniq[j].pos)
		if pi.Filename != pj.Filename {
			return pi.Filename < pj.Filename
		}
		return pi.Offset < pj.Offset
	})
	const max = 20
	lines := make([]string, 0, min(len(uniq), max)+1)
	for _, d := range uniq[:min(len(uniq), max)] {
		where := "<unknown position>"
		if d.pos.IsValid() {
			where = e.fset.Position(d.pos).String()
		}
		lines = append(lines, where+": "+d.msg)
	}
	if len(uniq) > max {
		lines = append(lines, fmt.Sprintf("... and %d more", len(uniq)-max))
	}
	return errors.New(strings.Join(lines, "\n"))
}

// fnPath returns the Rust path of fn, queueing it for emission on first use.
func (e *emitter) fnPath(fn *ssa.Function) string {
	if p, ok := e.fnPaths[fn]; ok {
		return p
	}
	pkg := fn.Pkg
	if pkg == nil && fn.Origin() != nil {
		pkg = fn.Origin().Pkg
	}
	m := e.module(pkg)
	base := fn.Name()
	if recv := fn.Signature.Recv(); recv != nil {
		base = types.TypeString(recv.Type(), func(*types.Package) string { return "" }) + "$" + base
	}
	p := "crate::" + m.name + "::" + m.ns.claim(mangle(base))
	e.fnPaths[fn] = p
	e.calledFrom[fn] = e.current
	e.queue = append(e.queue, fn)
	return p
}

// envStruct is the type of fn's captured environment: one field per free
// variable, in order. Emitting it as a Go struct reuses the value/place/trace
// machinery every other struct gets.
func (e *emitter) envStruct(fn *ssa.Function) *types.Struct {
	if st, ok := e.envs[fn]; ok {
		return st
	}
	var fields []*types.Var
	var pkg *types.Package
	if fn.Pkg != nil {
		pkg = fn.Pkg.Pkg
	}
	for i, fv := range fn.FreeVars {
		fields = append(fields, types.NewField(token.NoPos, pkg, fmt.Sprintf("f%d", i), fv.Type(), false))
	}
	st := types.NewStruct(fields, nil)
	e.envs[fn] = st
	return st
}

// shimPath returns the Rust path of a wrapper giving fn the shape of a
// closure — an environment parameter it ignores — so a plain function can be
// used as a func value.
func (e *emitter) shimPath(fn *ssa.Function) string {
	if p, ok := e.shims[fn]; ok {
		return p
	}
	target := e.fnPath(fn) // also queues fn for emission
	m := e.module(fn.Pkg)
	name := m.ns.claim(mangle(fn.Name() + "$funcval"))
	sig := fn.Signature
	params := []string{"_: Env"}
	args := make([]string, 0, sig.Params().Len())
	for i := 0; i < sig.Params().Len(); i++ {
		params = append(params, fmt.Sprintf("a%d: %s", i, e.types.rust(sig.Params().At(i).Type(), e, fn.Pos())))
		args = append(args, fmt.Sprintf("a%d", i))
	}
	ret := ""
	switch res := sig.Results(); res.Len() {
	case 0:
	case 1:
		ret = " -> " + e.types.rust(res.At(0).Type(), e, fn.Pos())
	default:
		ret = " -> " + e.types.rust(res, e, fn.Pos())
	}
	fmt.Fprintf(&m.buf, "\n// %s as a func value\npub fn %s(%s)%s {\n    %s(%s)\n}\n",
		fn.String(), name, strings.Join(params, ", "), ret, target, strings.Join(args, ", "))
	p := "crate::" + m.name + "::" + name
	e.shims[fn] = p
	return p
}

// deferThunk emits a function that unpacks a deferred call's arguments from
// its environment and makes the call, and returns its path along with the
// environment's struct info. Go evaluates deferred arguments at the `defer`
// statement, so they are stored when the thunk is created.
// deferBody says what a thunk calls: a named function, a func value stored
// as its first argument, or a builtin rendered from the loaded arguments.
type deferBody struct {
	target   string
	indirect bool
	builtin  func(loaded []string) string
}

func (e *emitter) deferThunk(d *ssa.Defer, in *ssa.Function, argTypes []types.Type, body deferBody) (string, *structInfo) {
	var fields []*types.Var
	var pkg *types.Package
	if in.Pkg != nil {
		pkg = in.Pkg.Pkg
	}
	for i, t := range argTypes {
		fields = append(fields, types.NewField(token.NoPos, pkg, fmt.Sprintf("f%d", i), t, false))
	}
	st := types.NewStruct(fields, nil)
	info := e.types.structInfo(st, in.Name()+"$deferred", e, d.Pos())

	m := e.module(in.Pkg)
	name := m.ns.claim(mangle(in.Name() + "$defer"))
	var args []string
	for i, f := range info.fields[:len(argTypes)] {
		args = append(args, fmt.Sprintf("__e.project(|e| &e.%s).load()", f))
		_ = i
	}
	call := fmt.Sprintf("%s(%s)", body.target, strings.Join(args, ", "))
	switch {
	case body.builtin != nil:
		call = body.builtin(args)
	case body.indirect:
		// The func value is the first stored argument.
		fv := args[0]
		rest := append([]string{"__f.env()"}, args[1:]...)
		call = fmt.Sprintf("let __f = %s; (__f.code())(%s)", fv, strings.Join(rest, ", "))
	}
	fmt.Fprintf(&m.buf, "\n// deferred call in %s\npub fn %s(__env: Env) {\n    let __e = __env.cast::<crate::ty::%s_P>();\n    %s;\n}\n",
		in.String(), name, info.name, call)
	return "crate::" + m.name + "::" + name, info
}

// globalPath returns the Rust path of the accessor for g, which yields a
// `Ptr` to its storage, emitting the global on first use.
func (e *emitter) globalPath(g *ssa.Global) string {
	if p, ok := e.globals[g]; ok {
		return p
	}
	m := e.module(g.Pkg)
	name := m.ns.claim(mangle(g.Name()))
	p := "crate::" + m.name + "::" + name
	e.globals[g] = p
	place := e.types.place(g.Type().(*types.Pointer).Elem(), e, g.Pos())
	// Globals live in thread-local statics: M0 is single-threaded, and a
	// `static` would need `Sync`. M2 replaces this with shared storage.
	cell := "G_" + strings.TrimPrefix(name, "r#")
	// The place itself lives forever, outside the heap; registering it makes
	// its contents roots (DESIGN §3).
	fmt.Fprintf(&m.buf, "\nthread_local! {\n    static %s: Ptr<%s> = {\n        let p: &'static %s = Box::leak(Box::new(<%s as Place>::new(GoValue::zero())));\n        rustygo::heap::register_global(p);\n        Ptr::to_global(p)\n    };\n}\n", cell, place, place, place)
	fmt.Fprintf(&m.buf, "pub fn %s() -> Ptr<%s> {\n    %s.with(|g| *g)\n}\n", name, place, cell)
	return p
}

func (e *emitter) write(opt Options, initPath, mainPath string) error {
	src := filepath.Join(opt.OutDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	bin := opt.BinName
	if bin == "" {
		bin = "main"
	}
	features := ""
	if opt.GcTorture {
		features = `, features = ["gc-torture"]`
	}
	cargo := fmt.Sprintf(`# Generated by rustygo. Do not edit.
[package]
name = "rustygo-out"
version = "0.0.0"
edition = "2024"
publish = false

[[bin]]
name = %q
path = "src/main.rs"

[dependencies]
rustygo = { path = %q%s }

[profile.release]
debug = "line-tables-only"

# Standalone: never part of an enclosing workspace.
[workspace]
`, bin, filepath.ToSlash(opt.RuntimePath), features)
	files := map[string][]byte{
		filepath.Join(opt.OutDir, "Cargo.toml"): []byte(cargo),
	}

	var mainRS bytes.Buffer
	mainRS.WriteString("// Generated by rustygo. Do not edit.\n#![allow(warnings, clippy::all)]\n\nmod ty;\n")
	mods := append([]*module(nil), e.modList...)
	sort.Slice(mods, func(i, j int) bool { return mods[i].name < mods[j].name })
	for _, m := range mods {
		fmt.Fprintf(&mainRS, "mod %s;\n", m.name)
		body := "// Generated by rustygo. Do not edit.\nuse rustygo::prelude::*;\n" + m.buf.String()
		files[filepath.Join(src, strings.TrimPrefix(m.name, "r#")+".rs")] = []byte(body)
	}
	// Teach the runtime the ids of Error() and String(), so a recovered
	// runtime error satisfies `error` the way gc's does.
	strSig := types.NewSignatureType(nil, nil, nil, types.NewTuple(), types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.String])), false)
	errorID := e.methodID(types.NewFunc(token.NoPos, nil, "Error", strSig))
	stringID := e.methodID(types.NewFunc(token.NoPos, nil, "String", strSig))
	fmt.Fprintf(&mainRS, "\nfn main() {\n    rustygo::panic::init_runtime_errors(%d, %d);\n    rustygo::rt::run_main(%s, %s)\n}\n", errorID, stringID, initPath, mainPath)
	files[filepath.Join(src, "main.rs")] = mainRS.Bytes()
	files[filepath.Join(src, "ty.rs")] = []byte("// Generated by rustygo. Do not edit.\nuse rustygo::prelude::*;\n" + e.types.buf.String())

	for name, data := range files {
		if err := os.WriteFile(name, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
