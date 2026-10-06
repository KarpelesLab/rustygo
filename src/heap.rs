//! The heap and its collector.
//!
//! M1 phase 1, as planned in DESIGN §3: a precise, stop-the-world mark-sweep
//! collector over one heap, correct and boring. Allocation triggers a
//! collection once the live set has grown past a threshold.
//!
//! Every object is an entry in a table holding its address range, its layout
//! and its [`Trace`] function. The table is what makes **interior pointers**
//! work: `&s.f` and `&a[i]` are ordinary one-word pointers into the middle of
//! an object, and marking resolves any address back to its object by binary
//! search. Words that resolve to nothing — a string literal in the binary, a
//! nil pointer, a non-heap address — are skipped.
//!
//! Roots are the shadow stack ([`crate::gc`]) and the registered globals.
//!
//! **One heap, one lock.** Every thread allocates from the same table, so
//! every allocation takes the lock around it; what that costs, and whether
//! per-thread caches over size classes are worth their complexity, is M6's
//! question and wants measuring first.

use crate::trace::{Trace, TraceFn, Tracer, trace_array_fn, trace_fn};
use alloc::alloc::{Layout, alloc, dealloc};
use alloc::collections::BTreeMap;
use alloc::vec::Vec;
use core::ptr::NonNull;

/// One heap object. Kept small: there is one of these per allocation, and a
/// linked list of a million nodes is a million entries.
struct Obj {
    /// Payload start, the address generated code points at.
    start: usize,
    /// Payload bytes.
    size: usize,
    trace: TraceFn,
    /// Runs the payload's Rust destructor before the memory is freed. Only
    /// objects that own memory outside the heap need one — a map's table,
    /// for instance.
    drop: Option<unsafe fn(*mut u8)>,
    /// log2 of the allocation's alignment; with `size` it rebuilds the
    /// `Layout` that `dealloc` needs.
    align_log2: u8,
    mark: bool,
}

impl Obj {
    /// One past the payload's last byte. A zero-sized object still occupies
    /// one byte, so distinct objects have distinct addresses.
    fn end(&self) -> usize {
        self.start + self.size.max(1)
    }

    fn layout(&self) -> Layout {
        Layout::from_size_align(self.size.max(1), 1 << self.align_log2).expect("layout was valid")
    }
}

/// The heap: every object, plus the global roots.
pub struct Heap {
    objs: Vec<Obj>,
    sorted: bool,
    live_bytes: usize,
    threshold: usize,
    globals: Vec<(usize, usize, TraceFn)>,
    collections: usize,
    /// Every object and payload byte ever allocated, which Go reports as
    /// TotalAlloc and Mallocs and never decreases.
    total_objects: u64,
    total_bytes: u64,
    /// GC torture only: collected objects, start to end. Their memory is
    /// poisoned and never reused, so any later use of one is a missing root
    /// and panics by name. A map, because under torture every allocation
    /// collects and a sorted vector would be re-sorted each time.
    quarantine: BTreeMap<usize, usize>,
}

/// Collect once the live set reaches this, before any doubling.
const MIN_THRESHOLD: usize = 4 << 20;

// One heap for the program, behind one lock (`src/tls.rs`). A Go value may be
// reached from any goroutine, so it cannot be allocated out of storage that
// belongs to a thread: the object table every mark phase walks has to hold
// every object there is.
//
// One lock around the whole heap is the simplest thing that is correct, and
// correct is what matters first (DESIGN §3, phase 1). It serializes
// allocation, which a program that allocates on several threads at once will
// feel; the answer is per-thread allocation caches over size classes, which is
// M6's size-class allocator and wants the cost of this measured before it is
// written.
//
// Const-initialized: no lazy-init check on the path every allocation takes.
rt_shared! {
    static HEAP: Heap = Heap {
        objs: Vec::new(),
        sorted: true,
        live_bytes: 0,
        threshold: MIN_THRESHOLD,
        globals: Vec::new(),
        collections: 0,
        total_objects: 0,
        total_bytes: 0,
        quarantine: BTreeMap::new(),
    };
}

