//! What reflection asks of the runtime (DESIGN §6).
//!
//! The Go-facing half lives in rustygo's `reflect` package
//! (internal/goroot/_overlay/reflect); this is what it cannot do in Go. Most
//! of reflection needs nothing here: a place has gc's layout (DESIGN §7), so
//! `reflect` reads a field or an element through `unsafe.Pointer` arithmetic,
//! exactly as gc's does. What it cannot do in Go is read a *type descriptor*,
//! which is a Rust static, and reach inside a map, which is a Rust hash
//! table rather than memory laid out by Go's rules.
//!
//! Every function takes a descriptor as a [`UPtr`], the address
//! [`Iface::desc_addr`] hands out.

use crate::iface::{Data, Iface, MapOps, TypeDesc};
use crate::string::GoStr;
use crate::unsafe_ptr::UPtr;

/// The descriptor at an address, which only ever comes from `desc_addr`.
fn desc(p: UPtr) -> &'static TypeDesc {
    assert!(p.addr() != 0, "reflection on a nil type");
    // SAFETY: descriptors are statics, so the address stays valid; it came
    // from `Iface::desc_addr` or from a descriptor's own `elem`/`key`/field
    // links.
    unsafe { &*(p.addr() as usize as *const TypeDesc) }
}

/// A descriptor pointer, or nil.
fn opt(d: Option<&'static TypeDesc>) -> UPtr {
    UPtr::from_addr(d.map_or(0, |d| d as *const TypeDesc as usize as u64))
}

/// The dynamic type of an interface value, or nil.
pub fn type_of(v: Iface) -> UPtr {
    UPtr::from_addr(v.desc_addr())
}

/// The data word of an interface value.
pub fn data_of(v: Iface) -> UPtr {
    UPtr::from_addr(v.data().addr())
}

/// An interface value from a descriptor and a boxed value.
pub fn make_iface(d: UPtr, data: UPtr) -> Iface {
    if d.addr() == 0 {
        return Iface::nil();
    }
    Iface::new(desc(d), Data::of::<u8>(unsafe { data.to_ptr() }))
}

/// `Type.Kind`, numbered as `reflect.Kind`.
pub fn kind(d: UPtr) -> u8 {
    desc(d).kind
}

/// The type's size in bytes.
pub fn size(d: UPtr) -> u64 {
    desc(d).size as u64
}

/// The type's alignment in bytes.
pub fn align(d: UPtr) -> u64 {
    desc(d).align as u64
}

/// `Type.Name`: the declared name alone.
pub fn name(d: UPtr) -> GoStr {
    GoStr::lit(desc(d).short.as_bytes())
}

/// `Type.String`: the name Go prints.
pub fn type_string(d: UPtr) -> GoStr {
    GoStr::lit(desc(d).name.as_bytes())
}

/// `Type.PkgPath`.
pub fn pkg_path(d: UPtr) -> GoStr {
    GoStr::lit(desc(d).pkg_path.as_bytes())
}

/// The element type of a pointer, slice, array, map or channel.
pub fn elem(d: UPtr) -> UPtr {
    opt(desc(d).elem)
}

/// A map's key type, or nil.
pub fn key(d: UPtr) -> UPtr {
    opt(desc(d).key)
}

/// An array's length.
pub fn len(d: UPtr) -> i64 {
    desc(d).len as i64
}

/// Whether `==` is defined on the type.
pub fn comparable(d: UPtr) -> bool {
    desc(d).equal.is_some()
}

/// Whether the type has the method with this id, which is how the Go side
/// answers `Implements`.
pub fn has_method(d: UPtr, id: crate::iface::MethodId) -> bool {
    Iface::new(desc(d), Data::NONE).implements(&[id])
}

/// A struct's field count.
pub fn num_field(d: UPtr) -> i64 {
    desc(d).fields.len() as i64
}

fn field(d: UPtr, i: i64) -> &'static crate::iface::FieldDesc {
    let fields = desc(d).fields;
    assert!((i as usize) < fields.len(), "field index out of range");
    &fields[i as usize]
}

/// A field's name.
pub fn field_name(d: UPtr, i: i64) -> GoStr {
    GoStr::lit(field(d, i).name.as_bytes())
}

/// A field's defining package, for an unexported field.
pub fn field_pkg_path(d: UPtr, i: i64) -> GoStr {
    GoStr::lit(field(d, i).pkg_path.as_bytes())
}

/// A field's type.
pub fn field_type(d: UPtr, i: i64) -> UPtr {
    UPtr::from_addr(field(d, i).typ as *const TypeDesc as usize as u64)
}

/// A field's offset in bytes.
pub fn field_offset(d: UPtr, i: i64) -> u64 {
    field(d, i).offset as u64
}

