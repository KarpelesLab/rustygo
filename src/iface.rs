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

use crate::panic::{GoPanic, go_panic};
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
    /// Erases a method wrapper, which generated code spells
    /// `ErasedFn::new(wrapper as *const ())`.
    pub const fn new(code: *const ()) -> Self {
        ErasedFn(code)
    }
}

/// What the runtime knows about one concrete type.
pub struct TypeDesc {
    /// As Go prints it: `main.Point`, `int`, `*main.Node`.
    pub name: &'static str,
    /// The type's method set, sorted by id.
    pub methods: &'static [(MethodId, ErasedFn)],
    /// Compares two values of this type, or `None` if Go says the type is
    /// not comparable (a slice, map or func).
    pub equal: Option<fn(Data, Data) -> bool>,
    /// Hashes a value of this type, for use as a map key. `None` exactly
    /// when `equal` is.
    pub hash: Option<fn(Data) -> u64>,
    /// Appends the value as `print` and `panic` render it.
    pub print: fn(Data, &mut Vec<u8>),
}

impl TypeDesc {
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
            go_panic(GoPanic::new(match self.desc {
                Some(d) => format!(
                    "interface conversion: {iface_name} is {}, not {}",
                    d.name, want.name
                ),
                None => format!(
                    "interface conversion: {iface_name} is nil, not {}",
                    want.name
                ),
            }));
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
            go_panic(GoPanic::new(format!(
                "interface conversion: {iface_name} is nil, not {want}"
            )))
        };
        for (id, name) in ids.iter().zip(names) {
            if desc.method(*id).is_none() {
                go_panic(GoPanic::new(format!(
                    "interface conversion: {} is not {want}: missing method {name}",
                    desc.name
                )));
            }
        }
        self
    }
}
