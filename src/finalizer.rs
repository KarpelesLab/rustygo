//! Finalizers: `runtime.SetFinalizer`.
//!
//! A finalizer is a function the program asks to have called once the
//! collector finds that nothing reaches an object any more. It is not a
//! destructor: Go promises only that the finalizer runs at most once, in a
//! goroutine of its own, some time after the object becomes unreachable, and
//! that the object is alive again while it runs.
//!
//! The table below holds each registration as three words: the object's
//! address, the finalizer closure's environment, and a function the emitter
//! generated to make the call. The address is deliberately *not* a traced
//! reference — an object nothing else points at is exactly what this is
//! waiting for — while the environment is, because a closure the program
//! handed over has to survive until it is called.
//!
//! A collection therefore has one more phase. After marking, every
//! registration whose object was not marked moves to the ready queue, and the
//! object is marked and traced from after all: it is about to be handed to its
//! own finalizer, and whatever it points at with it. That is Go's
//! resurrection, and it is why an object with a finalizer takes two
//! collections to free. The set is taken from the mark state as it stood, so
//! two objects that only reach each other are both finalized rather than
//! keeping each other waiting.
//!
//! Nothing runs during the collection. A goroutine is started to drain the
//! queue, which runs at the next switch, exactly as `go` would.

use crate::func::Env;
use crate::trace::Tracer;
use alloc::collections::BTreeMap;
use alloc::vec::Vec;

/// A finalizer waiting for its object, or waiting to run.
#[derive(Clone, Copy)]
struct Pending {
    /// The finalizer func value's environment.
    env: Env,
    /// Calls the finalizer with the object at an address. The emitter
    /// generates it, because it is the only place that knows the finalizer's
    /// signature and the object's type.
    run: fn(Env, usize),
}

struct Table {
    /// Registered, by object address, waiting for the object to die.
    set: BTreeMap<usize, Pending>,
    /// Objects that have died and whose finalizers have not run yet.
    ready: Vec<(usize, Pending)>,
    /// The one being called right now, which nothing else roots.
    running: Option<(usize, Pending)>,
    /// Whether a goroutine is draining the queue.
    draining: bool,
}

// One table for the program (`src/tls.rs`): `SetFinalizer` may be called from
// any goroutine, the collector reads the whole of it, and the goroutine that
// drains the queue is wherever the scheduler puts it.
rt_shared! {
    static TABLE: Table = Table {
        set: BTreeMap::new(),
        ready: Vec::new(),
        running: None,
        draining: false,
    };
}

// SAFETY: a registration is an address and a Go closure's environment. Neither
// belongs to the thread that registered it, for the reasons `sched::G` gives.
unsafe impl Send for Table {}

/// Runs `f` with the table locked. Taken while the heap's lock is held, during
/// a collection, and never the other way round.
fn with_table<R>(f: impl FnOnce(&mut Table) -> R) -> R {
    TABLE.with(f)
}

/// `runtime.SetFinalizer(obj, f)`, with `obj`'s address and the call the
/// emitter built for it. Registering twice for one object replaces the first,
/// which is what gc does.
pub fn set(addr: usize, env: Env, run: fn(Env, usize)) {
    if addr == 0 {
        return;
    }
    with_table(|t| t.set.insert(addr, Pending { env, run }));
}

/// `runtime.SetFinalizer(obj, nil)`: the object is collected without a call.
pub fn clear(addr: usize) {
    with_table(|t| t.set.remove(&addr));
}

/// What the collector must keep alive for this module.
///
/// A registered finalizer contributes its closure and *what its object points
/// at* — not the object, which is the one thing this is waiting to see die.
/// A queued or running one contributes the object too: it is about to be
/// handed to the call.
pub(crate) fn trace_roots(t: &mut Tracer<'_>) {
    let (registered, queued) = with_table(|table| {
        let registered: Vec<(usize, usize)> = table
            .set
            .iter()
            .map(|(addr, p)| (*addr, p.env.addr() as usize))
            .collect();
        let queued: Vec<(usize, usize)> = table
            .ready
            .iter()
            .chain(table.running.iter())
            .map(|(addr, p)| (*addr, p.env.addr() as usize))
            .collect();
        (registered, queued)
    });
    for (addr, env) in registered {
        t.edge(env);
        t.scan_contents(addr);
    }
    for (addr, env) in queued {
        t.edge(addr);
        t.edge(env);
    }
}

/// After the mark phase: queues the finalizer of every object the mark phase
/// did not reach, and brings those objects back for one more collection.
pub(crate) fn resurrect(t: &mut Tracer<'_>) {
    let dead: Vec<usize> = with_table(|table| {
        table
            .set
            .keys()
            .copied()
            .filter(|&addr| t.unmarked_object(addr))
            .collect()
    });
    if dead.is_empty() {
        return;
    }
    with_table(|table| {
        for addr in &dead {
            if let Some(p) = table.set.remove(addr) {
                table.ready.push((*addr, p));
            }
        }
    });
    for addr in dead {
        t.edge(addr);
    }
    t.drain();
}

/// Starts the goroutine that runs whatever became ready, if anything did.
///
/// Called at the end of a collection, which is a safe place for it: starting
/// a goroutine only pushes one onto the run queue, and none of the program
/// runs until the next switch.
pub(crate) fn kick() {
    let start = with_table(|t| {
        if t.ready.is_empty() || t.draining {
            return false;
        }
        t.draining = true;
        true
    });
    if start {
        crate::sched::spawn(|_| drain(), Env::NONE);
    }
}

/// The finalizer goroutine: runs what is ready, one at a time, and ends.
/// Anything queued after it ends starts another.
fn drain() {
    loop {
        let next = with_table(|t| {
            t.running = t.ready.pop();
            t.running
        });
        let Some((addr, p)) = next else { break };
        (p.run)(p.env, addr);
    }
    with_table(|t| {
        t.running = None;
        t.draining = false;
    });
}

/// How many finalizers are registered and how many are waiting to run, for
/// tests.
pub fn counts() -> (usize, usize) {
    with_table(|t| (t.set.len(), t.ready.len()))
}
