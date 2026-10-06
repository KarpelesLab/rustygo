package emit

import "fmt"

// intrinsics give Rust bodies to functions that gc implements in assembly or
// inside its runtime, and that no Go code of rustygo's implements either.
// Keyed by "importpath.Name"; each renders the body from the argument
// expressions.
var intrinsics = map[string]func(args []string) string{
	// The collector.
	"runtime.GC": func([]string) string { return "rustygo::heap::collect()" },
	"runtime.heapStats": func([]string) string {
		return "{ let s = rustygo::heap::stats(); (s.objects as u64, s.bytes as u64, s.total_objects, s.total_bytes, s.collections as u64) }"
	},

	// Process-level runtime services.
	"runtime.fatal": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::fatal(%s.bytes())", a[0])
	},
	"runtime.nanotime": func([]string) string { return "rustygo::rt::nanotime()" },
	"runtime.cmpstring": func(a []string) string {
		return fmt.Sprintf("rustygo::intrinsics::compare(%s.bytes(), %s.bytes())", a[0], a[1])
	},
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
	"reflect.descNumMethod": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::num_method(%s)", a[0])
	},
	"reflect.descMethodName": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::method_name(%s, %s)", a[0], a[1])
	},
	"reflect.descMethodPkgPath": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::method_pkg_path(%s, %s)", a[0], a[1])
	},
	"reflect.descMethodExprType": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::method_expr_type(%s, %s)", a[0], a[1])
	},
	"reflect.descMethodExprFunc": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::method_expr_func(%s, %s)", a[0], a[1])
	},
	"reflect.descMethodValueType": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::method_value_type(%s, %s)", a[0], a[1])
	},
	"reflect.descMethodValueFunc": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::method_value_func(%s, %s, %s)", a[0], a[1], a[2])
	},
	"reflect.descNumIn": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::num_in(%s)", a[0])
	},
	"reflect.descIn": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::type_in(%s, %s)", a[0], a[1])
	},
	"reflect.descNumOut": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::num_out(%s)", a[0])
	},
	"reflect.descOut": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::type_out(%s, %s)", a[0], a[1])
	},
	"reflect.chanRecv": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::chan_recv(%s, %s, %s)", a[0], a[1], a[2])
	},
	"reflect.chanSend": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::chan_send(%s, %s, %s, %s)", a[0], a[1], a[2], a[3])
	},
	"reflect.chanLen": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::chan_len(%s, %s)", a[0], a[1])
	},
	"reflect.chanCap": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::chan_cap(%s, %s)", a[0], a[1])
	},
	"reflect.chanClose": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::chan_close(%s, %s)", a[0], a[1])
	},
	"reflect.descChanDir": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::chan_dir(%s)", a[0])
	},
	"reflect.descVariadic": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::is_variadic(%s)", a[0])
	},
	"reflect.descEqual": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::equal(%s, %s, %s)", a[0], a[1], a[2])
	},
	"reflect.descCall": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::call(%s, %s, %s)", a[0], a[1], a[2])
	},
	"reflect.descImplements": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::implements(%s, %s)", a[0], a[1])
	},
	"reflect.descZero": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::zero_value(%s)", a[0])
	},
	"reflect.descMakeSlice": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::make_slice(%s, %s, %s)", a[0], a[1], a[2])
	},
	"reflect.descMakeFunc": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::make_func(%s, %s)", a[0], a[1])
	},
	"reflect.registerMakeFunc": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::set_make_func(%s)", a[0])
	},
	"reflect.descPtrTo": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::pointer_to(%s)", a[0])
	},
	"reflect.descNew": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::new_value(%s)", a[0])
	},
	"reflect.boxPointer": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::box_pointer(%s)", a[0])
	},
	"reflect.mapMake": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::map_make(%s)", a[0])
	},
	"reflect.mapSet": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::map_set(%s, %s, %s, %s)", a[0], a[1], a[2], a[3])
	},
	"reflect.mapDelete": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::map_delete(%s, %s, %s)", a[0], a[1], a[2])
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
	"runtime.mapClone": func(a []string) string {
		return fmt.Sprintf("rustygo::reflect::map_clone(%s)", a[0])
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
	"runtime.goexit":       func([]string) string { return "rustygo::sched::goexit()" },
	"runtime.NumGoroutine": func([]string) string { return "rustygo::sched::count()" },
	"runtime.yieldIfReady": func([]string) string { return "rustygo::sched::yield_now()" },
	"runtime.sleepUntil": func(a []string) string {
		return fmt.Sprintf("rustygo::sched::sleep_until(%s)", a[0])
	},
	"runtime.semapark": func(a []string) string {
		return fmt.Sprintf("rustygo::sched::park_on(%s as usize)", a[0])
	},
	"runtime.semawake": func(a []string) string {
		return fmt.Sprintf("rustygo::sched::wake_one(%s as usize)", a[0])
	},
	"runtime.semaparkall": func(a []string) string {
		return fmt.Sprintf("rustygo::sched::park_on(%s as usize)", a[0])
	},
	"runtime.semawakeall": func(a []string) string {
		return fmt.Sprintf("rustygo::sched::wake_all(%s as usize)", a[0])
	},

	// The netpoller (src/netpoll.rs).
	"runtime.pollOpen": func(a []string) string {
		return fmt.Sprintf("{ let (c, e) = rustygo::netpoll::open(%s as i32); (c as u64, e as i64) }", a[0])
	},
	"runtime.pollClose": func(a []string) string {
		return fmt.Sprintf("rustygo::netpoll::close(%s as usize)", a[0])
	},
	"runtime.pollReset": func(a []string) string {
		return fmt.Sprintf("rustygo::netpoll::reset(%s as usize, %s as i32) as i64", a[0], a[1])
	},
	"runtime.pollWait": func(a []string) string {
		return fmt.Sprintf("rustygo::netpoll::wait(%s as usize, %s as i32) as i64", a[0], a[1])
	},
	"runtime.pollSetDeadline": func(a []string) string {
		return fmt.Sprintf("rustygo::netpoll::set_deadline(%s as usize, %s, %s as i32)", a[0], a[1], a[2])
	},
	"runtime.pollUnblock": func(a []string) string {
		return fmt.Sprintf("rustygo::netpoll::unblock(%s as usize)", a[0])
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
	"runtime.sigpipe":  func([]string) string { return "rustygo::rt::sigpipe()" },
	// Where a fatal report goes besides standard error (debug.SetCrashOutput).
	"runtime.crashFD": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::set_crash_fd(%s as usize) as u64", a[0])
	},

	// Signals: the handler and the queue are the runtime's, and os/signal
	// reaches them through the overlay's runtime (src/rt.rs).
	"runtime.signalEnable": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::signal_enable(%s)", a[0])
	},
	"runtime.signalDisable": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::signal_disable(%s)", a[0])
	},
	"runtime.signalIgnore": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::signal_ignore(%s)", a[0])
	},
	"runtime.signalIgnored": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::signal_ignored(%s)", a[0])
	},
	"runtime.signalRecv": func([]string) string { return "rustygo::rt::signal_recv()" },
	"runtime.signalWaitIdle": func([]string) string {
		return "rustygo::rt::signal_wait_until_idle()"
	},
	"runtime.fcntl": func(a []string) string {
		return fmt.Sprintf("rustygo::rt::fcntl(%s, %s, %s)", a[0], a[1], a[2])
	},

	// The one raw system call the standard library funnels through. `Syscall6`
	// may block and `RawSyscall6` may not, which is the difference between
	// them: a goroutine making the first one can be parked (src/syscall.rs).
	"internal/runtime/syscall/linux.Syscall6": func(a []string) string {
		return fmt.Sprintf("rustygo::syscall::syscall6_go(%s, %s, %s, %s, %s, %s, %s)",
			a[0], a[1], a[2], a[3], a[4], a[5], a[6])
	},
	// The clone that starts a child process, which gc writes in assembly
	// because of the flags it asks for (src/syscall.rs).
	"syscall.rawVforkSyscall": func(a []string) string {
		return fmt.Sprintf("rustygo::syscall::vfork(%s, %s, %s, %s)", a[0], a[1], a[2], a[3])
	},
	// The same call, made on every OS thread. There is one, so it is the same
	// call made once (internal/goroot/_overlay/runtime/os.go).
	"runtime.doAllThreadsSyscall": func(a []string) string {
		return fmt.Sprintf("rustygo::syscall::syscall6_go(%s, %s, %s, %s, %s, %s, %s)",
			a[0], a[1], a[2], a[3], a[4], a[5], a[6])
	},
	"syscall.rawSyscallNoError": func(a []string) string {
		return fmt.Sprintf("{ let (r1, r2, _) = rustygo::syscall::syscall6(%s, %s, %s, %s, 0, 0, 0); (r1, r2) }",
			a[0], a[1], a[2], a[3])
	},

	// The clock `syscall` reads directly, which gc writes in assembly so that
	// it can read the kernel's vDSO page without entering the kernel. The
	// ordinary system call gives the same answer, and lets the kernel fill the
	// `Timeval` with its own layout (src/syscall.rs).
	"syscall.gettimeofday": func(a []string) string {
		return fmt.Sprintf("rustygo::syscall::gettimeofday(UPtr::from_ptr(%s).addr())", a[0])
	},
	// The bridge into libc, which exists in `syscall` because the call graph
	// reaches it and is never taken: every caller guards it on a
	// `cgo_libc_*` pointer that only cgo sets, and rustygo has no cgo (M4).
	// `Setegid` and the rest take their `AllThreadsSyscall` path instead.
	"syscall.cgocaller": func([]string) string {
		return `rustygo::panic::runtime_error_msg(alloc::string::String::from(` +
			`"syscall: cgocaller without cgo"))`
	},

	// `golang.org/x/sys/unix` is how Go programs outside the standard library
	// reach the kernel, and on linux/amd64 its six entry points are assembly
	// that jumps straight into `syscall`'s. They are the same calls with the
	// same convention — a positive errno, zero for success — so they come here
	// too. Nothing else in the package is assembly.
	//
	// It is not an obscure dependency: `sirupsen/logrus` asks it whether the
	// terminal is a terminal, and `spf13/viper` watches a directory with it.
	"golang.org/x/sys/unix.Syscall": func(a []string) string {
		return fmt.Sprintf("rustygo::syscall::syscall6_go(%s, %s, %s, %s, 0, 0, 0)",
			a[0], a[1], a[2], a[3])
	},
	"golang.org/x/sys/unix.Syscall6": func(a []string) string {
		return fmt.Sprintf("rustygo::syscall::syscall6_go(%s, %s, %s, %s, %s, %s, %s)",
			a[0], a[1], a[2], a[3], a[4], a[5], a[6])
	},
	// The raw forms may not be preempted, which only matters to a scheduler
	// that preempts; here the difference is that they never park.
	"golang.org/x/sys/unix.RawSyscall": func(a []string) string {
		return fmt.Sprintf("rustygo::syscall::syscall6(%s, %s, %s, %s, 0, 0, 0)",
			a[0], a[1], a[2], a[3])
	},
	"golang.org/x/sys/unix.RawSyscall6": func(a []string) string {
		return fmt.Sprintf("rustygo::syscall::syscall6(%s, %s, %s, %s, %s, %s, %s)",
			a[0], a[1], a[2], a[3], a[4], a[5], a[6])
	},
	"golang.org/x/sys/unix.SyscallNoError": func(a []string) string {
		return fmt.Sprintf("{ let (r1, r2, _) = rustygo::syscall::syscall6_go(%s, %s, %s, %s, 0, 0, 0); (r1, r2) }",
			a[0], a[1], a[2], a[3])
	},
	"golang.org/x/sys/unix.RawSyscallNoError": func(a []string) string {
		return fmt.Sprintf("{ let (r1, r2, _) = rustygo::syscall::syscall6(%s, %s, %s, %s, 0, 0, 0); (r1, r2) }",
			a[0], a[1], a[2], a[3])
	},

	// A func value's code address, which gc's compiler knows and rustygo does
	// not: nothing is at address zero, and the profiler that asks is not
	// sampling anything anyway.
	"internal/abi.FuncPCABIInternal": func([]string) string { return "0u64" },
	"internal/abi.FuncPCABI0":        func([]string) string { return "0u64" },

	// internal/cpu reads the processor's feature bits in assembly. Reporting
	// none is the honest answer and the safe one: every package that asks takes
	// its portable path.
	"internal/cpu.cpuid":           func([]string) string { return "(0u32, 0u32, 0u32, 0u32)" },
	"internal/cpu.xgetbv":          func([]string) string { return "(0u32, 0u32)" },
	"internal/cpu.getGOAMD64level": func([]string) string { return "1i32" },

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
