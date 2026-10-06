// A pointer that reaches the kernel as a `uintptr`, which is the one place the
// collector cannot see a reference at all: after `uintptr(unsafe.Pointer(p))`
// the only thing left is an integer, and nothing refers to the bytes the kernel
// is about to read. Go's unsafe.Pointer rule 4 says the compiler keeps them
// alive for the duration of the call.
//
// Each round allocates a fresh path and throws away enough to collect, so a
// pointer the collector cannot see is a path the kernel reads out of freed
// memory — which came out, before this was rooted, as `/dev/null` reporting
// that it did not exist.
//
// It then caught a second bug, which is why the last line is what it is: a
// program that never names `os.Stdout` had its standard descriptors closed by
// their own finalizers, because dead-initialization elimination dropped the
// stores into those variables while keeping the calls that made the files.
package main

import (
	"os"
	"syscall"
	"unsafe"
)

// atFDCWD is -100, which is how `openat` is told to resolve a relative path
// against the working directory. Spelled as the complement so that it is a
// uintptr on every size of word.
const atFDCWD = ^uintptr(99)

//go:noinline
func openat(path string) (int, syscall.Errno) {
	b := append([]byte(path), 0)
	fd, _, errno := syscall.Syscall6(syscall.SYS_OPENAT, atFDCWD,
		uintptr(unsafe.Pointer(&b[0])), uintptr(syscall.O_RDONLY), 0, 0, 0)
	return int(fd), errno
}

//go:noinline
func churn(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		s := make([]byte, 64)
		s[0] = byte(i)
		total += int(s[0])
	}
	return total
}

func main() {
	opened, failed := 0, 0
	for i := 0; i < 200; i++ {
		churn(8)
		fd, errno := openat("/dev/null")
		if errno != 0 {
			failed++
			continue
		}
		opened++
		syscall.Close(fd)
	}
	println("raw", opened, failed)

	// The same thing through the standard library, which spells the conversion
	// the same way inside `syscall.Open`.
	opened, failed = 0, 0
	for i := 0; i < 200; i++ {
		churn(8)
		f, err := os.Open("/dev/null")
		if err != nil {
			failed++
			continue
		}
		opened++
		f.Close()
	}
	println("os", opened, failed)

	// A name that is certainly not there, so that the error path is the error
	// path and not a freed buffer pretending to be one.
	if _, errno := openat("/definitely/not/here"); errno != syscall.ENOENT {
		println("unexpected errno", int(errno))
	} else {
		println("missing file reports ENOENT")
	}

	// And the standard descriptors are still open, which this line arriving is
	// the proof of.
	//
	// `os.Stdin`, `os.Stdout` and `os.Stderr` are made by `os`'s initializer
	// and each carries a finalizer that closes its descriptor. Naming them
	// anywhere above would hide what this program caught: dead-initialization
	// elimination dropped the stores into those three variables, because
	// nothing here reads them, while keeping the calls that made the files —
	// so all three became garbage, and the first collection closed fds 2, 1
	// and 0. Everything after that wrote into EBADF and the program exited 0.
	println("standard descriptors still open")
}
