//! Goroutines and the scheduler (DESIGN §4).
//!
//! A goroutine is a stack and a saved [`Context`]. Blocking switches stacks,
//! so generated code stays straight-line: no function is coloured `async`,
//! and any call may block anywhere.
//!
//! Goroutines run on worker threads, as many of them at once as `GOMAXPROCS`
//! allows. A worker stands on its thread's own stack between goroutines, and a
//! goroutine that blocks switches back to that stack rather than straight into
//! the next goroutine. The reason is the thing a scheduler over several
//! threads has to get right before anything else: a goroutine may be offered
//! to another thread only once nothing is running on its stack, and the only
//! place from which that is true is the stack it has just left. So the worker,
//! not the goroutine, is what puts a yielding goroutine back on the run queue,
//! marks a blocking one as waiting, and frees a finished one's stack.
//!
//! Each goroutine carries the runtime state that would otherwise belong to the
//! thread: its shadow stack of GC roots, and the panics it is handling. The
//! worker swaps that state in and out around every switch, which is what makes
//! a panic on one goroutine invisible to another and lets the collector trace
//! the roots of a goroutine that is parked.
//!
//! **A collection stops the world.** The collector has to see every
//! goroutine's roots, and it may not look at a stack that is changing, so
//! before it marks it asks every thread that is running Go code to stop
//! ([`stop_the_world`]). A thread notices at its next safe point — every loop
//! back-edge, and the top of every allocation — puts its goroutine's roots
//! where the collector looks for a parked one's, and waits. A thread that is
//! *not* running Go code, because it is between goroutines or asleep in the
//! netpoller, has nothing to stop and nothing to show.
//!
//! **Parking can no longer be done by inspection.** On one thread, a goroutine
//! that found a channel empty could park knowing nothing had run in between.
//! Now something can, so a goroutine registers itself as a waiter *while it
//! still holds the lock that guards what it is waiting for* ([`prepare_park`])
//! and only then parks ([`park`]). A wake that arrives in the window between
//! the two is not lost: it is recorded on the goroutine, and the park consumes
//! it and comes straight back. Every caller therefore re-tests what it was
//! waiting for, because a park may return with nothing having happened.

use crate::context::{self, Context};
use crate::func::Env;
use crate::stack::Stack;
use alloc::boxed::Box;
use alloc::collections::{BTreeMap, VecDeque};
use alloc::vec::Vec;
use core::cell::{Cell, UnsafeCell};
use core::sync::atomic::{AtomicU32, AtomicU64, AtomicUsize, Ordering};

/// How much stack a goroutine gets. Go starts at 8 KiB and grows; rustygo
/// reserves instead (DESIGN §4), so this is the ceiling, not the cost —
/// untouched pages are never committed.
const STACK_SIZE: usize = 8 * 1024 * 1024;

/// A goroutine's identity: an index into the scheduler's table.
pub type Gid = usize;

/// Neither a goroutine nor a worker. Both are indices, so the one value that
/// cannot be either stands for the absence of one.
const NONE: usize = usize::MAX;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum State {
    /// On a run queue, waiting for a worker to pick it up.
    Runnable,
    /// Running on some worker right now.
    Running,
    /// Blocked until something readies it: a channel, a semaphore.
    Waiting,
}

/// What a goroutine runs.
enum Entry {
    /// `go f(env)`.
    Go(fn(Env), Env),
    /// The program itself: its package initializers, then `main.main`.
    Main(fn(), fn()),
}

/// One goroutine.
struct G {
    ctx: Context,
    /// The stack it runs on. Every goroutine has one of its own, the program's
    /// first included: a worker needs its thread's native stack to stand on
    /// between goroutines, so it cannot be lending it to one.
    ///
    /// Held rather than read. `spawn_entry` prepares the context over it and
    /// nothing looks at it again; what the field is for is the moment this
    /// record is dropped, which unmaps the reservation, and which only happens
    /// once nothing is running on it.
    #[allow(dead_code)]
    stack: Stack,
    state: State,
    /// What it runs, until it starts running it.
    entry: Option<Entry>,
    /// The runtime state that belongs to this goroutine while it is not
    /// running. Swapped with the worker's on every switch.
    saved: Saved,
    /// A wake that arrived before the park it answers, which that park then
    /// consumes instead of blocking. Without it, a goroutine readied between
    /// deciding to park and getting there would never be readied again.
    notified: bool,
    /// The worker this goroutine may run on, and no other, while it is pinned.
    locked_to: Option<usize>,
    /// How many times it has asked to stay where it is: `LockOSThread` and
    /// `sync`'s processor pinning both count here, and both nest, so the
    /// goroutine is free to move again only when the count reaches zero.
    pins: u32,
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

/// What a worker owes the goroutine it has just switched away from.
///
/// None of it may be done by the goroutine itself. Until the switch has
/// finished saving its registers its stack is still in use, and a run-queue
/// entry is an invitation to another thread to start running on that stack.
///
/// Which goroutine it is about is deliberately *not* here. The worker already
/// knows — it is what `pick` put in `M::current` — and a second copy carried up
/// from the caller is a copy that can disagree with it.
#[derive(Clone, Copy)]
enum Leaving {
    /// The worker was not running a goroutine.
    Nothing,
    /// Still runnable: back on the queue, at the end of it.
    Yield,
    /// Waiting, unless a wake arrived while it was on its way out.
    Park,
    /// Over: free its stack and forget it.
    Exit,
}

/// One worker thread, as the scheduler sees it.
struct M {
    /// The goroutine it is running, or none while it is between goroutines.
    current: Option<Gid>,
    /// The processor slot that goroutine holds, which is what `GOMAXPROCS`
    /// limits and what `sync` shards its caches by.
    slot: Option<usize>,
    /// What it owes the goroutine it has just left.
    leaving: Leaving,
    /// Goroutines that may run on this worker only, because they are pinned to
    /// it. A queue of its own, because the shared one offers what is on it to
    /// whichever worker asks first.
    local: VecDeque<Gid>,
}

impl M {
    const fn new() -> M {
        M {
            current: None,
            slot: None,
            leaving: Leaving::Nothing,
            local: VecDeque::new(),
        }
    }
}

struct Sched {
    /// Every goroutine that exists, by id.
    gs: Vec<Option<Box<G>>>,
    /// Ready to run, in the order they became ready: Go's FIFO fairness.
    ///
    /// One queue that every worker takes from, which is the simplest thing
    /// that is correct. The roadmap's work stealing replaces it with a queue
    /// per worker and a shared overflow; the number to measure before writing
    /// that is how much of a program's time goes on waiting for this lock.
    run: VecDeque<Gid>,
    /// The workers, by number. It only grows, so a worker's number is its own
    /// for the life of its thread.
    ms: Vec<M>,
    /// `GOMAXPROCS`: how many goroutines may be running at once.
    procs: usize,
    /// How many goroutines exist. Counted rather than searched for, because
    /// `runtime.NumGoroutine` and the question `Gosched` asks — is there anyone
    /// else? — are both on paths a program may take in a loop, and the table
    /// below never shrinks.
    live: usize,
    /// Which processor slots below `procs` are taken.
    ///
    /// A running goroutine holds one, so the count of them *is* `GOMAXPROCS`.
    /// They are numbered rather than counted because `sync.Pool` and
    /// `sync/atomic` shard their caches by the number of the processor a pinned
    /// goroutine is on, and index arrays by it: it has to be below `GOMAXPROCS`,
    /// and no two goroutines running at once may be given the same one.
    slots: Vec<bool>,
}

impl Sched {
    const fn new() -> Sched {
        Sched {
            gs: Vec::new(),
            run: VecDeque::new(),
            ms: Vec::new(),
            procs: 1,
            live: 0,
            slots: Vec::new(),
        }
    }

