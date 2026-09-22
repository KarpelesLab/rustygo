//! Goroutines and the scheduler (DESIGN §4).
//!
//! A goroutine is a stack and a saved [`Context`]. Blocking switches stacks,
//! so generated code stays straight-line: no function is coloured `async`,
//! and any call may block anywhere.
//!
//! This is the single-threaded half of M2: one OS thread runs every
//! goroutine, cooperatively, switching at the points Go blocks at — a
//! channel that is not ready, a semaphore that is held, `runtime.Gosched`.
//! `GOMAXPROCS` is 1, so there is no data race to lose and no work to steal;
//! the M:N scheduler on top of this comes next, and the parking primitives
//! below are the ones it will drive.
//!
//! Each goroutine carries the runtime state that used to belong to the
//! thread: its shadow stack of GC roots, and the panics it is handling. They
//! are swapped in and out around every switch, which is what makes a panic
//! on one goroutine invisible to another, as Go requires.

use crate::context::{self, Context};
use crate::stack::Stack;
use alloc::boxed::Box;
use alloc::collections::VecDeque;
use alloc::vec::Vec;
use core::cell::RefCell;

/// How much stack a goroutine gets. Go starts at 8 KiB and grows; rustygo
/// reserves instead (DESIGN §4), so this is the ceiling, not the cost —
/// untouched pages are never committed.
const STACK_SIZE: usize = 8 * 1024 * 1024;

/// A goroutine's identity: an index into the scheduler's table.
pub type Gid = usize;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum State {
    /// On the run queue, waiting for a turn.
    Runnable,
    /// The goroutine running right now.
    Running,
    /// Blocked until something readies it: a channel, a semaphore.
    Waiting,
    /// Finished. Its stack is freed by whatever runs next.
    Dead,
}

/// One goroutine.
struct G {
    ctx: Context,
    /// The stack it runs on. The goroutine that started the program runs on
    /// the thread's own stack and has none of its own.
    stack: Option<Stack>,
    state: State,
    /// What it runs, until it starts running it.
    entry: Option<(fn(crate::func::Env), crate::func::Env)>,
    /// The runtime state that belongs to this goroutine while it is parked.
    /// Swapped with the thread's on every switch.
    saved: Saved,
}

/// The per-goroutine half of the runtime's state.
struct Saved {
    /// The top of this goroutine's shadow stack of GC roots.
    roots: usize,
    /// The panics it is handling, and which deferred call may recover them.
    panics: Vec<crate::panic::GoPanic>,
    boundary: (usize, usize),
}

impl Saved {
    const fn new() -> Saved {
        Saved {
            roots: 0,
            panics: Vec::new(),
            boundary: (0, 0),
        }
    }
}

struct Sched {
    /// Every goroutine, alive or not yet reaped, by id.
    gs: Vec<Option<Box<G>>>,
    /// Ready to run, in the order they became ready: Go's FIFO fairness.
    run: VecDeque<Gid>,
    current: Gid,
    /// Stacks of finished goroutines, freed once nothing runs on them.
    reap: Vec<Stack>,
}

impl Sched {
    fn new() -> Sched {
        // Goroutine 0 is the one already running: main, on the thread's own
        // stack. It never gets a stack of its own and is never reaped.
        let main = Box::new(G {
            ctx: Context::empty(),
            stack: None,
            state: State::Running,
            entry: None,
            saved: Saved::new(),
        });
        Sched {
            gs: alloc::vec![Some(main)],
            run: VecDeque::new(),
            current: 0,
            reap: Vec::new(),
        }
    }

    fn g(&mut self, id: Gid) -> &mut G {
        self.gs[id].as_mut().expect("goroutine was reaped")
    }
}

rt_global! {
    static SCHED: RefCell<Option<Sched>> = RefCell::new(None);
}

