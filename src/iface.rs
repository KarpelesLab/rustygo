//! Interface values.
//!
//! An interface value is a type descriptor plus one word of data (DESIGN §6).
//! The data is a pointer to a heap copy of the concrete value, so every
//! interface value has the same shape whatever it holds, and the collector
//! traces it as one edge.
//!
//! The descriptor is a static the emitter generates per concrete type: its
//! name, the methods of its method set, how to compare two of them, and how
//! `print` and `panic` render it. Method lookup is by id — the emitter numbers
//! each distinct method name and signature — so a call through an interface
//! finds the concrete method without knowing the interface it came from.
//!
//! **M1 status:** every conversion to an interface boxes, even for a pointer,
//! which gc stores directly. Method lookup is a linear scan of a small sorted
//! table rather than an itab. Both are the simple version; devirtualization
//! and itabs are M6.

use crate::panic::runtime_error_msg;
use crate::place::Ptr;
use crate::trace::{Trace, Tracer};
use crate::value::GoValue;
use alloc::format;
use alloc::vec::Vec;

/// Identifies a method by name and signature, across all types.
pub type MethodId = u32;

/// An erased method wrapper: the code address, with its signature dropped.
///
/// Rust will not cast one fn-pointer type to another, so a method table
/// stores the address and [`Iface::method`] casts it back to the signature
/// the caller expects. The emitter pairs each id with exactly one signature.
#[derive(Clone, Copy)]
pub struct ErasedFn(*const ());

// SAFETY: a code address is immutable and shared by every thread.
unsafe impl Sync for ErasedFn {}
// SAFETY: as above.
unsafe impl Send for ErasedFn {}

impl ErasedFn {
    /// No function at all, which is what an interface type's methods have:
    /// reflection reports their signatures and no func value.
    pub const NONE: ErasedFn = ErasedFn(core::ptr::null());

    /// Erases a method wrapper, which generated code spells
    /// `ErasedFn::new(wrapper as *const ())`.
    pub const fn new(code: *const ()) -> Self {
        ErasedFn(code)
    }

    /// Whether there is no function here.
    #[inline]
    pub fn is_none(self) -> bool {
        self.0.is_null()
    }
}