    fn g(&mut self, id: Gid) -> &mut G {
        self.gs[id].as_mut().expect("goroutine has ended")
    }

    /// The goroutine this worker should run next, if it may run one at all.
    ///
    /// A goroutine pinned to this worker comes first: nobody else will take it,
    /// and it has been waiting on a queue of one.
    fn pick(&mut self, me: usize) -> Option<Gid> {
        // A free processor slot is what says `GOMAXPROCS` has room. A worker
        // that finds none idles rather than exits, so that a program which
        // lowers the limit and raises it again — the shape Go's own tests use —
        // pays nothing for it.
        self.slots.resize(self.procs.max(self.slots.len()), false);
        let slot = (0..self.procs).find(|&i| !self.slots[i])?;
        let next = self.ms[me]
            .local
            .pop_front()
            .or_else(|| self.run.pop_front())?;
        // A goroutine is about to be resumed by restoring a stack pointer and
        // returning through it, so a context that does not point into its own
        // stack is a jump to nowhere. Two comparisons on words that are being
        // read anyway buy the difference between a scheduler bug that says so
        // and one that corrupts whatever it lands in.
        {
            let g = self.g(next);
            let sp = g.ctx.sp();
            let (lo, hi) = (g.stack.limit() as usize, g.stack.top() as usize);
            if sp < lo || sp > hi {
                crate::rt::trace(&alloc::format!(
                    "goroutine {next} was saved at {sp:#x}, outside its own stack [{lo:#x},{hi:#x}]"
                ));
                crate::rt::fatal(b"a goroutine's saved context is not on its own stack");
            }
        }
        self.slots[slot] = true;
        self.g(next).state = State::Running;
        self.ms[me].current = Some(next);
        self.ms[me].slot = Some(slot);
        Some(next)
    }

    /// Settles the goroutine this worker has just left.
    fn finish_leaving(&mut self, me: usize) {
        let leaving = core::mem::replace(&mut self.ms[me].leaving, Leaving::Nothing);
        if let Leaving::Nothing = leaving {
            return;
        }
        let id = self.ms[me]
            .current
            .take()
            .expect("a worker owes the goroutine it ran");
        if let Some(slot) = self.ms[me].slot.take() {
            self.slots[slot] = false;
        }
        match leaving {
            Leaving::Nothing => {}
            Leaving::Yield => {
                self.g(id).state = State::Runnable;
                self.enqueue_runnable(id);
            }
            Leaving::Park => {
                // The wake may have arrived while the switch was still in
                // progress, in which case the goroutine is runnable and never
                // waits at all.
                if core::mem::take(&mut self.g(id).notified) {
                    self.g(id).state = State::Runnable;
                    self.enqueue_runnable(id);
                } else {
                    self.g(id).state = State::Waiting;
                }
            }
            Leaving::Exit => {
                // Nothing runs on that stack any more: the switch that brought
                // this worker here was the last thing the goroutine did. The
                // slot stays, empty: a goroutine's id may be in a waiter queue
                // it never got to leave, so handing the number out again would
                // mean waking the wrong goroutine.
                self.gs[id] = None;
                self.live -= 1;
            }
        }
    }

    /// Puts a runnable goroutine where a worker will find it.
    fn enqueue_runnable(&mut self, id: Gid) {
        match self.g(id).locked_to {
            Some(m) => self.ms[m].local.push_back(id),
            None => self.run.push_back(id),
        }
    }

