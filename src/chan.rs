//! Channels (DESIGN §4).
//!
//! A channel is a heap object like a map, so it is a reference the collector
//! traces, `nil` until it is made, and shared by every goroutine holding it.
//!
//! Every goroutine blocked on a channel parks on the channel's own address
//! and re-tests its condition when woken, and every operation that changes
//! the channel wakes all of them. That is more wakeups than Go's queues of
//! waiters need, but with one thread and a handful of goroutines it costs
//! little and cannot lose one; the M:N scheduler brings the queues.

use crate::heap;
use crate::sched;
use crate::trace::{Trace, Tracer};
use crate::value::GoValue;
use alloc::collections::VecDeque;
use core::cell::RefCell;

/// The heap object behind a channel.
struct ChanObj<T: 'static> {
    state: RefCell<State<T>>,
}

struct State<T> {
    /// Values sent and not yet received. A synchronous channel holds one
    /// here while its sender waits for the receiver to take it.
    buf: VecDeque<T>,
    cap: usize,
    closed: bool,
    /// How many goroutines are parked waiting to receive. A synchronous send
    /// needs one of them to hand its value to.
    receivers: usize,
}

impl<T: Trace> Trace for ChanObj<T> {
    fn trace(&self, t: &mut Tracer<'_>) {
        // A value in flight is reachable from nothing else.
        for v in self.state.borrow().buf.iter() {
            v.trace(t);
        }
    }
}

/// A Go `chan T`.
pub struct Chan<T: 'static> {
    obj: Option<core::ptr::NonNull<ChanObj<T>>>,
}

impl<T> Clone for Chan<T> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<T> Copy for Chan<T> {}

impl<T: 'static> GoValue for Chan<T> {
    #[inline]
    fn zero() -> Self {
        Chan { obj: None }
    }
}

impl<T> Trace for Chan<T> {
    #[inline]
    fn trace(&self, t: &mut Tracer<'_>) {
        t.edge(self.addr() as usize);
    }
}

impl<T> PartialEq for Chan<T> {
    #[inline]
    fn eq(&self, other: &Self) -> bool {
        self.addr() == other.addr()
    }
}

impl<T> Eq for Chan<T> {}

impl<T> Chan<T> {
    /// `ch == nil`.
    #[inline]
    pub fn is_nil(self) -> bool {
        self.obj.is_none()
    }

    /// The address `println` shows, which the collector resolves and blocked
    /// goroutines park on.
    #[inline]
    pub fn addr(self) -> u64 {
        self.obj.map_or(0, |p| p.as_ptr() as usize as u64)
    }

    fn state(self) -> &'static RefCell<State<T>> {
        let obj = self.obj.expect("nil channel");
        // Under GC torture, name a channel that was collected because
        // something failed to root it, rather than reading its poison.
        heap::check_live(obj.as_ptr() as usize);
        // SAFETY: the object lives as long as any reference to it is
        // reachable, and this channel is one such reference.
        unsafe { &(*obj.as_ptr()).state }
    }
}

impl<T: GoValue + Trace> Chan<T> {
    /// `make(chan T, cap)`. A safe point: it allocates.
    pub fn make(cap: i64) -> Self {
        let cap = crate::ops::make_bounds(cap, cap, size_of::<T>()).0;
        let obj = heap::allocate(ChanObj {
            state: RefCell::new(State {
                buf: VecDeque::new(),
                cap,
                closed: false,
                receivers: 0,
            }),
        });
        Chan { obj: Some(obj) }
    }

    /// `len(ch)`: values waiting in the buffer.
    pub fn len(self) -> i64 {
        if self.is_nil() {
            return 0;
        }
        self.state().borrow().buf.len() as i64
    }

    /// `cap(ch)`.
    pub fn cap(self) -> i64 {
        if self.is_nil() {
            return 0;
        }
        self.state().borrow().cap as i64
    }

