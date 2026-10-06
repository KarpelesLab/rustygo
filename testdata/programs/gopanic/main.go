// Program gopanic raises a panic in a goroutine of its own.
//
// `go panic(v)` is a `go` statement whose callee is the builtin, which nothing
// can recover: `x/sync/singleflight` does it so that a waiter on a channel
// cannot be left behind for ever.
package main

// `go panic(v)` raises a panic in a goroutine of its own, which nothing can
// recover: x/sync/singleflight does it so that a waiter cannot be left behind.
func main() {
	done := make(chan bool)
	go func() {
		defer func() { done <- true }()
		println("before")
	}()
	<-done
	go panic("from a goroutine of its own")
	select {}
}
