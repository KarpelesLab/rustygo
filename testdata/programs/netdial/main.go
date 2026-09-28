// Sockets: a listener, an accepted connection, a dial, and a timer — all of
// it Go's own net package over rustygo's netpoller, with the goroutines
// parked on the descriptors rather than spinning.
package main

import (
	"fmt"
	"io"
	"net"
	"time"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	defer ln.Close()

	done := make(chan string)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- "accept: " + err.Error()
			return
		}
		defer c.Close()
		// Echo one line back, upper-cased by hand so no other package is
		// involved in what is being compared.
		buf := make([]byte, 64)
		n, err := c.Read(buf)
		if err != nil && err != io.EOF {
			done <- "read: " + err.Error()
			return
		}
		got := buf[:n]
		out := make([]byte, 0, n)
		for _, b := range got {
			if b >= 'a' && b <= 'z' {
				b -= 32
			}
			out = append(out, b)
		}
		if _, err := c.Write(out); err != nil {
			done <- "write: " + err.Error()
			return
		}
		done <- ""
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	if _, err := c.Write([]byte("hello sockets")); err != nil {
		fmt.Println("client write:", err)
		return
	}
	reply := make([]byte, 64)
	n, err := c.Read(reply)
	if err != nil {
		fmt.Println("client read:", err)
		return
	}
	fmt.Printf("echo: %s\n", reply[:n])
	c.Close()
	if msg := <-done; msg != "" {
		fmt.Println("server:", msg)
		return
	}

	// A timer fires on its own goroutine while this one waits for it.
	start := time.Now()
	<-time.After(15 * time.Millisecond)
	fmt.Println("timer fired after at least 15ms:", time.Since(start) >= 15*time.Millisecond)

	// And a ticker, stopped after a few ticks.
	tick := time.NewTicker(5 * time.Millisecond)
	ticks := 0
	for range tick.C {
		ticks++
		if ticks == 3 {
			tick.Stop()
			break
		}
	}
	fmt.Println("ticks:", ticks)
}
