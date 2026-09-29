// Command rustygo compiles Go packages to Rust.
//
//	rustygo build [-o dir] <packages>   compile to a native binary (via rustc)
//	rustygo emit  [-o dir] <packages>   write the generated Rust crate
//	rustygo test  <package> [flags]      build a package's tests and run them
//	rustygo ssa   <packages>            dump the go/ssa form the emitter consumes
//	rustygo version
package main

import (
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/KarpelesLab/rustygo/internal/build"
	"github.com/KarpelesLab/rustygo/internal/load"
	"golang.org/x/tools/go/ssa"
)

func usage() {
	fmt.Fprint(os.Stderr, `usage: rustygo <command> [arguments]

commands:
	build    compile packages to a native binary
	emit     write the generated Rust crate
	test     build a package's own tests and run them
	ssa      dump the SSA form of packages
	version  print the rustygo version
`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "build":
		err = runBuild(args)
	case "emit":
		err = runEmit(args)
	case "test":
		err = runTest(args)
	case "ssa":
		err = runSSA(args)
	case "version":
		fmt.Println("rustygo", version())
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "rustygo: unknown command %q\n", cmd)
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rustygo:", err)
		os.Exit(1)
	}
}

func runEmit(args []string) error {
	fs := flag.NewFlagSet("emit", flag.ExitOnError)
	out := fs.String("o", "out", "output directory for the crate")
	fs.Parse(args)
	res, err := load.Load("", patterns(fs.Args())...)
	if err != nil {
		return err
	}
	return build.Emit(res, *out, build.ConfigFromEnv())
}

func runBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("o", "", "output file (default: the package's last path element)")
	fs.Parse(args)
	res, err := load.Load("", patterns(fs.Args())...)
	if err != nil {
		return err
	}
	if *out == "" {
		if len(res.Pkgs) != 1 {
			return fmt.Errorf("-o is required with more than one package")
		}
		*out = path.Base(res.Pkgs[0].Pkg.Path())
		if runtime.GOOS == "windows" {
			*out += ".exe"
		}
	}
	return build.Binary(res, *out, build.ConfigFromEnv())
}

// runTest builds and runs one package's tests.
//
// `go list -test` writes the test's main package — the one that registers each
// `TestXxx` and calls `testing.Main` — so a test binary is an ordinary program
// here, built exactly as `rustygo build` builds any other. Flags after the
// package pattern go to the binary, so `rustygo test strings -test.v` says what
// `go test -v` would.
func runTest(args []string) error {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	out := fs.String("o", "", "write the test binary here and do not run it")
	fs.Parse(args)
	rest := fs.Args()
	pattern := "."
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		pattern, rest = rest[0], rest[1:]
	}
	res, err := load.LoadTests("", pattern)
	if err != nil {
		return err
	}
	if !hasTestMain(res) {
		return fmt.Errorf("%s has no test files", pattern)
	}
	bin := *out
	if bin == "" {
		dir, err := os.MkdirTemp("", "rustygo-test-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		bin = filepath.Join(dir, "test")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
	}
	if err := build.Binary(res, bin, build.ConfigFromEnv()); err != nil {
		return err
	}
	if *out != "" {
		return nil
	}
	cmd := exec.Command(bin, rest...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// The test binary's own verdict, reported as ours.
			os.Exit(exit.ExitCode())
		}
		return err
	}
	return nil
}

// hasTestMain reports whether the load produced a test binary's main package,
// which it does not when the package has no test files at all.
func hasTestMain(res *load.Result) bool {
	for _, p := range res.Pkgs {
		if p != nil && p.Pkg.Name() == "main" && p.Func("main") != nil {
			return true
		}
	}
	return false
}

func runSSA(args []string) error {
	fs := flag.NewFlagSet("ssa", flag.ExitOnError)
	fs.Parse(args)
	res, err := load.Load("", patterns(fs.Args())...)
	if err != nil {
		return err
	}
	for _, p := range res.Pkgs {
		p.WriteTo(os.Stdout)
		// Members is a map; sort for stable output.
		for _, name := range slices.Sorted(maps.Keys(p.Members)) {
			if fn, ok := p.Members[name].(*ssa.Function); ok {
				fn.WriteTo(os.Stdout)
			}
		}
	}
	return nil
}

func patterns(args []string) []string {
	if len(args) == 0 {
		return []string{"."}
	}
	return args
}

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}
