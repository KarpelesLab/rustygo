package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// watchRSS samples the child's VmHWM (its peak RSS) every millisecond until
// stopped. rusage's ru_maxrss is useless here: on Linux a child inherits its
// parent's high-water mark across fork, so it would report this process's
// size. VmHWM belongs to the exec'd image alone.
func watchRSS(pid int) (stop func() int64) {
	var peak atomic.Int64
	done, finished := make(chan struct{}), make(chan struct{})
	path := fmt.Sprintf("/proc/%d/status", pid)
	go func() {
		defer close(finished)
		t := time.NewTicker(time.Millisecond)
		defer t.Stop()
		for {
			if kb := vmHWM(path); kb > peak.Load() {
				peak.Store(kb)
			}
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	return func() int64 {
		close(done)
		<-finished
		return peak.Load() * 1024
	}
}

func vmHWM(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "VmHWM:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return kb
		}
	}
	return 0
}

func rusagePeak(*os.ProcessState) int64 { return 0 }
