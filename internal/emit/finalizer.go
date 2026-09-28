package emit

import (
	"fmt"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// runtime.SetFinalizer.
//
// The runtime holds the table and decides when a finalizer is due
// (src/finalizer.rs), but it cannot make the call: `SetFinalizer(obj, f)`
// takes two `any`s, and by the time the runtime has them the types are gone.
// Only the emitter knows that `obj` is a `*int32` and `f` a `func(*int32)`, so
// only the emitter can write the call.
//
// So a registration becomes three words the runtime can store without
// understanding any of them: the object's address, an environment holding the
// finalizer func value, and a generated function that loads the value out of
// that environment and calls it with a pointer rebuilt from the address. That
// is the same shape as a deferred call, and for the same reason.

// setFinalizer renders a call to runtime.SetFinalizer, and says whether it
// could. A call whose arguments do not reach the emitter as concrete types is
// left to the overlay's own body, which accepts the finalizer and never runs
// it — Go promises only that a finalizer *may* run.
func (f *fnEmitter) setFinalizer(c *ssa.CallCommon, pos token.Pos) (string, bool) {
	obj, ok := concrete(c.Args[0])
	if !ok {
		return "", false
	}
	if _, isPtr := obj.Type().Underlying().(*types.Pointer); !isPtr {
		return "", false
	}
	addr := fmt.Sprintf("(%s).addr() as usize", f.val(obj))
	if isNilConst(c.Args[1]) {
		// `SetFinalizer(obj, nil)` unregisters: the object is collected with
		// nothing called.
		return fmt.Sprintf("rustygo::finalizer::clear(%s)", addr), true
	}
	fin, ok := concrete(c.Args[1])
	if !ok {
		return "", false
	}
	// One argument, the object. Results are allowed and thrown away, which
	// Go's own test for finalizers relies on.
	sig, ok := fin.Type().Underlying().(*types.Signature)
	if !ok || sig.Params().Len() != 1 {
		return "", false
	}
	// Go also allows a finalizer taking an interface the object implements.
	// That one needs the object boxed rather than passed, and nothing in the
	// standard library asks for it.
	if _, isPtr := sig.Params().At(0).Type().Underlying().(*types.Pointer); !isPtr {
		return "", false
	}
	thunk, info := f.e.finalizerThunk(pos, f.fn, fin.Type())
	env := fmt.Sprintf("Env::of(Ptr::<%s>::alloc(%s { %s: %s }))",
		info.placePath(), info.path(), info.fields[0], f.val(fin))
	return fmt.Sprintf("rustygo::finalizer::set(%s, %s, %s)", addr, env, thunk), true
}

// concrete undoes the conversion to `any` that every argument of
// SetFinalizer's signature goes through, giving back the value the program
// wrote and the type it had.
func concrete(v ssa.Value) (ssa.Value, bool) {
	mi, ok := v.(*ssa.MakeInterface)
	if !ok {
		return nil, false
	}
	return mi.X, true
}

// finalizerThunk generates the call the runtime makes when the object dies:
// the finalizer func value comes out of the environment, and its argument is
// rebuilt from the address the table kept.
func (e *emitter) finalizerThunk(pos token.Pos, in *ssa.Function, finType types.Type) (string, *structInfo) {
	var pkg *types.Package
	if in.Pkg != nil {
		pkg = in.Pkg.Pkg
	}
	st := types.NewStruct([]*types.Var{types.NewField(token.NoPos, pkg, "f0", finType, false)}, nil)
	info := e.types.structInfo(st, in.Name()+"$finalizer", e, pos)

	m := e.module(in.Pkg)
	band := e.bands.fn(in)
	if info.band > band {
		band = info.band
	}
	name := m.ns.claim(mangle(in.Name() + "$finalize"))
	ptr := e.types.rust(finType.Underlying().(*types.Signature).Params().At(0).Type(), e, pos)
	fmt.Fprintf(m.at(band), "\n// runtime.SetFinalizer in %s\npub fn %s(__env: Env, __addr: usize) {\n"+
		"    let __f = __env.cast::<%s>().project(|__e| &__e.%s).load();\n"+
		"    // SAFETY: the collector brought the object back for this call.\n"+
		"    let _ = (__f.code())(__f.env(), unsafe { <%s>::from_addr(__addr) });\n}\n",
		in.String(), name, info.placePath(), info.fields[0], ptr)
	return crateName(band) + "::" + m.name + "::" + name, info
}
