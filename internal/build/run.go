package build

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// MaxTestMemory caps the address space of a rustygo-built binary run by the
// test harnesses. Generated programs reserve little virtual memory, so the
// cap only bites on a runaway allocation — a length read from freed memory,
// say — which then fails inside the child instead of asking the host for
// hundreds of gigabytes.
const MaxTestMemory = 8 << 30

// TestCommand runs a rustygo-built binary under MaxTestMemory. The limit is
// applied through the shell's `ulimit -v`, so the harness itself is not
// constrained; where there is no such shell (Windows) the binary runs
// unconstrained.
func TestCommand(ctx context.Context, bin string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, bin)
	}
	script := fmt.Sprintf(`ulimit -v %d && exec "$0"`, MaxTestMemory/1024)
	return exec.CommandContext(ctx, "/bin/sh", "-c", script, bin)
}
