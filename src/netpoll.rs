//! The netpoller: goroutines that wait on a file descriptor (DESIGN §4).
//!
//! `internal/poll` wraps every socket and asks the runtime nine questions
//! about readiness, all of them reached by `go:linkname`. This answers them
//! with `epoll`, and the scheduler calls [`wait`] when it has nothing to run:
//! a goroutine blocked on a socket is parked, not spinning, and the thread
//! sleeps in the kernel until a descriptor moves.
//!
//! Level-triggered, with the interest mask following the waiters: a
//! descriptor asks for readability only while a goroutine waits to read it.
//! Edge-triggered polling with readiness latches, which is what gc uses, buys
//! fewer system calls at the price of a latch that hangs the program if it is
//! ever wrong.
//!
//! Linux only for now, as the rest of the file layer is (roadmap M5).

use crate::sched::{self, Gid};
use alloc::boxed::Box;
use alloc::vec::Vec;
use core::cell::RefCell;

/// Ready, as `internal/poll` reads it.
pub const NO_ERROR: i32 = 0;
/// The descriptor is being closed.
pub const ERR_CLOSING: i32 = 1;
/// The deadline passed. Deadlines need timers, which are still to come.
pub const ERR_TIMEOUT: i32 = 2;
/// Not something this poller can watch, so `internal/poll` does the blocking
/// call itself — which is how a regular file is read under gc too.
pub const ERR_NOT_POLLABLE: i32 = 3;

/// Go's mode letters.
const MODE_READ: i32 = b'r' as i32;

const EPOLLIN: u32 = 0x001;
const EPOLLOUT: u32 = 0x004;
const EPOLLERR: u32 = 0x008;
const EPOLLHUP: u32 = 0x010;
const EPOLLRDHUP: u32 = 0x2000;

const EPOLL_CTL_ADD: u64 = 1;
const EPOLL_CTL_DEL: u64 = 2;
const EPOLL_CTL_MOD: u64 = 3;

/// One descriptor the poller watches.
struct Desc {
    fd: i32,
    /// The goroutine waiting to read, and the one waiting to write.
    reader: Option<Gid>,
    writer: Option<Gid>,
    /// What `epoll` has been asked for, so a change can be spotted.
    interest: u32,
    /// Set by `unblock` when the descriptor is being closed: every waiter
    /// wakes and reports that, as gc's poller does.
    closing: bool,
}

impl Desc {
    /// The addresses a waiter parks on. Two distinct words inside the
    /// descriptor, so a reader and a writer wake independently.
    fn key(&self, mode: i32) -> usize {
        let base = self as *const Desc as usize;
        if mode == MODE_READ { base } else { base + 1 }
    }
}

struct Poller {
    /// The `epoll` descriptor, created on first use.
    epfd: i32,
    /// Descriptors by context, which is the index plus one: `internal/poll`
    /// treats a zero context as "no poller".
    descs: Vec<Option<Box<Desc>>>,
}

rt_global! {
    static POLLER: RefCell<Option<Poller>> = RefCell::new(None);
}

fn with_poller<R>(body: impl FnOnce(&mut Poller) -> R) -> Option<R> {
    POLLER.with(|p| {
        let mut slot = p.borrow_mut();
        if slot.is_none() {
            let epfd = epoll_create()?;
            *slot = Some(Poller {
                epfd,
                descs: Vec::new(),
            });
        }
        Some(body(slot.as_mut().expect("poller")))
    })
}

/// `runtime_pollOpen`: starts watching `fd`, and returns its context.
///
/// The error is an errno, as `internal/poll` reports it; 0 is success.
pub fn open(fd: i32) -> (usize, i32) {
    let opened = with_poller(|p| {
        let desc = Box::new(Desc {
            fd,
            reader: None,
            writer: None,
            interest: 0,
            closing: false,
        });
        // Registered with no interest: a descriptor is only asked about while
        // a goroutine is waiting for it.
        let err = epoll_ctl(p.epfd, EPOLL_CTL_ADD, fd, 0, 0);
        if err != 0 {
            return (0, err);
        }
        let ctx = p.descs.len() + 1;
        // The event data is the context, which is how a readiness report
        // finds its descriptor again.
        let err = epoll_ctl(p.epfd, EPOLL_CTL_MOD, fd, 0, ctx as u64);
        if err != 0 {
            epoll_ctl(p.epfd, EPOLL_CTL_DEL, fd, 0, 0);
            return (0, err);
        }
        p.descs.push(Some(desc));
        (ctx, 0)
    });
    // ENOMEM if the poller itself could not be created.
    opened.unwrap_or((0, 12))
}

