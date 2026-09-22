package emit

import "golang.org/x/tools/go/ssa"

// framesForRecover picks the functions that must link a shadow-stack frame
// even with nothing to root.
//
// `recover` returns a value only when it is called directly by a function a
// defer invoked (DESIGN §5), and the runtime tells the two apart by the chain
// of linked frames: the deferred call's frame sits directly on the one the
// defer machinery linked, and anything that call goes on to call has a frame
// of its own in between. That only works if no function on the way is
// missing from the chain — so every function that can reach a `recover`,
// through however many calls, links a frame.
//
// A frame costs a few instructions per call, so the rest do not link one: a
// program with no `recover` anywhere pays nothing at all, which is most of
// them. Calls through a func value or an interface could go anywhere, so a
// function making one counts as reaching a `recover` if the program has one.
func framesForRecover(fns []*ssa.Function) map[*ssa.Function]bool {
	type info struct {
		direct   []*ssa.Function
		indirect bool
	}
	calls := make(map[*ssa.Function]*info, len(fns))
	need := map[*ssa.Function]bool{}
	callers := map[*ssa.Function][]*ssa.Function{}
	anyRecover := false

	for _, fn := range fns {
		in := &info{}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				c, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				common := c.Common()
				if b, ok := common.Value.(*ssa.Builtin); ok {
					if b.Name() == "recover" {
						need[fn] = true
						anyRecover = true
					}
					continue
				}
				if callee := common.StaticCallee(); callee != nil {
					in.direct = append(in.direct, callee)
					continue
				}
				in.indirect = true
			}
		}
		calls[fn] = in
	}
	for fn, in := range calls {
		if anyRecover && in.indirect {
			need[fn] = true
		}
		for _, callee := range in.direct {
			callers[callee] = append(callers[callee], fn)
		}
	}
	if !anyRecover {
		return nil
	}
	// A caller of a function that needs a frame needs one too: it is on the
	// way from the deferred call down to the `recover`.
	work := make([]*ssa.Function, 0, len(need))
	for fn := range need {
		work = append(work, fn)
	}
	for len(work) > 0 {
		fn := work[len(work)-1]
		work = work[:len(work)-1]
		for _, caller := range callers[fn] {
			if !need[caller] {
				need[caller] = true
				work = append(work, caller)
			}
		}
	}
	return need
}
