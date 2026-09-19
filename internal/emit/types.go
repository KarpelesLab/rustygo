package emit

import (
	"bytes"
	"fmt"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/types/typeutil"
)

// typeReg maps Go types to Rust types and emits the struct definitions.
//
// Struct types are keyed by their underlying structure, so a named struct
// type and its underlying struct literal type share one Rust struct and
// conversion between them costs nothing. Methods are free functions, so they
// never need the named type itself.
type typeReg struct {
	structs typeutil.Map // *types.Struct -> *structInfo
	ns      namespace
	buf     bytes.Buffer
}

type structInfo struct {
	name   string   // value struct; the place struct is name + "_P"
	fields []string // Rust field names, by index
}

func newTypeReg() *typeReg {
	return &typeReg{ns: namespace{}}
}

// rust returns the Rust value type for t.
func (r *typeReg) rust(t types.Type, e *emitter, pos token.Pos) string {
	switch t := types.Unalias(t).(type) {
	case *types.Basic:
		if s, ok := basicTypes[t.Kind()]; ok {
			return s
		}
	case *types.Named:
		if st, ok := t.Underlying().(*types.Struct); ok {
			return "crate::ty::" + r.structInfo(st, t.Obj().Name(), e, pos).name
		}
		return r.rust(t.Underlying(), e, pos)
	case *types.Struct:
		return "crate::ty::" + r.structInfo(t, "", e, pos).name
	case *types.Pointer:
		return "Ptr<" + r.place(t.Elem(), e, pos) + ">"
	case *types.Array:
		return fmt.Sprintf("[%s; %d]", r.rust(t.Elem(), e, pos), t.Len())
	case *types.Slice:
		return "Slice<" + r.place(t.Elem(), e, pos) + ">"
	case *types.Signature:
		return "Func<" + r.fnPtr(t, e, pos) + ">"
	case *types.Tuple:
		parts := make([]string, t.Len())
		for i := range parts {
			parts[i] = r.rust(t.At(i).Type(), e, pos) + ","
		}
		return "(" + strings.Join(parts, " ") + ")"
	}
	e.errorf(pos, "type %s is not supported yet (roadmap M1)", t)
	return "()"
}

// place returns the Rust place type holding a value of type t.
func (r *typeReg) place(t types.Type, e *emitter, pos token.Pos) string {
	switch u := t.Underlying().(type) {
	case *types.Struct:
		hint := ""
		if n, ok := types.Unalias(t).(*types.Named); ok {
			hint = n.Obj().Name()
		}
		return "crate::ty::" + r.structInfo(u, hint, e, pos).name + "_P"
	case *types.Array:
		return fmt.Sprintf("[%s; %d]", r.place(u.Elem(), e, pos), u.Len())
	}
	return "Slot<" + r.rust(t, e, pos) + ">"
}

// fnPtr is the Rust fn-pointer type for a Go signature: the closure's
// environment, then the parameters.
func (r *typeReg) fnPtr(sig *types.Signature, e *emitter, pos token.Pos) string {
	parts := []string{"Env"}
	if recv := sig.Recv(); recv != nil {
		parts = append(parts, r.rust(recv.Type(), e, pos))
	}
	for i := 0; i < sig.Params().Len(); i++ {
		parts = append(parts, r.rust(sig.Params().At(i).Type(), e, pos))
	}
	ret := ""
	switch res := sig.Results(); res.Len() {
	case 0:
	case 1:
		ret = " -> " + r.rust(res.At(0).Type(), e, pos)
	default:
		ret = " -> " + r.rust(res, e, pos)
	}
	return fmt.Sprintf("fn(%s)%s", strings.Join(parts, ", "), ret)
}

// field returns the Rust field name of field i of struct type t (any type
// whose underlying type is a struct).
func (r *typeReg) field(t types.Type, i int, e *emitter, pos token.Pos) string {
	st := t.Underlying().(*types.Struct)
	return r.structInfo(st, "", e, pos).fields[i]
}

func (r *typeReg) structInfo(st *types.Struct, hint string, e *emitter, pos token.Pos) *structInfo {
	if si, ok := r.structs.At(st).(*structInfo); ok {
		return si
	}
	base := "Struct"
	if hint != "" {
		base = mangle(hint)
	}
	si := &structInfo{name: r.ns.claim(strings.TrimPrefix(base, "r#"))}
	// Register before emitting fields: a field may point back to this type.
	r.structs.Set(st, si)
	fieldNS := namespace{}
	for i := 0; i < st.NumFields(); i++ {
		name := st.Field(i).Name()
		if name == "_" {
			name = fmt.Sprintf("_b%d", i) // not a possible mangle output
		} else {
			name = mangle(name)
		}
		si.fields = append(si.fields, fieldNS.claim(name))
	}

	var def, place, zero, newP, load, store, trace bytes.Buffer
	for i, f := range si.fields {
		ft := st.Field(i).Type()
		fmt.Fprintf(&def, "    pub %s: %s,\n", f, r.rust(ft, e, pos))
		fmt.Fprintf(&place, "    pub %s: %s,\n", f, r.place(ft, e, pos))
		fmt.Fprintf(&zero, " %s: GoValue::zero(),", f)
		fmt.Fprintf(&newP, " %s: Place::new(v.%s),", f, f)
		fmt.Fprintf(&load, " %s: self.%s.load(),", f, f)
		fmt.Fprintf(&store, "        self.%s.store(v.%s);\n", f, f)
		if containsRef(ft) {
			fmt.Fprintf(&trace, "        self.%s.trace(t);\n", f)
		}
	}
	n := si.name
	fmt.Fprintf(&r.buf, `
// Go: %s
#[derive(Clone, Copy, PartialEq)]
pub struct %s {
%s}

impl GoValue for %s {
    fn zero() -> Self {
        %s {%s }
    }
}

pub struct %s_P {
%s}

impl Place for %s_P {
    type Value = %s;
    fn new(v: %s) -> Self {
        %s_P {%s }
    }
    fn load(&self) -> %s {
        %s {%s }
    }
    fn store(&self, v: %s) {
%s    }
}

impl Trace for %s {
    fn trace(&self, t: &mut Tracer<'_>) {
%s    }
}

impl Trace for %s_P {
    fn trace(&self, t: &mut Tracer<'_>) {
%s    }
}
`, types.TypeString(st, nil), n, def.String(), n, n, zero.String(),
		n, place.String(), n, n, n, n, newP.String(), n, n, load.String(), n, store.String(),
		n, trace.String(), n, trace.String())
	return si
}

var basicTypes = map[types.BasicKind]string{
	types.Bool:    "bool",
	types.Int:     "i64",
	types.Int8:    "i8",
	types.Int16:   "i16",
	types.Int32:   "i32",
	types.Int64:   "i64",
	types.Uint:    "u64",
	types.Uint8:   "u8",
	types.Uint16:  "u16",
	types.Uint32:  "u32",
	types.Uint64:  "u64",
	types.Uintptr: "u64",
	types.Float32: "f32",
	types.Float64: "f64",
	types.String:  "GoStr",
}

// basicInfo returns the underlying basic type of t, or nil.
func basicInfo(t types.Type) *types.Basic {
	b, _ := t.Underlying().(*types.Basic)
	return b
}
