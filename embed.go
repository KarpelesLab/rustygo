// Package rustygo carries the source of the runtime crate, so the compiler
// always builds against the runtime it was released with.
package rustygo

import "embed"

// Runtime holds the runtime crate: Cargo.toml and src/.
//
//go:embed Cargo.toml src
var Runtime embed.FS
