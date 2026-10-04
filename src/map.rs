//! Go maps.
//!
//! A map is a handle to one heap object holding an open-addressing table.
//! Keys and values are stored by value — Go forbids `&m[k]`, so map elements
//! need no places — and the object's trace walks the live entries, which is
//! how keys and values stay reachable.
//!
//! Hashing follows Go's rules rather than Rust's: a key type provides
//! [`GoKey`], floats hash by value (so `NaN` is never found again, as in Go),
//! and interfaces hash through their type descriptor.
//!
//! Iteration starts at a pseudo-random bucket, like gc, so programs cannot
//! come to depend on the order.
//!
//! **Not synchronized**, because a Go map is not. Go allows any number of
//! goroutines to read one map at once and makes a concurrent write the
//! program's own bug, which gc reports as a fatal error and rustygo does not yet
//! detect. The table therefore sits in a plain [`UnsafeCell`] rather than a
//! `RefCell`: a `RefCell`'s borrow counter is itself shared mutable state, so
//! two reads that Go says are perfectly legal would race on *it*, and the
//! program's own synchronization — which is what Go asks for around a write —
//! would not help, because it was never told about the counter. Concurrent
//! reads are then plain loads, which is the position DESIGN §2 takes for every
//! other word on the heap and §13 question 6 leaves open.
//!
//! **M1 status:** one table, grown by rehashing, with no incremental growth
//! and no per-size-class allocation.

use crate::heap;
use crate::panic::{RuntimeError, runtime_error};
use crate::trace::{Trace, Tracer};
use crate::value::GoValue;
use alloc::vec::Vec;
use core::cell::UnsafeCell;

/// A key type: Go's equality and hashing for map keys.
pub trait GoKey: GoValue + Trace {
    /// The key's hash. Equal keys must hash equally.
    fn go_hash(&self) -> u64;
    /// Go's `==` on keys. `NaN` is equal to nothing, itself included.
    fn go_eq(&self, other: &Self) -> bool;
}

/// One slot of the table.
enum Slot<K, V> {
    Empty,
    /// Removed: the probe sequence must continue past it.
    Dead,
    Live(K, V),
}

struct Table<K, V> {
    slots: Vec<Slot<K, V>>,
    live: usize,
    /// Live plus dead, which is what decides when to grow.
    used: usize,
    /// Advances on every iteration, so the starting bucket varies.
    seed: u64,
}

/// The heap object behind a map handle.
pub struct MapObj<K, V> {
    table: UnsafeCell<Table<K, V>>,
}

impl<K: GoKey, V: GoValue + Trace> Trace for MapObj<K, V> {
    fn trace(&self, t: &mut Tracer<'_>) {
        // SAFETY: a collection reaches this with every goroutine stopped at a
        // safe point, and no safe point falls inside one of the accessors
        // below: they move words, and growing the table allocates from Rust's
        // allocator rather than the Go heap.
        for slot in &unsafe { &*self.table.get() }.slots {
            if let Slot::Live(k, v) = slot {
                k.trace(t);
                v.trace(t);
            }
        }
    }
}

/// A Go `map[K]V`.
pub struct GoMap<K: 'static, V: 'static> {
    obj: Option<core::ptr::NonNull<MapObj<K, V>>>,
}

impl<K, V> Clone for GoMap<K, V> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<K, V> Copy for GoMap<K, V> {}

impl<K: 'static, V: 'static> GoValue for GoMap<K, V> {
    #[inline]
    fn zero() -> Self {
        GoMap { obj: None }
    }
}

impl<K, V> Trace for GoMap<K, V> {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        t.edge(self.addr() as usize);
    }
}

impl<K, V> GoMap<K, V> {
    /// `m == nil`.
    #[inline]
    pub fn is_nil(self) -> bool {
        self.obj.is_none()
    }

    /// The address `println` shows, and what the collector resolves.
    #[inline]
    pub fn addr(self) -> u64 {
        self.obj.map_or(0, |p| p.as_ptr() as usize as u64)
    }
}

