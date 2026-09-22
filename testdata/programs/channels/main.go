// Channels: a rendezvous, a buffer, close and range, select with and
// without a default, and a pipeline of goroutines.
package main

import "fmt"

func main() {
	// Unbuffered: a rendezvous.
	done := make(chan string)
	go func() { done <- "hello" }()
	fmt.Println(<-done)

	// Buffered, then drained by range after close.
	nums := make(chan int, 3)
	go func() {
		for i := 1; i <= 5; i++ {
			nums <- i * i
		}
		close(nums)
	}()
	sum := 0
	for n := range nums {
		sum += n
	}
	fmt.Println("sum", sum)

	// Receive from a closed channel.
	v, ok := <-nums
	fmt.Println("drained", v, ok)

	// select, with and without a default.
	a, b := make(chan int, 1), make(chan int, 1)
	a <- 1
	select {
	case x := <-a:
		fmt.Println("from a", x)
	case x := <-b:
		fmt.Println("from b", x)
	}
	select {
	case x := <-b:
		fmt.Println("unexpected", x)
	default:
		fmt.Println("nothing ready")
	}

	// A pipeline of goroutines.
	in := make(chan int)
	out := make(chan int)
	go func() {
		for v := range in {
			out <- v * 2
		}
		close(out)
	}()
	go func() {
		for i := 0; i < 4; i++ {
			in <- i
		}
		close(in)
	}()
	total := 0
	for v := range out {
		total += v
	}
	fmt.Println("total", total, "len", len(nums), "cap", cap(a))
}
