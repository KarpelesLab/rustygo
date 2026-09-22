// Copyright 2026 Karpelès Lab Inc. MIT license.

package runtime

import (
	"internal/runtime/exithook"
	"unsafe"
)

// What syscall and os ask of the runtime: the process's arguments and
// environment, and a few odds and ends gc implements in its runtime or in
// assembly. The values come from the Rust runtime, which has them from the
// process itself.

// entersyscall and exitsyscall are what syscall pulls in by name
// (`//go:linkname runtime_entersyscall runtime.entersyscall`). With one
// goroutine there is no scheduler slot to hand off; M2 makes them real.

func entersyscall() {}
func exitsyscall()  {}

// fcntl is named by internal/syscall/unix, and returns gc's (value, errno).
func fcntl(fd int32, cmd int32, arg int32) (int32, int32)

// args and envs are the process's, as the Rust runtime saw them.
func args() []string
func envs() []string

//go:linkname os_runtime_args os.runtime_args
func os_runtime_args() []string { return args() }

//go:linkname syscall_runtime_envs syscall.runtime_envs
func syscall_runtime_envs() []string { return envs() }

//go:linkname syscall_Getpagesize syscall.Getpagesize
func syscall_Getpagesize() int { return 4096 }

// os.Exit runs the exit hooks first, which is how a coverage or trace file
// gets written. Nothing in rustygo registers one yet, so this is the whole
// of it; gc also runs them when main returns, which rustygo's generated main
// does not do yet.

//go:linkname os_runtime_beforeExit os.runtime_beforeExit
func os_runtime_beforeExit(exitCode int) { exithook.Run(exitCode) }

// exit ends the process. syscall.Exit is gc's runtime too, not a system call
// the syscall package makes for itself.
func exit(code int32)

//go:linkname syscall_Exit syscall.Exit
func syscall_Exit(code int) { exit(int32(code)) }

// Signal handling waits for M3; nothing installs a handler, so there is
// nothing to ignore or restore.

//go:linkname os_ignoreSIGSYS os.ignoreSIGSYS
func os_ignoreSIGSYS() {}

//go:linkname os_restoreSIGSYS os.restoreSIGSYS
func os_restoreSIGSYS() {}

var _ unsafe.Pointer

// time pulls the monotonic clock from the runtime.

//go:linkname time_runtimeNano time.runtimeNano
func time_runtimeNano() int64 { return nanotime() }

//go:linkname time_runtimeNow time.runtimeNow
func time_runtimeNow() (sec int64, nsec int32, mono int64) {
	sec, nsec = walltime()
	return sec, nsec, nanotime()
}

//go:linkname time_runtimeIsBubbled time.runtimeIsBubbled
func time_runtimeIsBubbled() bool { return false } // no synctest bubbles

// walltime is the wall clock: seconds and nanoseconds since the epoch.
func walltime() (sec int64, nsec int32)

// nanosleep sleeps this thread, which with one goroutine is the whole
// program. time.NewTimer and the rest of the timer machinery wait for M2.
func nanosleep(ns int64)

//go:linkname time_Sleep time.Sleep
func time_Sleep(ns int64) { nanosleep(ns) }

// sigpipe is raised when a write to a closed pipe is ignored; with no signal
// handling yet (M3) there is nothing to raise.

//go:linkname os_sigpipe os.sigpipe
func os_sigpipe() {}
