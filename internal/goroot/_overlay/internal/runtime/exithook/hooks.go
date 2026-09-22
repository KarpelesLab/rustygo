// Copyright 2026 Karpelès Lab Inc. MIT license.

// Package exithook provides limited support for on-exit cleanup.
//
// rustygo's replacement. gc's version guards the hook list with
// internal/runtime/atomic and reaches back into the scheduler through
// Gosched, Goid and Throw, which its runtime assigns at startup. rustygo
// runs one goroutine (roadmap M2), so the list needs no lock and no hook can
// be reached from a second goroutine; the three callback variables stay
// declared so that a runtime which does set them keeps compiling (DESIGN §8).
package exithook

// A Hook is a function to be run at program termination (when someone
// invokes os.Exit, or when main.main returns). Hooks are run in reverse
// order of registration: the first hook added is the last one run.
type Hook struct {
	F            func() // func to run
	RunOnFailure bool   // whether to run on non-zero exit code
}

var (
	hooks []Hook

	// Set by the runtime under gc; rustygo's runtime leaves them nil. With
	// one goroutine there is nothing to yield to and no other goroutine to
	// name, and a hook that panics panics out through Run.
	Gosched func()
	Goid    func() uint64
	Throw   func(string)
)

// Add adds a new exit hook.
func Add(h Hook) {
	hooks = append(hooks, h)
}

// Run runs the exit hooks.
//
// A hook that panics takes the program down with it, as it does under gc,
// though as a Go panic rather than a runtime throw: rustygo has no throw to
// call yet.
func Run(code int) {
	for len(hooks) > 0 {
		h := hooks[len(hooks)-1]
		hooks = hooks[:len(hooks)-1]
		if code != 0 && !h.RunOnFailure {
			continue
		}
		h.F()
	}
}