    /// Whether any goroutine is running or waiting for a turn, which is what
    /// says a program with nothing runnable right now is not stuck.
    fn has_work(&self) -> bool {
        !self.run.is_empty()
            || self
                .ms
                .iter()
                .any(|m| m.current.is_some() || !m.local.is_empty())
    }
}

// The goroutine table and its run queue: one for the program, not one per
// thread (`src/tls.rs`). That is the point of the thing — a goroutine any
// worker may pick up has to be recorded somewhere every worker can see — and
// it is also why the lock below exists at all.
rt_shared! {
    static SCHED: Sched = Sched::new();
}

// SAFETY: a goroutine is a stack, a saved stack pointer, and Go values. None of
// it belongs to the thread that created it: handing a goroutine from one thread
// to another is exactly what the scheduler is for. The Go values are the
// program's own, shared between goroutines by Go's rules rather than by Rust's,
// which is the position DESIGN §2 takes and §13 question 6 leaves open — a racy
// load would be a race whichever thread it came from.
unsafe impl Send for G {}

/// Runs `body` with the scheduler locked.
///
/// The lock never spans a context switch: every caller takes what it needs,
/// releases it, and only then switches. Holding it across a switch would hand
/// the next goroutine a scheduler it cannot take.
fn with_sched<R>(body: impl FnOnce(&mut Sched) -> R) -> R {
    SCHED.with(body)
}

// Which worker this thread is, and which goroutine it is running. Per thread,
// and the goroutine's id is kept here rather than looked up in the table
// because everything asks for it: `current` is on the path of every channel
// operation and every park, and it should not cost a lock.
rt_global! {
    static ME: Cell<usize> = Cell::new(NONE);
}
rt_global! {
    static CUR_G: Cell<Gid> = Cell::new(NONE);
}

// Where a goroutine switches to in order to leave: its worker, looking for the
// next one. A thread-local's address belongs to the thread for as long as the
// thread lives, which is what lets a goroutine switch away without looking
// anything up.
rt_global! {
    static WORKER_CTX: UnsafeCell<Context> = UnsafeCell::new(Context::empty());
}

/// The id of the running goroutine, for a waiter to record.
pub fn current() -> Gid {
    CUR_G.with(|g| g.get())
}

/// Whether more than the goroutine the program was born with exists.
///
/// Everything below is written for many goroutines, but a program that never
/// says `go` should not pay for any of it.
pub fn concurrent() -> bool {
    with_sched(|s| s.live > 1)
}

/// `runtime.NumGoroutine`: how many goroutines exist, running or parked.
pub fn count() -> i64 {
    with_sched(|s| s.live as i64)
}

/// Runs the Go program, and never comes back.
///
/// The program's own goroutine gets a stack like any other and runs the package
/// initializers and then `main.main`; this thread becomes worker 0 and spends
/// the rest of the process in the loop below. The program ends from inside that
/// goroutine, which is where Go ends it too.
pub fn run_program(init: fn(), main: fn()) -> ! {
    let procs = default_procs();
    with_sched(|s| {
        s.procs = procs;
        s.ms.push(M::new());
    });
    spawn_entry(Entry::Main(init, main));
    worker(0)
}

/// `GOMAXPROCS` at startup, which `GOMAXPROCS` in the environment sets.
///
/// **One, and not the number of CPUs that Go's default is.** Everything above is
/// written for more and setting the variable gets it, but more is not yet
/// correct: a goroutine's saved context acquires a stack pointer belonging to a
/// worker's own stack, which `pick` catches, and the program it belongs to ends
/// up returning from `main` without having run it. Two workers get that wrong
/// about one run in seven, so two is not a safer default than sixty-four; it is
/// the same bug at a lower rate, which is worse, because a lower rate is how a
/// bug reaches a user.
///
/// What is known about it, for whoever picks it up: one worker is correct;
/// `testdata/programs/parallel` and sixty-four goroutines on one mutex reproduce
/// it in seconds at two; the thread-local that names the running goroutine, the
/// scheduler's own record of it, and the stack the thread is standing on all
/// agree at the moment of failure, so it is not a stale thread-local and not one
/// `Gid` handed to two threads; and the corruption is a worker's own stack
/// pointer appearing in a goroutine's context, which only `leave` writes and
/// only ever on the goroutine's own stack.
///
/// Read once, before any worker exists. `runtime.GOMAXPROCS` changes it
/// afterwards, which is what Go's own tests do when they want to be
/// deterministic.
fn default_procs() -> usize {
    if let Some(v) = std::env::var_os("GOMAXPROCS")
        && let Some(n) = v.to_str().and_then(|s| s.trim().parse::<usize>().ok())
        && n > 0
    {
        return n;
    }
    1
}

/// `runtime.NumCPU`: how many processors this program may use.
pub fn num_cpu() -> i64 {
    std::thread::available_parallelism().map_or(1, |n| n.get()) as i64
}

/// `runtime.GOMAXPROCS(n)`: the limit on how many goroutines run at once, and
/// the limit that was in force before.
///
/// Lowering it does not stop a worker thread. The extra workers idle until the
/// limit rises again, which is both cheaper than taking a thread apart and the
/// only thing that can be done to a worker that is asleep in the kernel.
pub fn gomaxprocs(n: i64) -> i64 {
    let (old, want) = with_sched(|s| {
        let old = s.procs;
        if n > 0 {
            s.procs = n as usize;
        }
        (old, s.procs)
    });
    if n > 0 && want > old {
        // Workers appear as goroutines ask for them; raising the limit only
        // means the ones that are idling may run again.
        wake_idle();
    }
    old as i64
}

/// Starts one more worker, if `GOMAXPROCS` has room for it and none of the ones
/// there are is waiting for work.
///
/// Called when a goroutine is started, which is the only moment at which a
/// program can be said to want another thread. Starting `GOMAXPROCS` of them up
/// front would cost a program that never says `go` one thread per processor and
/// buy it nothing; Go does not do that either.
fn grow_workers() {
    if IDLE_COUNT.load(Ordering::SeqCst) > 0 {
        // Somebody is already waiting to be given something to run.
        return;
    }
    let next = with_sched(|s| {
        if s.ms.len() >= s.procs {
            return None;
        }
        s.ms.push(M::new());
        Some(s.ms.len() - 1)
    });
    // Worker 0 is the thread that started the program, already in the loop.
    if let Some(i) = next.filter(|&i| i > 0) {
        // A thread the operating system refuses is a slower program, not a wrong
        // one: what would have run there runs elsewhere. The record stays,
        // unused and empty, because a goroutine reaches a worker's own queue
        // only by pinning itself to the thread it is already running on.
        let _ = std::thread::Builder::new()
            .name(alloc::format!("rustygo-p{i}"))
            .spawn(move || {
                worker(i);
            });
    }
}

/// A worker: runs goroutines, and waits when there is none to run.
fn worker(me: usize) -> ! {
    ME.with(|m| m.set(me));
    loop {
        let picked = with_sched(|s| {
            s.finish_leaving(me);
            match s.pick(me) {
                Some(next) => Picked::Run(next),
                // Read with the scheduler still locked, which is the whole
                // point of it: anything that becomes runnable after this moment
                // raises the count past what `wait_for_work` was told, and so
                // cannot be waited through. Reading it after the lock is
                // released leaves a window in which another thread readies a
                // goroutine, raises the count, and the worker then compares
                // against the *raised* value and waits anyway.
                None => Picked::Idle(READY_GEN.load(Ordering::SeqCst)),
            }
        });
        match picked {
            Picked::Run(next) => {
                CUR_G.with(|g| g.set(next));
                // From here until the switch comes back, this thread is running
                // Go code and a collection has to wait for it. `attach` is also
                // where it waits out one that has already begun, which is why it
                // comes before the goroutine's roots go back on the thread:
                // while they are on its own record the collector can read them,
                // and once they are on the thread only this thread can.
                attach();
                // The state the goroutine parked with goes back on the thread
                // just before it resumes, and not a moment sooner: while it is
                // on the goroutine's own record the collector can read it, and
                // while it is on the thread only this thread can. The worker's
                // own goes nowhere, because a worker holds no Go roots and
                // handles no panics.
                let ctx = with_sched(|s| {
                    let incoming = core::mem::replace(&mut s.g(next).saved, Saved::new());
                    put_thread_state(incoming);
                    &raw const s.g(next).ctx
                });
                let from = WORKER_CTX.with(|c| c.get());
                // SAFETY: `ctx` points into the goroutine's own box, which is
                // not freed while it is Running, and holds either a context
                // `spawn_entry` prepared over a live stack or one an earlier
                // switch out of that stack saved. `from` is this thread's own
                // slot, whose address is good for the life of the thread.
                unsafe { context::switch(from, ctx) };
                detach();
                CUR_G.with(|g| g.set(NONE));
            }
            Picked::Idle(seen) => wait_for_work(me, seen),
        }
    }
}

/// What a worker found when it looked for something to run: a goroutine, or
/// nothing and the count of wakes it had seen by then.
enum Picked {
    Run(Gid),
    Idle(u64),
}

/// Leaves the running goroutine, telling its worker what it owes it.
///
/// Returns when some worker switches back into this goroutine, which for a
/// [`Leaving::Exit`] is never.
fn leave(how: Leaving) {
    let from = with_sched(|s| {
        let me = ME.with(|m| m.get());
        // Which goroutine is leaving is the scheduler's to say. Asking the
        // thread-local instead would be asking a second copy of the same fact,
        // and a copy carried up from the caller through `Leaving` was exactly
        // the bug: a goroutine that had been handed from one worker to another
        // saved its registers into whichever goroutine the *caller's* copy
        // named, which put two identities on one stack.
        let id = s.ms[me]
            .current
            .expect("a worker leaves the goroutine it is running");
        s.ms[me].leaving = how;
        // The thread's state belongs to the goroutine that is leaving, so it
        // travels with it. One that is over has nowhere to put it and nothing
        // left that needs it.
        let state = take_thread_state();
        if let Leaving::Exit = how {
            drop(state);
        } else {
            s.g(id).saved = state;
        }
        &raw mut s.g(id).ctx
    });
    let to = WORKER_CTX.with(|c| c.get());
    // SAFETY: `from` points into the running goroutine's own box, which no
    // other thread may free while this thread is still on its stack, and `to`
    // holds the worker loop's context, saved when it switched in here.
    unsafe { context::switch(from, to) };
}

/// `go f(args)`: starts a goroutine, which runs when a worker takes it up. Go
/// does not run it immediately, and neither does this.
pub fn spawn(f: fn(Env), env: Env) {
    spawn_entry(Entry::Go(f, env));
    // A `go` statement is the only moment at which a program can be said to
    // want another thread, so it is where one is started.
    grow_workers();
}

fn spawn_entry(entry: Entry) {
    let stack = match Stack::new(STACK_SIZE) {
        Some(s) => s,
        None => crate::rt::fatal(b"out of memory (stack allocate)"),
    };
    with_sched(|s| {
        let id = s.gs.len();
        // SAFETY: the stack is fresh and owned by this goroutine, `id` fits in
        // a pointer, and `run_goroutine` never returns.
        let ctx = unsafe { Context::prepare(stack.top(), run_goroutine, id as *mut u8) };
        s.gs.push(Some(Box::new(G {
            ctx,
            stack,
            state: State::Runnable,
            entry: Some(entry),
            saved: Saved::new(),
            notified: false,
            locked_to: None,
            pins: 0,
        })));
        s.live += 1;
        s.run.push_back(id);
    });
    wake_idle();
}

/// The first thing a new goroutine runs, on its own stack.
unsafe extern "C" fn run_goroutine(arg: *mut u8) -> ! {
    let id = arg as usize;
    let entry = with_sched(|s| s.g(id).entry.take().expect("goroutine has no entry"));
    match entry {
        Entry::Main(init, main) => run_main_goroutine(init, main),
        Entry::Go(f, env) => {
            // The environment holds the call's arguments, and from here on
            // nothing else refers to it: the `go` statement's frame has moved
            // on and the scheduler has handed it over. It is this goroutine's
            // root for as long as it runs, exactly as a deferred call's is.
            let frame = crate::gc::Frame::<1>::new();
            let result = frame.scope(|| {
                frame.set(0, &env);
                // A panic that escapes a goroutine takes the program with it,
                // as Go's does: there is no other goroutine to report it to.
                std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| f(env)))
            });
            if let Err(payload) = result
                && !is_goexit(&*payload)
            {
                crate::rt::report_unrecovered(payload);
            }
        }
    }
    exit()
}

