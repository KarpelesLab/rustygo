package emit

import (
	"golang.org/x/tools/go/ssa"
)

// Dead initialization: a package initializer builds every package-level
// variable, but a program uses few of the standard library's. `unicode`
// alone initializes a table per script and category — 130,000 lines of Rust
// if all of them are emitted — while strings.ToUpper reads three. gc keeps
// such tables as static data; rustygo instead skips building the ones
// nothing reads.
//
// The emitter runs twice. The first run records every global that some
// emitted function other than a package initializer refers to (liveGlobals).
// The second emits each initializer without the instructions that only feed
// variables outside that set. It is conservative: whatever the first run
// reached is kept, and calls are always kept, so no initializer's side
// effects are lost.

// isPackageInit reports go/ssa's synthesized package initializer.
func isPackageInit(fn *ssa.Function) bool {
	return fn.Synthetic == "package initializer"
}

// deadInit returns the instructions of a package initializer that only build
// globals no other code reads.
func deadInit(fn *ssa.Function, liveGlobals map[*ssa.Global]bool) map[ssa.Instruction]bool {
	live := map[ssa.Instruction]bool{}
	var work []ssa.Instruction
	mark := func(instr ssa.Instruction) {
		if !live[instr] {
			live[instr] = true
			work = append(work, instr)
		}
	}
	// objectLive reports whether a store through addr can be observed: its
	// base object is a live global, or a live value of this function.
	var objectLive func(v ssa.Value) bool
	objectLive = func(v ssa.Value) bool {
		switch v := v.(type) {
		case *ssa.Global:
			return liveGlobals[v]
		case *ssa.FieldAddr:
			return objectLive(v.X)
		case *ssa.IndexAddr:
			return objectLive(v.X)
		case ssa.Instruction:
			return live[v]
		}
		return true // a parameter, or something else not ours to judge
	}
	var stores []ssa.Instruction
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch instr.(type) {
			case *ssa.Store, *ssa.MapUpdate:
				stores = append(stores, instr)
			case *ssa.Call:
				mark(instr) // a call may have effects
			case ssa.Value:
				// Any other value is live only if something live uses it.
			default:
				mark(instr) // control flow, panics, defers, sends
			}
		}
	}
	for {
		for len(work) > 0 {
			instr := work[len(work)-1]
			work = work[:len(work)-1]
			var ops []*ssa.Value
			for _, op := range instr.Operands(ops) {
				if def, ok := (*op).(ssa.Instruction); ok && *op != nil {
					mark(def)
				}
			}
		}
		progress := false
		for _, s := range stores {
			if live[s] {
				continue
			}
			var target ssa.Value
			switch s := s.(type) {
			case *ssa.Store:
				target = s.Addr
			case *ssa.MapUpdate:
				target = s.Map
			}
			if objectLive(target) {
				mark(s)
				progress = true
			}
		}
		if !progress && len(work) == 0 {
			break
		}
	}
	dead := map[ssa.Instruction]bool{}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if !live[instr] {
				dead[instr] = true
			}
		}
	}
	return dead
}
