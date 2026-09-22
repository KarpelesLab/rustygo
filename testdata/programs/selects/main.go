// select with a send case and a default, and a sleep that leaves the other
// goroutines running.
package main

import "time"

func main() {
	// A select with a send case, and one that blocks until a goroutine acts.
	c := make(chan int, 1)
	sent := 0
	for range 3 {
		select {
		case c <- sent:
			sent++
		default:
			<-c
		}
	}
	println("sent", sent, "len", len(c))

	// Sleeping must not stop the other goroutines from running.
	tick := make(chan int)
	go func() {
		for i := range 3 {
			tick <- i
		}
		close(tick)
	}()
	sum := 0
	for {
		select {
		case v, ok := <-tick:
			if !ok {
				println("sum", sum)
				start := time.Now()
				time.Sleep(10 * time.Millisecond)
				println("slept", time.Since(start) >= 10*time.Millisecond)
				return
			}
			sum += v
		}
	}
}
