// Goroutines, and the sync primitives that park on the scheduler.
package main

import (
	"runtime"
	"sync"
)

func main() {
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 1; i <= 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mu.Lock()
			total += n
			mu.Unlock()
			runtime.Gosched()
		}(i)
	}
	wg.Wait()
	println("total", total)
	println("goroutines", runtime.NumGoroutine())
}
