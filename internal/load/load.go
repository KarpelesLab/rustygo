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

	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	return &Result{Prog: prog, Pkgs: ssaPkgs}, nil
}

func joinTags() string {
	s := ""
	for i, t := range Tags {
		if i > 0 {
			s += ","
		}
		s += t
	}
	return s
}
