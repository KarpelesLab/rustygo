// Command rustygo compiles Go packages to Rust.
//
//	rustygo build [-o dir] <packages>   compile to a native binary (via rustc)
//	rustygo emit  [-o dir] <packages>   write the generated Rust crate
//	rustygo test  <packages>            go test semantics, run through rustc
//	rustygo ssa   <packages>            dump the go/ssa form the emitter consumes
//	rustygo version
package main

import (
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path"
	"runtime"
	"runtime/debug"
	"slices"

	"github.com/KarpelesLab/rustygo/internal/build"
	"github.com/KarpelesLab/rustygo/internal/load"
	"golang.org/x/tools/go/ssa"
)

func usage() {
	fmt.Fprint(os.Stderr, `usage: rustygo <command> [arguments]

commands:
	build    compile packages to a native binary
	emit     write the generated Rust crate
	test     run package tests through rustygo
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
		err = errors.New("test: not implemented yet (roadmap M3)")
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
	return build.Emit(res, *out)
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
	return build.Binary(res, *out)
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