/// Runs `f` with the heap locked.
///
/// The outermost of the runtime's locks: a collection holds this one while it
/// asks the scheduler for parked goroutines' roots and the finalizer table for
/// its own, so nothing on the other side of those may allocate. Nothing does —
/// their critical sections move words.
fn with_heap<R>(f: impl FnOnce(&mut Heap) -> R) -> R {
    HEAP.with(f)
}

/// Allocates space for a `T`, collecting first if the heap has grown enough,
/// and returns a pointer to it.
///
/// The value is written after any collection, so nothing this call reads has
/// to survive one.
pub fn allocate<T: Trace>(value: T) -> NonNull<T> {
    let layout = Layout::new::<T>();
    if layout.size() == 0 {
        // Go gives every zero-sized allocation one shared address, and
        // programs compare those pointers (test/zerosize.go). There is
        // nothing to write and nothing to collect.
        return zerobase().cast();
    }
    // Before the heap's lock, not after: a collector that has asked the world
    // to stop is waiting for this thread, and it cannot be waiting for it to
    // give back a lock the collector itself is about to want.
    crate::gc::safepoint();
    let should_collect = with_heap(|h| h.live_bytes + layout.size() > h.threshold);
    if should_collect || cfg!(feature = "gc-torture") {
        collect();
    }
    // SAFETY: `Layout::new::<T>` has a non-zero size for every type rustygo
    // allocates (a zero-sized payload still gets one byte through `pad`).
    let ptr = unsafe { alloc(pad(layout)) } as *mut T;
    let Some(ptr) = NonNull::new(ptr) else {
        alloc::alloc::handle_alloc_error(layout)
    };
    // SAFETY: freshly allocated, correctly aligned, and not yet reachable.
    unsafe { ptr.write(value) };
    with_heap(|h| {
        h.objs.push(Obj {
            start: ptr.as_ptr() as usize,
            size: layout.size(),
            trace: trace_fn::<T>(),
            drop: needs_drop::<T>(),
            align_log2: layout.align().trailing_zeros() as u8,
            mark: false,
        });
        h.sorted = false;
        h.live_bytes += layout.size();
        h.total_objects += 1;
        h.total_bytes += layout.size() as u64;
    });
    ptr
}

/// Allocates `len` bytes on the heap and fills them through `fill`: the
/// backing array of a string (and of a byte slice, from M1 on). Bytes hold no
/// references.
///
/// `fill` receives uninitialized memory and must write all of it. Taking a
/// closure lets a caller build the bytes in place — string concatenation
/// writes both halves straight into the object instead of through a
/// temporary.
pub fn allocate_bytes(len: usize, fill: impl FnOnce(&mut [u8])) -> NonNull<u8> {
    assert!(len > 0, "zero-length allocations use a static empty");
    let layout = Layout::array::<u8>(len).expect("fits in memory");
    crate::gc::safepoint();
    let should_collect = with_heap(|h| h.live_bytes + len > h.threshold);
    if should_collect || cfg!(feature = "gc-torture") {
        collect();
    }
    // SAFETY: `len` is non-zero, so the layout has a non-zero size.
    let ptr = unsafe { alloc(layout) };
    let Some(ptr) = NonNull::new(ptr) else {
        alloc::alloc::handle_alloc_error(layout)
    };
    // SAFETY: `len` freshly allocated bytes, not aliased and not yet read.
    // `u8` needs no initialization to form a reference.
    fill(unsafe { core::slice::from_raw_parts_mut(ptr.as_ptr(), len) });
    with_heap(|h| {
        h.objs.push(Obj {
            start: ptr.as_ptr() as usize,
            size: len,
            trace: |_, _, _| {},
            drop: None,
            align_log2: 0,
            mark: false,
        });
        h.sorted = false;
        h.live_bytes += len;
        h.total_objects += 1;
        h.total_bytes += len as u64;
    });
    ptr
}

