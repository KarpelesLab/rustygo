// Package match is imported under a name that is a Rust keyword, from a
// directory that is one too.
package match

func Const() string { return "const" }

type Type struct{ Enum int }

func (t Type) Extern() int { return t.Enum * 2 }