    /// `len(ch) == 0`, for clippy's sake.
    pub fn is_empty(self) -> bool {
        self.len() == 0
    }

    /// `ch <- v`. Blocks until the value is taken or there is room for it.
    pub fn send(self, v: T) {
        if self.is_nil() {
            // A send on a nil channel blocks for ever, which is a deadlock
            // unless another goroutine can still run.
            block_forever();
        }
        let addr = self.addr() as usize;
        loop {
            {
                let mut s = self.state().borrow_mut();
                if s.closed {
                    drop(s);
                    crate::panic::runtime_error_msg(alloc::string::String::from(
                        "send on closed channel",
                    ));
                }
                if s.cap > 0 && s.buf.len() < s.cap {
                    s.buf.push_back(v);
                    drop(s);
                    sched::wake_all(addr);
                    return;
                }
                if s.cap == 0 && s.receivers > 0 && s.buf.is_empty() {
                    // A synchronous hand-off: leave the value for the
                    // receiver, then wait until it has been taken.
                    s.buf.push_back(v);
                    drop(s);
                    sched::wake_all(addr);
                    loop {
                        let s = self.state().borrow();
                        if s.buf.is_empty() {
                            return;
                        }
                        let closed = s.closed;
                        drop(s);
                        if closed {
                            crate::panic::runtime_error_msg(alloc::string::String::from(
                                "send on closed channel",
                            ));
                        }
                        sched::park_on(addr);
                    }
                }
            }
            // Nothing to do but wait; waking the others first lets a
            // receiver see that a sender is here.
            sched::wake_all(addr);
            sched::park_on(addr);
        }
    }

    /// `v, ok := <-ch`. Blocks until a value arrives or the channel closes;
    /// `ok` is false once a closed channel has been drained.
    pub fn recv(self) -> (T, bool) {
        if self.is_nil() {
            block_forever();
        }
        let addr = self.addr() as usize;
        loop {
            {
                let mut s = self.state().borrow_mut();
                if let Some(v) = s.buf.pop_front() {
                    drop(s);
                    sched::wake_all(addr);
                    return (v, true);
                }
                if s.closed {
                    return (T::zero(), false);
                }
                s.receivers += 1;
            }
            // Telling the senders there is a receiver is what lets a
            // synchronous send go ahead.
            sched::wake_all(addr);
            sched::park_on(addr);
            self.state().borrow_mut().receivers -= 1;
        }
    }

    /// `<-ch`, where the program does not ask whether the channel is closed.
    pub fn recv_value(self) -> T {
        self.recv().0
    }

    /// Whether a receive would proceed without blocking, for `select`.
    pub fn can_recv(self) -> bool {
        if self.is_nil() {
            return false;
        }
        let s = self.state().borrow();
        !s.buf.is_empty() || s.closed
    }

    /// Whether a send would proceed without blocking, for `select`.
    pub fn can_send(self) -> bool {
        if self.is_nil() {
            return false;
        }
        let s = self.state().borrow();
        // A closed channel "can" send: the send panics, which is what Go
        // does when `select` picks that case.
        s.closed || (s.cap > 0 && s.buf.len() < s.cap) || (s.cap == 0 && s.receivers > 0)
    }

    /// `close(ch)`.
    pub fn close(self) {
        if self.is_nil() {
            crate::panic::runtime_error_msg(alloc::string::String::from("close of nil channel"));
        }
        let addr = self.addr() as usize;
        {
            let mut s = self.state().borrow_mut();
            if s.closed {
                drop(s);
                crate::panic::runtime_error_msg(alloc::string::String::from(
                    "close of closed channel",
                ));
            }
            s.closed = true;
        }
        sched::wake_all(addr);
    }
}

/// Blocks the goroutine for ever, as an operation on a nil channel does. With
/// nothing else to run, the scheduler reports Go's deadlock.
fn block_forever() -> ! {
    loop {
        sched::park_on(0);
    }
}
