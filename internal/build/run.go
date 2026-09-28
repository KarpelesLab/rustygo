package build

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// MaxTestMemory caps the address space of a rustygo-built binary run by the
// test harnesses, so that a runaway allocation — a length read from freed
// memory, say — fails inside the child instead of asking the host for
// hundreds of gigabytes.
//
// It has to be generous, because a goroutine reserves its whole stack up
// front: 8 MiB of address space each, touched a page at a time, so a thousand
// goroutines account for 8 GiB of the limit without using any of it. Go's own
// `chanlinear` test starts a thousand and doubles from there.
const MaxTestMemory = 64 << 30

// TestCommand runs a rustygo-built binary under MaxTestMemory. The limit is
// applied through the shell's `ulimit -v`, so the harness itself is not
// constrained. It is best-effort: macOS does not enforce an address-space
// limit and refuses to set one, and Windows has no such shell, so there the
// binary runs unconstrained rather than not at all.
func TestCommand(ctx context.Context, bin string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, bin)
	}
	script := fmt.Sprintf(`ulimit -v %d 2>/dev/null; exec "$0"`, MaxTestMemory/1024)
	return exec.CommandContext(ctx, "/bin/sh", "-c", script, bin)
}
