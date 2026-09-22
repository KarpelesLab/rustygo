//! The context switch a goroutine rides on (DESIGN §4).
//!
//! Every Go function may block, so rustygo does not colour functions `async`:
//! a goroutine is a real stack, and blocking swaps stacks. Only the
//! callee-saved registers and the stack pointer are saved, because that is
//! all the C ABI promises across a call — the switch *is* a call as far as
//! the compiler knows, so it may keep nothing else live.

#[cfg(any(
    all(target_arch = "x86_64", not(windows)),
    all(target_arch = "aarch64", not(windows))
))]
use core::arch::naked_asm;

/// A suspended execution: the stack pointer its registers were saved at.
///
/// A zero `sp` is a context nothing has been saved into yet.
#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct Context {
    sp: usize,
}

impl Default for Context {
    fn default() -> Self {
        Self::empty()
    }
}

impl Context {
    /// A context to switch *out of* for the first time: the running stack
    /// saves itself into one of these.
    pub const fn empty() -> Context {
        Context { sp: 0 }
    }

    /// The stack pointer this context was saved at, for the collector.
    pub fn sp(&self) -> usize {
        self.sp
    }
}

/// Saves the running execution into `from` and resumes `to`.
///
/// # Safety
///
/// `to` must hold either a context saved by an earlier `switch`, or one
/// prepared by [`Context::prepare`] over a stack that is still alive. The
/// stack `to` runs on must stay mapped until it is switched away from for
/// the last time.
#[inline]
pub unsafe fn switch(from: *mut Context, to: *const Context) {
    // SAFETY: the caller's contract; `switch_asm` saves and restores exactly
    // the registers the C ABI makes callee-saved.
    unsafe { switch_asm(from, to) }
}

#[cfg(all(target_arch = "x86_64", not(windows)))]
#[unsafe(naked)]
unsafe extern "C" fn switch_asm(from: *mut Context, to: *const Context) {
    // rdi = from, rsi = to. The return address is already on the stack, so
    // restoring a stack and returning lands in whatever called `switch` on
    // it — or, for a fresh stack, in `start`.
    naked_asm!(
        "push rbp",
        "push rbx",
        "push r12",
        "push r13",
        "push r14",
        "push r15",
        "mov [rdi], rsp",
        "mov rsp, [rsi]",
        "pop r15",
        "pop r14",
        "pop r13",
        "pop r12",
        "pop rbx",
        "pop rbp",
        "ret",
    )
}

/// Where a fresh x86-64 stack's saved registers sit, in words below the
/// prepared stack pointer: the order `switch_asm` pops them in, then the
/// address it returns to.
#[cfg(all(target_arch = "x86_64", not(windows)))]
mod slots {
    pub const R13: usize = 2; // the function to run
    pub const R12: usize = 3; // its argument
    pub const RETURN: usize = 6; // `start`
    pub const COUNT: usize = 7;
}

#[cfg(all(target_arch = "x86_64", not(windows)))]
#[unsafe(naked)]
unsafe extern "C" fn start() -> ! {
    // Entered by the `ret` in `switch_asm`, with the function in r13 and its
    // argument in r12, both planted by `prepare`. The stack is fresh, so
    // forcing the ABI's alignment costs nothing and assumes nothing.
    naked_asm!(
        "and rsp, -16",
        "mov rdi, r12",
        "call r13",
        // A goroutine's entry never returns: it switches away for the last
        // time instead.
        "ud2",
    )
}

#[cfg(all(target_arch = "aarch64", not(windows)))]
#[unsafe(naked)]
unsafe extern "C" fn switch_asm(from: *mut Context, to: *const Context) {
    // x0 = from, x1 = to. AAPCS64 makes x19-x28, x29, x30 and the low halves
    // of v8-v15 callee-saved.
    naked_asm!(
        "stp x29, x30, [sp, #-160]!",
        "stp x19, x20, [sp, #16]",
        "stp x21, x22, [sp, #32]",
        "stp x23, x24, [sp, #48]",
        "stp x25, x26, [sp, #64]",
        "stp x27, x28, [sp, #80]",
        "stp d8,  d9,  [sp, #96]",
        "stp d10, d11, [sp, #112]",
        "stp d12, d13, [sp, #128]",
        "stp d14, d15, [sp, #144]",
        "mov x9, sp",
        "str x9, [x0]",
        "ldr x9, [x1]",
        "mov sp, x9",
        "ldp x19, x20, [sp, #16]",
        "ldp x21, x22, [sp, #32]",
        "ldp x23, x24, [sp, #48]",
        "ldp x25, x26, [sp, #64]",
        "ldp x27, x28, [sp, #80]",
        "ldp d8,  d9,  [sp, #96]",
        "ldp d10, d11, [sp, #112]",
        "ldp d12, d13, [sp, #128]",
        "ldp d14, d15, [sp, #144]",
        "ldp x29, x30, [sp], #160",
        "ret",
    )
}

/// Where a fresh aarch64 stack's saved registers sit, in words below the
/// prepared stack pointer: the frame `switch_asm` loads from.
#[cfg(all(target_arch = "aarch64", not(windows)))]
mod slots {
    pub const RETURN: usize = 1; // x30, the link register: `start`
    pub const R13: usize = 2; // x19, the function to run
    pub const R12: usize = 3; // x20, its argument
    pub const COUNT: usize = 20; // 160 bytes
}

