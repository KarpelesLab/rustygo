package emit

import (
	"fmt"
	"strings"
)

// mangle turns a Go name into a Rust identifier.
//
// The encoding is prefix-free, so distinct Go names never collide:
// ASCII letters and digits stand for themselves; `_` becomes `_1`; `$` (in
// go/ssa's synthetic names such as `main$1` and `init$guard`) becomes `__`;
// any other character becomes `_u<hex>_`. Nothing else in a mangled name
// starts with `_`, which leaves suffixes such as `_P` and prefixes such as
// `G_` free for the emitter's own items. A Rust keyword gets a raw
// identifier, or a `_0` suffix where Rust allows no raw form.
func mangle(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_':
			b.WriteString("_1")
		case r == '$':
			b.WriteString("__")
		default:
			fmt.Fprintf(&b, "_u%x_", r)
		}
	}
	s := b.String()
	switch s {
	case "self", "Self", "super", "crate", "_":
		return s + "_0"
	}
	if rustKeywords[s] {
		return "r#" + s
	}
	return s
}

var rustKeywords = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`as async await break const continue dyn else enum
		extern false fn for gen if impl in let loop match mod move mut pub ref return
		static struct trait true type unsafe use where while abstract become box do
		final macro override priv try typeof unsized virtual yield`) {
		rustKeywords[k] = true
	}
}

// namespace hands out unique identifiers within one Rust module.
type namespace map[string]bool

func (ns namespace) claim(base string) string {
	name := base
	for i := 2; ns[name]; i++ {
		name = fmt.Sprintf("%s_0%d", strings.TrimPrefix(base, "r#"), i)
	}
	ns[name] = true
	return name
}