/// What the runtime knows about one concrete type.
pub struct TypeDesc {
    /// As Go prints it: `main.Point`, `int`, `*main.Node`.
    pub name: &'static str,
    /// The declared name alone (`Point`), empty for an unnamed type.
    pub short: &'static str,
    /// The defining package's import path, empty for an unnamed or
    /// predeclared type.
    pub pkg_path: &'static str,
    /// The kind, numbered as `reflect.Kind` is.
    pub kind: u8,
    /// Size and alignment in bytes, as gc lays the type out (DESIGN §7).
    pub size: usize,
    /// The type's alignment in bytes.
    pub align: usize,
    /// The type's method set, sorted by id.
    pub methods: &'static [(MethodId, ErasedFn)],
    /// An interface type's own methods, by id and sorted. A concrete type
    /// implements the interface exactly when its table holds all of them,
    /// which is what `reflect.Type.Implements` asks and what an interface call
    /// relies on.
    pub iface_methods: &'static [MethodId],
    /// Compares two values of this type, or `None` if Go says the type is
    /// not comparable (a slice, map or func).
    pub equal: Option<fn(Data, Data) -> bool>,
    /// Hashes a value of this type, for use as a map key. `None` exactly
    /// when `equal` is.
    pub hash: Option<fn(Data) -> u64>,
    /// Appends the value as `print` and `panic` render it.
    pub print: fn(Data, &mut Vec<u8>),
    /// Copies a value at an address into a fresh heap object, which is how
    /// reflection hands one back as an interface.
    pub box_value: fn(crate::unsafe_ptr::UPtr) -> Data,
    /// A fresh heap object holding this type's zero value, for
    /// `reflect.Zero`. Go's zero value is all-zero bytes, but the *object*
    /// has to exist, and it has to know how to trace itself.
    pub zero: fn() -> Data,
    /// A slice type's `make`, boxed: `reflect.MakeSlice`. The elements have to
    /// be traced as that element type, which only this type knows how.
    pub make_slice: Option<fn(i64, i64) -> Data>,
    /// The element type of a pointer, slice, array, map or channel.
    pub elem: Option<&'static TypeDesc>,
    /// The descriptor of `*T`, for the types whose pointer the emitter wrote
    /// one for.
    ///
    /// `reflect.New`, `reflect.PointerTo` and `Value.Addr` all hand back a
    /// `*T`, and the runtime can derive most of a pointer's descriptor on its
    /// own (`crate::reflect`). What it cannot derive is the method set, and
    /// Go's type identity says a type has exactly one descriptor — so
    /// wherever a program already contains `*T`, or `*T` has methods, the
    /// emitted descriptor is named here and wins.
    pub ptr: Option<&'static TypeDesc>,
    /// A map's key type.
    pub key: Option<&'static TypeDesc>,
    /// An array's length.
    pub len: usize,
    /// A channel type's direction, numbered as `reflect.ChanDir`: 1 for
    /// `<-chan`, 2 for `chan<-`, 3 for both, 0 for a type that is not a
    /// channel. Go's own numbering, so that reflection can hand it straight
    /// out.
    pub chan_dir: u8,
    /// A struct's fields, in declaration order.
    pub fields: &'static [FieldDesc],
    /// A map's accessors: our maps are hash tables, not memory gc's layout
    /// rules describe, so reflection reaches them through these.
    pub map_ops: Option<&'static MapOps>,
    /// A channel's accessors, for the same reason: a channel is a runtime
    /// object rather than memory, so reflection cannot read one by address.
    pub chan_ops: Option<&'static ChanOps>,
    /// A func type's parameter types, and whether the last of them is `...`.
    pub params: &'static [&'static TypeDesc],
    /// A func type's result types.
    pub results: &'static [&'static TypeDesc],
    /// Whether a func type's last parameter is variadic.
    pub variadic: bool,
    /// Calls a func value of this type: `reflect.Value.Call`. Generated only
    /// for a program that asks, since it is a function per func type.
    pub call: Option<CallFn>,
    /// How many exported methods the type has, which
    /// `reflect.Type.NumMethod` reports. Always known, even when the table
    /// below is not generated: a program can ask how many methods a type has
    /// without asking what they are, and `encoding/asn1` does exactly that to
    /// recognize the empty interface.
    pub num_methods: usize,
    /// The exported methods, in Go's order, for `reflect.Type.Method`. Empty
    /// unless the program reaches that part of reflection: method *dispatch*
    /// needs none of it, and generating it for every method of every type
    /// would be so much code for nothing.
    pub reflect_methods: &'static [MethodDesc],
}

/// How `reflect.Value.Call` reaches a func value of a given type: the boxed
/// func, its boxed arguments, and the boxed results to fill in, which the
/// caller has sized to the number the signature has.
pub type CallFn = fn(Data, &[Data], &mut [Data]);

/// One method, as reflection hands it out.
///
/// Reflection wants two func values for a method, and neither is the wrapper
/// interface dispatch uses (that one takes the receiver boxed). `T.M` is a
/// func whose first parameter is the receiver; `x.M` is a func without it,
/// carrying the receiver with it. The emitter generates the pair, along with
/// the descriptor of each one's type, so that `.Interface()` on either can be
/// asserted back to the func type the program wrote.
pub struct MethodDesc {
    /// The method's name.
    pub name: &'static str,
    /// The defining package's path, empty for an exported method, as
    /// `reflect.Method` reports it.
    pub pkg_path: &'static str,
    /// The type of the method expression `T.M`.
    pub expr_type: &'static TypeDesc,
    /// Its code: a func value's code, with no environment.
    pub expr: ErasedFn,
    /// The type of a method value `x.M`, which is the method's own signature.
    pub value_type: &'static TypeDesc,
    /// Its code, which takes the receiver's box as its environment.
    pub value: ErasedFn,
}

