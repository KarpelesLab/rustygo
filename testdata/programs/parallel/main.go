// Goroutines on more than one thread.
//
// Everything here is written so that its output cannot depend on which
// goroutine got there first: counts are summed, results are collected and
// sorted, and nothing is printed from a goroutine. What the program checks is
// that the answers are the ones a single thread would have given — that a mutex
// really excludes, that a wait group really waits, that a channel hands each
// value to exactly one receiver, and that allocation from several threads at
// once loses nothing.
package main

import (
	"fmt"
	"runtime"
	"sync"
)

const workers = 64
const each = 500

func main() {
	// The program runs at whatever GOMAXPROCS the environment asked for rather
	// than choosing a number itself, so that the same binary is the
	// single-worker case and, with GOMAXPROCS=2 in front of it, the
	// many-worker one. Setting the limit to what it already is must report that
	// same number back; the number itself is never printed, because gc's
	// default is the machine's CPU count and rustygo's is one.
	procs := runtime.GOMAXPROCS(0)
	if procs < 1 {
		fmt.Println("GOMAXPROCS(0) =", procs, "want at least 1")
	}
	if n := runtime.GOMAXPROCS(procs); n != procs {
		fmt.Println("GOMAXPROCS(procs) =", n, "want", procs)
	}
	if runtime.NumCPU() < 1 {
		fmt.Println("NumCPU =", runtime.NumCPU())
	}

	fmt.Println("mutex", countedByEveryone())
	fmt.Println("channel", squaresThroughOneChannel())
	fmt.Println("allocated", builtOnEveryThread())
	fmt.Println("once", calledOnce())
	fmt.Println("cond", signalledUntilDone())
	fmt.Println("locked", pinnedToTheirThreads())
	fmt.Println("selected", chosenAmongCases())

	if n := runtime.GOMAXPROCS(0); n != procs {
		fmt.Println("GOMAXPROCS drifted to", n, "from", procs)
	}
	fmt.Println("goroutines", runtime.NumGoroutine() > 0)
}

// A mutex and a wait group over real threads. The total is exact only if every
// increment had the lock to itself.
func countedByEveryone() int {
	var mu sync.Mutex
	var wg sync.WaitGroup
	total := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				mu.Lock()
				total++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return total
}

// Many senders, one receiver, on a channel with no buffer: every value arrives
// exactly once, whatever order they come in.
func squaresThroughOneChannel() int {
	ch := make(chan int)
	for i := 0; i < workers; i++ {
		go func(i int) { ch <- i * i }(i)
	}
	got := make([]int, 0, workers)
	for i := 0; i < workers; i++ {
		got = append(got, <-ch)
	}
	sorted(got)
	sum := 0
	for i, v := range got {
		if v != i*i {
			fmt.Println("square", i, "arrived as", v)
		}
		sum += v
	}
	return sum
}

// Allocation from several threads at once, which is what the heap's lock is
// for. Each goroutine builds a slice of strings and reports its length, so a
// value lost to a collection on another thread would show up as a wrong total
// or as a panic.
func builtOnEveryThread() int {
	lens := make(chan int, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			s := []string{}
			for j := 0; j <= i; j++ {
				s = append(s, fmt.Sprint(j))
			}
			joined := ""
			for _, v := range s {
				joined += v
			}
			lens <- len(s) + len(joined) - len(joined)
		}(i)
	}
	sum := 0
	for i := 0; i < workers; i++ {
		sum += <-lens
	}
	return sum
}

// sync.Once from many goroutines: the body runs once.
func calledOnce() int {
	var once sync.Once
	var mu sync.Mutex
	var wg sync.WaitGroup
	calls := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			once.Do(func() {
				mu.Lock()
				calls++
				mu.Unlock()
			})
		}()
	}
	wg.Wait()
	return calls
}

// sync.Cond, whose ticket counters are the runtime's: Signal may be called from
// a goroutine that does not hold the lock the waiter does.
func signalledUntilDone() int {
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	done := 0
	for i := 0; i < 16; i++ {
		go func() {
			mu.Lock()
			done++
			mu.Unlock()
			cond.Signal()
		}()
	}
	mu.Lock()
	for done < 16 {
		cond.Wait()
	}
	n := done
	mu.Unlock()
	return n
}

// LockOSThread: a goroutine that asks to stay where it is still runs, and still
// takes its turn with the others.
func pinnedToTheirThreads() int {
	var mu sync.Mutex
	var wg sync.WaitGroup
	n := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			runtime.Gosched()
			mu.Lock()
			n++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return n
}

// A select over two channels fed from two goroutines, drained until both are
// closed. Which case is taken when both are ready is random, so only the total
// is checked.
func chosenAmongCases() int {
	a := make(chan int, 4)
	b := make(chan int, 4)
	go func() {
		for i := 1; i <= 50; i++ {
			a <- i
		}
		close(a)
	}()
	go func() {
		for i := 1; i <= 50; i++ {
			b <- 100 * i
		}
		close(b)
	}()
	sum, open := 0, 2
	for open > 0 {
		select {
		case v, ok := <-a:
			if !ok {
				a = nil
				open--
				continue
			}
			sum += v
		case v, ok := <-b:
			if !ok {
				b = nil
				open--
				continue
			}
			sum += v
		}
	}
	return sum
}

// An insertion sort, so that the program depends on nothing but the runtime.
func sorted(xs []int) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}
