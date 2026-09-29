// Package load runs the front end: go/packages and go/types over the
// requested packages, then go/ssa over everything they reach.
//
// Packages are loaded with the real GOOS/GOARCH (see DESIGN §1) plus the
// `rustygo` and `purego` build tags, so files written for rustygo are
// selected and assembly-backed packages take their pure-Go paths wherever
// the standard library offers one. cgo is off for now; M4 turns it on for
// targets with a C toolchain (DESIGN §9a).
package load

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"strings"

	"github.com/KarpelesLab/rustygo/internal/goroot"
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
	// Linknames maps a function declared without a body, as
	// "importpath.Name", to the function that implements it, found through
	// `//go:linkname` directives on either side (see collectLinknames).
	Linknames map[string]string
	// LinknameVars is the same for a *variable* declared without a value,
	// which shares the storage of the one it names: `math/bits` reaches the
	// runtime's division error that way.
	LinknameVars map[string]string
}

// Load type-checks the packages matching patterns (relative to dir, or the
// current directory if empty) and builds SSA for them and all their
// dependencies. Generic functions are instantiated, as the emitter needs
// every body monomorphic.
func Load(dir string, patterns ...string) (*Result, error) {
	return load(dir, false, patterns...)
}

// LoadTests is Load over a package's tests.
//
// `go list -test` writes the test's main package — the one that registers each
// `TestXxx` and calls `testing.Main` — and go/packages hands it back like any
// other, so a test binary is an ordinary program to everything downstream of
// here. The [Result] then holds four packages for each pattern: the package
// itself, the copy compiled with its in-package `_test.go` files, the external
// `_test` package, and that generated main.
func LoadTests(dir string, patterns ...string) (*Result, error) {
	return load(dir, true, patterns...)
}

func load(dir string, tests bool, patterns ...string) (*Result, error) {
	overlay, err := goroot.Overlay()
	if err != nil {
		return nil, err
	}
	cfg := &packages.Config{
		// rustygo's own runtime and the packages that read gc's internals
		// replace gc's at their real import paths (internal/goroot).
		Overlay: overlay,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes |
			packages.NeedModule,
		Dir:        dir,
		Tests:      tests,
		BuildFlags: []string{"-tags=" + joinTags()},
		// Packages load with cgo off until M4 binds C (DESIGN §9a), so
		// anything with both paths takes its pure-Go one.
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
	links := map[string]string{}
	vars := map[string]string{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if isStd(p) {
			std[p.Types] = true
		}
		collectLinknames(p, links, vars)
	})

	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	return &Result{Prog: prog, Pkgs: ssaPkgs, Std: std, Linknames: links, LinknameVars: vars}, nil
}

// collectLinknames records p's `//go:linkname local target` directives.
//
// The standard library uses them in two directions. A *pull* declares a
// function without a body and names the function implementing it
// (`//go:linkname runtime_rand runtime.rand` in sync). A *push* sits on the
// implementing side and names the bodyless function it fills in
// (`//go:linkname sync_runtime_Semacquire sync.runtime_Semacquire` in
// runtime). Either way the entry maps the bodyless function to its
// implementation.
func collectLinknames(p *packages.Package, links, vars map[string]string) {
	hasBody := map[string]bool{}
	// A variable is the same story with a value in place of a body: the side
	// without one uses the other's storage.
	isVar := map[string]bool{}
	for _, f := range p.Syntax {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					hasBody[d.Name.Name] = d.Body != nil
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						isVar[name.Name] = true
						hasBody[name.Name] = len(vs.Values) > 0
					}
				}
			}
		}
	}
	for _, f := range p.Syntax {
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				fields := strings.Fields(c.Text)
				if len(fields) != 3 || fields[0] != "//go:linkname" {
					continue
				}
				local, target := p.PkgPath+"."+fields[1], fields[2]
				body, declared := hasBody[fields[1]]
				if isVar[fields[1]] {
					if declared && !body {
						vars[local] = target // this side has no storage
					} else if declared {
						vars[target] = local // this side is the storage
					}
					continue
				}
				switch {
				case !declared:
				case local == target:
					// A function linknamed to its own name is saying only
					// that something else provides it — the same thing the
					// one-argument form says. Recording it would map the
					// name to itself and hide the push that fills it in.
				case body:
					links[target] = local // push
				default:
					links[local] = target // pull
				}
			}
		}
	}
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
