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
