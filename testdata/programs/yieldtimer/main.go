// A timer that has to fire while a goroutine is spinning rather than parked.
//
// `for { select { case <-c: ...; default: runtime.Gosched() } }` is a real Go
// idiom — `crypto/tls`'s own `TestWeakCertCache` is written that way — and it
// never parks, so a scheduler that only looks at the clock on the park path
// leaves the timer pending for ever. Under gc the first one fires after about
// a million turns of the loop.
package main

import (
	"runtime"
	"time"
)

// spin waits for a channel the way a test that must not block does.
func spin(c <-chan time.Time) bool {
	for i := 0; i < 500_000_000; i++ {
		select {
		case <-c:
			return true
		default:
			runtime.Gosched()
		}
	}
	return false
}

func main() {
	println("after", spin(time.After(time.Millisecond)))

	// A ticker, so that the second tick has to arrive while spinning too.
	t := time.NewTicker(time.Millisecond)
	defer t.Stop()
	println("tick", spin(t.C), spin(t.C))

	// And a timer that a goroutine is waiting on in the ordinary way, parked,
	// while another spins: both have to be served.
	done := make(chan bool)
	go func() {
		<-time.After(time.Millisecond)
		done <- true
	}()
	spun := spin(time.After(2 * time.Millisecond))
	println("both", spun, <-done)
}