/// Allocates `n` places for a slice's backing array, each holding the zero
/// value, and returns a pointer to the first. A safe point.
pub fn allocate_array<P: crate::place::Place + Trace>(n: usize) -> NonNull<P> {
    let layout = Layout::array::<P>(n).expect("slice fits in memory");
    crate::gc::safepoint();
    let should_collect = with_heap(|h| h.live_bytes + layout.size() > h.threshold);
    if should_collect || cfg!(feature = "gc-torture") {
        collect();
    }
    // SAFETY: `pad` gives a zero-length array a one-byte layout, so the size
    // is never zero.
    let ptr = unsafe { alloc(pad(layout)) } as *mut P;
    let Some(ptr) = NonNull::new(ptr) else {
        alloc::alloc::handle_alloc_error(layout)
    };
    for i in 0..n {
        // SAFETY: `i < n`, so this is inside the allocation, and nothing has
        // been written there yet.
        unsafe { ptr.add(i).write(P::new(crate::value::GoValue::zero())) };
    }
    with_heap(|h| {
        h.objs.push(Obj {
            start: ptr.as_ptr() as usize,
            size: layout.size(),
            trace: trace_array_fn::<P>(),
            drop: needs_drop::<P>(),
            align_log2: layout.align().trailing_zeros() as u8,
            mark: false,
        });
        h.sorted = false;
        h.live_bytes += layout.size();
        h.total_objects += 1;
        h.total_bytes += layout.size() as u64;
    });
    ptr
}

/// The destructor for `T`, if it has one worth running.
fn needs_drop<T>() -> Option<unsafe fn(*mut u8)> {
    if core::mem::needs_drop::<T>() {
        // SAFETY: called once, on the payload of an object allocated as `T`,
        // just before its memory is freed.
        Some(|p| unsafe { core::ptr::drop_in_place(p as *mut T) })
    } else {
        None
    }
}

/// The address every zero-sized allocation shares, as gc's `zerobase` is.
/// Aligned generously, so it suits a zero-sized type of any alignment.
pub(crate) fn zerobase() -> NonNull<u8> {
    // Its contents are never read; only its address matters.
    #[repr(align(16))]
    struct Zerobase([u8; 0]);
    static ZEROBASE: Zerobase = Zerobase([]);
    NonNull::from(&ZEROBASE).cast()
}

/// A zero-sized payload still needs a distinct address.
fn pad(l: Layout) -> Layout {
    if l.size() == 0 {
        Layout::from_size_align(1, l.align()).expect("valid layout")
    } else {
        l
    }
}

/// A Go package-level variable's storage.
///
/// One place for the program, not one per thread: a global that a goroutine
/// writes on one worker has to be the one another goroutine reads on another,
/// and registering it as a root has to happen exactly once. Before this, the
/// emitter put every global in a thread-local, which was right while one thread
/// ran everything and silently wrong as soon as two did.
///
/// Made on first use rather than at compile time, because a place is a struct of
/// cells that no `const` expression can build. After that a read of the variable
/// is a load and a branch, which is no worse than the thread-local access it
/// replaces.
pub struct Global<P: 'static> {
    /// The place's address, as a `Ptr` holds it, or 0 before it is made.
    at: core::sync::atomic::AtomicUsize,
    /// Serializes the making of it, so that two goroutines reaching the variable
    /// at the same moment do not each leak a place and register a root for it.
    once: crate::tls::Lock<()>,
    kind: core::marker::PhantomData<fn() -> P>,
}

impl<P: crate::place::Place + Trace> Default for Global<P> {
    fn default() -> Self {
        Self::new()
    }
}

impl<P: crate::place::Place + Trace> Global<P> {
    /// Storage that has not been made yet.
    pub const fn new() -> Self {
        Global {
            at: core::sync::atomic::AtomicUsize::new(0),
            once: crate::tls::Lock::new(()),
            kind: core::marker::PhantomData,
        }
    }

    /// The pointer generated code reads and writes the variable through.
    #[inline]
    pub fn get(&self) -> crate::place::Ptr<P> {
        let at = self.at.load(core::sync::atomic::Ordering::Acquire);
        if at == 0 {
            return self.make();
        }
        // SAFETY: the address was published by `make`, which leaked the place,
        // so it is a live place of this type for the rest of the program.
        unsafe { crate::place::Ptr::from_addr(at) }
    }