/// One field of a struct type, as reflection sees it.
pub struct FieldDesc {
    /// The field's name.
    pub name: &'static str,
    /// The defining package's path, for an unexported field.
    pub pkg_path: &'static str,
    /// The field's type.
    pub typ: &'static TypeDesc,
    /// Its offset in the struct, in bytes.
    pub offset: usize,
    /// Its struct tag, as written.
    pub tag: &'static str,
    /// Whether it is embedded (anonymous).
    pub embedded: bool,
}

/// Type-erased access to a channel, generated per channel type.
///
/// Every one of these may block, which is what makes them unlike a map's: a
/// receive parks the goroutine until a value arrives, and reflection's callers
/// expect exactly that.
pub struct ChanOps {
    /// `v, ok := <-ch`, with the value boxed.
    pub recv: fn(Data) -> (Data, bool),
    /// `v, ok := <-ch` without blocking: `ok` is false when nothing was ready.
    pub try_recv: fn(Data) -> (Data, bool),
    /// `ch <- v`, with the value given boxed.
    pub send: fn(Data, Data),
    /// `ch <- v` without blocking, reporting whether it went.
    pub try_send: fn(Data, Data) -> bool,
    /// `len(ch)` and `cap(ch)`.
    pub len: fn(Data) -> i64,
    /// `cap(ch)`.
    pub cap: fn(Data) -> i64,
    /// `close(ch)`.
    pub close: fn(Data),
}

/// Type-erased access to a map value, generated per map type.
pub struct MapOps {
    /// `len(m)`.
    pub len: fn(Data) -> i64,
    /// Starts an iteration, returning an opaque cursor.
    pub iter: fn(Data) -> Data,
    /// Advances a cursor: `(ok, key, value)`, each boxed.
    pub next: fn(Data) -> (bool, Data, Data),
    /// `m[k]`, with the key given boxed: `(value, ok)`.
    pub index: fn(Data, Data) -> (Data, bool),
    /// A fresh map holding the same entries, boxed, for `maps.Clone`.
    pub clone: fn(Data) -> Data,
    /// An empty map of this type, boxed: `reflect.MakeMap`.
    pub make: fn() -> Data,
    /// `m[k] = v`, with both given boxed.
    pub set: fn(Data, Data, Data),
    /// `delete(m, k)`.
    pub delete: fn(Data, Data),
}

impl TypeDesc {
    /// A descriptor with nothing filled in, to be updated with the fields a
    /// type actually has: `TypeDesc { name: "int", ..TypeDesc::DEFAULT }`.
    pub const DEFAULT: TypeDesc = TypeDesc {
        name: "",
        short: "",
        pkg_path: "",
        kind: 0,
        size: 0,
        align: 1,
        methods: &[],
        iface_methods: &[],
        equal: None,
        hash: None,
        print: |_, out| out.extend_from_slice(b"<value>"),
        box_value: |_| Data::NONE,
        zero: || Data::NONE,
        make_slice: None,
        elem: None,
        ptr: None,
        key: None,
        len: 0,
        chan_dir: 0,
        fields: &[],
        map_ops: None,
        chan_ops: None,
        params: &[],
        results: &[],
        variadic: false,
        call: None,
        num_methods: 0,
        reflect_methods: &[],
    };

    /// Whether this type's method set holds every method of the interface
    /// type `iface`.
    pub fn implements(&self, iface: &TypeDesc) -> bool {
        iface.iface_methods.iter().all(|id| self.has_method(*id))
    }

