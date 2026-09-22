//! Goroutine stacks: a fixed reservation with a guard page below it.
//!
//! Go grows a stack by copying it and rewriting every pointer into it, which
//! needs to know where all those pointers are. Rust frames hold raw addresses
//! of locals that no compiler reports — generated code, this runtime, `std`
//! and any crate reached through interop all do it — so rustygo reserves
//! instead of growing (DESIGN §4). Only touched pages cost memory, so the
//! reservation can be generous, and the guard below it turns an overflow into
//! a fault at the boundary rather than a write into whatever lies beneath.

/// How much is reserved below every stack as a guard, and the granularity
/// stacks are rounded to.
///
/// 64 KiB rather than one page, because a page is 4 KiB on one machine and
/// 16 KiB on the next, and because a single Rust frame holding a large array
/// can step over a smaller guard before touching it. `rustc`'s stack probes
/// keep a frame from skipping the guard entirely.
pub const GUARD: usize = 64 * 1024;

/// One goroutine's stack.
pub struct Stack {
    /// The reservation, guard page included.
    base: *mut u8,
    /// Its length, guard page included.
    len: usize,
}

// The stack is a plain reservation; which thread frees it does not matter.
unsafe impl Send for Stack {}

impl Stack {
    /// Reserves a stack with room for at least `size` bytes, plus a guard.
    ///
    /// Returns `None` if the reservation fails, which a caller reports as
    /// Go's `out of memory` rather than panicking into an unwinding path
    /// that would need another stack.
    pub fn new(size: usize) -> Option<Stack> {
        let usable = size.next_multiple_of(GUARD).max(GUARD);
        let len = usable.checked_add(GUARD)?;
        let base = reserve(len)?;
        Some(Stack { base, len })
    }

    /// The top of the stack: the highest address, where a fresh context's
    /// registers are planted. Stacks grow down from here.
    pub fn top(&self) -> *mut u8 {
        // SAFETY: `len` bytes are mapped from `base`, so one past the end is
        // a valid address to form, and 16-byte alignment holds because the
        // length is a multiple of the guard size.
        unsafe { self.base.add(self.len) }
    }

    /// The lowest address a frame may touch: everything below is the guard.
    pub fn limit(&self) -> *mut u8 {
        // SAFETY: the guard is inside the reservation.
        unsafe { self.base.add(GUARD) }
    }

    /// The bytes a goroutine may actually use.
    pub fn size(&self) -> usize {
        self.len - GUARD
    }
}

rt_global! {
    static LEAKING: core::cell::Cell<bool> = core::cell::Cell::new(false);
}

/// Stops stacks from ever being unmapped again, for a process on its way out.
///
/// An exit runs on whichever stack called it, which may be a goroutine's. The
/// thread's destructors would then unmap the very stack the exit is running
/// on, and the process would fault instead of exiting. Nothing is lost by
/// keeping the reservations: the kernel takes them back.
pub fn leak_all() {
    LEAKING.with(|l| l.set(true));
}

impl Drop for Stack {
    fn drop(&mut self) {
        if LEAKING.with(|l| l.get()) {
            return;
        }
        // SAFETY: this reservation was made by `reserve` and is not used any
        // more: a `Stack` is dropped only once its goroutine has finished.
        unsafe { release(self.base, self.len) }
    }
}

#[cfg(unix)]
mod sys {
    use super::GUARD;
    use core::ffi::c_void;

    const PROT_NONE: i32 = 0;
    const PROT_READ: i32 = 1;
    const PROT_WRITE: i32 = 2;
    const MAP_PRIVATE: i32 = 0x0002;
    #[cfg(target_os = "linux")]
    const MAP_ANON: i32 = 0x0020;
    #[cfg(not(target_os = "linux"))]
    const MAP_ANON: i32 = 0x1000;
    const MAP_FAILED: *mut c_void = usize::MAX as *mut c_void;

    unsafe extern "C" {
        fn mmap(
            addr: *mut c_void,
            len: usize,
            prot: i32,
            flags: i32,
            fd: i32,
            offset: i64,
        ) -> *mut c_void;
        fn mprotect(addr: *mut c_void, len: usize, prot: i32) -> i32;
        fn munmap(addr: *mut c_void, len: usize) -> i32;
    }