impl<K: GoKey, V: GoValue + Trace> GoMap<K, V> {
    /// `make(map[K]V)`. A safe point: it allocates.
    pub fn make(hint: i64) -> Self {
        let cap = (hint.max(0) as usize).next_power_of_two().max(8);
        let obj = heap::allocate(MapObj {
            table: UnsafeCell::new(Table {
                slots: (0..cap).map(|_| Slot::Empty).collect(),
                live: 0,
                used: 0,
                seed: 0x9E3779B97F4A7C15,
            }),
        });
        GoMap { obj: Some(obj) }
    }

    /// `len(m)`.
    pub fn len(self) -> i64 {
        self.with(|t| t.live as i64).unwrap_or(0)
    }

    /// `len(m) == 0`.
    pub fn is_empty(self) -> bool {
        self.len() == 0
    }

    /// `m[k]`: the value, or the zero value when absent.
    pub fn get(self, k: K) -> V {
        self.get_ok(k).0
    }

    /// `v, ok := m[k]`.
    pub fn get_ok(self, k: K) -> (V, bool) {
        self.with(|t| match t.find(&k) {
            Some(i) => match &t.slots[i] {
                Slot::Live(_, v) => (*v, true),
                _ => (V::zero(), false),
            },
            None => (V::zero(), false),
        })
        .unwrap_or((V::zero(), false))
    }

    /// `k, ok := m[k]` where only membership matters.
    pub fn contains(self, k: K) -> bool {
        self.get_ok(k).1
    }

    /// `m[k] = v`. Assigning to a nil map panics, as in Go.
    pub fn set(self, k: K, v: V) {
        let Some(obj) = self.obj else {
            runtime_error(RuntimeError::NilMapWrite)
        };
        heap::check_live(obj.as_ptr() as usize);
        // SAFETY: a non-nil handle points at a live map object, which the
        // caller keeps rooted.
        // SAFETY: as `with_mut`.
        let t = unsafe { &mut *obj.as_ref().table.get() };
        if (t.used + 1) * 4 >= t.slots.len() * 3 {
            t.grow();
        }
        t.insert(k, v);
    }

    /// `delete(m, k)`. Deleting from a nil map does nothing.
    pub fn delete(self, k: K) {
        self.with_mut(|t| {
            if let Some(i) = t.find(&k) {
                t.slots[i] = Slot::Dead;
                t.live -= 1;
            }
        });
    }

    /// `clear(m)`.
    pub fn clear(self) {
        self.with_mut(|t| {
            for slot in &mut t.slots {
                *slot = Slot::Empty;
            }
            t.live = 0;
            t.used = 0;
        });
    }

    /// The iterator of `for k, v := range m`.
    pub fn iter(self) -> MapIter<K, V> {
        let start = self
            .with_mut(|t| {
                // A different starting bucket every time, as gc does.
                t.seed = t.seed.wrapping_mul(6364136223846793005).wrapping_add(1);
                (t.seed >> 33) as usize
            })
            .unwrap_or(0);
        MapIter {
            map: self,
            start,
            step: 0,
        }
    }

    fn with<R>(self, f: impl FnOnce(&Table<K, V>) -> R) -> Option<R> {
        let obj = self.obj?;
        heap::check_live(obj.as_ptr() as usize);
        // SAFETY: a non-nil handle points at a live map object, which the caller
        // keeps rooted. Nothing in `f` reaches this map again — it reads slots
        // and computes — and a Go program that reads a map while another
        // goroutine writes it is in the wrong, as the module comment explains.
        Some(f(unsafe { &*obj.as_ref().table.get() }))
    }

    fn with_mut<R>(self, f: impl FnOnce(&mut Table<K, V>) -> R) -> Option<R> {
        let obj = self.obj?;
        heap::check_live(obj.as_ptr() as usize);
        // SAFETY: as `with`.
        Some(f(unsafe { &mut *obj.as_ref().table.get() }))
    }
}

/// Looking a string key up from bytes, for the one key type a Go string has:
/// a named string type unfolds to `GoStr` like any other named non-struct type,
/// so every `map[...]V` with a string key is a `GoMap<GoStr, V>`.
impl<V: GoValue + Trace> GoMap<crate::string::GoStr, V> {
    /// `m[string(b)]` over a byte slice's own array, without the string the
    /// conversion would have made.
    ///
    /// gc elides the same copy and the standard library leans on it:
    /// `net/textproto` looks up every header name this way. A lookup only reads
    /// its key, so the bytes answer everything the string would; the emitter
    /// decides when the shape applies (bytecmp.go).
    ///
    /// Nothing in here allocates, so the borrow into the slice's array crosses
    /// no safe point.
    pub fn get_bytes(self, b: crate::slice::Slice<crate::place::Slot<u8>>) -> V {
        self.get_bytes_ok(b).0
    }

