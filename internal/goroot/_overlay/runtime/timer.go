// Copyright 2026 Karpelès Lab Inc. MIT license.

package runtime

import "unsafe"

// Timers, for package time.
//
// gc keeps a heap of timers per processor and checks it in the scheduler.
// rustygo keeps one list and one goroutine: the goroutine sleeps until the
// earliest deadline (parking, so the others keep running), fires what is due,
// and parks on a semaphore when there is nothing left to wait for — which is
// what lets a program whose only remaining goroutine is the timer loop still
// report Go's deadlock.
//
// The layout of timeTimer up to `init` is time.Timer's, because the time
// package casts what newTimer returns to *time.Timer and assigns its channel
// itself. Everything after that is this runtime's and invisible to it.
type timeTimer struct {
	c    unsafe.Pointer // time.Timer.C, which the time package fills in
	init bool           // time.Timer.initTimer

	when   int64 // when to fire, on the monotonic clock
	period int64 // repeat interval, or 0 for a one-shot
	f      func(arg any, seq uintptr, delay int64)
	arg    any
	seq    uintptr
	active bool
	queued bool // in the timers list, whether or not it is active
}

var (
	timers      []*timeTimer
	timerLoopOn bool
	timerSema   uint32
	// timerLock covers everything above. A timer may be created, stopped or
	// reset from any goroutine, and goroutines run on several threads, so the
	// list needs one owner at a time. The timer goroutine never holds it while
	// a timer fires: firing sends on a channel, which blocks.
	timerLock uint32 = 1
)

// yieldIfReady gives the processor to another goroutine if one is ready, and
// says whether it did. It is the runtime's (src/sched.rs).
func yieldIfReady() bool

func lockTimers()   { semacquire(&timerLock) }
func unlockTimers() { semrelease(&timerLock) }

// wakeTimerLoop tells the timer goroutine that the list has changed. Called
// with timerLock held.
func wakeTimerLoop() {
	if !timerLoopOn {
		timerLoopOn = true
		go timerLoop()
		return
	}
	atomicStore32(&timerSema, 1)
	semawake(uintptr(unsafe.Pointer(&timerSema)))
}

func timerLoop() {
	for {
		lockTimers()
		now := nanotime()
		next := int64(0)
		// What is due is collected here and called below, with the list
		// unlocked: a timer's function sends on a channel, which blocks, and
		// another goroutine may well want to add a timer meanwhile.
		var due []func()
		for _, t := range timers {
			if !t.active {
				continue
			}
			if t.when <= now {
				f, arg, seq := t.f, t.arg, t.seq
				// How late the firing is. time.sendTime subtracts it from the
				// current time to send the moment the tick was *due*, so that
				// a receiver that was slow to arrive still sees evenly spaced
				// ticks rather than the times this loop got round to them.
				delay := now - t.when
				if t.period > 0 {
					// Whole periods on from the time it was due, which is how
					// gc keeps a ticker's phase: adding the period to `now`
					// instead would let every late tick push the next one
					// later still.
					t.when += t.period * (1 + delay/t.period)
					if t.when < next || next == 0 {
						next = t.when
					}
				} else {
					t.active = false
				}
				due = append(due, func() { f(arg, seq, delay) })
				continue
			}
			if next == 0 || t.when < next {
				next = t.when
			}
		}
		// Drop the timers that will never fire again, and let a reset put one
		// back.
		live := timers[:0]
		for _, t := range timers {
			if t.active {
				live = append(live, t)
			} else {
				t.queued = false
			}
		}
		clear(timers[len(live):])
		timers = live
		unlockTimers()

		for _, fire := range due {
			fire()
		}
		if len(due) > 0 {
			// The list is read again from the top: firing ran other goroutines,
			// which may have changed it.
			continue
		}

		if next == 0 {
			// Nothing to wait for. Parking here, rather than sleeping, is
			// what makes a program with no other runnable goroutine a
			// deadlock rather than a wait.
			semacquire(&timerSema)
		} else {
			// A period short enough that the next firing is already due would
			// otherwise spin here for ever: sleepUntil returns at once when the
			// deadline has passed, and nothing else in this loop waits. A
			// one-nanosecond ticker is the extreme case, and time's own tests
			// make one — then wait to receive from it, which only happens if
			// this goroutine gives the receiver a turn.
			yieldIfReady()
			sleepUntil(next)
		}
	}
}

//go:linkname time_newTimer time.newTimer
func time_newTimer(when, period int64, f func(arg any, seq uintptr, delay int64), arg any, cp unsafe.Pointer) *timeTimer {
	lockTimers()
	defer unlockTimers()
	t := &timeTimer{
		when:   when,
		period: period,
		f:      f,
		arg:    arg,
		active: true,
		queued: true,
		init:   true,
	}
	timers = append(timers, t)
	wakeTimerLoop()
	return t
}

//go:linkname time_stopTimer time.stopTimer
func time_stopTimer(t *timeTimer) bool {
	lockTimers()
	defer unlockTimers()
	was := t.active
	t.active = false
	return was
}

//go:linkname time_resetTimer time.resetTimer
func time_resetTimer(t *timeTimer, when, period int64) bool {
	lockTimers()
	defer unlockTimers()
	was := t.active
	t.when = when
	t.period = period
	t.active = true
	// A timer the loop has already dropped has to go back in; one it has not
	// got round to dropping is still there, and appending it again would leave
	// two entries for one timer and grow the list for ever under a timer that
	// is reset in a loop.
	if !t.queued {
		t.queued = true
		timers = append(timers, t)
	}
	wakeTimerLoop()
	return was
}
