package main

import (
	"os"
	"syscall"
)

func watchRSS(int) func() int64 { return func() int64 { return 0 } }

// rusagePeak is ru_maxrss, in bytes on macOS.
func rusagePeak(ps *os.ProcessState) int64 {
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
		return ru.Maxrss
	}
	return 0
}
