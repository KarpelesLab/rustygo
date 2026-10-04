//go:build !unix

package main

import "os/exec"

// killGroup does nothing where there are no process groups to kill: the build
// gets os/exec's own cancellation, which reaches the child and not the cargo
// under it.
func killGroup(cmd *exec.Cmd) {}
