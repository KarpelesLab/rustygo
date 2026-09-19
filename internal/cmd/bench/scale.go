package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/KarpelesLab/rustygo/internal/build"
	"github.com/KarpelesLab/rustygo/internal/load"
)

// scaleFunc is one generated function: loops, branches, a struct, a pointer
// and a call, the ordinary mix M0 supports. %[1]d is the function's index.
const scaleFunc = `
type S%[1]d struct{ A, B int; Name string }

func f%[1]d(n int) int {
	s := S%[1]d{A: n, B: %[1]d, Name: "f%[1]d"}
	p := &s
	acc := 0
	for i := 0; i < n; i++ {
		switch {
		case i%%3 == 0:
			acc += p.A * i
		case i%%3 == 1:
			acc -= p.B
		default:
			acc ^= i << 2
		}
		if acc > 1000000 {
			acc /= 7
		}
	}
	if len(s.Name) > 2 {
		acc += int(s.Name[1])
	}
	return acc + g%[1]d(p)
}

func g%[1]d(p *S%[1]d) int { p.B++; return p.A + p.B }
`

// compileScale builds generated programs of increasing size and reports how
// emitted Rust and cargo build time grow with lines of Go, then extrapolates
// to the size of the standard library.
func compileScale(tmp string) string {
	var b strings.Builder
	b.WriteString("| functions | Go lines | emitted Rust lines | cargo build (release) | ms per 1k Go lines |\n|---:|---:|---:|---:|---:|\n")
	var msPerK float64
	for _, nfn := range []int{250, 1000} {
		dir := filepath.Join(tmp, fmt.Sprintf("scale%d", nfn))
		check(os.MkdirAll(dir, 0o755))
		var src strings.Builder
		src.WriteString("package main\n")
		for i := 0; i < nfn; i++ {
			fmt.Fprintf(&src, scaleFunc, i)
		}
		src.WriteString("\nfunc main() {\n\ttotal := 0\n")
		for i := 0; i < nfn; i++ {
			fmt.Fprintf(&src, "\ttotal += f%d(%d)\n", i, 10+i%50)
		}
		src.WriteString("\tprintln(total)\n}\n")
		check(os.WriteFile(filepath.Join(dir, "main.go"), []byte(src.String()), 0o644))
		check(os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scale\n\ngo 1.26\n"), 0o644))

		res, err := load.Load(dir, ".")
		check(err)
		start := time.Now()
		check(build.Binary(res, filepath.Join(dir, "bin"+exeSuffix())))
		d := time.Since(start)
		work, err := build.WorkDir(res)
		check(err)
		goLines := countLines(dir, ".go")
		msPerK = d.Seconds() * 1000 / (float64(goLines) / 1000)
		fmt.Fprintf(&b, "| %d | %d | %d | %s | %.0f |\n", nfn, goLines, countLines(filepath.Join(work, "src"), ".rs"), dur(d), msPerK)
	}

	std := stdLines()
	fmt.Fprintf(&b, "\nThe standard library of %s has %d non-blank lines of non-test Go\n"+
		"(GOROOT/src, excluding cmd/, testdata/ and _test.go). At the largest\n"+
		"program's rate that is about **%s** of release build for the whole library.\n"+
		"This is linear extrapolation from generated code, so read it as an order of magnitude.\n"+
		"It is why M3 builds the stdlib once per Go version and target and caches it.\n",
		runtime.Version(), std, dur(time.Duration(msPerK*float64(std)/1000*float64(time.Millisecond))))
	return b.String()
}

func stdLines() int {
	root := filepath.Join(runtime.GOROOT(), "src")
	n := 0
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() && (d.Name() == "testdata" || rel == "cmd") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			n += countLines(path, ".go")
		}
		return nil
	})
	return n
}