/// A field's struct tag.
pub fn field_tag(d: UPtr, i: i64) -> GoStr {
    GoStr::lit(field(d, i).tag.as_bytes())
}

/// Whether a field is embedded.
pub fn field_embedded(d: UPtr, i: i64) -> bool {
    field(d, i).embedded
}

/// Copies the value at `addr` into a fresh heap object, so reflection can
/// hand it back as an interface.
pub fn box_value(d: UPtr, addr: UPtr) -> UPtr {
    UPtr::from_addr((desc(d).box_value)(addr).addr())
}

fn map_ops(d: UPtr) -> &'static MapOps {
    desc(d).map_ops.expect("not a map type")
}

/// `len(m)`.
pub fn map_len(d: UPtr, m: UPtr) -> i64 {
    (map_ops(d).len)(data(m))
}

/// Starts iterating a map; the result is an opaque cursor.
pub fn map_iter(d: UPtr, m: UPtr) -> UPtr {
    UPtr::from_addr((map_ops(d).iter)(data(m)).addr())
}

/// Advances a cursor: `(ok, key, value)`.
pub fn map_next(d: UPtr, cursor: UPtr) -> (bool, UPtr, UPtr) {
    let (ok, k, v) = (map_ops(d).next)(data(cursor));
    (ok, UPtr::from_addr(k.addr()), UPtr::from_addr(v.addr()))
}

/// A map with the same entries, as `maps.Clone` wants it: the same dynamic
/// type, so the result goes back into an interface with the same descriptor.
pub fn map_clone(m: Iface) -> Iface {
    if m.is_nil() {
        return Iface::nil();
    }
    let d = desc(UPtr::from_addr(m.desc_addr()));
    let ops = d.map_ops.expect("not a map type");
    Iface::new(d, (ops.clone)(m.data()))
}

/// `m[k]`: `(value, ok)`.
pub fn map_index(d: UPtr, m: UPtr, k: UPtr) -> (UPtr, bool) {
    let (v, ok) = (map_ops(d).index)(data(m), data(k));
    (UPtr::from_addr(v.addr()), ok)
}

/// How many parameters a func type has.
pub fn num_in(d: UPtr) -> i64 {
    desc(d).params.len() as i64
}

/// The type of parameter `i`.
pub fn type_in(d: UPtr, i: i64) -> UPtr {
    match desc(d).params.get(i as usize) {
        Some(t) => UPtr::from_addr(*t as *const TypeDesc as usize as u64),
        None => crate::panic::runtime_error_msg(alloc::format!(
            "reflect: In({i}) out of range for {}",
            desc(d).name
        )),
    }
}

/// How many results a func type has.
pub fn num_out(d: UPtr) -> i64 {
    desc(d).results.len() as i64
}

/// The type of result `i`.
pub fn type_out(d: UPtr, i: i64) -> UPtr {
    match desc(d).results.get(i as usize) {
        Some(t) => UPtr::from_addr(*t as *const TypeDesc as usize as u64),
        None => crate::panic::runtime_error_msg(alloc::format!(
            "reflect: Out({i}) out of range for {}",
            desc(d).name
        )),
    }
}

/// Whether a func type's last parameter is `...`.
pub fn is_variadic(d: UPtr) -> bool {
    desc(d).variadic
}

/// Whether two values of this type are equal, for `reflect.Value.Equal`. Only
/// asked of a comparable type.
pub fn equal(d: UPtr, a: UPtr, b: UPtr) -> bool {
    match desc(d).equal {
        Some(eq) => eq(data(a), data(b)),
        None => crate::panic::runtime_error_msg(alloc::format!(
            "reflect: {} is not comparable",
            desc(d).name
        )),
    }
}

/// `reflect.Value.Call`: calls the func value at `f`, whose type is `d`, with
/// the boxed arguments in `args`, and returns its boxed results.
///
/// The arguments and results are addresses of heap boxes, which is how a
/// `reflect.Value` holds a value either way. The result slice is allocated
/// before the call, so that the call's own allocations cannot collect it.
pub fn call(
    d: UPtr,
    f: UPtr,
    args: crate::slice::Slice<crate::place::Slot<UPtr>>,
) -> crate::slice::Slice<crate::place::Slot<UPtr>> {
    let t = desc(d);
    let Some(shim) = t.call else {
        crate::panic::runtime_error_msg(alloc::format!(
            "reflect: Call of {} is not in the program's call tables",
            t.name
        ))
    };
    let mut boxed = alloc::vec::Vec::with_capacity(args.len() as usize);
    for i in 0..args.len() {
        boxed.push(data(args.at(i).load()));
    }
    let n = t.results.len();
    let out = crate::slice::Slice::<crate::place::Slot<UPtr>>::make(n as i64, n as i64);
    let mut results = alloc::vec::Vec::new();
    results.resize(n, Data::NONE);
    let frame = crate::gc::Frame::<1>::new();
    frame.scope(|| {
        frame.set(0, &out);
        shim(data(f), &boxed, &mut results);
        // Nothing allocates between here and the stores, so the boxes the
        // shim made are still the ones it rooted.
        for (i, r) in results.iter().enumerate() {
            out.at(i as i64).store(UPtr::from_addr(r.addr()));
        }
    });
    out
}

