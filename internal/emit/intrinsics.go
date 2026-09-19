package emit

import "fmt"

// intrinsics give Rust bodies to functions that gc implements in assembly or
// inside its runtime, and that no Go code of rustygo's implements either.
// Keyed by "importpath.Name"; each renders the body from the argument
// expressions.
var intrinsics = map[string]func(args []string) string{
	// The collector.
	"runtime.GC": func([]string) string { return "rustygo::heap::collect()" },

	// Process-level runtime services.
	"runtime.fatal": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::fatal(%s.bytes())", a[0])
	},
	"runtime.nanotime": func([]string) string { return "rustygo::rt::nanotime()" },

	// sync's acquire/release loads, which gc links to its runtime atomics.
	// One goroutine (M1): plain accesses are atomic.
	"sync.runtime_LoadAcquintptr":  func(a []string) string { return a[0] + ".load()" },
	"sync.runtime_StoreReluintptr": func(a []string) string { return fmt.Sprintf("%s.store(%s)", a[0], a[1]) },

	// internal/reflectlite, answered from the runtime's type descriptors.
	"internal/reflectlite.typeOf": func(a []string) string {
		return fmt.Sprintf("UPtr::from_addr((%s).desc_addr())", a[0])
	},
	"internal/reflectlite.descComparable": func(a []string) string {
		return fmt.Sprintf("rustygo::iface::desc_comparable(%s)", a[0])
	},
	"internal/reflectlite.descName": func(a []string) string {
		return fmt.Sprintf("rustygo::iface::desc_name(%s)", a[0])
	},

	// internal/bytealg: gc's assembly, done with Rust's slice routines.
	"internal/bytealg.IndexByteString": func(a []string) string {
		return fmt.Sprintf("rustygo::intrinsics::index_byte(%s.bytes(), %s)", a[0], a[1])
	},
	"internal/bytealg.IndexByte": func(a []string) string {
		return fmt.Sprintf("(%s).with_bytes(|b| rustygo::intrinsics::index_byte(b, %s))", a[0], a[1])
	},
	"internal/bytealg.IndexString": func(a []string) string {
		return fmt.Sprintf("rustygo::intrinsics::index(%s.bytes(), %s.bytes())", a[0], a[1])
	},
	"internal/bytealg.Index": func(a []string) string {
		return fmt.Sprintf("(%s).with_bytes(|s| (%s).with_bytes(|sep| rustygo::intrinsics::index(s, sep)))", a[0], a[1])
	},
	"internal/bytealg.Compare": func(a []string) string {
		return fmt.Sprintf("(%s).with_bytes(|x| (%s).with_bytes(|y| rustygo::intrinsics::compare(x, y)))", a[0], a[1])
	},
	"internal/bytealg.Count": func(a []string) string {
		return fmt.Sprintf("(%s).with_bytes(|b| rustygo::intrinsics::count(b, %s))", a[0], a[1])
	},
	"internal/bytealg.CountString": func(a []string) string {
		return fmt.Sprintf("rustygo::intrinsics::count(%s.bytes(), %s)", a[0], a[1])
	},
	"internal/bytealg.MakeNoZero": func(a []string) string {
		return fmt.Sprintf("Slice::<Slot<u8>>::make(%s, %s)", a[0], a[0])
	},
}
