package emit

import "fmt"

// fallbacks redirect a function gc writes in assembly to the generic Go
// version its own package already carries, keyed by "importpath.Name"
// (DESIGN §8).
//
// These packages are written to have both: a portable implementation, and an
// assembly one selected by the build tags for a real GOARCH. rustygo compiles
// the package for that same GOARCH, so it sees the assembly declaration —
// and the Go body sitting right next to it, which is what it calls instead.
// The cost is the speed the assembly was written for; the alternative is
// rewriting each one in Rust, which M6 may do where it measures.
var fallbacks = map[string]string{
	// math/big's addition, subtraction and shifts over words.
	"math/big.addVV":      "math/big.addVV_g",
	"math/big.subVV":      "math/big.subVV_g",
	"math/big.lshVU":      "math/big.lshVU_g",
	"math/big.rshVU":      "math/big.rshVU_g",
	"math/big.mulAddVWW":  "math/big.mulAddVWW_g",
	"math/big.addMulVVWW": "math/big.addMulVVWW_g",

	// math's transcendental functions, which some architectures have in
	// hardware and all of them have in Go.
	"math.archLog":       "math.log",
	"math.archExp":       "math.exp",
	"math.archLog10":     "math.log10",
	"math.archExp2":      "math.exp2",
	"math.archSin":       "math.sin",
	"math.archCos":       "math.cos",
	"math.archTan":       "math.tan",
	"math.archAsin":      "math.asin",
	"math.archAcos":      "math.acos",
	"math.archAtan":      "math.atan",
	"math.archAtan2":     "math.atan2",
	"math.archCbrt":      "math.cbrt",
	"math.archSinh":      "math.sinh",
	"math.archCosh":      "math.cosh",
	"math.archTanh":      "math.tanh",
	"math.archLog1p":     "math.log1p",
	"math.archExpm1":     "math.expm1",
	"math.archHypot":     "math.hypot",
	"math.archPow":       "math.pow",
	"math.archFrexp":     "math.frexp",
	"math.archLdexp":     "math.ldexp",
	"math.archModf":      "math.modf",
	"math.archRemainder": "math.remainder",
	"math.archFloor":     "math.floor",
	"math.archCeil":      "math.ceil",
	"math.archTrunc":     "math.trunc",
	"math.archMax":       "math.max",
	"math.archMin":       "math.min",

	// The ChaCha8 block function behind math/rand/v2 and the runtime's own
	// randomness.
	"internal/chacha8rand.block": "internal/chacha8rand.block_generic",
}

// unreachableAssembly names functions that exist only for a processor feature
// rustygo never reports as present, so nothing calls them: `hash/crc32`
// selects them on SSE 4.2 or the carry-less multiply, and `internal/cpu`
// answers that neither is there. They are still *reachable* in the call
// graph, so they need a body; the body says what happened, rather than
// pretending to compute a checksum.
var unreachableAssembly = []string{
	"hash/crc32.castagnoliSSE42",
	"hash/crc32.castagnoliSSE42Triple",
	"hash/crc32.ieeeCLMUL",
}

// crateName is what a band's crate is called. Every generated path names the
// crate its item lives in, and `extern crate self as …` at the top of each
// crate is what makes a crate's own name work inside it (bands.go).
func crateName(band int) string {
	return fmt.Sprintf("go_b%d", band)
}