/// The program's own goroutine: the package initializers, then `main.main`,
/// then the end of the process.
fn run_main_goroutine(init: fn(), main: fn()) -> ! {
    let result = std::panic::catch_unwind(|| {
        init();
        main();
    });
    match result {
        Ok(()) => crate::rt::exit(0),
        // `runtime.Goexit` in the main goroutine ends it without ending the
        // program: what is left runs, and the scheduler reports a deadlock once
        // nothing can.
        Err(payload) if is_goexit(&*payload) => park_forever(),
        Err(payload) => crate::rt::report_unrecovered(payload),
    }
}

/// Ends the running goroutine and gives its worker back. Never returns.
fn exit() -> ! {
    // A goroutine that pinned itself to a thread releases it by ending, as
    // Go's does, and the worker does that by forgetting the goroutine whole.
    leave(Leaving::Exit);
    unreachable!("a goroutine that ended was resumed")
}

/// What `runtime.Goexit` unwinds with.
///
/// Not a Go panic: `recover` must not see it, and nothing can stop it. The
/// deferred calls of every frame on the way out still run, which is the whole
/// of what Goexit promises, and the goroutine's entry frame treats it as an
/// ordinary end rather than a panic nobody caught.
pub struct Goexit;

/// `runtime.Goexit`: ends the running goroutine after its deferred calls.
///
/// The main goroutine is a special case Go spells out: the program carries on
/// with the other goroutines, and crashes once none of them can run.
pub fn goexit() -> ! {
    // `resume_unwind` rather than `panic!`: it does not go through Rust's
    // panic hook, which would print a line about a payload it cannot name.
    // Go's own panics take the same route (`panic::go_panic`).
    std::panic::resume_unwind(alloc::boxed::Box::new(Goexit))
}

/// Whether a caught payload is a [`Goexit`].
pub fn is_goexit(p: &(dyn core::any::Any + Send)) -> bool {
    p.is::<Goexit>()
}

/// Parks the goroutine for ever, which is what the main goroutine does after
/// `runtime.Goexit`: it is over, but the program is not.
pub fn park_forever() -> ! {
    loop {
        park_on(0);
    }
}

/// `runtime.Gosched`: gives up the processor, staying runnable.
pub fn gosched() {
    if !concurrent() {
        return;
    }
    // A yield that finds nobody else runnable has to ask the clock itself.
    // Waiting for work is where the sleepers and the descriptors are looked at,
    // and a yielding goroutine never gets there: it is runnable again the
    // moment it leaves, so the worker picks it straight back up. A loop around
    // `runtime.Gosched` waiting for a `select` case to become ready is a real
    // Go idiom — `crypto/tls`'s own `TestWeakCertCache` spins until a
    // `time.After` fires — and it would spin for ever, because the goroutine
    // that timer will ready is asleep and not on any run queue.
    //
    // The test is the shared queue only, so with several workers it may ask the
    // clock when another worker did have something to run. That costs a glance
    // at the sleepers on an idle yield and never gets an answer wrong.
    if with_sched(|s| s.run.is_empty()) {
        wake_expired();
        crate::netpoll::expire();
    }
    leave(Leaving::Yield);
}

/// Gives the processor to another goroutine if one is ready to run, and says
/// whether it did.
///
/// `time.Sleep` uses this: the time belongs to whoever can use it, and only
/// when nobody can is it worth waiting on the clock.
pub fn yield_now() -> bool {
    if with_sched(|s| s.run.is_empty()) {
        return false;
    }
    leave(Leaving::Yield);
    true
}

/// Parks the running goroutine until something calls [`ready`] on it. The
/// caller has already registered itself where the waker will look.
///
/// A wake that arrived since the caller registered is consumed here and the
/// call comes straight back, so a park never loses one. It may therefore also
/// return with nothing having happened, and every caller re-tests what it was
/// waiting for.
pub fn park() {
    leave(Leaving::Park);
}

/// Makes a parked goroutine runnable again.
///
/// A goroutine that has not parked yet is marked instead, so that the park it
/// is on its way into comes straight back. One that is already runnable is left
/// alone, which is what a semaphore released twice wants.
pub fn ready(id: Gid) {
    ready_all(&[id]);
}

/// Readies several goroutines, which is what one wake of an address does.
///
/// One turn at the scheduler's lock and one notification, however many waiters
/// there are: Go's own `chanlinear` test puts thousands of goroutines on one
/// channel and requires that waking them stay linear in their number.
fn ready_all(ids: &[Gid]) {
    if ids.is_empty() {
        return;
    }
    let woke = with_sched(|s| {
        let mut woke = false;
        for &id in ids {
            if s.gs.get(id).is_none_or(|g| g.is_none()) {
                continue;
            }
            let g = s.g(id);
            match g.state {
                State::Waiting => {
                    g.state = State::Runnable;
                    s.enqueue_runnable(id);
                    woke = true;
                }
                State::Running => g.notified = true,
                State::Runnable => {}
            }
        }
        woke
    });
    if woke {
        wake_idle();
    }
}

