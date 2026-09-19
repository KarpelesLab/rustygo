// Package load runs the front end: go/packages and go/types over the
// requested packages, then go/ssa over everything they reach.
//
// Packages are loaded with the real GOOS/GOARCH (see DESIGN §1) plus the
// `rustygo` and `purego` build tags, so files written for rustygo are
// selected and assembly-backed packages take their pure-Go paths wherever
// the standard library offers one.
package load

import (
	"errors"
	"fmt"
	"go/types"
	"os"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// Tags are the build tags every rustygo load sets.
var Tags = []string{"rustygo", "purego"}

// Result is a fully built SSA program.
type Result struct {
	Prog *ssa.Program
	// Pkgs are the SSA packages for the requested patterns, in the order
	// go/packages returned them.
	Pkgs []*ssa.Package
	// Std holds the standard-library packages among everything loaded.
	Std map[*types.Package]bool
}

// Load type-checks the packages matching patterns (relative to dir, or the
// current directory if empty) and builds SSA for them and all their
// dependencies. Generic functions are instantiated, as the emitter needs
// every body monomorphic.
func Load(dir string, patterns ...string) (*Result, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes |
			packages.NeedModule,
		Dir:        dir,
		BuildFlags: []string{"-tags=" + joinTags()},
		// cgo is out of scope by construction (README, "What it is not").
		Env: append(os.Environ(), "CGO_ENABLED=0"),
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, errors.New("no packages matched")
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		return nil, fmt.Errorf("%d error(s) loading packages", n)
	}
	for _, p := range pkgs {
		if p.TypesSizes.Sizeof(types.Typ[types.Uintptr]) != 8 {
			return nil, fmt.Errorf("%s: rustygo supports 64-bit targets only", p.PkgPath)
		}
	}

	std := map[*types.Package]bool{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if isStd(p) {
			std[p.Types] = true
		}
	})

	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	return &Result{Prog: prog, Pkgs: ssaPkgs, Std: std}, nil
}

// isStd reports whether p is in the standard library: it belongs to no
// module, and its first path element has no dot (so it is not a GOPATH
// import path either).
func isStd(p *packages.Package) bool {
	if p.Module != nil || p.PkgPath == "command-line-arguments" {
		return false
	}
	first, _, _ := strings.Cut(p.PkgPath, "/")
	return !strings.Contains(first, ".")
}

func joinTags() string { return strings.Join(Tags, ",") }