    /// `v, ok := m[string(b)]`, the same way.
    pub fn get_bytes_ok(self, b: crate::slice::Slice<crate::place::Slot<u8>>) -> (V, bool) {
        b.with_bytes(|bs| {
            self.with(|t| match t.find_by(hash_bytes(bs), |k| k.bytes() == bs) {
                Some(i) => match &t.slots[i] {
                    Slot::Live(_, v) => (*v, true),
                    _ => (V::zero(), false),
                },
                None => (V::zero(), false),
            })
            .unwrap_or((V::zero(), false))
        })
    }
}

impl<K: GoKey, V: GoValue> Table<K, V> {
    /// The slot holding `k`, if any.
    fn find(&self, k: &K) -> Option<usize> {
        self.find_by(k.go_hash(), |key| key.go_eq(k))
    }

    /// The slot a key with this hash sits in, whatever stands for the key.
    ///
    /// Taking the hash and the comparison rather than the key itself is what
    /// lets `m[string(b)]` be answered without making the string: the bytes
    /// hash and compare the same way the string would (`get_bytes`).
    fn find_by(&self, hash: u64, eq: impl Fn(&K) -> bool) -> Option<usize> {
        let mask = self.slots.len() - 1;
        let mut i = (hash as usize) & mask;
        for _ in 0..self.slots.len() {
            match &self.slots[i] {
                Slot::Empty => return None,
                Slot::Live(key, _) if eq(key) => return Some(i),
                _ => i = (i + 1) & mask,
            }
        }
        None
    }

    fn insert(&mut self, k: K, v: V) {
        if let Some(i) = self.find(&k) {
            self.slots[i] = Slot::Live(k, v);
            return;
        }
        let mask = self.slots.len() - 1;
        let mut i = (k.go_hash() as usize) & mask;
        loop {
            match &self.slots[i] {
                Slot::Live(..) => i = (i + 1) & mask,
                _ => {
                    self.slots[i] = Slot::Live(k, v);
                    self.live += 1;
                    self.used += 1;
                    return;
                }
            }
        }
    }

    /// Rehashes into a table twice the size, dropping the dead slots.
    fn grow(&mut self) {
        let cap = (self.live.max(1) * 4).next_power_of_two().max(8);
        let old = core::mem::replace(
            &mut self.slots,
            (0..cap).map(|_| Slot::Empty).collect::<Vec<_>>(),
        );
        self.live = 0;
        self.used = 0;
        for slot in old {
            if let Slot::Live(k, v) = slot {
                self.insert(k, v);
            }
        }
    }
}

/// State of a `for k, v := range m` loop.
pub struct MapIter<K: 'static, V: 'static> {
    map: GoMap<K, V>,
    start: usize,
    step: usize,
}

impl<K, V> Clone for MapIter<K, V> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<K, V> Copy for MapIter<K, V> {}

impl<K: 'static, V: 'static> GoValue for MapIter<K, V> {
    fn zero() -> Self {
        MapIter {
            map: GoMap::zero(),
            start: 0,
            step: 0,
        }
    }
}

impl<K, V> Trace for MapIter<K, V> {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        self.map.trace(t);
    }
}

impl<K: GoKey, V: GoValue + Trace> MapIter<K, V> {
    /// Advances: `(ok, key, value)`.
    ///
    /// Entries added during the iteration may or may not be produced, and
    /// deleted ones are not, which is what Go promises.
    pub fn advance(&mut self) -> (bool, K, V) {
        let n = self.map.with(|t| t.slots.len()).unwrap_or(0);
        while self.step < n {
            let i = (self.start + self.step) % n;
            self.step += 1;
            let entry = self.map.with(|t| match &t.slots[i] {
                Slot::Live(k, v) => Some((*k, *v)),
                _ => None,
            });
            if let Some(Some((k, v))) = entry {
                return (true, k, v);
            }
        }
        (false, K::zero(), V::zero())
    }
}