/// Runs `body` with the scheduler borrowed, creating it on first use.
///
/// The borrow never spans a context switch: every caller takes what it needs,
/// drops the borrow, and only then switches. A switch with the borrow held
/// would hand the other goroutine a scheduler it cannot touch.
fn with_sched<R>(body: impl FnOnce(&mut Sched) -> R) -> R {
    SCHED.with(|s| {
        let mut slot = s.borrow_mut();
        body(slot.get_or_insert_with(Sched::new))
    })
}

/// Whether the program has started more than the goroutine it was born with.
///
/// Everything below is written for many goroutines, but a program that never
/// says `go` should not pay for any of it.
pub fn concurrent() -> bool {
    SCHED.with(|s| {
        s.borrow()
            .as_ref()
            .is_some_and(|sched| sched.gs.len() > 1 || !sched.reap.is_empty())
    })
}

/// `go f(args)`: starts a goroutine, which runs when the current one blocks
/// or yields. Go does not run it immediately, and neither does this.
pub fn spawn(f: fn(crate::func::Env), env: crate::func::Env) {
    let stack = match Stack::new(STACK_SIZE) {
        Some(s) => s,
        None => crate::rt::fatal(b"out of memory (stack allocate)"),
    };
    with_sched(|sched| {
        let id = sched.gs.len();
        // SAFETY: the stack is fresh and owned by this goroutine, `id` fits
        // in a pointer, and `run_goroutine` never returns.
        let ctx = unsafe { Context::prepare(stack.top(), run_goroutine, id as *mut u8) };
        sched.gs.push(Some(Box::new(G {
            ctx,
            stack: Some(stack),
            state: State::Runnable,
            entry: Some((f, env)),
            saved: Saved::new(),
        })));
        sched.run.push_back(id);
    });
}

/// The first thing a new goroutine runs, on its own stack.
unsafe extern "C" fn run_goroutine(arg: *mut u8) -> ! {
    let id = arg as usize;
    let (f, env) = with_sched(|sched| sched.g(id).entry.take().expect("goroutine has no entry"));
    // The environment holds the call's arguments, and from here on nothing
    // else refers to it: the `go` statement's frame has moved on and the
    // scheduler has handed it over. It is this goroutine's root for as long
    // as it runs, exactly as a deferred call's is.
    let frame = crate::gc::Frame::<1>::new();
    let result = frame.scope(|| {
        frame.set(0, &env);
        // A panic that escapes a goroutine takes the program with it, as
        // Go's does: there is no other goroutine to report it to.
        std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| f(env)))
    });
    if let Err(payload) = result {
        crate::rt::report_unrecovered(payload);
    }
    exit()
}

/// Ends the running goroutine and switches to the next. Never returns.
fn exit() -> ! {
    let me = with_sched(|sched| {
        let id = sched.current;
        sched.g(id).state = State::Dead;
        id
    });
    // The stack underfoot cannot be freed yet, so it is handed to whoever
    // runs next.
    schedule(Switch::Exit(me));
    unreachable!("a dead goroutine was resumed")
}

/// `runtime.Gosched`: gives up the processor, staying runnable.
pub fn gosched() {
    if !concurrent() {
        return;
    }
    schedule(Switch::Yield);
}

/// Gives the processor to another goroutine if one is ready to run, and says
/// whether it did.
///
/// `time.Sleep` uses this: the time belongs to whoever can use it, and only
/// when nobody can is it worth sleeping the thread the goroutines share.
pub fn yield_now() -> bool {
    if !concurrent() {
        return false;
    }
    if with_sched(|sched| sched.run.is_empty()) {
        return false;
    }
    schedule(Switch::Yield);
    true
}

/// Parks the running goroutine until something calls [`ready`] on it. The
/// caller has already put its id where the waker will find it.
pub fn park() {
    schedule(Switch::Park);
}

/// The id of the running goroutine, for a waiter to record.
pub fn current() -> Gid {
    with_sched(|sched| sched.current)
}

/// Makes a parked goroutine runnable again. Readying one that is already
/// runnable does nothing, which is what a semaphore released twice wants.
pub fn ready(id: Gid) {
    with_sched(|sched| {
        let g = sched.g(id);
        if g.state == State::Waiting {
            g.state = State::Runnable;
            sched.run.push_back(id);
        }
    });
}

