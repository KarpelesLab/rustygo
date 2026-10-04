//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// killGroup arranges for a timed-out build to take cargo and rustc with it.
// They are grandchildren, and os/exec's own cancellation signals the child
// alone, which would leave a rustc holding a core for the rest of the sweep.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