/// `runtime.LockOSThread`: this goroutine runs on this worker and no other
/// until it says otherwise.
pub fn lock_os_thread() {
    pin();
}

/// `runtime.UnlockOSThread`.
pub fn unlock_os_thread() {
    unpin();
}

/// `sync.runtime_procPin`: the number of the processor this goroutine is on,
/// which it stays on until it unpins.
///
/// `sync.Pool` and `sync/atomic` shard their caches by this number and index
/// arrays with it, so it is a processor slot below `GOMAXPROCS` and not a
/// worker number, and nothing else may hold it meanwhile. Pinning is the same
/// mechanism as `LockOSThread`, because staying on one worker is exactly what
/// keeps the slot.
pub fn proc_pin() -> i64 {
    pin();
    with_sched(|s| {
        let worker = ME.with(|m| m.get());
        s.ms.get(worker).and_then(|m| m.slot).unwrap_or(0) as i64
    })
}

/// `sync.runtime_procUnpin`.
pub fn proc_unpin() {
    unpin();
}

/// Keeps the running goroutine on the worker it is on. Nests.
fn pin() {
    let (me, worker) = (current(), ME.with(|m| m.get()));
    if me == NONE || worker == NONE {
        return;
    }
    with_sched(|s| {
        let g = s.g(me);
        g.pins += 1;
        g.locked_to = Some(worker);
    });
}

/// Lets it move again, once every pin has been given back.
fn unpin() {
    let me = current();
    if me == NONE {
        return;
    }
    with_sched(|s| {
        let g = s.g(me);
        g.pins = g.pins.saturating_sub(1);
        if g.pins == 0 {
            g.locked_to = None;
        }
    });
}

/// The atomics the `runtime` package needs for its own bookkeeping — the ticket
/// counters behind `sync.Cond` — and cannot reach through `sync/atomic` from
/// where it sits.
///
/// # Safety
///
/// Each takes the address of a `*uint32` the standard library handed the
/// runtime, which is therefore aligned and alive for the call.
pub fn atomic_load32(addr: usize) -> u32 {
    // SAFETY: the contract above.
    unsafe { &*(addr as *const AtomicU32) }.load(Ordering::Acquire)
}

/// Stores a `uint32` atomically. See [`atomic_load32`].
pub fn atomic_store32(addr: usize, v: u32) {
    // SAFETY: the contract above.
    unsafe { &*(addr as *const AtomicU32) }.store(v, Ordering::Release)
}

/// Adds to a `uint32` atomically and returns the new value. See
/// [`atomic_load32`].
pub fn atomic_add32(addr: usize, d: u32) -> u32 {
    // SAFETY: the contract above.
    unsafe { &*(addr as *const AtomicU32) }
        .fetch_add(d, Ordering::AcqRel)
        .wrapping_add(d)
}

// How many workers are waiting for something to run, and a count that rises
// every time something may have become runnable. Between them they make a
// worker's decision to wait and another thread's decision to notify agree: the
// worker reads the count while it still holds the scheduler's lock, so anything
// that becomes runnable afterwards raises it, and the worker sees that before
// it waits. Both are sequentially consistent, which is what that argument
// needs: each side writes one and reads the other, and at least one of them has
// to see the other's write.
static IDLE_COUNT: AtomicUsize = AtomicUsize::new(0);
static READY_GEN: AtomicU64 = AtomicU64::new(0);

/// What an idle worker waits on. The value it guards is nothing — the predicate
/// is [`READY_GEN`] — and its job is to close the window between a worker
/// deciding to wait and its waiting.
fn idle() -> &'static (std::sync::Mutex<()>, std::sync::Condvar) {
    static IDLE: std::sync::OnceLock<(std::sync::Mutex<()>, std::sync::Condvar)> =
        std::sync::OnceLock::new();
    IDLE.get_or_init(|| (std::sync::Mutex::new(()), std::sync::Condvar::new()))
}

/// Tells the idle workers that something may be runnable now.
///
/// Also pokes whichever worker is asleep in the netpoller: it may be the one
/// that has to run what just became runnable, and it would otherwise sleep
/// until a descriptor moved.
fn wake_idle() {
    READY_GEN.fetch_add(1, Ordering::SeqCst);
    if IDLE_COUNT.load(Ordering::SeqCst) == 0 {
        return;
    }
    crate::netpoll::interrupt();
    let (lock, cv) = idle();
    // Taken and given straight back rather than held across the notify: what
    // it orders is the window in which a worker has decided to wait and is not
    // waiting yet.
    drop(lock.lock().unwrap_or_else(|e| e.into_inner()));
    cv.notify_all();
}

/// Waits until there may be something for this worker to run.
///
/// With nothing runnable the program is not necessarily stuck: it may be
/// waiting on a descriptor, or on the clock. Only when there is nothing to wait
/// for either is it Go's deadlock.
fn wait_for_work(me: usize, seen: u64) {
    // A sleeper whose moment has passed is work, and the worker should not
    // wait at all.
    if wake_expired() || crate::netpoll::expire() {
        return;
    }
    let mut deadline = next_deadline();
    if let Some(poll_at) = crate::netpoll::next_deadline()
        && deadline.is_none_or(|d| poll_at < d)
    {
        deadline = Some(poll_at);
    }
    let watching = crate::netpoll::watching();
    let anything = with_sched(|s| s.has_work());
    if !anything && !watching && deadline.is_none() {
        deadlock();
    }
    let timeout_ms = deadline.map(|d| {
        // Rounded up, so a wait never ends early and spins.
        let left = d - crate::rt::nanotime();
        (left.max(0) as u64)
            .div_ceil(1_000_000)
            .min(i32::MAX as u64) as i32
    });
    if crate::rt::trace_sched() {
        crate::rt::trace(&alloc::format!(
            "worker {me}: nothing to run (watching={watching} timeout={timeout_ms:?} goroutines={})",
            count()
        ));
    }
    IDLE_COUNT.fetch_add(1, Ordering::SeqCst);
    // One worker sleeps in the netpoller, because one `epoll_wait` reports
    // every descriptor at once; the rest wait on the condition variable, and
    // whoever readies a goroutine pokes both.
    //
    // Both branches look at the count once more, after announcing that they are
    // about to wait. That is the whole handshake: a wake either sees the
    // announcement and interrupts the wait, or the waiter sees the wake and
    // does not start one.
    if watching && crate::netpoll::claim_polling() {
        if READY_GEN.load(Ordering::SeqCst) == seen {
            crate::netpoll::poll(timeout_ms.unwrap_or(-1));
        }
        crate::netpoll::release_polling();
    } else if READY_GEN.load(Ordering::SeqCst) == seen {
        let (lock, cv) = idle();
        let guard = lock.lock().unwrap_or_else(|e| e.into_inner());
        if READY_GEN.load(Ordering::SeqCst) == seen {
            match timeout_ms {
                Some(ms) => {
                    let d = core::time::Duration::from_millis(ms.max(0) as u64);
                    drop(cv.wait_timeout(guard, d));
                }
                None => drop(cv.wait(guard)),
            }
        }
    }
    IDLE_COUNT.fetch_sub(1, Ordering::SeqCst);
    wake_expired();
    crate::netpoll::expire();
}