/// `runtime_pollClose`: stops watching, and forgets the descriptor.
pub fn close(ctx: usize) {
    with_poller(|p| {
        if let Some(desc) = desc_at(p, ctx) {
            let fd = desc.fd;
            epoll_ctl(p.epfd, EPOLL_CTL_DEL, fd, 0, 0);
        }
        if ctx >= 1 && ctx <= p.descs.len() {
            p.descs[ctx - 1] = None;
        }
    });
}

/// `runtime_pollReset`: nothing to reset. gc clears a readiness latch here;
/// this poller has none, because it asks the kernel each time instead.
pub fn reset(ctx: usize, _mode: i32) -> i32 {
    match with_poller(|p| desc_at(p, ctx).map(|d| d.closing)) {
        Some(Some(true)) => ERR_CLOSING,
        Some(Some(false)) => NO_ERROR,
        _ => ERR_NOT_POLLABLE,
    }
}

/// `runtime_pollWait`: parks until the descriptor is ready for `mode`.
pub fn wait(ctx: usize, mode: i32) -> i32 {
    let key = match with_poller(|p| {
        let me = sched::current();
        let Some(desc) = desc_at(p, ctx) else {
            return Err(ERR_NOT_POLLABLE);
        };
        if desc.closing {
            return Err(ERR_CLOSING);
        }
        if mode == MODE_READ {
            desc.reader = Some(me);
        } else {
            desc.writer = Some(me);
        }
        let key = desc.key(mode);
        let (fd, interest) = (desc.fd, wanted(desc));
        arm(p, ctx, fd, interest);
        Ok(key)
    }) {
        None => return ERR_NOT_POLLABLE,
        Some(Err(e)) => return e,
        Some(Ok(key)) => key,
    };
    sched::park_on(key);
    // Woken by readiness, or by the descriptor closing under us.
    match with_poller(|p| desc_at(p, ctx).map(|d| d.closing)) {
        Some(Some(true)) => ERR_CLOSING,
        Some(Some(false)) => NO_ERROR,
        _ => ERR_NOT_POLLABLE,
    }
}

/// `runtime_pollUnblock`: the descriptor is closing, so every waiter wakes
/// and is told so.
pub fn unblock(ctx: usize) {
    let keys = with_poller(|p| {
        let Some(desc) = desc_at(p, ctx) else {
            return (0, 0);
        };
        desc.closing = true;
        desc.reader = None;
        desc.writer = None;
        let keys = (desc.key(MODE_READ), desc.key(MODE_READ) + 1);
        let fd = desc.fd;
        arm(p, ctx, fd, 0);
        keys
    });
    if let Some((r, w)) = keys
        && r != 0
    {
        sched::wake_all(r);
        sched::wake_all(w);
    }
}

/// Whether anything is registered, which is what tells the scheduler that a
/// program with no runnable goroutine may still be waiting rather than stuck.
pub fn watching() -> bool {
    POLLER.with(|p| {
        p.borrow()
            .as_ref()
            .is_some_and(|poller| poller.descs.iter().any(|d| d.is_some()))
    })
}

/// Waits for a descriptor to become ready and readies the goroutines waiting
/// on it. `timeout_ms` of -1 blocks until something happens.
///
/// Returns whether anything was readied. The scheduler calls this when it has
/// nothing to run.
pub fn poll(timeout_ms: i32) -> bool {
    let Some((epfd, count)) = POLLER.with(|p| {
        p.borrow()
            .as_ref()
            .map(|poller| (poller.epfd, poller.descs.len()))
    }) else {
        return false;
    };
    if count == 0 {
        return false;
    }
    // The poller is not borrowed across the system call: a goroutine readied
    // below will want it.
    let mut events = [EpollEvent::ZERO; 64];
    let n = epoll_wait(epfd, &mut events, timeout_ms);
    let mut woke = false;
    for ev in events.iter().take(n) {
        let ctx = ev.data as usize;
        let ready = ev.events;
        let keys = with_poller(|p| {
            let Some(desc) = desc_at(p, ctx) else {
                return (0, 0);
            };
            let mut keys = (0, 0);
            // An error or a hangup wakes both sides: the next read or write
            // is what reports it, exactly as it would under gc.
            let failed = ready & (EPOLLERR | EPOLLHUP) != 0;
            if (ready & (EPOLLIN | EPOLLRDHUP) != 0 || failed) && desc.reader.take().is_some() {
                keys.0 = desc.key(MODE_READ);
            }
            if (ready & EPOLLOUT != 0 || failed) && desc.writer.take().is_some() {
                keys.1 = desc.key(MODE_READ) + 1;
            }
            let (fd, interest) = (desc.fd, wanted(desc));
            arm(p, ctx, fd, interest);
            keys
        });
        if let Some((r, w)) = keys {
            if r != 0 {
                sched::wake_all(r);
                woke = true;
            }
            if w != 0 {
                sched::wake_all(w);
                woke = true;
            }
        }
    }
    woke
}