    pub fn reserve(len: usize) -> Option<*mut u8> {
        // SAFETY: an anonymous private mapping at an address of the kernel's
        // choosing; nothing is read from it before it is written.
        let p = unsafe {
            mmap(
                core::ptr::null_mut(),
                len,
                PROT_READ | PROT_WRITE,
                MAP_PRIVATE | MAP_ANON,
                -1,
                0,
            )
        };
        if p == MAP_FAILED || p.is_null() {
            return None;
        }
        // SAFETY: the low `GUARD` bytes of the mapping just made.
        if unsafe { mprotect(p, GUARD, PROT_NONE) } != 0 {
            // SAFETY: undoing the mapping made above.
            unsafe { munmap(p, len) };
            return None;
        }
        Some(p as *mut u8)
    }

    /// # Safety
    ///
    /// `base` and `len` must come from a successful [`reserve`].
    pub unsafe fn release(base: *mut u8, len: usize) {
        // SAFETY: the caller's contract.
        unsafe { munmap(base as *mut c_void, len) };
    }
}

#[cfg(windows)]
mod sys {
    use super::GUARD;
    use core::ffi::c_void;

    const MEM_COMMIT: u32 = 0x1000;
    const MEM_RESERVE: u32 = 0x2000;
    const MEM_RELEASE: u32 = 0x8000;
    const PAGE_READWRITE: u32 = 0x04;
    const PAGE_NOACCESS: u32 = 0x01;

    unsafe extern "system" {
        fn VirtualAlloc(addr: *mut c_void, size: usize, kind: u32, protect: u32) -> *mut c_void;
        fn VirtualProtect(addr: *mut c_void, size: usize, new: u32, old: *mut u32) -> i32;
        fn VirtualFree(addr: *mut c_void, size: usize, kind: u32) -> i32;
    }

    pub fn reserve(len: usize) -> Option<*mut u8> {
        // SAFETY: a fresh reservation at an address of the system's choosing.
        let p = unsafe {
            VirtualAlloc(
                core::ptr::null_mut(),
                len,
                MEM_COMMIT | MEM_RESERVE,
                PAGE_READWRITE,
            )
        };
        if p.is_null() {
            return None;
        }
        let mut old = 0u32;
        // SAFETY: the low `GUARD` bytes of the region just reserved.
        if unsafe { VirtualProtect(p, GUARD, PAGE_NOACCESS, &mut old) } == 0 {
            // SAFETY: releasing the region made above, as MEM_RELEASE wants:
            // the base address and a size of zero.
            unsafe { VirtualFree(p, 0, MEM_RELEASE) };
            return None;
        }
        Some(p as *mut u8)
    }

    /// # Safety
    ///
    /// `base` must come from a successful [`reserve`].
    pub unsafe fn release(base: *mut u8, _len: usize) {
        // SAFETY: the caller's contract; MEM_RELEASE takes a size of zero.
        unsafe { VirtualFree(base as *mut c_void, 0, MEM_RELEASE) };
    }
}

#[cfg(not(any(unix, windows)))]
mod sys {
    use alloc::alloc::{Layout, alloc, dealloc};

    fn layout(len: usize) -> Layout {
        // 16 is every supported ABI's stack alignment.
        Layout::from_size_align(len, 16).expect("stack layout")
    }

    /// Reserves a stack from the allocator. **No guard page**: a target
    /// without `mmap` or `VirtualAlloc` has no way to ask for one, so an
    /// overflow corrupts whatever lies below instead of faulting.
    pub fn reserve(len: usize) -> Option<*mut u8> {
        // SAFETY: a non-zero layout, since `len` is at least one guard.
        let p = unsafe { alloc(layout(len)) };
        if p.is_null() { None } else { Some(p) }
    }

    /// # Safety
    ///
    /// `base` and `len` must come from a successful [`reserve`].
    pub unsafe fn release(base: *mut u8, len: usize) {
        // SAFETY: the caller's contract.
        unsafe { dealloc(base, layout(len)) };
    }
}

use sys::{release, reserve};

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reserves_a_usable_stack() {
        let s = Stack::new(128 * 1024).expect("stack");
        assert!(s.size() >= 128 * 1024);
        assert_eq!(s.top() as usize % 16, 0);
        assert_eq!(s.top() as usize - s.limit() as usize, s.size());
        // The top of the stack is writable, and the whole of it is: a
        // goroutine that goes deep must not fault before its limit.
        // SAFETY: inside the mapping, which nothing else is using.
        unsafe {
            s.top().sub(8).write(0x5a);
            assert_eq!(s.top().sub(8).read(), 0x5a);
            s.limit().write(0x1b);
            assert_eq!(s.limit().read(), 0x1b);
        }
    }

    #[test]
    fn rounds_up_to_the_guard_size() {
        let s = Stack::new(1).expect("stack");
        assert_eq!(s.size(), GUARD);
    }
}
