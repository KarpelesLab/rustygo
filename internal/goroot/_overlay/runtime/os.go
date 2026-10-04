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

// mapClone is maps.Clone: a map of the same dynamic type with the same
// entries, which only the runtime can build because only it knows the type.
func mapClone(m any) any

//go:linkname maps_clone maps.clone
func maps_clone(m any) any { return mapClone(m) }

// nanosleep sleeps this thread, which with one goroutine is the whole
// program. time.NewTimer and the rest of the timer machinery wait for M2.
func nanosleep(ns int64)

// sleepUntil parks this goroutine until the monotonic clock reaches the given
// moment, leaving the others running.
func sleepUntil(deadline int64)

//go:linkname time_Sleep time.Sleep
func time_Sleep(ns int64) {
	if ns <= 0 {
		return
	}
	sleepUntil(nanotime() + ns)
}

// sigpipe is what os calls when a write to standard output or standard error
// failed because nothing is reading the other end. Go's answer is the default
// disposition — the process stops — and the runtime raises it.

func sigpipe()

//go:linkname os_sigpipe os.sigpipe
func os_sigpipe() { sigpipe() }

// Fork and exec.
//
// gc stops the world around a fork, because a forked child inherits only the
// thread that called fork and must not touch a lock another thread held. One
// goroutine on one thread has nothing to stop, and the child does nothing but
// the raw system calls `syscall.forkAndExecInChild` makes. So these are the
// hooks and none of them has anything to do.
//
// Everything else fork and exec need is Go in the `syscall` package, over the
// raw system calls rustygo already answers.

//go:linkname syscall_runtime_BeforeFork syscall.runtime_BeforeFork
func syscall_runtime_BeforeFork() {}

//go:linkname syscall_runtime_AfterFork syscall.runtime_AfterFork
func syscall_runtime_AfterFork() {}

//go:linkname syscall_runtime_AfterForkInChild syscall.runtime_AfterForkInChild
func syscall_runtime_AfterForkInChild() {}

//go:linkname syscall_runtime_BeforeExec syscall.runtime_BeforeExec
func syscall_runtime_BeforeExec() {}

//go:linkname syscall_runtime_AfterExec syscall.runtime_AfterExec
func syscall_runtime_AfterExec() {}

// The environment.
//
// `syscall` keeps the environment itself, in the Go slice and map it built from
// what the runtime handed it, and every Go read goes there. These two only
// exist so that the C environment a cgo program shares stays in step, and
// rustygo has no C environment to keep — a child process gets its environment
// from `os.Environ`, which is the Go side.

//go:linkname syscall_runtimeSetenv syscall.runtimeSetenv
func syscall_runtimeSetenv(k, v string) {}

//go:linkname syscall_runtimeUnsetenv syscall.runtimeUnsetenv
func syscall_runtimeUnsetenv(k string) {}

// Clearenv hands the runtime the map it is about to empty, so that a C
// environment could be cleared key by key. There is none to clear.

//go:linkname syscall_runtimeClearenv syscall.runtimeClearenv
func syscall_runtimeClearenv(env map[string]int) {}
