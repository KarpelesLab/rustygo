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
}

var (
	timers      []*timeTimer
	timerLoopOn bool
	timerSema   uint32
)

// wakeTimerLoop tells the timer goroutine that the list has changed.
func wakeTimerLoop() {
	if !timerLoopOn {
		timerLoopOn = true
		go timerLoop()
		return
	}
	timerSema = 1
	semawake(uintptr(unsafe.Pointer(&timerSema)))
}

func timerLoop() {
	for {
		now := nanotime()
		next := int64(0)
		for _, t := range timers {
			if !t.active {
				continue
			}
			if t.when <= now {
				f, arg, seq := t.f, t.arg, t.seq
				if t.period > 0 {
					t.when = now + t.period
					if t.when < next || next == 0 {
						next = t.when
					}
				} else {
					t.active = false
				}
				// A timer's function sends on a channel, which may run other
				// goroutines, so the list is read again from the top after it.
				f(arg, seq, 0)
				now = nanotime()
				continue
			}
			if next == 0 || t.when < next {
				next = t.when
			}
		}
		// Drop the timers that will never fire again.
		live := timers[:0]
		for _, t := range timers {
			if t.active {
				live = append(live, t)
			}
		}
		clear(timers[len(live):])
		timers = live

		if next == 0 {
			// Nothing to wait for. Parking here, rather than sleeping, is
			// what makes a program with no other runnable goroutine a
			// deadlock rather than a wait.
			semacquire(&timerSema)
		} else {
			sleepUntil(next)
		}
	}
}

//go:linkname time_newTimer time.newTimer
func time_newTimer(when, period int64, f func(arg any, seq uintptr, delay int64), arg any, cp unsafe.Pointer) *timeTimer {
	t := &timeTimer{
		when:   when,
		period: period,
		f:      f,
		arg:    arg,
		active: true,
		init:   true,
	}
	timers = append(timers, t)
	wakeTimerLoop()
	return t
}

//go:linkname time_stopTimer time.stopTimer
func time_stopTimer(t *timeTimer) bool {
	was := t.active
	t.active = false
	return was
}

//go:linkname time_resetTimer time.resetTimer
func time_resetTimer(t *timeTimer, when, period int64) bool {
	was := t.active
	t.when = when
	t.period = period
	t.active = true
	if !was {
		timers = append(timers, t)
	}
	wakeTimerLoop()
	return was
}