    /// Whether the method set holds the method with this id.
    ///
    /// A concrete type answers from its wrapper table and an interface type
    /// from its own method list, because an interface type has no wrappers —
    /// its values' methods come from their dynamic types. One interface
    /// implementing another is a question `reflect.Type.Implements` is asked
    /// all the same, and `encoding/json` asks it of every field whose type is
    /// `json.Marshaler`.
    fn has_method(&self, id: MethodId) -> bool {
        self.method(id).is_some() || self.iface_methods.binary_search(&id).is_ok()
    }

    fn method(&self, id: MethodId) -> Option<ErasedFn> {
        self.methods
            .binary_search_by_key(&id, |(i, _)| *i)
            .ok()
            .map(|i| self.methods[i].1)
    }
}

/// The data word of an interface value: the address of the boxed value.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Data(usize);

impl Data {
    /// Boxes nothing: the data of a nil interface.
    pub const NONE: Data = Data(0);

    /// The data word pointing at a boxed value.
    #[inline]
    pub fn of<P>(p: Ptr<P>) -> Data {
        Data(p.addr() as usize)
    }

    /// Recovers the boxed value's place, as the type that boxed it.
    #[inline]
    pub fn cast<P>(self) -> Ptr<P> {
        // SAFETY (invariant): generated code only casts a data word back to
        // the place type its own descriptor boxed, and the interface value
        // keeps it alive.
        unsafe { Ptr::from_addr(self.0) }
    }

    /// The address `println` shows.
    pub fn addr(self) -> u64 {
        self.0 as u64
    }
}

impl Trace for Data {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        t.edge(self.0);
    }
}

/// A Go interface value.
#[derive(Clone, Copy)]
pub struct Iface {
    desc: Option<&'static TypeDesc>,
    data: Data,
}

impl GoValue for Iface {
    #[inline]
    fn zero() -> Self {
        Iface {
            desc: None,
            data: Data::NONE,
        }
    }
}

impl Trace for Iface {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        self.data.trace(t);
    }
}

impl PartialEq for Iface {
    /// Two interface values are equal when their dynamic types are the same
    /// and their values compare equal. Comparing values of an uncomparable
    /// type panics, as in Go.
    fn eq(&self, other: &Self) -> bool {
        match (self.desc, other.desc) {
            (None, None) => true,
            (Some(a), Some(b)) => {
                if !core::ptr::eq(a, b) {
                    return false;
                }
                match a.equal {
                    Some(eq) => eq(self.data, other.data),
                    None => {
                        crate::panic::runtime_error(crate::panic::RuntimeError::UncomparableType {
                            type_name: a.name,
                        })
                    }
                }
            }
            _ => false,
        }
    }
}

impl Iface {
    /// An interface value holding a boxed concrete value.
    #[inline]
    pub fn new(desc: &'static TypeDesc, data: Data) -> Self {
        Iface {
            desc: Some(desc),
            data,
        }
    }

    /// The nil interface value, as an inherent method.
    #[inline]
    pub fn nil() -> Self {
        Iface {
            desc: None,
            data: Data::NONE,
        }
    }

    /// The hash of an interface used as a map key: the dynamic type and the
    /// value together. Panics like gc if that type is not comparable.
    pub fn hash_value(self) -> u64 {
        let Some(desc) = self.desc else {
            return 0;
        };
        match desc.hash {
            Some(h) => crate::map::mix(desc.name.len() as u64, h(self.data)),
            None => crate::panic::runtime_error(crate::panic::RuntimeError::UnhashableKey {
                type_name: desc.name,
            }),
        }
    }

    /// The address of the dynamic type's descriptor, or 0 for nil: how
    /// reflection refers to a type.
    pub fn desc_addr(self) -> u64 {
        self.desc
            .map_or(0, |d| d as *const TypeDesc as usize as u64)
    }

    /// `x == nil`.
    #[inline]
    pub fn is_nil(self) -> bool {
        self.desc.is_none()
    }

    /// The data word, to pass to a method wrapper.
    #[inline]
    pub fn data(self) -> Data {
        self.data
    }