// Stopping the world, so that the collector may walk every goroutine's roots
// (DESIGN §3). `running` counts the threads that are executing Go code and have
// not stopped; `stopping` says a collector is waiting for that to reach zero.
//
// Both live under one mutex, because every change to one is a decision about
// the other and a condition variable needs them to agree. The atomic beside it
// is a copy of `stopping` for [`safepoint`] to read, which is the only part of
// this on a hot path: it is called at every loop back-edge, and all it may cost
// when no collection is pending is a load.
struct World {
    /// Threads running Go code that have not stopped.
    running: usize,
    /// A collector is waiting for them.
    stopping: bool,
}

static STOPPING: core::sync::atomic::AtomicBool = core::sync::atomic::AtomicBool::new(false);

fn world() -> &'static (std::sync::Mutex<World>, std::sync::Condvar) {
    static WORLD: std::sync::OnceLock<(std::sync::Mutex<World>, std::sync::Condvar)> =
        std::sync::OnceLock::new();
    WORLD.get_or_init(|| {
        (
            std::sync::Mutex::new(World {
                running: 0,
                stopping: false,
            }),
            std::sync::Condvar::new(),
        )
    })
}

/// Runs `body` with the world's bookkeeping locked.
fn with_world<R>(body: impl FnOnce(&mut World, &std::sync::Condvar) -> R) -> R {
    let (lock, cv) = world();
    let mut guard = lock.lock().unwrap_or_else(|e| e.into_inner());
    body(&mut guard, cv)
}

/// This thread is about to run Go code, and waits first if a collection is
/// stopping the world.
fn attach() {
    let (lock, cv) = world();
    let mut guard = lock.lock().unwrap_or_else(|e| e.into_inner());
    while guard.stopping {
        guard = cv.wait(guard).unwrap_or_else(|e| e.into_inner());
    }
    guard.running += 1;
}

/// This thread has stopped running Go code.
fn detach() {
    with_world(|w, cv| {
        w.running -= 1;
        if w.stopping && w.running == 0 {
            cv.notify_all();
        }
    });
}

/// A safe point: where a collection may happen, and where a thread that has
/// been asked to stop stops.
///
/// Emitted at every loop back-edge and taken at the top of every allocation, so
/// that a thread reaches one in bounded time whatever it is doing. The fast
/// path is a single load, which is what makes it affordable there.
#[inline]
pub fn safepoint() {
    if STOPPING.load(core::sync::atomic::Ordering::Acquire) {
        stop_here();
    }
}

// Set on the one thread that has stopped the world across a `fork`, and holding
// what will start it again. A thread-local, because the thread that forks is the
// only thread the child has: the child inherits this and is therefore the only
// one that can start the world again, which it does by the same `after_fork` the
// parent calls.
rt_global! {
    static FORK_STOP: core::cell::RefCell<Option<Stopped>> =
        core::cell::RefCell::new(None);
}

/// Whether this thread is the one holding the world stopped across a fork.
///
/// Such a thread must not stop at a safe point and must not stop the world a
/// second time: it is the only thread that can start it again, so waiting for
/// anyone else to do so would be waiting for ever. In the child of the fork it
/// is also the only thread there is.
fn forking() -> bool {
    FORK_STOP.with(|f| f.borrow().is_some())
}

/// `syscall.runtime_BeforeFork`: stops the world across a fork.
///
/// A forked child inherits one thread and every lock exactly as it stood, so a
/// lock another thread was holding would never be given back — and the child
/// does reach the runtime, if only for the quarantine check that GC torture puts
/// in front of every dereference. The world being stopped is the one state in
/// which no thread holds a runtime lock, because no safe point falls inside a
/// critical section.
pub fn before_fork() {
    let stopped = stop_the_world();
    FORK_STOP.with(|f| *f.borrow_mut() = Some(stopped));
}

/// `syscall.runtime_AfterFork`, and the child's `runtime_AfterForkInChild`.
///
/// The same work in both: dropping the guard clears the stop and counts this
/// thread as running Go code again. In the child that is the whole world, and
/// the condition variable it notifies has nobody waiting on it.
pub fn after_fork() {
    FORK_STOP.with(|f| f.borrow_mut().take());
}

/// Waits out the collection, with this goroutine's roots where the collector
/// will find them.
#[cold]
fn stop_here() {
    if forking() {
        return;
    }
    let me = current();
    if me == NONE {
        // Not a worker running a goroutine, so there is nothing to stop and
        // nothing the collector wants from this thread. The runtime's own unit
        // tests reach the allocator from threads libtest made, and they are not
        // counted among the ones a collection waits for.
        return;
    }
    // The collector reads a stopped goroutine's roots exactly where it reads a
    // parked one's, so they go on its own record and come back afterwards.
    let state = take_thread_state();
    with_sched(|s| s.g(me).saved = state);
    with_world(|w, cv| {
        w.running -= 1;
        if w.running == 0 {
            cv.notify_all();
        }
    });
    let (lock, cv) = world();
    let mut guard = lock.lock().unwrap_or_else(|e| e.into_inner());
    while guard.stopping {
        guard = cv.wait(guard).unwrap_or_else(|e| e.into_inner());
    }
    guard.running += 1;
    drop(guard);
    let state = with_sched(|s| core::mem::replace(&mut s.g(me).saved, Saved::new()));
    put_thread_state(state);
}

/// Stops every other thread that is running Go code, and keeps them stopped
/// until the result is dropped.
///
/// Taken before the heap's own lock, never while holding it: a thread that has
/// not reached a safe point yet may well be inside a heap critical section, and
/// waiting for it with that lock held would be waiting for itself.
pub fn stop_the_world() -> Stopped {
    if forking() {
        // This thread already holds the world stopped, across a fork. Asking
        // again is not a second stop, it is the same one, and waiting for it to
        // end would be waiting for this thread.
        return Stopped {
            running: false,
            restart: false,
        };
    }
    // A thread with no goroutine was never counted among the ones running Go
    // code, so it has nothing to step out of. That is how the runtime's own
    // unit tests collect.
    let me = current();
    let running = me != NONE;
    // The roots go on the goroutine's own record *before* this thread says it
    // has stopped, exactly as a safe point does it, and for a reason that took
    // a while to find: two threads can reach a collection at the same moment,
    // and the one that loses the race waits below having already said it is not
    // running Go code. If its roots were still only on its own thread's shadow
    // stack, the winner would mark without them and free what the loser was
    // holding — which showed up as a stack pointer appearing inside a scheduler
    // record, a collected object's memory handed back out as something else.
    if running {
        let state = take_thread_state();
        with_sched(|s| s.g(me).saved = state);
    }
    let (lock, cv) = world();
    let mut guard = lock.lock().unwrap_or_else(|e| e.into_inner());
    // This thread is one of the ones that would have to stop, and it is the one
    // doing the stopping, so it steps out first. A second collector waiting
    // below would otherwise be a thread the first one waits for for ever.
    if running {
        guard.running -= 1;
        if guard.stopping && guard.running == 0 {
            cv.notify_all();
        }
    }
    while guard.stopping {
        guard = cv.wait(guard).unwrap_or_else(|e| e.into_inner());
    }
    guard.stopping = true;
    STOPPING.store(true, core::sync::atomic::Ordering::Release);
    while guard.running > 0 {
        guard = cv.wait(guard).unwrap_or_else(|e| e.into_inner());
    }
    drop(guard);
    Stopped {
        running,
        restart: true,
    }
}