/// Whether the type `d` has every method of the interface type `iface`.
pub fn implements(d: UPtr, iface: UPtr) -> bool {
    desc(d).implements(desc(iface))
}

/// A fresh zero value of this type, boxed: `reflect.Zero`.
pub fn zero_value(d: UPtr) -> UPtr {
    UPtr::from_addr((desc(d).zero)().addr())
}

/// An empty map of this type, boxed: `reflect.MakeMap`.
pub fn map_make(d: UPtr) -> UPtr {
    UPtr::from_addr((map_ops(d).make)().addr())
}

/// `m[k] = v`, all three given by address.
pub fn map_set(d: UPtr, m: UPtr, k: UPtr, v: UPtr) {
    (map_ops(d).set)(data(m), data(k), data(v))
}

/// `delete(m, k)`.
pub fn map_delete(d: UPtr, m: UPtr, k: UPtr) {
    (map_ops(d).delete)(data(m), data(k))
}

/// An address as a data word.
fn data(p: UPtr) -> Data {
    Data::of::<u8>(unsafe { p.to_ptr() })
}

// Methods, as `reflect.Type.Method` and `reflect.Value.Method` hand them out.
//
// A method's two func values differ only in whether the receiver is a
// parameter or an environment, and a `Func<F>` is a code address and an
// environment whatever `F` is. So one box serves any method's type, and the
// program reads it back as the signature the descriptor promised — the bargain
// [`crate::iface::ErasedFn`] already makes for dispatch.

/// How many exported methods a type has.
pub fn num_method(d: UPtr) -> i64 {
    desc(d).num_methods as i64
}

fn method(d: UPtr, i: i64) -> &'static crate::iface::MethodDesc {
    let d = desc(d);
    match d.reflect_methods.get(i as usize) {
        Some(m) => m,
        // The count comes from the descriptor and the table from the pass that
        // saw the program ask for a method, so a program that reaches here
        // with an index in range asked in a way that pass did not see.
        None => crate::panic::runtime_error_msg(alloc::format!(
            "reflect: method {i} of {} is not in the program's method tables",
            d.name
        )),
    }
}

/// The method's name.
pub fn method_name(d: UPtr, i: i64) -> GoStr {
    GoStr::lit(method(d, i).name.as_bytes())
}

/// The method's package path, empty for an exported one.
pub fn method_pkg_path(d: UPtr, i: i64) -> GoStr {
    GoStr::lit(method(d, i).pkg_path.as_bytes())
}

/// The type of the method expression `T.M`.
pub fn method_expr_type(d: UPtr, i: i64) -> UPtr {
    UPtr::from_addr(method(d, i).expr_type as *const TypeDesc as usize as u64)
}

/// The method expression `T.M` as a boxed func value.
pub fn method_expr_func(d: UPtr, i: i64) -> UPtr {
    boxed_func(method(d, i).expr, crate::func::Env::NONE)
}

/// The type of a method value `x.M`.
pub fn method_value_type(d: UPtr, i: i64) -> UPtr {
    UPtr::from_addr(method(d, i).value_type as *const TypeDesc as usize as u64)
}

/// The method value `x.M` as a boxed func value, over the receiver at `recv`.
pub fn method_value_func(d: UPtr, i: i64, recv: UPtr) -> UPtr {
    // The receiver's box is the environment: the generated code for `value`
    // reads the receiver straight out of it.
    boxed_func(
        method(d, i).value,
        crate::func::Env::of(unsafe { recv.to_ptr::<crate::place::Slot<u8>>() }),
    )
}

/// A func value with the code and environment given, boxed as an interface's
/// data word.
fn boxed_func(code: crate::iface::ErasedFn, env: crate::func::Env) -> UPtr {
    if code.is_none() {
        // An interface type's method: a signature and nothing to call.
        return UPtr::from_addr(0);
    }
    // SAFETY (invariant): the emitter pairs each code address with the
    // descriptor that names its signature, and a `Func` holding it is the same
    // two words whichever signature that is.
    let f = crate::func::Func::<fn(crate::func::Env)>::new(
        unsafe { core::mem::transmute_copy(&code) },
        env,
    );
    let boxed =
        crate::place::Ptr::<crate::place::Slot<crate::func::Func<fn(crate::func::Env)>>>::alloc(f);
    UPtr::from_addr(Data::of(boxed).addr())
}