#[cfg(all(target_arch = "aarch64", not(windows)))]
#[unsafe(naked)]
unsafe extern "C" fn start() -> ! {
    // Entered by the `ret` in `switch_asm`, which returns to x30.
    naked_asm!("mov x0, x20", "blr x19", "brk #1",)
}

/// Goroutines on a platform whose calling convention has no switch here yet.
///
/// Windows x64 makes `rdi`, `rsi` and `xmm6`-`xmm15` callee-saved and keeps
/// the stack's bounds in the thread information block, so switching stacks
/// there means saving all of that and updating the TIB — a different switch,
/// written and tested with the rest of the Windows port (roadmap M5). Until
/// then the program says so rather than corrupting itself quietly.
#[cfg(not(any(
    all(target_arch = "x86_64", not(windows)),
    all(target_arch = "aarch64", not(windows))
)))]
unsafe extern "C" fn switch_asm(_from: *mut Context, _to: *const Context) {
    unsupported()
}

#[cfg(not(any(
    all(target_arch = "x86_64", not(windows)),
    all(target_arch = "aarch64", not(windows))
)))]
mod slots {
    pub const R13: usize = 0;
    pub const R12: usize = 1;
    pub const RETURN: usize = 2;
    pub const COUNT: usize = 3;
}

#[cfg(not(any(
    all(target_arch = "x86_64", not(windows)),
    all(target_arch = "aarch64", not(windows))
)))]
unsafe extern "C" fn start() -> ! {
    unsupported()
}

#[cfg(not(any(
    all(target_arch = "x86_64", not(windows)),
    all(target_arch = "aarch64", not(windows))
)))]
fn unsupported() -> ! {
    #[cfg(feature = "std")]
    crate::rt::fatal(b"goroutines are not supported on this platform yet (roadmap M5)");
    #[cfg(not(feature = "std"))]
    panic!("goroutines are not supported on this platform yet");
}

impl Context {
    /// Prepares a context that, when switched to, runs `entry(arg)` on the
    /// stack ending at `stack_top`.
    ///
    /// `entry` must never return: a goroutine leaves by switching away.
    ///
    /// # Safety
    ///
    /// `stack_top` must be the top (highest address) of a writable stack of
    /// at least a few pages, aligned to 16 bytes, and that memory must stay
    /// mapped for as long as this context can be switched to.
    pub unsafe fn prepare(
        stack_top: *mut u8,
        entry: unsafe extern "C" fn(*mut u8) -> !,
        arg: *mut u8,
    ) -> Context {
        // SAFETY: the caller guarantees the stack; the words written are the
        // topmost `COUNT` of it, which is where `switch_asm` will read the
        // saved registers from.
        unsafe {
            let sp = (stack_top as *mut usize).sub(slots::COUNT);
            for i in 0..slots::COUNT {
                sp.add(i).write(0);
            }
            sp.add(slots::RETURN).write(start as *const () as usize);
            sp.add(slots::R13).write(entry as *const () as usize);
            sp.add(slots::R12).write(arg as usize);
            Context { sp: sp as usize }
        }
    }
}

#[cfg(all(
    test,
    feature = "std",
    any(target_arch = "x86_64", target_arch = "aarch64")
))]
mod tests {
    use super::*;
    use crate::stack::Stack;

    // Where the test and the switched-to stack meet: the context to go back
    // to, and what the other stack did.
    static mut BACK: Context = Context::empty();
    static mut HERE: Context = Context::empty();
    static STEPS: core::sync::atomic::AtomicU64 = core::sync::atomic::AtomicU64::new(0);

    fn steps() -> u64 {
        STEPS.load(core::sync::atomic::Ordering::Relaxed)
    }

    fn set_steps(v: u64) {
        STEPS.store(v, core::sync::atomic::Ordering::Relaxed);
    }

    unsafe extern "C" fn counts(arg: *mut u8) -> ! {
        // Runs on its own stack. Deep enough to touch pages beyond the first.
        let n = arg as usize as u64;
        for i in 0..n {
            set_steps(steps() + i + 1);
            // SAFETY: single-threaded test; the other stack is parked.
            unsafe { switch(&raw mut HERE, &raw const BACK) };
        }
        set_steps(u64::MAX);
        // SAFETY: as above. The last switch never comes back.
        unsafe { switch(&raw mut HERE, &raw const BACK) };
        unreachable!("switched back into a finished context")
    }

    #[test]
    fn switches_back_and_forth() {
        let stack = Stack::new(64 * 1024).expect("stack");
        // SAFETY: the stack outlives every switch below, and `counts` never
        // returns.
        unsafe {
            HERE = Context::prepare(stack.top(), counts, 3 as *mut u8);
            for want in [1, 3, 6] {
                switch(&raw mut BACK, &raw const HERE);
                assert_eq!(steps(), want);
            }
            switch(&raw mut BACK, &raw const HERE);
            assert_eq!(steps(), u64::MAX);
        }
    }
}