    #[cold]
    fn make(&self) -> crate::place::Ptr<P> {
        self.once.with(|()| {
            let at = self.at.load(core::sync::atomic::Ordering::Acquire);
            if at != 0 {
                // Another thread got here first.
                // SAFETY: as in `get`.
                return unsafe { crate::place::Ptr::from_addr(at) };
            }
            // The place lives for the rest of the program, outside the heap;
            // registering it is what makes its contents roots (DESIGN §3).
            let p: &'static P = alloc::boxed::Box::leak(alloc::boxed::Box::new(P::new(
                crate::value::GoValue::zero(),
            )));
            register_global(p);
            let ptr = crate::place::Ptr::to_global(p);
            self.at
                .store(ptr.addr() as usize, core::sync::atomic::Ordering::Release);
            ptr
        })
    }
}

/// Registers a global as a root. Globals live outside the heap and are never
/// collected; their contents are traced.
///
/// The place must live for the rest of the program, which is what generated
/// code registers.
pub fn register_global<T: Trace>(place: &'static T) {
    with_heap(|h| {
        h.globals
            .push((place as *const T as usize, size_of::<T>(), trace_fn::<T>()))
    });
}

/// Collects now: stop the world, mark from the roots, then sweep.
///
/// The world is stopped *before* the heap is locked, and that order is not
/// negotiable. A thread that has not reached a safe point yet may be inside a
/// heap critical section of its own, and waiting for it with the heap's lock
/// held would be waiting for a lock this thread is holding.
pub fn collect() {
    #[cfg(feature = "std")]
    let stopped = crate::sched::stop_the_world();
    with_heap(|h| {
        h.collections += 1;
        for o in &mut h.objs {
            o.mark = false;
        }
        if !h.sorted {
            h.objs.sort_unstable_by_key(|o| o.start);
            h.sorted = true;
        }
        let mut tracer = Tracer::new(h);
        crate::gc::trace_roots(&mut tracer);
        crate::panic::trace_panics(&mut tracer);
        #[cfg(feature = "std")]
        crate::sched::trace_parked_roots(&mut tracer);
        #[cfg(feature = "std")]
        crate::finalizer::trace_roots(&mut tracer);
        let globals = tracer.heap.globals.clone();
        for (addr, size, trace) in globals {
            // SAFETY: a registered global lives for the rest of the program,
            // and its trace function was derived from its own type.
            unsafe { trace(addr as *const u8, size, &mut tracer) };
        }
        tracer.drain();
        // Objects with finalizers that nothing reaches come back for one more
        // collection, with their finalizers queued (src/finalizer.rs).
        #[cfg(feature = "std")]
        crate::finalizer::resurrect(&mut tracer);

        let mut live = 0;
        let quarantine = &mut h.quarantine;
        h.objs.retain(|o| {
            if o.mark {
                live += o.size;
                return true;
            }
            if let Some(drop) = o.drop {
                // SAFETY: the object is unreachable, so nothing can observe
                // the payload afterwards, and this runs once.
                unsafe { drop(o.start as *mut u8) };
            }
            if cfg!(feature = "gc-torture") {
                // Poison and keep: the memory is never handed out again, so a
                // stale pointer into it can be recognized (`check_live`) and
                // reads through one see poison rather than someone else's
                // data.
                // SAFETY: the object's own allocation, which nothing live
                // references.
                unsafe { core::ptr::write_bytes(o.start as *mut u8, POISON, o.size) };
                quarantine.insert(o.start, o.end());
                return false;
            }
            // SAFETY: unreachable, so nothing points at it; freed once,
            // with the layout it was allocated with.
            unsafe { dealloc(o.start as *mut u8, o.layout()) };
            false
        });
        h.live_bytes = live;
        h.threshold = (live * 2).max(MIN_THRESHOLD);
    });
    // The other threads may run again from here, which they must before a
    // finalizer goroutine can be of any use.
    #[cfg(feature = "std")]
    drop(stopped);
    // Outside the heap's lock: this starts a goroutine, and the finalizers it
    // runs allocate.
    #[cfg(feature = "std")]
    crate::finalizer::kick();
}

