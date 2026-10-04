//! Channels (DESIGN §4).
//!
//! A channel is a heap object like a map, so it is a reference the collector
//! traces, `nil` until it is made, and shared by every goroutine holding it.
//!
//! Its state sits behind a lock of its own, for two reasons. Goroutines on
//! different threads reach the same channel at once; and the test a blocking
//! operation makes and the queue it puts itself on have to happen together. A
//! goroutine that found the channel empty and *then* registered as a waiter
//! would never hear from the sender that arrived in between. So every operation
//! decides what to do under the lock and registers itself there if it is going
//! to wait, and only then — with the lock released, because a park switches
//! stacks — parks.
//!
//! Every goroutine blocked on a channel waits on the channel's own address and
//! re-tests its condition when woken, and every operation that changes the
//! channel wakes all of them. That is more wakeups than Go's queues of waiters
//! need, but it cannot lose one; the queues are still to come.

use crate::heap;
use crate::sched;
use crate::tls::Lock;
use crate::trace::{Trace, Tracer};
use crate::value::GoValue;
use alloc::collections::VecDeque;

/// The heap object behind a channel.
struct ChanObj<T: 'static> {
    state: Lock<State<T>>,
}

struct State<T> {
    /// Values sent and not yet received. A synchronous channel holds one here
    /// while its sender waits for the receiver to take it.
    buf: VecDeque<T>,
    cap: usize,
    closed: bool,
    /// How many goroutines are waiting to receive. A synchronous send needs one
    /// of them to hand its value to.
    receivers: usize,
}

impl<T: Trace> Trace for ChanObj<T> {
    fn trace(&self, t: &mut Tracer<'_>) {
        // Taken while the collector holds the heap's own lock, which is safe
        // because no goroutine is ever stopped inside a channel's critical
        // section: they move words, and nothing in one allocates or parks.
        self.state.with(|s| {
            // A value in flight is reachable from nothing else.
            for v in s.buf.iter() {
                v.trace(t);
            }
        });
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

    /// The address `println` shows, which the collector resolves.
    #[inline]
    pub fn addr(self) -> u64 {
        self.obj.map_or(0, |p| p.as_ptr() as usize as u64)
    }

    /// Where senders wait, which receivers wake.
    ///
    /// The two directions have separate addresses so that a receiver only ever
    /// wakes senders. Waking every waiter would make two `select`s that are
    /// both receiving wake each other for ever without either making progress.
    #[inline]
    pub fn send_key(self) -> usize {
        self.addr() as usize
    }

    /// Where receivers wait, which senders and `close` wake.
    ///
    /// One past the object, which no other key can be: every other address a
    /// goroutine parks on is an object's or a variable's own, and those are
    /// aligned to at least four bytes.
    #[inline]
    pub fn recv_key(self) -> usize {
        self.addr() as usize + 1
    }

    fn state(self) -> &'static Lock<State<T>> {
        let obj = self.obj.expect("nil channel");
        // Under GC torture, name a channel that was collected because
        // something failed to root it, rather than reading its poison.
        heap::check_live(obj.as_ptr() as usize);
        // SAFETY: the object lives as long as any reference to it is
        // reachable, and this channel is one such reference.
        unsafe { &(*obj.as_ptr()).state }
    }
}

/// What a send found when it looked.
enum Offer {
    /// In the buffer: the send is over.
    Sent,
    /// Left for a receiver that is already waiting. The send is not over until
    /// that receiver has taken it.
    Handed,
    /// No room and no receiver. The sender is on the queue.
    Blocked,
    /// Closed, which for a send is Go's panic.
    Closed,
}

/// What a sender waiting for its value to be collected found.
enum Handoff {
    /// Taken: the send is over.
    Taken,
    /// Closed under it, which is still the sender's panic — its send never
    /// completed.
    Closed,
    /// Still there. The sender is on the queue.
    Waiting,
}

