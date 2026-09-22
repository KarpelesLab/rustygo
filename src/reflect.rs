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

/// `m[k]`: `(value, ok)`.
pub fn map_index(d: UPtr, m: UPtr, k: UPtr) -> (UPtr, bool) {
    let (v, ok) = (map_ops(d).index)(data(m), data(k));
    (UPtr::from_addr(v.addr()), ok)
}

/// An address as a data word.
fn data(p: UPtr) -> Data {
    Data::of::<u8>(unsafe { p.to_ptr() })
}