/// The byte collected objects are filled with under GC torture.
const POISON: u8 = 0xA5;

/// GC torture only: panics if `addr` points into a collected object. Every
/// such use is a reference the emitted code (or the runtime) failed to root,
/// and this names it instead of letting it read garbage. Free otherwise.
#[inline]
pub fn check_live(addr: usize) {
    if cfg!(feature = "gc-torture") && addr != 0 {
        let hit = with_heap(|h| {
            h.quarantine
                .range(..=addr)
                .next_back()
                .is_some_and(|(_, &end)| addr < end)
        });
        if hit {
            panic!("rustygo gc-torture: use of collected object at {addr:#x} (a missing GC root)");
        }
    }
}

impl Heap {
    /// Finds the object containing `addr` and returns its index, if any.
    fn find(&self, addr: usize) -> Option<usize> {
        if addr == 0 {
            return None;
        }
        // `objs` is sorted by start during a collection.
        let i = self.objs.partition_point(|o| o.start <= addr);
        let o = self.objs.get(i.checked_sub(1)?)?;
        (addr < o.end()).then(|| i - 1)
    }
}

/// Heap statistics, as tests and `runtime.ReadMemStats` read them.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Stats {
    /// Objects currently allocated.
    pub objects: usize,
    /// Bytes of payload currently allocated.
    pub bytes: usize,
    /// Collections run so far.
    pub collections: usize,
    /// Objects allocated since the program started, freed ones included.
    pub total_objects: u64,
    /// Payload bytes allocated since the program started.
    pub total_bytes: u64,
}

/// Current heap statistics.
pub fn stats() -> Stats {
    with_heap(|h| Stats {
        objects: h.objs.len(),
        bytes: h.live_bytes,
        collections: h.collections,
        total_objects: h.total_objects,
        total_bytes: h.total_bytes,
    })
}

// Tracer needs to reach the object table; it lives here so `Obj` stays
// private to this module.
impl<'h> Tracer<'h> {
    pub(crate) fn new(heap: &'h mut Heap) -> Self {
        Tracer {
            heap,
            work: Vec::new(),
        }
    }

    /// Records a possible reference: marks the object containing `addr`, if
    /// there is one, and queues it for tracing.
    pub fn edge(&mut self, addr: usize) {
        if let Some(i) = self.heap.find(addr)
            && !self.heap.objs[i].mark
        {
            self.heap.objs[i].mark = true;
            self.work.push(i);
        }
    }

    /// Traces what the object beginning at `addr` points at, without marking
    /// the object itself. For finalizers, which need the scheduler and so
    /// exist only in a `std` build.
    ///
    /// gc does this for every object with a finalizer, and for two reasons.
    /// The finalizer is handed the object, so everything the object reaches
    /// has to survive with it; but marking the object would mean it was
    /// reachable, and it would never be finalized at all. It is also what
    /// orders two finalizers when one object points at the other: the
    /// pointed-at one stays marked until the pointer's own finalizer has run
    /// and freed it.
    #[cfg(feature = "std")]
    pub(crate) fn scan_contents(&mut self, addr: usize) {
        let Some(i) = self.heap.find(addr) else {
            return;
        };
        let o = &self.heap.objs[i];
        if o.start != addr {
            return;
        }
        let (start, size, trace) = (o.start, o.size, o.trace);
        // SAFETY: the object is live (it is in the table), and its trace
        // function was derived from the type it was allocated with.
        unsafe { trace(start as *const u8, size, self) };
    }

    /// Whether an object begins at `addr` and the mark phase did not reach
    /// it, which is what makes its finalizer ready to run.
    ///
    /// False for an address that begins no object, so a finalizer set on
    /// something that is not a heap object's first byte never runs. gc panics
    /// for that instead, but it can tell the difference at the moment of the
    /// call and this cannot: the object table is only in order during a
    /// collection.
    #[cfg(feature = "std")]
    pub(crate) fn unmarked_object(&self, addr: usize) -> bool {
        self.heap
            .find(addr)
            .map(|i| &self.heap.objs[i])
            .is_some_and(|o| o.start == addr && !o.mark)
    }