/// What a receive found when it looked.
enum Got<T> {
    /// A value, which is now this goroutine's.
    Value(T),
    /// Closed and drained: the zero value and `false`.
    Drained,
    /// Nothing yet. The receiver is on the queue and counted.
    Blocked,
}

impl<T: GoValue + Trace> Chan<T> {
    /// `make(chan T, cap)`. A safe point: it allocates.
    pub fn make(cap: i64) -> Self {
        // A channel's own complaint, which is not a slice's: gc says
        // "makechan: size out of range".
        if cap < 0
            || (cap as u64)
                .checked_mul(size_of::<T>().max(1) as u64)
                .is_none_or(|bytes| bytes > 1 << 48)
        {
            crate::panic::runtime_error(crate::panic::RuntimeError::MakeChan { size: cap });
        }
        let cap = cap as usize;
        let obj = heap::allocate(ChanObj {
            state: Lock::new(State {
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
        self.state().with(|s| s.buf.len() as i64)
    }

    /// `cap(ch)`.
    pub fn cap(self) -> i64 {
        if self.is_nil() {
            return 0;
        }
        self.state().with(|s| s.cap as i64)
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
            sched::block_forever();
        }
        loop {
            match self.offer(v) {
                Offer::Sent => {
                    sched::wake_all(self.recv_key());
                    return;
                }
                Offer::Handed => {
                    sched::wake_all(self.recv_key());
                    return self.wait_taken();
                }
                Offer::Closed => send_on_closed(),
                Offer::Blocked => {
                    // Waking the receivers first is what lets one of them see
                    // that a sender is here and hand-shake with it, for a
                    // channel with no buffer.
                    sched::wake_all(self.recv_key());
                    sched::park();
                    sched::leave_park(self.send_key());
                }
            }
        }
    }

    /// One attempt at a send, deciding and registering under the lock.
    fn offer(self, v: T) -> Offer {
        self.state().with(|s| {
            if s.closed {
                return Offer::Closed;
            }
            if s.cap > 0 && s.buf.len() < s.cap {
                s.buf.push_back(v);
                return Offer::Sent;
            }
            if s.cap == 0 && s.receivers > 0 && s.buf.is_empty() {
                s.buf.push_back(v);
                sched::prepare_park(self.send_key());
                return Offer::Handed;
            }
            sched::prepare_park(self.send_key());
            Offer::Blocked
        })
    }

    /// Waits for a synchronous send's value to be collected, which is what
    /// makes the send and the receive happen together.
    ///
    /// The caller is already registered as a waiter — the offer that handed the
    /// value over did it under the channel's lock, which is what keeps the
    /// receiver's wake from arriving too early to be heard — and registers again
    /// on every look, because a wake of an address takes every waiter off it.
    /// What is left over is given back on the way out.
    fn wait_taken(self) {
        loop {
            let step = self.state().with(|s| {
                if s.buf.is_empty() {
                    return Handoff::Taken;
                }
                if s.closed {
                    return Handoff::Closed;
                }
                sched::prepare_park(self.send_key());
                Handoff::Waiting
            });
            match step {
                Handoff::Taken => break,
                Handoff::Closed => {
                    sched::leave_park(self.send_key());
                    send_on_closed();
                }
                Handoff::Waiting => sched::park(),
            }
        }
        sched::leave_park(self.send_key());
    }

    /// `v, ok := <-ch`. Blocks until a value arrives or the channel closes;
    /// `ok` is false once a closed channel has been drained.
    pub fn recv(self) -> (T, bool) {
        if self.is_nil() {
            sched::block_forever();
        }
        loop {
            let got = self.state().with(|s| {
                if let Some(v) = s.buf.pop_front() {
                    return Got::Value(v);
                }
                if s.closed {
                    return Got::Drained;
                }
                s.receivers += 1;
                sched::prepare_park(self.recv_key());
                Got::Blocked
            });
            match got {
                Got::Value(v) => {
                    sched::wake_all(self.send_key());
                    return (v, true);
                }
                Got::Drained => return (T::zero(), false),
                Got::Blocked => {
                    // Telling the senders there is a receiver is what lets a
                    // synchronous send go ahead.
                    sched::wake_all(self.send_key());
                    sched::park();
                    sched::leave_park(self.recv_key());
                    self.state()
                        .with(|s| s.receivers = s.receivers.saturating_sub(1));
                }
            }
        }
    }

    /// `<-ch`, where the program does not ask whether the channel is closed.
    pub fn recv_value(self) -> T {
        self.recv().0
    }

    /// Receives without blocking, for `select`: a value if the case was ready,
    /// and nothing if it was not.
    ///
    /// One look under the lock, rather than asking whether a receive would
    /// block and then receiving. Between those two another goroutine could take
    /// the value, and the receive would block — which a `select` case may not
    /// do, and a `select` with a `default` may not do at all.
    pub fn try_recv(self) -> Option<(T, bool)> {
        if self.is_nil() {
            return None;
        }
        let got = self.state().with(|s| {
            if let Some(v) = s.buf.pop_front() {
                return Some((v, true));
            }
            if s.closed {
                return Some((T::zero(), false));
            }
            None
        });
        if let Some((_, true)) = got {
            // Room for a sender now, or a synchronous one waiting to hear that
            // its value was collected.
            sched::wake_all(self.send_key());
        }
        got
    }

    /// Sends without blocking, for `select`, and says whether the case was
    /// taken.
    ///
    /// A synchronous send that finds a receiver waiting *is* taken, and then
    /// waits for its value to be collected: the case has been chosen by then,
    /// and what is left is Go's ordinary blocking send.
    pub fn try_send(self, v: T) -> bool {
        if self.is_nil() {
            return false;
        }
        match self.offer_once(v) {
            Offer::Sent => {
                sched::wake_all(self.recv_key());
                true
            }
            Offer::Handed => {
                sched::wake_all(self.recv_key());
                self.wait_taken();
                true
            }
            // A closed channel "can" send: the send panics, which is what Go
            // does when `select` picks that case.
            Offer::Closed => send_on_closed(),
            Offer::Blocked => false,
        }
    }

    /// One attempt at a send that will not wait, so it registers nothing.
    fn offer_once(self, v: T) -> Offer {
        self.state().with(|s| {
            if s.closed {
                return Offer::Closed;
            }
            if s.cap > 0 && s.buf.len() < s.cap {
                s.buf.push_back(v);
                return Offer::Sent;
            }
            if s.cap == 0 && s.receivers > 0 && s.buf.is_empty() {
                s.buf.push_back(v);
                sched::prepare_park(self.send_key());
                return Offer::Handed;
            }
            Offer::Blocked
        })
    }

    /// Counts the running goroutine as waiting to receive, and tells the
    /// senders so.
    ///
    /// A plain receive does this as it parks. A `select` has to do it too, or a
    /// synchronous send can never see it: the send waits for a receiver to hand
    /// its value to, and two `select`s on the same unbuffered channel would
    /// each wait for the other for ever.
    pub fn enter_recv(self) {
        if self.is_nil() {
            return;
        }
        self.state().with(|s| s.receivers += 1);
        sched::wake_all(self.send_key());
    }

    /// Stops counting the running goroutine as waiting to receive.
    pub fn leave_recv(self) {
        if self.is_nil() {
            return;
        }
        self.state()
            .with(|s| s.receivers = s.receivers.saturating_sub(1));
    }

    /// `close(ch)`.
    pub fn close(self) {
        if self.is_nil() {
            crate::panic::runtime_error_msg(alloc::string::String::from("close of nil channel"));
        }
        if self
            .state()
            .with(|s| core::mem::replace(&mut s.closed, true))
        {
            crate::panic::runtime_error_msg(alloc::string::String::from("close of closed channel"));
        }
        sched::wake_all(self.send_key());
        sched::wake_all(self.recv_key());
    }
}

/// Go's panic for a send on a closed channel, raised with no lock held.
fn send_on_closed() -> ! {
    crate::panic::runtime_error_msg(alloc::string::String::from("send on closed channel"))
}