/// Mixes a word into a hash. Any avalanche would do; Go's own hash is
/// seeded per process and not reproduced here.
#[inline]
pub fn mix(h: u64, word: u64) -> u64 {
    let mut x = h ^ word.wrapping_mul(0x9E3779B97F4A7C15);
    x ^= x >> 29;
    x = x.wrapping_mul(0xBF58476D1CE4E5B9);
    x ^= x >> 32;
    x
}

macro_rules! int_key {
    ($($t:ty),*) => {$(
        impl GoKey for $t {
            #[inline]
            fn go_hash(&self) -> u64 { mix(0, *self as u64) }
            #[inline]
            fn go_eq(&self, other: &Self) -> bool { self == other }
        }
    )*};
}

int_key!(i8, i16, i32, i64, u8, u16, u32, u64);

impl GoKey for bool {
    #[inline]
    fn go_hash(&self) -> u64 {
        mix(0, *self as u64)
    }
    #[inline]
    fn go_eq(&self, other: &Self) -> bool {
        self == other
    }
}

macro_rules! float_key {
    ($($t:ty),*) => {$(
        impl GoKey for $t {
            #[inline]
            fn go_hash(&self) -> u64 {
                // +0.0 and -0.0 are equal keys in Go, so they must agree.
                if *self == 0.0 { mix(0, 0) } else { mix(0, self.to_bits() as u64) }
            }
            #[inline]
            fn go_eq(&self, other: &Self) -> bool {
                // NaN != NaN: a NaN key can never be found again, as in Go.
                self == other
            }
        }
    )*};
}

float_key!(f32, f64);

impl GoKey for crate::string::GoStr {
    #[inline]
    fn go_hash(&self) -> u64 {
        hash_bytes(self.bytes())
    }
    #[inline]
    fn go_eq(&self, other: &Self) -> bool {
        self == other
    }
}

/// A string key's hash, over the bytes alone, so that `m[string(b)]` can be
/// answered from the slice's own array without a string ever existing. The one
/// definition, used by both, because the two drifting apart would lose keys.
#[inline]
pub(crate) fn hash_bytes(bytes: &[u8]) -> u64 {
    let mut h = 0u64;
    for b in bytes {
        h = mix(h, *b as u64);
    }
    mix(h, bytes.len() as u64)
}

impl GoKey for crate::unsafe_ptr::UPtr {
    #[inline]
    fn go_hash(&self) -> u64 {
        mix(0, self.addr())
    }
    #[inline]
    fn go_eq(&self, other: &Self) -> bool {
        self == other
    }
}

// A channel is comparable in Go: two are equal when one `make` produced both,
// which is its address.
#[cfg(feature = "std")]
impl<T> GoKey for crate::chan::Chan<T> {
    #[inline]
    fn go_hash(&self) -> u64 {
        mix(0, self.addr())
    }
    #[inline]
    fn go_eq(&self, other: &Self) -> bool {
        self == other
    }
}

impl<P> GoKey for crate::place::Ptr<P> {
    #[inline]
    fn go_hash(&self) -> u64 {
        mix(0, self.addr())
    }
    #[inline]
    fn go_eq(&self, other: &Self) -> bool {
        self == other
    }
}

impl<T: GoKey, const N: usize> GoKey for [T; N] {
    #[inline]
    fn go_hash(&self) -> u64 {
        let mut h = 0u64;
        for v in self {
            h = mix(h, v.go_hash());
        }
        h
    }
    #[inline]
    fn go_eq(&self, other: &Self) -> bool {
        self.iter().zip(other).all(|(a, b)| a.go_eq(b))
    }
}

impl<F: GoKey + GoValue> GoKey for crate::complex::Complex<F> {
    #[inline]
    fn go_hash(&self) -> u64 {
        mix(self.re.go_hash(), self.im.go_hash())
    }
    #[inline]
    fn go_eq(&self, other: &Self) -> bool {
        // Both parts compare as floats do, so a NaN part is never equal.
        self.re.go_eq(&other.re) && self.im.go_eq(&other.im)
    }
}

impl GoKey for crate::iface::Iface {
    /// An interface key hashes through its type descriptor, and panics like
    /// gc if the dynamic type is not comparable.
    #[inline]
    fn go_hash(&self) -> u64 {
        self.hash_value()
    }
    #[inline]
    fn go_eq(&self, other: &Self) -> bool {
        self == other
    }
}