enum Switch {
    /// Stay runnable: go to the back of the queue.
    Yield,
    /// Do not come back until readied.
    Park,
    /// Never come back; free this goroutine's stack.
    Exit(Gid),
}

/// Picks the next goroutine and switches to it.
fn schedule(how: Switch) {
    // Everything the switch needs is worked out under the borrow, which is
    // dropped before the stacks change.
    let switching = with_sched(|sched| {
        let me = sched.current;
        let next = match sched.run.pop_front() {
            Some(next) => next,
            None => {
                return match how {
                    // Nothing else to run, so carry on.
                    Switch::Yield => None,
                    Switch::Park => deadlock(sched),
                    Switch::Exit(_) => {
                        // The last goroutine finished, and it is not main:
                        // main must be parked somewhere, waiting for it.
                        deadlock(sched)
                    }
                };
            }
        };
        match how {
            Switch::Yield => {
                sched.g(me).state = State::Runnable;
                sched.run.push_back(me);
            }
            Switch::Park => sched.g(me).state = State::Waiting,
            Switch::Exit(_) => {}
        }
        // Free the stack of any goroutine that finished earlier: nothing has
        // run on it since it switched away.
        sched.reap.clear();
        if let Switch::Exit(id) = how
            && let Some(stack) = sched.gs[id].take().and_then(|g| g.stack)
        {
            sched.reap.push(stack);
        }
        sched.g(next).state = State::Running;
        sched.current = next;
        // Hand the thread's runtime state to the goroutine taking over, and
        // take the parked one's back. `me` may be gone (it just exited), in
        // which case its state goes nowhere.
        let thread = take_thread_state();
        if sched.gs[me].is_some() {
            sched.g(me).saved = thread;
        }
        let incoming = core::mem::replace(&mut sched.g(next).saved, Saved::new());
        put_thread_state(incoming);
        let from: *mut Context = match sched.gs[me].as_mut() {
            Some(g) => &mut g.ctx,
            // A goroutine that has exited still needs somewhere to save the
            // registers the switch writes before it abandons them.
            None => &raw mut DISCARD,
        };
        let to: *const Context = &sched.g(next).ctx;
        Some((from, to))
    });
    if let Some((from, to)) = switching {
        // SAFETY: `to` was prepared by `spawn` over a stack that is alive (it
        // belongs to a goroutine that has not exited), or saved by an earlier
        // switch out of one.
        unsafe { context::switch(from, to) };
    }
}

/// Registers the running stack's context for a goroutine that has exited and
/// will never read it back.
static mut DISCARD: Context = Context::empty();

/// Go's report when nothing can run: every goroutine is blocked forever.
fn deadlock(_sched: &mut Sched) -> ! {
    crate::rt::fatal(b"all goroutines are asleep - deadlock!")
}

fn take_thread_state() -> Saved {
    Saved {
        roots: crate::gc::take_top(),
        panics: crate::panic::take_panics(),
        boundary: crate::panic::take_boundary(),
    }
}

fn put_thread_state(s: Saved) {
    crate::gc::put_top(s.roots);
    crate::panic::put_panics(s.panics);
    crate::panic::put_boundary(s.boundary);
}

