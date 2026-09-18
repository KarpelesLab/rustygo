// Package emit turns a built SSA program into a Rust crate that links against
// the rustygo runtime crate.
//
// Not written yet: this is the first piece of roadmap milestone M0
// (docs/ROADMAP.md).
package emit

import (
	"errors"

	"github.com/KarpelesLab/rustygo/internal/load"
)

// ErrNotImplemented is returned until M0 code generation lands.
var ErrNotImplemented = errors.New("code generation is not implemented yet (roadmap M0)")

// Options controls where and how the crate is written.
type Options struct {
	// OutDir receives the generated crate (Cargo.toml and src/).
	OutDir string
}

// Crate writes the Rust crate for the main package(s) in res.
func Crate(res *load.Result, opt Options) error {
	return ErrNotImplemented
}