    /// Traces everything reachable from what has been marked.
    pub(crate) fn drain(&mut self) {
        while let Some(i) = self.work.pop() {
            let o = &self.heap.objs[i];
            let (start, size, trace) = (o.start, o.size, o.trace);
            // SAFETY: the object is live (it is in the table), and its trace
            // function was derived from the type it was allocated with.
            unsafe { trace(start as *const u8, size, self) };
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::gc::Frame;
    use crate::place::{Place, Ptr, Slot};
    use crate::string::GoStr;

    /// A node with a pointer and a string, the two reference shapes.
    struct NodeP {
        next: Slot<Ptr<NodeP>>,
        name: Slot<GoStr>,
    }

    impl Trace for NodeP {
        fn trace(&self, t: &mut Tracer<'_>) {
            self.next.trace(t);
            self.name.trace(t);
        }
    }

    impl Place for NodeP {
        type Value = (Ptr<NodeP>, GoStr);
        fn new(v: Self::Value) -> Self {
            NodeP {
                next: Place::new(v.0),
                name: Place::new(v.1),
            }
        }
        fn load(&self) -> Self::Value {
            (self.next.load(), self.name.load())
        }
        fn store(&self, v: Self::Value) {
            self.next.store(v.0);
            self.name.store(v.1);
        }
    }

    fn chain(n: usize) -> Ptr<NodeP> {
        let mut head = Ptr::<NodeP>::zero();
        // Both the list so far and the new node's string have to survive the
        // node's own allocation — under GC torture that allocation collects.
        let frame = Frame::<2>::new();
        frame.scope(|| {
            for _ in 0..n {
                frame.set(0, &head);
                let name = GoStr::lit(b"x").concat(GoStr::lit(b"y"));
                frame.set(1, &name);
                head = Ptr::alloc((head, name));
            }
        });
        head
    }

    #[test]
    fn collects_garbage_but_keeps_roots() {
        let before = stats();
        // Unrooted: every node of every chain is garbage by the next collect.
        for _ in 0..4 {
            chain(50);
        }
        collect();
        let after_garbage = stats();
        assert_eq!(
            after_garbage.objects, before.objects,
            "garbage chains should be gone"
        );

        // Rooted: the whole chain survives, reachable through the head.
        let frame = Frame::<1>::new();
        frame.scope(|| {
            let head = chain(50);
            frame.set(0, &head);
            collect();
            assert_eq!(stats().objects, before.objects + 100); // node + string
            // Walk it: every link and every string must still be intact.
            let mut n = head;
            let mut count = 0;
            while n != Ptr::zero() {
                assert_eq!(n.load().1.bytes(), b"xy");
                n = n.load().0;
                count += 1;
            }
            assert_eq!(count, 50);
        });
        collect();
        assert_eq!(stats().objects, before.objects, "dropped after the scope");
    }

    #[test]
    fn interior_pointers_keep_their_object_alive() {
        let frame = Frame::<1>::new();
        frame.scope(|| {
            let head = chain(3); // 3 nodes, 3 strings
            // Walk to the tail, whose `next` is nil, and keep a pointer to
            // one of its *fields* — nothing else stays reachable.
            let mut tail = head;
            while tail.load().0 != Ptr::zero() {
                tail = tail.load().0;
            }
            let field: Ptr<Slot<GoStr>> = tail.project(|n| &n.name);
            frame.set(0, &field);
            collect();
            // The field's own object survives, with the string it holds;
            // the two nodes in front of it, and their strings, do not.
            assert_eq!(stats().objects, 2);
            assert_eq!(field.load().bytes(), b"xy");
        });
    }

    #[test]
    fn substrings_keep_the_whole_array_alive() {
        let frame = Frame::<1>::new();
        frame.scope(|| {
            let s = GoStr::lit(b"hello, ").concat(GoStr::lit(b"world"));
            let tail = s.slice(7, None);
            frame.set(0, &tail);
            collect();
            assert_eq!(tail.bytes(), b"world");
            assert_eq!(stats().objects, 1);
        });
    }
}
