//! Raw system calls.
//!
//! The standard library's whole `syscall` package funnels into one function,
//! `internal/runtime/syscall/linux.Syscall6`, which gc writes in assembly.
//! rustygo provides it here, so `syscall`, `os` and everything above them is
//! ordinary Go compiled like any other package (DESIGN §8).
//!
//! The contract is gc's: on success `(r1, r2, 0)`, where `r2` is the second
//! return register; on failure `(-1, 0, errno)` with a positive errno.

/// `Syscall6(num, a1..a6) -> (r1, r2, errno)`.
#[cfg(all(target_os = "linux", target_arch = "x86_64"))]
pub fn syscall6(num: u64, a1: u64, a2: u64, a3: u64, a4: u64, a5: u64, a6: u64) -> (u64, u64, u64) {
    let ret: i64;
    let r2: u64;
    // SAFETY: a system call with the kernel's own calling convention: the
    // number in rax, arguments in rdi/rsi/rdx/r10/r8/r9, and rcx and r11
    // clobbered. Whether the arguments are meaningful is the Go caller's
    // responsibility, exactly as under gc.
    unsafe {
        core::arch::asm!(
            "syscall",
            inlateout("rax") num as i64 => ret,
            in("rdi") a1,
            in("rsi") a2,
            inlateout("rdx") a3 => r2,
            in("r10") a4,
            in("r8") a5,
            in("r9") a6,
            lateout("rcx") _,
            lateout("r11") _,
            options(nostack, preserves_flags)
        );
    }
    split(ret, r2)
}

/// `Syscall6(num, a1..a6) -> (r1, r2, errno)`.
#[cfg(all(target_os = "linux", target_arch = "aarch64"))]
pub fn syscall6(num: u64, a1: u64, a2: u64, a3: u64, a4: u64, a5: u64, a6: u64) -> (u64, u64, u64) {
    let ret: i64;
    let r2: u64;
    // SAFETY: as above, with the aarch64 convention: the number in x8 and
    // arguments in x0..x5.
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") num,
            inlateout("x0") a1 => ret,
            inlateout("x1") a2 => r2,
            in("x2") a3,
            in("x3") a4,
            in("x4") a5,
            in("x5") a6,
            options(nostack, preserves_flags)
        );
    }
    split(ret, r2)
}

/// The system call numbers this module treats specially, which it only does
/// where there are goroutines to yield to.
#[cfg(all(target_os = "linux", target_arch = "x86_64", feature = "std"))]
mod nr {
    pub const WAIT4: u64 = 61;
    pub const WAITID: u64 = 247;
}
#[cfg(all(target_os = "linux", target_arch = "aarch64", feature = "std"))]
mod nr {
    pub const WAIT4: u64 = 260;
    pub const WAITID: u64 = 95;
}

/// How many system calls one goroutine may make before the others get a turn.
///
/// gc takes the processor away from a goroutine that will not give it up; this
/// compiler emits no preemption checks, so a goroutine yields only where it
/// waits for something. A loop that does nothing but make system calls — a
/// test that opens and closes a file as fast as it can while another goroutine
/// tries to close the directory under it is one of os's own — then never lets
/// anything else run at all. A system call is the one place such a loop is
/// certain to pass through, so it is where the turn is taken. The count is high
/// enough that ordinary I/O does not pay for it and low enough that nothing
/// waits long.
#[cfg(all(target_os = "linux", feature = "std"))]
const CALLS_PER_TURN: u32 = 128;

#[cfg(all(target_os = "linux", feature = "std"))]
static CALLS: core::sync::atomic::AtomicU32 = core::sync::atomic::AtomicU32::new(0);

/// Gives the others a turn, every so often.
///
/// This must happen *after* the call and never before or during it. A pointer
/// argument reaches here as a `uintptr`, and the Go frame that made it has no
/// further use for the pointer it came from, so nothing roots the object any
/// more: `openat`'s path is a byte slice whose only reference died in the
/// conversion. Letting another goroutine run while the kernel still holds that
/// address means the collector may take the object first, and the kernel then
/// reads whatever replaced it — `open` of a file that is certainly there coming
/// back ENOENT is what that looks like. (gc keeps such a pointer alive for the
/// duration of the call, which is a rule this compiler's liveness does not
/// model yet.) Once the call has returned the kernel is done with the
/// addresses, and anything the Go code still needs is rooted by the use it is
/// about to make of it.
#[cfg(all(target_os = "linux", feature = "std"))]
fn take_turn() {
    use core::sync::atomic::Ordering;
    if CALLS.fetch_add(1, Ordering::Relaxed) >= CALLS_PER_TURN {
        CALLS.store(0, Ordering::Relaxed);
        crate::sched::yield_now();
    }
}

