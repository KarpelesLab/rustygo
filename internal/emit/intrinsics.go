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
	"runtime.writeErr": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::write_err(%s, %s as i64)", a[0], a[1])
	},

	// sync's acquire/release loads, which gc links to its runtime atomics.
	// One goroutine (M1): plain accesses are atomic.
	"sync.runtime_LoadAcquintptr":  func(a []string) string { return a[0] + ".load()" },
	"sync.runtime_StoreReluintptr": func(a []string) string { return fmt.Sprintf("%s.store(%s)", a[0], a[1]) },

	// reflect, answered from the runtime's type descriptors (src/reflect.rs).
	"reflect.ifaceType": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::type_of(%s)", a[0])
	},
	"reflect.ifaceData": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::data_of(%s)", a[0])
	},
	"reflect.makeIface": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::make_iface(%s, %s)", a[0], a[1])
	},
	"reflect.descKind": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::kind(%s)", a[0])
	},
	"reflect.descSize": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::size(%s)", a[0])
	},
	"reflect.descAlign": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::align(%s)", a[0])
	},
	"reflect.descName": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::name(%s)", a[0])
	},
	"reflect.descString": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::type_string(%s)", a[0])
	},
	"reflect.descPkgPath": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::pkg_path(%s)", a[0])
	},
	"reflect.descElem": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::elem(%s)", a[0])
	},
	"reflect.descKey": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::key(%s)", a[0])
	},
	"reflect.descLen": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::len(%s)", a[0])
	},
	"reflect.descComparable": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::comparable(%s)", a[0])
	},
	"reflect.descNumField": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::num_field(%s)", a[0])
	},
	"reflect.descFieldName": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::field_name(%s, %s as i64)", a[0], a[1])
	},
	"reflect.descFieldPkgPath": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::field_pkg_path(%s, %s as i64)", a[0], a[1])
	},
	"reflect.descFieldType": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::field_type(%s, %s as i64)", a[0], a[1])
	},
	"reflect.descFieldOffset": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::field_offset(%s, %s as i64)", a[0], a[1])
	},
	"reflect.descFieldTag": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::field_tag(%s, %s as i64)", a[0], a[1])
	},
	"reflect.descFieldEmbedded": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::field_embedded(%s, %s as i64)", a[0], a[1])
	},
	"reflect.descBox": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::box_value(%s, %s)", a[0], a[1])
	},
	"reflect.mapLen": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::map_len(%s, %s)", a[0], a[1])
	},
	"reflect.mapIter": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::map_iter(%s, %s)", a[0], a[1])
	},
	"reflect.mapNext": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::map_next(%s, %s)", a[0], a[1])
	},
	"reflect.mapIndex": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::map_index(%s, %s, %s)", a[0], a[1], a[2])
	},

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

	// Goroutines: the scheduler (src/sched.rs).
	"runtime.Gosched":      func([]string) string { return "rustygo::sched::gosched()" },
	"runtime.NumGoroutine": func([]string) string { return "rustygo::sched::count()" },
	"runtime.yieldIfReady": func([]string) string { return "rustygo::sched::yield_now()" },
	"runtime.semapark": func(a []string) string {
		return fmt.Sprintf("rustygo::sched::park_on(%s as usize)", a[0])
	},
	"runtime.semawake": func(a []string) string {
		return fmt.Sprintf("rustygo::sched::wake_one(%s as usize)", a[0])
	},

	// The process itself: arguments, environment, and the fcntl gc puts in
	// its runtime.
	"runtime.args": func([]string) string { return "rustygo::rt::args()" },
	"runtime.nanosleep": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::nanosleep(%s)", a[0])
	},
	"runtime.exit": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::exit(%s)", a[0])
	},
	"runtime.walltime": func([]string) string { return "rustygo::rt::walltime()" },
	"runtime.envs":     func([]string) string { return "rustygo::rt::envs()" },
	"runtime.fcntl": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::fcntl(%s, %s, %s)", a[0], a[1], a[2])
	},

	// The one raw system call the standard library funnels through.
	"internal/runtime/syscall/linux.Syscall6": func(a []string) string {
		return fmt.Sprintf("rustygo::syscall::syscall6(%s, %s, %s, %s, %s, %s, %s)",
			a[0], a[1], a[2], a[3], a[4], a[5], a[6])
	},
	// The clone/vfork probe: rustygo starts no processes yet, so it reports
	// ENOSYS and Go takes its fallback path.
	"syscall.rawVforkSyscall": func([]string) string {
		return "(u64::MAX, 38u64)"
	},
	"syscall.rawSyscallNoError": func(a []string) string {
		return fmt.Sprintf("{ let (r1, r2, _) = rustygo::syscall::syscall6(%s, %s, %s, %s, 0, 0, 0); (r1, r2) }",
			a[0], a[1], a[2], a[3])
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