fn desc_at(p: &mut Poller, ctx: usize) -> Option<&mut Desc> {
    if ctx == 0 || ctx > p.descs.len() {
        return None;
    }
    p.descs[ctx - 1].as_deref_mut()
}

/// What a descriptor should be asking `epoll` for, given who is waiting.
fn wanted(desc: &Desc) -> u32 {
    let mut m = 0;
    if desc.reader.is_some() {
        m |= EPOLLIN | EPOLLRDHUP;
    }
    if desc.writer.is_some() {
        m |= EPOLLOUT;
    }
    m
}

/// Tells `epoll` what this descriptor is interested in, if it changed.
fn arm(p: &mut Poller, ctx: usize, fd: i32, interest: u32) {
    let epfd = p.epfd;
    let Some(desc) = desc_at(p, ctx) else { return };
    if desc.interest == interest {
        return;
    }
    desc.interest = interest;
    epoll_ctl(epfd, EPOLL_CTL_MOD, fd, interest, ctx as u64);
}

// The kernel's `struct epoll_event`, which x86-64 packs and aarch64 does not.
#[cfg(target_arch = "x86_64")]
#[repr(C, packed)]
#[derive(Clone, Copy)]
struct EpollEvent {
    events: u32,
    data: u64,
}

#[cfg(not(target_arch = "x86_64"))]
#[repr(C)]
#[derive(Clone, Copy)]
struct EpollEvent {
    events: u32,
    data: u64,
}

impl EpollEvent {
    const ZERO: EpollEvent = EpollEvent { events: 0, data: 0 };
}

#[cfg(all(target_os = "linux", target_arch = "x86_64"))]
mod nr {
    pub const EPOLL_CREATE1: u64 = 291;
    pub const EPOLL_CTL: u64 = 233;
    pub const EPOLL_PWAIT: u64 = 281;
}

#[cfg(all(target_os = "linux", target_arch = "aarch64"))]
mod nr {
    pub const EPOLL_CREATE1: u64 = 20;
    pub const EPOLL_CTL: u64 = 21;
    pub const EPOLL_PWAIT: u64 = 22;
}

#[cfg(target_os = "linux")]
const EPOLL_CLOEXEC: u64 = 0o2000000;

#[cfg(target_os = "linux")]
fn epoll_create() -> Option<i32> {
    let (fd, _, errno) = crate::syscall::syscall6(nr::EPOLL_CREATE1, EPOLL_CLOEXEC, 0, 0, 0, 0, 0);
    if errno != 0 { None } else { Some(fd as i32) }
}

#[cfg(target_os = "linux")]
fn epoll_ctl(epfd: i32, op: u64, fd: i32, events: u32, data: u64) -> i32 {
    let mut ev = EpollEvent { events, data };
    let p = &raw mut ev as u64;
    let (_, _, errno) = crate::syscall::syscall6(
        nr::EPOLL_CTL,
        epfd as u64,
        op,
        fd as u64,
        if op == EPOLL_CTL_DEL { 0 } else { p },
        0,
        0,
    );
    errno as i32
}

#[cfg(target_os = "linux")]
fn epoll_wait(epfd: i32, events: &mut [EpollEvent], timeout_ms: i32) -> usize {
    let (n, _, errno) = crate::syscall::syscall6(
        nr::EPOLL_PWAIT,
        epfd as u64,
        events.as_mut_ptr() as u64,
        events.len() as u64,
        timeout_ms as i64 as u64,
        0, // no signal mask
        0,
    );
    if errno != 0 { 0 } else { n as usize }
}

// A platform without this poller: `open` fails, and `internal/poll` falls
// back to treating every descriptor as unpollable, which is what it does for
// regular files.
#[cfg(not(target_os = "linux"))]
fn epoll_create() -> Option<i32> {
    None
}

#[cfg(not(target_os = "linux"))]
fn epoll_ctl(_: i32, _: u64, _: i32, _: u32, _: u64) -> i32 {
    38 // ENOSYS
}

#[cfg(not(target_os = "linux"))]
fn epoll_wait(_: i32, _: &mut [EpollEvent], _: i32) -> usize {
    0
}
