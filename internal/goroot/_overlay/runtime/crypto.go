// Copyright 2026 Karpelès Lab Inc. MIT license.

package runtime

// What the crypto packages ask of the runtime.
//
// The FIPS 140 machinery keeps two per-goroutine flags: one saying whether
// the service running now is an approved one, and one saying whether
// enforcement is switched off for the duration of a call. gc keeps them on
// the g; rustygo runs one goroutine at a time and switches only where it says
// so, so a package-level pair behaves identically — a flag set before a call
// and read inside it is the same flag. They move with the goroutine when M2's
// second half makes that distinction matter.

var (
	fipsIndicator uint8
	fipsBypass    bool
)

//go:linkname fips140_getIndicator crypto/internal/fips140.getIndicator
func fips140_getIndicator() uint8 { return fipsIndicator }

//go:linkname fips140_setIndicator crypto/internal/fips140.setIndicator
func fips140_setIndicator(v uint8) { fipsIndicator = v }

//go:linkname fips140_setBypass crypto/fips140.setBypass
func fips140_setBypass() { fipsBypass = true }

//go:linkname fips140_unsetBypass crypto/fips140.unsetBypass
func fips140_unsetBypass() { fipsBypass = false }

//go:linkname fips140_isBypassed crypto/fips140.isBypassed
func fips140_isBypassed() bool { return fipsBypass }

// The three fatal errors the crypto packages report through the runtime: a
// self-test that failed, or a system that cannot produce randomness. Neither
// is recoverable, and gc does not let them be.

//go:linkname fips140_fatal crypto/internal/fips140.fatal
func fips140_fatal(s string) { fatal(s) }

//go:linkname sysrand_fatal crypto/internal/sysrand.fatal
func sysrand_fatal(s string) { fatal(s) }

//go:linkname rand_fatal crypto/rand.fatal
func rand_fatal(s string) { fatal(s) }

// StandardCrypto is a marker: the linker looks for it to tell which crypto
// implementation was built in. Standard Go crypto is the only one rustygo
// has, so saying so is all there is to do.

//go:linkname sig_StandardCrypto crypto/internal/boring/sig.StandardCrypto
func sig_StandardCrypto() {}

// cmpstring is Go's three-way string comparison, which internal/bytealg
// reaches by name for the sort routines.
func cmpstring(a, b string) int

// vgetrandom is Linux's getrandom through the vDSO, which gc reaches with a
// hand-written call into a kernel-provided function. rustygo reports it
// unsupported, so crypto/internal/sysrand makes the ordinary system call
// instead — slower per call, and correct. internal/syscall/unix reaches this
// by name, so it needs no linkname of its own.
func vgetrandom(p []byte, flags uint32) (int, bool) { return 0, false }