/// Traces the roots of every goroutine that is not running.
///
/// The running one's roots are on the thread's shadow stack, which the
/// collector walks directly; a parked goroutine's are on the chain saved
/// when it switched away, and its stack is untouched until it resumes.
pub(crate) fn trace_parked_roots(t: &mut crate::trace::Tracer<'_>) {
    // The collector runs with the scheduler untouched, so this borrow cannot
    // clash with one held across a switch — there are none.
    SCHED.with(|s| {
        let Ok(slot) = s.try_borrow() else { return };
        let Some(sched) = slot.as_ref() else { return };
        for (id, g) in sched.gs.iter().enumerate() {
            let Some(g) = g else { continue };
            // A goroutine that has not started yet holds its arguments in
            // the environment the `go` statement built, and nothing else
            // refers to it: the frame that built it has moved on.
            if let Some((_, env)) = &g.entry {
                t.edge(env.addr() as usize);
            }
            if id == sched.current {
                continue;
            }
            // SAFETY: the chain belongs to a stack that is parked, so every
            // frame on it is alive and unchanged.
            unsafe { crate::gc::trace_chain(g.saved.roots, t) };
            for p in &g.saved.panics {
                crate::trace::Trace::trace(&p.value(), t);
            }
        }
    });
}

// Goroutines parked on an address: Go's semaphores, and the channel queues
// to come. A linear scan, because a program has few of these at a time and
// the M:N scheduler will replace the table wholesale.
rt_global! {
    static WAITERS: RefCell<Vec<(usize, Gid)>> = RefCell::new(Vec::new());
}

/// Parks the running goroutine on `addr` until [`wake_one`] names it.
///
/// Nothing can run between the caller's decision to park and the park
/// itself: a goroutine yields only where it says so, which is what makes
/// this safe without a lock.
pub fn park_on(addr: usize) {
    let me = current();
    WAITERS.with(|w| w.borrow_mut().push((addr, me)));
    park();
}

/// Readies every goroutine parked on `addr`.
///
/// More than Go's queues would wake, and each re-tests what it was waiting
/// for. It cannot lose a wakeup, which a queue of one can when the goroutine
/// it picks turns out not to be able to proceed after all — a `select` that
/// another goroutine got to first.
pub fn wake_all(addr: usize) {
    let woken = WAITERS.with(|w| {
        let mut w = w.borrow_mut();
        let mut woken = Vec::new();
        w.retain(|&(a, g)| {
            if a == addr {
                woken.push(g);
                false
            } else {
                true
            }
        });
        woken
    });
    for g in woken {
        ready(g);
    }
}

/// Readies the goroutine that has waited longest on `addr`, if any.
pub fn wake_one(addr: usize) {
    let waiter = WAITERS.with(|w| {
        let mut w = w.borrow_mut();
        w.iter()
            .position(|&(a, _)| a == addr)
            .map(|i| w.remove(i).1)
    });
    if let Some(g) = waiter {
        ready(g);
    }
}

/// `runtime.NumGoroutine`: how many goroutines exist, running or parked.
pub fn count() -> i64 {
    with_sched(|sched| sched.gs.iter().filter(|g| g.is_some()).count() as i64)
}

/// Parks the running goroutine until any of `addrs` is woken, then takes it
/// off all of them. An empty list parks for ever, which is `select {}`.
pub fn park_on_any(addrs: &[usize]) {
    let me = current();
    WAITERS.with(|w| {
        let mut w = w.borrow_mut();
        if addrs.is_empty() {
            w.push((0, me));
        }
        for &a in addrs {
            w.push((a, me));
        }
    });
    park();
    // Woken through one address; the registrations on the others would
    // otherwise wake this goroutine again long after it stopped waiting.
    WAITERS.with(|w| w.borrow_mut().retain(|&(_, g)| g != me));
}

// Which ready case a `select` takes. Go picks uniformly at random, so that a
// case cannot be starved by an earlier one that is always ready.
rt_global! {
    static SEED: core::cell::Cell<u64> = core::cell::Cell::new(0x2545F4914F6CDD1D);
}

/// A number below `n`, for `select` to start its poll at.
pub fn pick(n: usize) -> usize {
    if n <= 1 {
        return 0;
    }
    // xorshift64*, which is small, fast and more than random enough to keep
    // one case from starving another.
    let mut x = SEED.with(|s| s.get());
    x ^= x >> 12;
    x ^= x << 25;
    x ^= x >> 27;
    SEED.with(|s| s.set(x));
    (x.wrapping_mul(0x2545F4914F6CDD1D) >> 33) as usize % n
}