/// Lets the world run again when it is dropped, so that a collection which
/// unwinds does not leave every other thread stopped for ever.
pub struct Stopped {
    /// Whether the thread that stopped the world was itself running Go code,
    /// and so has to be counted again.
    running: bool,
    /// Whether this is the guard that started the stop. A thread that asked
    /// twice gets one that does nothing.
    restart: bool,
}

impl Drop for Stopped {
    fn drop(&mut self) {
        if !self.restart {
            return;
        }
        // Back on the thread before it runs Go code again, which is the mirror
        // of `stop_the_world` putting them on the record.
        if self.running {
            let me = current();
            let state = with_sched(|s| core::mem::replace(&mut s.g(me).saved, Saved::new()));
            put_thread_state(state);
        }
        STOPPING.store(false, core::sync::atomic::Ordering::Release);
        let running = self.running;
        with_world(|w, cv| {
            w.stopping = false;
            if running {
                w.running += 1;
            }
            cv.notify_all();
        });
    }
}

/// Go's report when nothing can run: every goroutine is blocked forever.
fn deadlock() -> ! {
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

/// Traces the roots of every goroutine there is.
///
/// Every one of them, with no exception for the goroutine the collector is
/// itself running: a collection stops the world first, and stopping means every
/// thread has put its goroutine's roots on that goroutine's own record —
/// including the collector, in `stop_the_world`. Skipping the collector's own
/// used to be right when its roots were still on its thread and nowhere else,
/// and wrong the moment a second thread could be the one marking.
pub(crate) fn trace_parked_roots(t: &mut crate::trace::Tracer<'_>) {
    // Taken while the heap's own lock is held, which is the one place the two
    // are nested and therefore fixes their order: heap, then scheduler. It is
    // safe to wait for because nothing on the other side allocates — the
    // scheduler's critical sections move words and queue ids, and the Go heap
    // is never touched under them.
    SCHED.with(|sched| {
        for g in sched.gs.iter().flatten() {
            // A goroutine that has not started yet holds its arguments in the
            // environment the `go` statement built, and nothing else refers to
            // it: the frame that built it has moved on.
            if let Some(Entry::Go(_, env)) = &g.entry {
                t.edge(env.addr() as usize);
            }
            // SAFETY: the chain belongs to a stack nothing is running on — every
            // thread is stopped and has handed its chain over — so every frame
            // on it is alive and unchanged.
            unsafe { crate::gc::trace_chain(g.saved.roots, t) };
            for p in &g.saved.panics {
                crate::trace::Trace::trace(&p.value(), t);
            }
        }
    });
}

// Goroutines parked until a moment on the monotonic clock: `time.Sleep`, and
// the timer goroutine waiting for its next timer. A worker with nothing to run
// wakes them, and their deadlines bound how long it may wait.
//
// Shared, like the run queue: whichever worker finds nothing to run is the one
// that has to know when the earliest sleeper is due, and it is not necessarily
// the worker that put it there.
rt_shared! {
    static SLEEPERS: Vec<(i64, Gid)> = Vec::new();
}

/// Parks the running goroutine until the monotonic clock reaches `deadline`.
///
/// Unlike sleeping the thread, this leaves the other goroutines running, and a
/// worker with nothing to do still waits in the kernel rather than spinning: a
/// deadline is how long its next wait may last.
pub fn sleep_until(deadline: i64) {
    if crate::rt::nanotime() >= deadline {
        return;
    }
    let me = current();
    SLEEPERS.with(|s| s.push((deadline, me)));
    // A park may come back with nothing having happened, and `time.Sleep` is
    // not allowed to return early, so the clock is what says when it is over.
    while crate::rt::nanotime() < deadline {
        park();
    }
    SLEEPERS.with(|s| s.retain(|&(_, g)| g != me));
}

/// The earliest deadline anything is waiting for, if any.
fn next_deadline() -> Option<i64> {
    SLEEPERS.with(|s| s.iter().map(|&(d, _)| d).min())
}

/// Readies every goroutine whose deadline has passed, and says whether any had.
///
/// The ones it readies are taken off the list here rather than by the sleeper
/// itself. Several workers call this, and a sleeper that stayed on the list
/// until it next ran would be reported as due to every one of them in the
/// meantime, which is a spin.
fn wake_expired() -> bool {
    let now = crate::rt::nanotime();
    let due: Vec<Gid> = SLEEPERS.with(|s| {
        let mut due = Vec::new();
        s.retain(|&(d, g)| {
            if d <= now {
                due.push(g);
                return false;
            }
            true
        });
        due
    });
    ready_all(&due);
    !due.is_empty()
}

// Goroutines parked on an address: Go's semaphores and the channel queues.
//
// By address, because that is how every wake names its waiters, and a program
// can have a great many of them at once — Go's own `chanlinear` test puts
// thousands of goroutines in a `select` on one channel, wakes each of them
// through a channel of its own, and requires that the whole thing stay linear
// in the number of wakes. Two things about one shared address therefore have to
// be cheap: waking the goroutines at it, and *one* goroutine leaving it, which
// is what a `select` does to every case it did not take.
//
// Shared for the reason the run queue is, and the reason this is a table rather
// than a field on each goroutine: a wake names an address, not a goroutine, and
// the thread that wakes it is rarely the thread that runs it.
rt_shared! {
    static WAITERS: BTreeMap<usize, Queue> = BTreeMap::new();
}

/// The goroutines waiting at one address, oldest first.
///
/// Leaving has to be as cheap as arriving, and a goroutine leaves by name, so
/// every registration is numbered and the numbers say where in the queue it
/// sits. Nothing shifts: a departure empties its slot, and the empty slots at
/// the front are dropped as they are reached.
#[derive(Default)]
struct Queue {
    /// The number of `slots[0]`.
    first: u64,
    slots: VecDeque<Option<Gid>>,
    /// Which number each goroutine here was given. A goroutine registers at one
    /// address at most once.
    at: BTreeMap<Gid, u64>,
}

impl Queue {
    fn push(&mut self, g: Gid) {
        if self.at.contains_key(&g) {
            return;
        }
        self.at.insert(g, self.first + self.slots.len() as u64);
        self.slots.push_back(Some(g));
    }

    /// Takes the goroutine that has waited longest.
    fn pop(&mut self) -> Option<Gid> {
        while let Some(slot) = self.slots.pop_front() {
            self.first += 1;
            if let Some(g) = slot {
                self.at.remove(&g);
                return Some(g);
            }
        }
        None
    }

    /// Takes one named goroutine, wherever it is.
    fn remove(&mut self, g: Gid) {
        if let Some(seq) = self.at.remove(&g) {
            self.slots[(seq - self.first) as usize] = None;
            self.trim();
        }
    }

    /// Drops the empty slots the front has collected.
    fn trim(&mut self) {
        while let Some(None) = self.slots.front() {
            self.slots.pop_front();
            self.first += 1;
        }
    }

    fn is_empty(&self) -> bool {
        self.at.is_empty()
    }
}

/// Registers the running goroutine as waiting at `addr`, without parking yet.
///
/// Called while the caller still holds whatever guards the condition it is
/// waiting for — a channel's lock, the word a semaphore counts down — so that a
/// wake arriving after the condition was tested cannot miss it. The park itself
/// follows once that lock has been released, because a park switches stacks and
/// must not carry a lock across.
pub fn prepare_park(addr: usize) {
    let me = current();
    WAITERS.with(|w| w.entry(addr).or_default().push(me));
}

/// Stops the running goroutine waiting at `addr`, whether it parked or thought
/// better of it.
pub fn leave_park(addr: usize) {
    let me = current();
    WAITERS.with(|w| dequeue(w, addr, me));
    forget_wake(me);
}

/// Takes one goroutine off one address.
fn dequeue(w: &mut BTreeMap<usize, Queue>, addr: usize, g: Gid) {
    if let Some(q) = w.get_mut(&addr) {
        q.remove(g);
        if q.is_empty() {
            w.remove(&addr);
        }
    }
}

/// Forgets a wake this goroutine has not used.
///
/// A goroutine that has stopped waiting for anything has nothing to be woken
/// from, and keeping the mark would make its *next* park come straight back —
/// which `time.Sleep` would see as returning early.
fn forget_wake(me: Gid) {
    with_sched(|s| {
        if let Some(Some(g)) = s.gs.get_mut(me) {
            g.notified = false;
        }
    });
}

/// Parks the running goroutine on `addr` until a wake of that address names it.
///
/// For a wait whose condition is the wake itself, with nothing to re-test: a
/// goroutine that is over, or one that will never run again.
pub fn park_on(addr: usize) {
    prepare_park(addr);
    park();
    leave_park(addr);
}

/// Readies every goroutine parked on `addr`.
///
/// More than Go's queues would wake, and each re-tests what it was waiting for.
/// It cannot lose a wakeup, which a queue of one can when the goroutine it
/// picks turns out not to be able to proceed after all — a `select` that
/// another goroutine got to first.
pub fn wake_all(addr: usize) {
    let woken = WAITERS.with(|w| {
        let mut woken = Vec::new();
        if let Some(q) = w.get_mut(&addr) {
            while let Some(g) = q.pop() {
                woken.push(g);
            }
            w.remove(&addr);
        }
        woken
    });
    ready_all(&woken);
}

/// Readies the goroutine that has waited longest on `addr`, if any.
pub fn wake_one(addr: usize) {
    let waiter = WAITERS.with(|w| {
        let q = w.get_mut(&addr)?;
        let g = q.pop();
        if q.is_empty() {
            w.remove(&addr);
        }
        g
    });
    if let Some(g) = waiter {
        ready(g);
    }
}

/// Registers the running goroutine on every one of `addrs` without parking.
///
/// What a `select` does before it tests its cases: every channel it might block
/// on has to have this goroutine on its queue before the test, or a sender that
/// arrives between the test and the park wakes nobody.
pub fn prepare_park_any(addrs: &[usize]) {
    let me = current();
    WAITERS.with(|w| {
        for &a in addrs {
            w.entry(a).or_default().push(me);
        }
    });
}

/// Takes the running goroutine off every one of `addrs`.
pub fn leave_park_any(addrs: &[usize]) {
    let me = current();
    WAITERS.with(|w| {
        for &a in addrs {
            dequeue(w, a, me);
        }
    });
    forget_wake(me);
}

/// Parks the running goroutine until any of `addrs` is woken, then takes it off
/// all of them. An empty list parks for ever, which is `select {}`.
pub fn park_on_any(addrs: &[usize]) {
    // `select {}` waits at an address nothing ever wakes.
    let addrs = if addrs.is_empty() { &[0][..] } else { addrs };
    prepare_park_any(addrs);
    park();
    leave_park_any(addrs);
}

/// `select {}`: waits for nothing, for ever.
pub fn block_forever() -> ! {
    loop {
        park_on(0);
    }
}

// A Go semaphore: the counter `sync` and `internal/poll` hand the runtime, and
// the goroutines waiting for it. gc keeps this in its runtime for the reason
// this does — the test and the park have to agree about what happened in
// between, and only the runtime can make them — and with more than one thread
// the counter has to be read and written atomically as well, or two goroutines
// take the same unit.

/// `sync.runtime_Semacquire`: takes one unit of the semaphore at `addr`,
/// waiting until there is one to take.
pub fn sem_acquire(addr: usize) {
    loop {
        if sem_try(addr) {
            return;
        }
        // Registered before the second look, so that a release between the two
        // finds this goroutine on the queue rather than nothing at all.
        prepare_park(addr);
        if sem_try(addr) {
            leave_park(addr);
            return;
        }
        park();
        leave_park(addr);
    }
}

/// `sync.runtime_Semrelease`: gives one unit back, and wakes a waiter.
pub fn sem_release(addr: usize) {
    sema(addr).fetch_add(1, Ordering::Release);
    wake_one(addr);
}

/// Takes one unit if there is one.
fn sem_try(addr: usize) -> bool {
    let s = sema(addr);
    let mut have = s.load(Ordering::Acquire);
    while have > 0 {
        match s.compare_exchange_weak(have, have - 1, Ordering::Acquire, Ordering::Relaxed) {
            Ok(_) => return true,
            Err(now) => have = now,
        }
    }
    false
}

/// The counter at an address the standard library handed the runtime.
fn sema(addr: usize) -> &'static AtomicU32 {
    // SAFETY (invariant): every caller is a `go:linkname` hook that `sync`,
    // `internal/sync` or `internal/poll` calls with a `*uint32` of its own, so
    // the address is aligned for a `u32` and outlives the call. Reading it as
    // an atomic is what keeps two threads counting it down from disagreeing;
    // the Go side only ever touches it through these hooks.
    unsafe { &*(addr as *const AtomicU32) }
}

// Which ready case a `select` takes. Go picks uniformly at random, so that a
// case cannot be starved by an earlier one that is always ready.
//
// Per thread, because nothing about it has to be shared: a worker needs only
// that its own choices are not all the same one, and a sequence per thread is
// both cheaper and no less random than one behind a lock.
rt_global! {
    static SEED: Cell<u64> = Cell::new(0x2545F4914F6CDD1D);
}

/// A number below `n`, for `select` to start its poll at.
pub fn pick(n: usize) -> usize {
    if n <= 1 {
        return 0;
    }
    // xorshift64*, which is small, fast and more than random enough to keep one
    // case from starving another.
    let mut x = SEED.with(|s| s.get());
    x ^= x >> 12;
    x ^= x << 25;
    x ^= x >> 27;
    SEED.with(|s| s.set(x));
    (x.wrapping_mul(0x2545F4914F6CDD1D) >> 33) as usize % n
}
