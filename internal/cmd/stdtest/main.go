// Command stdtest runs the standard library's own tests through rustygo and
// reports what passes.
//
//	go run ./internal/cmd/stdtest [-n 4] [-o docs/STDTEST.md] [packages...]
//
// Each package's test binary is the one `go list -test` writes — the main that
// registers every `TestXxx` and calls `testing.Main` — built with rustygo and
// run with `-test.v`, so the report counts the tests Go ships rather than any
// written here. gc's own result is not compared: these are Go's tests, and a
// test that fails under gc too would be gc's news, not rustygo's.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KarpelesLab/rustygo/internal/build"
	"github.com/KarpelesLab/rustygo/internal/load"
)

// packages is what stdtest measures when it is given nothing: the packages
// rustygo has a chance at, in rough order of how much they need.
var packages = []string{
	"errors", "cmp", "sort", "slices", "maps", "container/list", "container/heap",
	"container/ring", "unicode", "unicode/utf8", "unicode/utf16", "math/bits",
	"strconv", "strings", "bytes", "path", "hash/crc32", "hash/fnv",
	"encoding/hex", "encoding/base32", "encoding/base64", "io", "bufio",
	"sync", "sync/atomic", "context", "time", "fmt", "math", "math/cmplx",
	"net/url", "mime", "path/filepath", "os", "regexp/syntax", "regexp",
}

type result struct {
	pkg     string
	built   bool
	pass    int
	fail    int
	skip    int
	failed  []string // the names of failing tests
	problem string   // a build or run failure, when there is no test result
	elapsed time.Duration
}

func main() {
	workers := flag.Int("n", runtime.NumCPU()/8+1, "packages to build at once")
	outFile := flag.String("o", "", "also write the markdown report here")
	timeout := flag.Duration("timeout", 5*time.Minute, "per-binary run timeout")
	flag.Parse()

	list := flag.Args()
	if len(list) == 0 {
		list = packages
	}
	fmt.Fprintf(os.Stderr, "%d packages, %d at a time\n", len(list), *workers)

	tmp, err := os.MkdirTemp("", "rustygo-stdtest-")
	check(err)
	defer os.RemoveAll(tmp)

	results := make([]result, len(list))
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		// One cargo cache per worker: cargo locks its target directory.
		cfg := build.Config{Cache: filepath.Join(tmp, fmt.Sprintf("cache%d", w))}
		wg.Add(1)
		go func(w int, cfg build.Config) {
			defer wg.Done()
			for i := range work {
				results[i] = runPkg(list[i], filepath.Join(tmp, fmt.Sprintf("w%d", w)), cfg, *timeout)
				fmt.Fprintf(os.Stderr, "%s\n", oneLine(results[i]))
			}
		}(w, cfg)
	}
	for i := range list {
		work <- i
	}
	close(work)
	wg.Wait()

	report := markdown(results)
	fmt.Print(report)
	if *outFile != "" {
		check(os.WriteFile(*outFile, []byte(report), 0o644))
	}
}

func runPkg(pkg, work string, cfg build.Config, timeout time.Duration) result {
	start := time.Now()
	r := result{pkg: pkg}
	defer func() { r.elapsed = time.Since(start) }()
	if err := os.MkdirAll(work, 0o755); err != nil {
		r.problem = err.Error()
		return r
	}
	res, err := load.LoadTests("", pkg)
	if err != nil {
		r.problem = "load: " + firstLine(err.Error())
		return r
	}
	bin := filepath.Join(work, "test")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := build.Binary(res, bin, cfg); err != nil {
		r.problem = "build: " + firstLine(err.Error())
		return r
	}
	r.built = true
	defer os.Remove(bin)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := build.TestCommand(ctx, bin, "-test.v").CombinedOutput()
	for line := range strings.Lines(string(out)) {
		switch {
		case strings.HasPrefix(line, "--- PASS: "):
			r.pass++
		case strings.HasPrefix(line, "--- SKIP: "):
			r.skip++
		case strings.HasPrefix(line, "--- FAIL: "):
			r.fail++
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "--- FAIL: "), " ")
			r.failed = append(r.failed, name)
		}
	}
	if ctx.Err() != nil {
		r.problem = fmt.Sprintf("timed out after %v", timeout)
		return r
	}
	if r.pass+r.fail+r.skip == 0 {
		r.problem = "ran no tests: " + lastInterestingLine(string(out))
		if err != nil {
			r.problem += " (" + err.Error() + ")"
		}
		return r
	}
	// A binary that stopped early — a panic the harness could not report as a
	// test failure — is worth saying so, even though some tests did run.
	var exit *exec.ExitError
	if errors.As(err, &exit) && r.fail == 0 {
		r.problem = "stopped: " + lastInterestingLine(string(out))
	}
	return r
}

func oneLine(r result) string {
	if r.problem != "" && r.pass == 0 {
		return fmt.Sprintf("%-16s %s", r.pkg, r.problem)
	}
	return fmt.Sprintf("%-16s %d/%d pass, %d skipped, %v", r.pkg, r.pass, r.pass+r.fail, r.skip, r.elapsed.Round(time.Second))
}

func markdown(results []result) string {
	var b strings.Builder
	b.WriteString(`# The standard library's own tests

Each package's test binary is the one ` + "`go list -test`" + ` writes — the main that
registers every ` + "`TestXxx`" + ` and calls ` + "`testing.Main`" + ` — built with rustygo and run
with ` + "`-test.v`" + `. The counts are of Go's own tests, not of tests written here.

Generated by ` + "`go run ./internal/cmd/stdtest`" + `.

`)
	var pass, total int
	full := 0
	for _, r := range results {
		pass += r.pass
		total += r.pass + r.fail
		if r.fail == 0 && r.problem == "" && r.pass > 0 {
			full++
		}
	}
	fmt.Fprintf(&b, "| packages | all passing | tests | passing |\n|---:|---:|---:|---:|\n| %d | %d | %d | %d |\n\n",
		len(results), full, total, pass)
	b.WriteString("| package | pass | fail | skip | notes |\n|---|---:|---:|---:|---|\n")
	sorted := make([]result, len(results))
	copy(sorted, results)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].pkg < sorted[j].pkg })
	for _, r := range sorted {
		note := r.problem
		if len(r.failed) > 0 {
			names := r.failed
			if len(names) > 6 {
				names = append(names[:6:6], "…")
			}
			note = strings.Join(names, ", ")
			if r.problem != "" {
				note += "; " + r.problem
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", r.pkg, r.pass, r.fail, r.skip, truncate(note, 110))
	}
	return b.String()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return truncate(line, 140)
}

// lastInterestingLine is the last line that is not test chatter, which is
// where a panic or a fatal error says what happened.
func lastInterestingLine(out string) string {
	var last string
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "", strings.HasPrefix(line, "==="), strings.HasPrefix(line, "---"),
			strings.HasPrefix(line, "    "), strings.HasPrefix(line, "\t"):
		default:
			last = line
		}
	}
	return truncate(last, 120)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "stdtest:", err)
		os.Exit(1)
	}
}
