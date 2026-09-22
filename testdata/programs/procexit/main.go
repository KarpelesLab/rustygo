// Sleeping, the wall clock, and os.Exit with a status: the pieces gc keeps in
// its runtime rather than in the syscall package.
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	start := time.Now()
	time.Sleep(20 * time.Millisecond)
	if d := time.Since(start); d < 20*time.Millisecond {
		fmt.Println("BUG: slept only", d)
		os.Exit(1)
	}
	if time.Now().Year() < 2020 {
		fmt.Println("BUG: wall clock reads", time.Now())
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "slept, and the clock is sane")
	os.Exit(3)
}
