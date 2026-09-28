package emit

import "fmt"

// Overrides: functions with a Go body that must never run.
//
// gc compiles a few standard-library functions into machine instructions
// rather than calls, and writes their Go body as a panic saying so. The body
// is real Go, so nothing marks these as needing an implementation — they
// simply fail at run time if emitted as written. Each one gets the Rust that
// means the same thing, keyed by "importpath.Name" and rendered from the
// argument expressions, as an intrinsic is.
var overrides = map[string]func(args []string) string{
	// Constant-time primitives for the FIPS 140-3 module: gc turns them into
	// branchless instructions. Rust's `as` conversion from bool is branchless
	// too, and the arithmetic around it is what the Go source already says.
	"crypto/internal/constanttime.boolToUint8": func(a []string) string {
		return fmt.Sprintf("(%s as u8)", a[0])
	},
}