    /// The dynamic type's name, or `nil`.
    pub fn type_name(self) -> &'static str {
        self.desc.map_or("nil", |d| d.name)
    }

    /// Appends the value as `print` renders it.
    pub fn print_to(self, out: &mut Vec<u8>) {
        match self.desc {
            Some(d) => (d.print)(self.data, out),
            None => out.extend_from_slice(b"<nil>"),
        }
    }

    /// The method with this id, as the signature the caller expects.
    ///
    /// Calling a method on a nil interface panics like gc.
    #[inline]
    pub fn method<F: Copy>(self, id: MethodId) -> F {
        assert_eq!(
            size_of::<F>(),
            size_of::<*const ()>(),
            "method type must be a fn pointer"
        );
        let Some(desc) = self.desc else {
            crate::panic::runtime_error(crate::panic::RuntimeError::NilDeref)
        };
        let Some(f) = desc.method(id) else {
            // The emitter only asks for methods the static type has, so a
            // miss means the tables disagree.
            unreachable!("method {id} missing from {}", desc.name)
        };
        // SAFETY (invariant): the emitter numbers methods by name and
        // signature, and generates every wrapper for that id with the
        // signature `F` its callers use.
        unsafe { core::mem::transmute_copy::<*const (), F>(&f.0) }
    }

    /// `x.(T)` for a concrete type: the boxed value's place, or Go's panic.
    pub fn assert_concrete(self, want: &'static TypeDesc, iface_name: &str) -> Data {
        let (data, ok) = self.try_concrete(want);
        if !ok {
            runtime_error_msg(match self.desc {
                Some(d) => format!(
                    "interface conversion: {iface_name} is {}, not {}",
                    d.name, want.name
                ),
                None => format!(
                    "interface conversion: {iface_name} is nil, not {}",
                    want.name
                ),
            });
        }
        data
    }

    /// `v, ok := x.(T)` for a concrete type.
    pub fn try_concrete(self, want: &'static TypeDesc) -> (Data, bool) {
        match self.desc {
            Some(d) if core::ptr::eq(d, want) => (self.data, true),
            _ => (Data::NONE, false),
        }
    }

    /// Whether the dynamic type has all of these methods.
    pub fn implements(self, ids: &[MethodId]) -> bool {
        match self.desc {
            Some(d) => ids.iter().all(|id| d.method(*id).is_some()),
            None => false,
        }
    }

    /// `x.(I)` for an interface type: the same value, or Go's panic naming
    /// the first missing method.
    pub fn assert_iface(
        self,
        ids: &[MethodId],
        names: &[&str],
        want: &str,
        iface_name: &str,
    ) -> Iface {
        let Some(desc) = self.desc else {
            runtime_error_msg(format!(
                "interface conversion: {iface_name} is nil, not {want}"
            ))
        };
        for (id, name) in ids.iter().zip(names) {
            if desc.method(*id).is_none() {
                runtime_error_msg(format!(
                    "interface conversion: {} is not {want}: missing method {name}",
                    desc.name
                ));
            }
        }
        self
    }
}

/// The descriptor at an address [`Iface::desc_addr`] produced.
fn desc_at(p: crate::unsafe_ptr::UPtr) -> &'static TypeDesc {
    assert!(p.addr() != 0, "reflection on a nil type");
    // SAFETY: the address came from `desc_addr`, which takes it from a
    // `&'static TypeDesc`, and descriptors are statics that never move.
    unsafe { &*(p.addr() as usize as *const TypeDesc) }
}

/// Whether values of the type can be compared with `==`.
pub fn desc_comparable(p: crate::unsafe_ptr::UPtr) -> bool {
    desc_at(p).equal.is_some()
}

/// The type's name as Go prints it.
pub fn desc_name(p: crate::unsafe_ptr::UPtr) -> crate::string::GoStr {
    crate::string::GoStr::lit(desc_at(p).name.as_bytes())
}