/// `Syscall6`, with the one call that must not block outright.
///
/// Every goroutine shares one thread (roadmap M2), so a call that blocks the
/// thread blocks all of them. `wait4` is where that bites: `os/exec` waits for
/// a child while another goroutine is still feeding that child's standard
/// input, and a child reading its input will not exit. So a wait that would
/// block is asked not to, and tried again after letting everything else have a
/// turn — including the netpoller, which is what the goroutine feeding the
/// child is waiting on. gc gives the wait a thread of its own instead.
#[cfg(all(target_os = "linux", feature = "std"))]
pub fn syscall6_go(
    num: u64,
    a1: u64,
    a2: u64,
    a3: u64,
    a4: u64,
    a5: u64,
    a6: u64,
) -> (u64, u64, u64) {
    const WNOHANG: u64 = 1;
    match num {
        // `wait4(pid, status, options, rusage)`: a pid of zero back means no
        // child has changed state yet.
        nr::WAIT4 if a3 & WNOHANG == 0 => loop {
            let r = syscall6(num, a1, a2, a3 | WNOHANG, a4, a5, a6);
            if r.0 != 0 || r.2 != 0 {
                return r;
            }
            poll_again();
        },
        // `waitid(which, id, infop, options, rusage)`, which is how `os` waits
        // for a process it will collect later. It answers in the `siginfo` it
        // was given rather than in its return value, and leaves it alone when
        // nothing is waitable, so it is cleared before each try.
        nr::WAITID if a4 & WNOHANG == 0 && a3 != 0 => loop {
            // SAFETY: the caller's own buffer, which it passed for the kernel
            // to fill; Go's is 128 bytes and this writes the first 32.
            unsafe { core::ptr::write_bytes(a3 as *mut u8, 0, 32) };
            let r = syscall6(num, a1, a2, a3, a4 | WNOHANG, a5, a6);
            // SAFETY: as above; `si_pid` is the fourth word of a `siginfo`.
            let si_pid = unsafe { core::ptr::read_unaligned((a3 as *const u32).add(4)) };
            if r.2 != 0 || si_pid != 0 {
                return r;
            }
            poll_again();
        },
        _ => {
            let r = syscall6(num, a1, a2, a3, a4, a5, a6);
            take_turn();
            r
        }
    }
}

/// Lets everything else have a turn before asking the kernel again.
///
/// A short wait rather than a long one: there is no descriptor to park on for
/// a child's exit, so this is the one place rustygo polls. It is also not a
/// busy wait — [`crate::sched::sleep_until`] runs whatever is runnable first,
/// and only sleeps the thread when nothing is.
#[cfg(all(target_os = "linux", feature = "std"))]
fn poll_again() {
    crate::sched::sleep_until(crate::rt::nanotime() + 1_000_000);
}

/// Without the scheduler there is nothing to yield to.
#[cfg(not(all(target_os = "linux", feature = "std")))]
pub fn syscall6_go(
    num: u64,
    a1: u64,
    a2: u64,
    a3: u64,
    a4: u64,
    a5: u64,
    a6: u64,
) -> (u64, u64, u64) {
    syscall6(num, a1, a2, a3, a4, a5, a6)
}

/// Splits a kernel return value into gc's `(r1, r2, errno)`.
#[cfg(target_os = "linux")]
fn split(ret: i64, r2: u64) -> (u64, u64, u64) {
    // The kernel returns -errno for errno in 1..4095.
    if (-4095..0).contains(&ret) {
        (u64::MAX, 0, (-ret) as u64)
    } else {
        (ret as u64, r2, 0)
    }
}

/// On a platform whose syscalls rustygo does not make yet, every call fails
/// with ENOSYS rather than doing something unpredictable.
#[cfg(not(target_os = "linux"))]
pub fn syscall6(
    _num: u64,
    _a1: u64,
    _a2: u64,
    _a3: u64,
    _a4: u64,
    _a5: u64,
    _a6: u64,
) -> (u64, u64, u64) {
    const ENOSYS: u64 = 38;
    (u64::MAX, 0, ENOSYS)
}

/// The clone that starts a child process, which gc writes in assembly
/// (`rawVforkSyscall`).
///
/// gc asks the kernel for `CLONE_VM|CLONE_VFORK`: the child shares the
/// parent's memory and the parent is suspended until the child execs or exits.
/// That is fast, and it requires that the child touch nothing the parent will
/// look at again — a discipline gc's compiler keeps by holding the results in
/// registers and returning from the frame at once. It is not a discipline this
/// compiler can promise: the generated child would go on writing shadow-stack
/// frames into memory the parent is still using.
///
/// So the two flags come off and this is an ordinary fork. The child gets its
/// own copy of everything and can do as it likes, which is what Go itself did
/// before it changed to vfork, and what the child does next is raw system calls
/// and `execve` either way.
///
/// `clone3` is not answered: Go only reaches for it to put the child in a
/// cgroup or a new time namespace, and reports the failure to its caller.
#[cfg(target_os = "linux")]
pub fn vfork(num: u64, a1: u64, a2: u64, a3: u64) -> (u64, u64) {
    #[cfg(target_arch = "x86_64")]
    const SYS_CLONE: u64 = 56;
    #[cfg(target_arch = "aarch64")]
    const SYS_CLONE: u64 = 220;
    const CLONE_VM: u64 = 0x100;
    const CLONE_VFORK: u64 = 0x4000;
    const ENOSYS: u64 = 38;
    if num != SYS_CLONE {
        return (u64::MAX, ENOSYS);
    }
    let (r1, _, errno) = syscall6(num, a1 & !(CLONE_VM | CLONE_VFORK), a2, a3, 0, 0, 0);
    (r1, errno)
}

/// Starting a process needs system calls this platform does not make yet.
#[cfg(not(target_os = "linux"))]
pub fn vfork(_num: u64, _a1: u64, _a2: u64, _a3: u64) -> (u64, u64) {
    const ENOSYS: u64 = 38;
    (u64::MAX, ENOSYS)
}
