// Command stdtest runs a package's own tests through rustygo and reports what
// passes.
//
//	go run ./internal/cmd/stdtest [-o docs/STDTEST.md] [packages...]
//
// Each package's test binary is the one `go list -test` writes — the main that
// registers every `TestXxx` and calls `testing.Main` — built with rustygo and
// run with `-test.v`, so the report counts the tests Go ships rather than any
// written here.
//
// A failure is put to gc as well. A test that fails under gc on this machine is
// gc's news and not rustygo's — this one's Go has no `regexp/testdata`, and two
// of `regexp`'s tests want it — so those are counted apart.
//
// `-emit` stops at the generated Rust instead of building it. That costs seconds
// where a build costs minutes, so it reaches every package in the library, and it
// is what answers the question a sweep cannot afford to: which missing piece
// blocks how many packages, and which fix would free the most of them.
//
// The work is done by the `rustygo` binary, built once and then run per
// package: a build that wedges can be killed on a timeout, an emitter panic
// costs one package instead of the whole sweep, and the diagnostics the report
// groups by are the ones a reader would see from `rustygo test` themselves.
//
// `-n` builds several packages at once and defaults to one, which is the number
// to leave it at. How many rustc processes a cargo build runs is cargo's
// decision, taken from `jobs` in the machine's `~/.cargo/config.toml`; two
// cargo invocations do not share that budget, they each get it, so `-n 8` is
// eight times the parallelism the machine was configured for and the first
// thing it costs is the measurement itself — rustc gets picked off by the
// out-of-memory killer and a package that compiles perfectly well is recorded
// as a build failure. Raise `jobs`, not `-n`.
//
// Packages are measured in the order they are given, and the default order puts
// one package of each kind — an encoder, a compressor, a crypto primitive, an
// image codec, a `go/*` package, a net package — before the rest, so that a
// sweep stopped halfway still says something about every corner of the library.
//
// With `-C` the packages are loaded from another module, which is how the
// report covers code from outside the standard library:
//
//	go run ./internal/cmd/stdtest -C /tmp/third-party github.com/google/uuid
//
// `-state` keeps one JSON object per package in a file and skips on a later run
// what that file already holds, so a sweep interrupted after three hours resumes
// instead of starting over. `-report` then writes the document from that file
// and measures nothing, and `-redo` measures a package again whatever the file
// says — which is what to run after a fix, on the packages it was meant to
// help.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KarpelesLab/rustygo/internal/build"
	"github.com/KarpelesLab/rustygo/internal/goroot"
	"github.com/KarpelesLab/rustygo/internal/load"
)

// packages is what stdtest measures when it is given nothing: every package of
// the standard library that ships tests, which `go list std` names, less the
// `internal/` and `vendor/` trees, whose tests mostly measure gc's own guts.
//
// The order is the point. A test binary that compiles costs minutes of rustc,
// and one build runs at a time, so a full sweep is a day's work and gets
// interrupted. One of each kind comes first — a core package, an encoder, a
// hash, a compressor, a crypto primitive, an image codec, a template engine, a
// `go/*` package, a net package, a database driver — and the rest follow, so
// that stopping anywhere leaves a report that covers the library rather than the
// beginning of its alphabet.
//
// A package that cannot be built fails in seconds, which is why names with no
// hope of passing yet are here too: what they cost is nothing and what they say
// is which missing piece blocks how many packages. `-emit` is that measurement
// on its own, and it reaches all of them.
var packages = []string{
	// One of every kind.
	"errors", "strings", "bytes", "fmt", "strconv", "sort", "io", "bufio",
	"time", "sync", "context", "os", "path/filepath", "regexp", "net/url",
	"encoding/binary", "encoding/json", "encoding/gob", "hash/crc32",
	"compress/flate", "archive/tar", "crypto/sha256", "crypto/aes",
	"image/png", "text/template", "go/parser", "net/netip", "net/http",
	"database/sql", "log/slog", "math/big", "testing", "reflect", "iter",

	// The language and the small libraries on top of it.
	"cmp", "slices", "maps", "unique", "weak", "container/list",
	"container/heap", "container/ring", "unicode", "unicode/utf8",
	"unicode/utf16", "math", "math/bits", "math/cmplx", "math/rand",
	"math/rand/v2", "path", "io/fs", "io/ioutil", "flag", "sync/atomic",
	"embed", "expvar", "hash", "runtime/debug",

	// Hashes, encodings, compression and archives.
	"hash/adler32", "hash/crc64", "hash/fnv", "hash/maphash",
	"encoding/ascii85", "encoding/asn1", "encoding/base32", "encoding/base64",
	"encoding/csv", "encoding/hex", "encoding/pem", "encoding/xml",
	"compress/bzip2", "compress/gzip", "compress/lzw", "compress/zlib",
	"archive/zip", "index/suffixarray",

	// Text and templates.
	"text/scanner", "text/tabwriter", "text/template/parse", "regexp/syntax",
	"html", "html/template", "mime", "mime/multipart", "mime/quotedprintable",

	// Crypto, where the pure-Go paths are what `purego` selects.
	"crypto", "crypto/cipher", "crypto/des", "crypto/ecdh", "crypto/ecdsa",
	"crypto/ed25519", "crypto/elliptic", "crypto/hkdf", "crypto/hmac",
	"crypto/md5", "crypto/rand", "crypto/rc4", "crypto/rsa", "crypto/sha1",
	"crypto/sha3", "crypto/sha512", "crypto/subtle", "crypto/tls",
	"crypto/x509",

	// Images.
	"image", "image/color", "image/draw", "image/gif", "image/jpeg",

	// The operating system and the network.
	"os/exec", "os/signal", "os/user", "syscall", "net", "net/textproto",
	"net/mail", "net/http/httptest", "net/http/httputil", "net/http/internal",
	"net/rpc", "net/smtp", "database/sql/driver",

	// Logging, the toolchain's own libraries, and testing itself.
	"log", "log/syslog", "go/ast", "go/build", "go/build/constraint",
	"go/constant", "go/doc", "go/doc/comment", "go/format", "go/importer",
	"go/printer", "go/scanner", "go/token", "go/types", "go/version",
	"testing/fstest", "testing/iotest", "testing/quick", "testing/slogtest",
	"testing/synctest", "testing/cryptotest", "runtime", "runtime/metrics",
	"runtime/pprof", "runtime/trace",

	// The object-file readers, and the rest of the library's corners.
	"debug/buildinfo", "debug/dwarf", "debug/elf", "debug/gosym", "debug/macho",
	"debug/pe", "debug/plan9obj", "crypto/dsa", "crypto/fips140", "crypto/hpke",
	"crypto/mlkem", "crypto/pbkdf2", "net/http/cgi", "net/http/cookiejar",
	"net/http/fcgi", "net/http/httptrace", "net/http/pprof", "net/rpc/jsonrpc",
}

// result is one package's verdict, and what the state file holds.
type result struct {
	Pkg     string   `json:"pkg"`
	Built   bool     `json:"built"`
	Pass    int      `json:"pass"`
	Skip    int      `json:"skip"`
	Failed  []string `json:"failed,omitempty"`  // every failing test, in the order they ran
	AlsoGc  []string `json:"alsoGc,omitempty"`  // of those, the ones gc fails here too
	Problem string   `json:"problem,omitempty"` // a build or run failure
	Causes  []string `json:"causes,omitempty"`  // what the build or load failure was, one entry per distinct diagnostic
	Says    []string `json:"says,omitempty"`    // what each failing test complained about, in Failed's order
	Tests   int      `json:"tests,omitempty"`   // how many tests gc's build of the package registers
	Seconds float64  `json:"seconds"`
}

func (r result) elapsed() time.Duration { return time.Duration(r.Seconds * float64(time.Second)) }

// ran is how many of the package's tests the binary got through, whatever their
// verdict.
func (r result) ran() int { return r.Pass + len(r.Failed) + r.Skip }

// whole reports whether every test in the package passed.
func (r result) whole() bool { return len(r.ours()) == 0 && r.Problem == "" && r.Pass > 0 }

// allocs is the failures that count allocations, which rustygo cannot satisfy
// while it boxes a value on its way into an interface where gc does not. Nothing
// about the compiler will change them, so they are counted apart.
//
// The judgement is made here rather than when the package was measured, so that
// sharpening it does not mean building a hundred test binaries again.
func (r result) allocs() []string {
	var counting []string
	for i, name := range r.Failed {
		said := ""
		if i < len(r.Says) {
			said = r.Says[i]
		}
		if countsAllocations(name, said) && !slices.Contains(r.AlsoGc, name) {
			counting = append(counting, name)
		}
	}
	return counting
}

// ours is the failures that are rustygo's: all of them but the ones gc fails on
// this machine as well and the ones that count allocations.
func (r result) ours() []string {
	apart := r.allocs()
	var mine []string
	for _, name := range r.Failed {
		if !slices.Contains(r.AlsoGc, name) && !slices.Contains(apart, name) {
			mine = append(mine, name)
		}
	}
	return mine
}

func main() {
	workers := flag.Int("n", 1, "packages to build at once (see the package comment before raising it)")
	outFile := flag.String("o", "", "also write the markdown report here")
	timeout := flag.Duration("timeout", 5*time.Minute, "per-binary run timeout")
	buildTimeout := flag.Duration("build-timeout", 30*time.Minute, "per-package build timeout")
	dir := flag.String("C", "", "load the packages from this directory's module")
	state := flag.String("state", "", "a JSON file of results to resume from and append to")
	redo := flag.Bool("redo", false, "measure the named packages again even if the state file has them")
	reportOnly := flag.Bool("report", false, "write the document from the state file and measure nothing")
	budget := flag.Int64("max-cache", 40<<30, "wipe a worker's cargo target directory once it passes this many bytes")
	cache := flag.String("cache", "", "keep the cargo caches here instead of in a temporary directory, so a resumed sweep does not rebuild the runtime crate")
	census := flag.Bool("emit", false, "stop at the generated Rust: report what builds, not what passes")
	one := flag.String("emit1", "", "internal: emit one package in this process and exit")
	flag.Parse()

	if *one != "" {
		emitOne(*dir, *one)
		return
	}

	list := flag.Args()
	if len(list) == 0 {
		list = packages
	}

	done := map[string]result{}
	if *state != "" {
		var err error
		done, err = readState(*state)
		check(err)
	}
	todo := make([]string, 0, len(list))
	for _, pkg := range list {
		if _, ok := done[pkg]; (!ok || *redo) && !*reportOnly {
			todo = append(todo, pkg)
		}
	}
	fmt.Fprintf(os.Stderr, "%d packages, %d already measured, %d at a time\n",
		len(list), len(list)-len(todo), *workers)

	tmp, err := os.MkdirTemp("", "rustygo-stdtest-")
	check(err)
	defer os.RemoveAll(tmp)

	// One rustygo for the whole sweep: `go build` once rather than per package,
	// and the binary is what every worker then runs.
	rustygo := filepath.Join(tmp, "rustygo")
	if len(todo) > 0 {
		gobuild := exec.Command("go", "build", "-o", rustygo, "github.com/KarpelesLab/rustygo/cmd/rustygo")
		gobuild.Stderr = os.Stderr
		check(gobuild.Run())
	}

	var mu sync.Mutex // guards done and the state file
	var stateFile *os.File
	if *state != "" {
		stateFile, err = os.OpenFile(*state, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		check(err)
		defer stateFile.Close()
	}
	record := func(r result) {
		mu.Lock()
		defer mu.Unlock()
		done[r.Pkg] = r
		fmt.Fprintf(os.Stderr, "%s\n", oneLine(r))
		if stateFile != nil {
			line, err := json.Marshal(r)
			if err == nil {
				fmt.Fprintf(stateFile, "%s\n", line)
			}
		}
	}

	self, err := os.Executable()
	check(err)
	opts := options{
		rustygo: rustygo, self: self, dir: *dir, census: *census,
		run: *timeout, build: *buildTimeout, budget: *budget,
	}
	caches := tmp
	if *cache != "" {
		caches = *cache
		check(os.MkdirAll(caches, 0o755))
	}
	work := make(chan string)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		// One cargo cache per worker: cargo locks its target directory.
		o := opts
		o.cache = filepath.Join(caches, fmt.Sprintf("cache%d", w))
		o.work = filepath.Join(tmp, fmt.Sprintf("w%d", w))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pkg := range work {
				record(runPkg(pkg, o))
			}
		}()
	}
	for _, pkg := range todo {
		work <- pkg
	}
	close(work)
	wg.Wait()

	results := make([]result, 0, len(list))
	for _, pkg := range list {
		if r, ok := done[pkg]; ok {
			results = append(results, r)
		}
	}
	// A binary that stopped after three tests has not passed the other twenty,
	// and a report that counts only what ran would not say so. gc's own list of
	// the package's tests is the denominator, asked for here rather than when the
	// package was measured, so that a report written from an old state file gets
	// it too.
	if !*census {
		for i := range results {
			results[i].Tests = testCount(results[i].Pkg, opts)
		}
	}
	report := markdown(results, *census, *dir)
	fmt.Print(report)
	if *outFile != "" {
		check(os.WriteFile(*outFile, []byte(report), 0o644))
	}
}

// options is what every package in a sweep is measured with, plus the two
// directories that belong to the worker doing the measuring.
type options struct {
	rustygo string // the compiler binary
	self    string // this harness's own binary, which emits a package on request
	census  bool   // stop at the generated Rust, and build nothing
	dir     string // where packages are loaded from, "" for the current directory
	cache   string // this worker's RUSTYGO_CACHE
	work    string // this worker's scratch space
	run     time.Duration
	build   time.Duration
	budget  int64 // how large this worker's cargo target directory may grow
}

func runPkg(pkg string, o options) (res result) {
	start := time.Now()
	r := result{Pkg: pkg}
	// Named, because `return r` copies the struct before this runs.
	defer func() { res.Seconds = time.Since(start).Seconds() }()
	if err := os.MkdirAll(o.work, 0o755); err != nil {
		r.Problem = err.Error()
		return r
	}
	if o.census {
		return emitPkg(pkg, o, r)
	}
	bin := filepath.Join(o.work, "test")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	defer os.Remove(bin)
	// The crate for one test binary is tens of megabytes of Rust and its
	// object files are more, so each package's goes away once it has been
	// measured. The runtime crate and the dependencies cargo built alongside
	// it stay, which is what makes the next package in this worker quick.
	defer os.RemoveAll(filepath.Join(o.cache, "work"))
	pruneCache(o.cache, o.budget)

	// attempt builds the test binary once, and says whether it was the timeout
	// that ended it rather than the compiler.
	attempt := func() (out string, err error, timedOut bool) {
		ctx, cancel := context.WithTimeout(context.Background(), o.build)
		defer cancel()
		cmd := exec.CommandContext(ctx, o.rustygo, "test", "-o", bin, pkg)
		cmd.Dir = o.dir
		cmd.Env = append(os.Environ(), "RUSTYGO_CACHE="+o.cache)
		killGroup(cmd)
		printed, err := cmd.CombinedOutput()
		return string(printed), err, ctx.Err() != nil
	}
	out, err, late := attempt()
	// An rustc that died on a signal was not killed by anything in the
	// compiler: on a loaded machine the out-of-memory killer takes one, and the
	// package would be recorded as failing to build when it compiles perfectly
	// well. The second attempt is the one that counts.
	if err != nil && !late && signalled.MatchString(out) {
		out, err, late = attempt()
	}
	if err != nil {
		switch {
		case late:
			r.Problem = fmt.Sprintf("build timed out after %v", o.build)
		case signalled.MatchString(out):
			r.Problem = "build killed twice: " + signalled.FindString(out)
		case strings.Contains(out, "has no test files") && slices.Contains(goroot.Replaced(), pkg):
			// rustygo's replacement for a package stands in for every file of
			// gc's, its tests included, so the tests Go wrote for `reflect` or
			// `unique` are not there to run. What the replacements do is
			// measured by every other package that uses them.
			r.Problem = "replaced by rustygo, so gc's tests for it are hidden too"
		default:
			r.Problem, r.Causes = diagnose(out)
			if why := gcCannotBuild(pkg, o); why != "" {
				// Not rustygo's failure: this Go installation cannot build the
				// test binary either, because the distribution stripped the
				// testdata the tests embed. Recording a cause here would put a
				// package in the blocker ranking that no work on rustygo frees.
				r.Problem, r.Causes = "gc cannot build it here either: "+why, nil
			}
		}
		return r
	}
	r.Built = true

	runCtx, runCancel := context.WithTimeout(context.Background(), o.run)
	defer runCancel()
	test := build.TestCommand(runCtx, bin, "-test.v")
	// In the package's own directory, as `go test` runs a test binary: a test
	// that opens `testdata/something` expects to find it.
	test.Dir = pkgDir(pkg, o.dir)
	raw, err := test.CombinedOutput()
	// What a test printed is kept until its verdict arrives, because the
	// complaint is what says whether the failure is one rustygo could fix.
	var said strings.Builder
	for line := range strings.Lines(string(raw)) {
		switch {
		case strings.HasPrefix(line, "=== RUN   "):
			said.Reset()
		case strings.HasPrefix(line, "--- PASS: "):
			r.Pass++
		case strings.HasPrefix(line, "--- SKIP: "):
			r.Skip++
		case strings.HasPrefix(line, "--- FAIL: "):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "--- FAIL: "), " ")
			r.Failed = append(r.Failed, name)
			r.Says = append(r.Says, complaint(said.String()))
		default:
			said.WriteString(line)
		}
	}
	if runCtx.Err() != nil {
		r.Problem = fmt.Sprintf("timed out after %v", o.run)
	} else if r.ran() == 0 {
		r.Problem = "ran no tests: " + lastInterestingLine(string(raw))
		if err != nil {
			r.Problem += " (" + err.Error() + ")"
		}
		return r
	} else {
		// A test binary exits 1 when tests failed and it ran to the end. Any
		// other way out — a panic, a fatal runtime error, a signal — means the
		// tests after it never ran, which the counts would otherwise hide.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() != 1 {
			// Whatever it said last, or how it left if it said nothing.
			why := lastInterestingLine(string(raw))
			if why == "" {
				why = err.Error()
			}
			r.Problem = "stopped: " + why
		}
	}

	// A failure gc shares on this machine is the machine's news, and which of
	// the rest are rustygo's is decided when the report is written.
	r.AlsoGc = failsUnderGc(pkg, r.Failed, o)
	return r
}

// emitPkg generates the Rust for a package's test binary and stops there.
//
// Everything that keeps a package from building — a type error in a replaced
// package, a function with no body, a construct the emitter has not reached —
// is found before cargo is called at all, and finding it costs seconds instead
// of the minutes rustc wants. That is what makes a census of the whole library
// affordable, and why the harness can answer "how many packages does this one
// missing piece block" over far more packages than it can run.
//
// The emitting is done by this same binary in a child process: the loader
// reports type errors on its stderr rather than returning them, and an emitter
// that panics on one package should not take the sweep with it.
func emitPkg(pkg string, o options, r result) result {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.self, "-emit1", pkg, "-C", o.dir)
	out, err := cmd.CombinedOutput()
	switch {
	case err == nil:
		r.Built = true
	case ctx.Err() != nil:
		r.Problem = "emitting timed out"
	case strings.Contains(string(out), "has no test files"):
		r.Problem = "no test files"
		if slices.Contains(goroot.Replaced(), pkg) {
			r.Problem = "replaced by rustygo, so gc's tests for it are hidden too"
		}
	default:
		r.Problem, r.Causes = diagnose(string(out))
		if why := gcCannotBuild(pkg, o); why != "" {
			r.Problem, r.Causes = "gc cannot build it here either: "+why, nil
		}
	}
	return r
}

// emitOne is the child of emitPkg: load one package's tests, write the crate to
// a directory nobody reads, and exit non-zero with the diagnostics on the way
// out.
func emitOne(dir, pkg string) {
	res, err := load.LoadTests(dir, pkg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// The load produces a test binary's main package unless the package has no
	// test files at all, which is what `rustygo test` itself checks for.
	hasMain := false
	for _, p := range res.Pkgs {
		if p != nil && p.Pkg.Name() == "main" && p.Func("main") != nil {
			hasMain = true
		}
	}
	if !hasMain {
		fmt.Fprintf(os.Stderr, "%s has no test files\n", pkg)
		os.Exit(1)
	}
	tmp, err := os.MkdirTemp("", "rustygo-emit-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)
	if err := build.Emit(res, tmp, build.Config{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// pruneCache empties the worker's cargo target directory once it has grown
// past its budget. The object files of a hundred test binaries fit on no disk
// worth filling, and what the next package pays for the space is one rebuild of
// the runtime crate and its dependencies.
func pruneCache(cache string, budget int64) {
	if budget <= 0 {
		return
	}
	target := filepath.Join(cache, "target")
	var size int64
	filepath.WalkDir(target, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
		}
		if size > budget {
			return filepath.SkipAll
		}
		return nil
	})
	if size > budget {
		os.RemoveAll(target)
	}
}

// allocCounting matches the tests that assert how many allocations a call makes,
// which `testing.AllocsPerRun` is how Go writes. Nothing rustygo can do short of
// unboxing small values inside interfaces will make these pass, so they are
// counted apart from the failures worth fixing.
//
// The name alone is not enough — `strings.TestBuilderGrow` counts allocations
// without saying so — so what the test printed is read as well, and a complaint
// that talks about allocations is taken at its word.
var (
	allocCounting  = regexp.MustCompile(`Alloc|Malloc`)
	allocComplaint = regexp.MustCompile(`(?i)alloc`)
)

func countsAllocations(test, said string) bool {
	return allocCounting.MatchString(test) || allocComplaint.MatchString(said)
}

// complaint is the first thing a failing test said, which is nearly always the
// line that names the difference. A test's name tells the next person where to
// look; this tells them what they will find.
func complaint(said string) string {
	for line := range strings.Lines(said) {
		line = strings.TrimSpace(line)
		// A testing.T message carries its own file and line, which says nothing
		// a reader of the table needs.
		if _, rest, ok := strings.Cut(line, ".go:"); ok {
			if _, after, ok := strings.Cut(rest, ": "); ok {
				line = after
			}
		}
		if line != "" {
			return truncate(line, 150)
		}
	}
	return ""
}

// pkgDir is where the package's own files are, which is where `go test` runs a
// test binary built from them. An answer is not essential — only a test
// reading `testdata/` needs it — so a failure leaves the directory unset.
func pkgDir(pkg, dir string) string {
	cmd := exec.Command("go", "list", "-f", "{{.Dir}}", pkg)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return dir
	}
	return strings.TrimSpace(string(out))
}

// gcCannotBuild reports why gc cannot build this package's test binary, or "" if
// it can. A package rustygo fails on is only rustygo's news when the toolchain
// it is being compared against manages: this machine's Go ships no
// `embed/internal/embedtest/testdata`, so `embed`'s own example refuses to
// compile under either compiler.
func gcCannotBuild(pkg string, o options) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-c", "-o", os.DevNull, pkg)
	cmd.Dir = o.dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}
	for line := range strings.Lines(string(out)) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return truncate(line, 100)
		}
	}
	return firstLine(err.Error())
}

// testCount is how many tests gc's build of the package registers, which
// `go test -list` answers without running any of them. Zero means the question
// could not be answered, and the report then says nothing about it.
func testCount(pkg string, o options) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-list", "^(Test|Example)", pkg)
	cmd.Dir = o.dir
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	n := 0
	for line := range strings.Lines(string(out)) {
		if strings.HasPrefix(line, "Test") || strings.HasPrefix(line, "Example") {
			n++
		}
	}
	return n
}

// failsUnderGc returns the tests among `names` that fail when gc builds and
// runs them.
func failsUnderGc(pkg string, names []string, o options) []string {
	if len(names) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.run)
	defer cancel()
	run := "^(" + strings.Join(names, "|") + ")$"
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-v", "-run", run, pkg)
	cmd.Dir = o.dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil // gc passed them all
	}
	var also []string
	for line := range strings.Lines(string(out)) {
		if name, ok := strings.CutPrefix(line, "--- FAIL: "); ok {
			name, _, _ = strings.Cut(name, " ")
			if slices.Contains(names, name) {
				also = append(also, name)
			}
		}
	}
	return also
}

// signalled matches cargo's report of a child that died on a signal, which is
// how the out-of-memory killer shows up in a build's output.
var signalled = regexp.MustCompile(`signal: \d+(, SIG\w+)?`)

// position matches the `file:line:col: ` a type-checker or emitter diagnostic
// starts with. The file is not always a `.go` one: a package that embeds files
// or runs a generator is type-checked from the go command's build cache, and a
// diagnostic there names a file with no extension at all.
var position = regexp.MustCompile(`^\S+:\d+(:\d+)?: `)

// rustcError matches a diagnostic from rustc, which is what a package that
// generates Rust the compiler then rejects fails with.
var rustcError = regexp.MustCompile(`^error(\[E\d+\])?: `)

var (
	noBody      = regexp.MustCompile(`^(\S+) has no Go body `)
	noMethod    = regexp.MustCompile(`^\S+ undefined \(type (\S+) has no field or method (\w+)\)`)
	undefinedID = regexp.MustCompile(`^undefined: (\S+)$`)
)

// diagnose turns what `rustygo test` printed into a one-line summary and the
// list of distinct things that blocked it.
//
// Both halves of the compiler report every diagnostic they found, not only the
// first, so a package that needs four missing `reflect` methods says so, and
// the report can count how many packages each one holds up.
func diagnose(out string) (problem string, causes []string) {
	seen := map[string]bool{}
	for line := range strings.Lines(out) {
		// `rustygo: ` is how the command prefixes the error it exits with, and
		// the diagnostic a reader wants is what follows it.
		line = strings.TrimPrefix(strings.TrimSpace(line), "rustygo: ")
		if !position.MatchString(line) {
			continue
		}
		c := cause(position.ReplaceAllString(line, ""))
		if !seen[c] {
			seen[c] = true
			causes = append(causes, c)
		}
	}
	// Nothing with a position in it means the generated Rust is what failed, so
	// rustc's own diagnostics are the causes. Failing that, the last thing said
	// before the exit is all there is.
	if len(causes) == 0 {
		for line := range strings.Lines(out) {
			line = strings.TrimSpace(line)
			if rustcError.MatchString(line) && !seen[line] {
				seen[line] = true
				causes = append(causes, line)
			}
		}
	}
	switch {
	case len(causes) > 0:
		problem = causes[0]
		if len(causes) > 1 {
			problem += fmt.Sprintf(" (+%d more)", len(causes)-1)
		}
	default:
		problem = lastInterestingLine(out)
	}
	return problem, causes
}

// cause reduces a diagnostic to the thing that caused it, so that two packages
// stopped by the same missing piece group together. The position is already
// gone; what is left is the message, with the wording that varies between call
// sites — which function reached the missing one, which local held the value —
// taken out.
func cause(msg string) string {
	if i := strings.Index(msg, ", reached from "); i >= 0 {
		msg = msg[:i]
	}
	switch {
	case noBody.MatchString(msg):
		return "no Go body: " + noBody.FindStringSubmatch(msg)[1]
	case noMethod.MatchString(msg):
		m := noMethod.FindStringSubmatch(msg)
		return "missing: " + strings.TrimPrefix(m[1], "*") + "." + m[2]
	case undefinedID.MatchString(msg):
		return "missing: " + undefinedID.FindStringSubmatch(msg)[1]
	}
	return msg
}

func oneLine(r result) string {
	if r.Problem != "" && r.Pass == 0 {
		return fmt.Sprintf("%-22s %s", r.Pkg, r.Problem)
	}
	if r.Built && r.ran() == 0 {
		// A census, which stops at the Rust and runs nothing.
		return fmt.Sprintf("%-22s emits, %v", r.Pkg, r.elapsed().Round(time.Second))
	}
	extra := ""
	if n := len(r.AlsoGc); n > 0 {
		extra += fmt.Sprintf(", %d gc fails too", n)
	}
	if n := len(r.allocs()); n > 0 {
		extra += fmt.Sprintf(", %d count allocations", n)
	}
	if r.Problem != "" {
		extra += ", " + r.Problem
	}
	if r.Tests > 0 {
		extra += fmt.Sprintf(", of %d", r.Tests)
	}
	return fmt.Sprintf("%-22s %d/%d pass, %d skipped%s, %v",
		r.Pkg, r.Pass, r.Pass+len(r.ours()), r.Skip, extra, r.elapsed().Round(time.Second))
}

func markdown(results []result, census bool, dir string) string {
	if census {
		return censusReport(results, dir)
	}
	var b strings.Builder
	b.WriteString(intro(dir))
	var pass, total, whole, built, allocs, alsoGc int
	for _, r := range results {
		pass += r.Pass
		total += r.Pass + len(r.ours())
		allocs += len(r.allocs())
		alsoGc += len(r.AlsoGc)
		if r.Built {
			built++
		}
		if r.whole() {
			whole++
		}
	}
	fmt.Fprintf(&b, "| packages | building | all passing | tests | passing |\n|---:|---:|---:|---:|---:|\n| %d | %d | %d | %d | %d |\n\n",
		len(results), built, whole, total, pass)
	if dir == "" && len(results) < len(packages) {
		fmt.Fprintf(&b, `This sweep reached %d of the %d packages the library ships tests for. The
order is fixed and takes one package of each kind first, so what is here is a
cross-section and not the beginning of the alphabet; what the rest would need in
order to build at all is in [BLOCKERS.md](BLOCKERS.md).

`, len(results), len(packages))
	}
	fmt.Fprintf(&b, "Beside those, %d failures are gc's here too and %d count allocations.\n\n", alsoGc, allocs)

	b.WriteString("| package | pass | fail | skip | notes |\n|---|---:|---:|---:|---|\n")
	sorted := slices.Clone(results)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Pkg < sorted[j].Pkg })
	for _, r := range sorted {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", r.Pkg, r.Pass, len(r.ours()), r.Skip, truncate(note(r), 110))
	}
	b.WriteString(complaints(sorted))
	b.WriteString(blockers(sorted))
	b.WriteString(order(sorted))
	return b.String()
}

// complaints lists what each failing test said, which is what a reader who means
// to fix one needs and the notes column has no room for. Only the failures that
// are rustygo's are here: a test gc fails too, or one counting allocations, is
// not news.
func complaints(results []result) string {
	var b strings.Builder
	for _, r := range results {
		mine := r.ours()
		for i, name := range r.Failed {
			if !slices.Contains(mine, name) {
				continue
			}
			said := ""
			if i < len(r.Says) {
				said = r.Says[i]
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", r.Pkg, name, truncate(said, 130))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return `
## What the failures say

The first thing each failing test printed. Failures gc shares and failures that
count allocations are left out: neither is news about rustygo.

| package | test | complaint |
|---|---|---|
` + b.String()
}

// censusReport is what `-emit` writes: which packages rustygo can generate Rust
// for at all, and what stops the others. It says nothing about tests, because
// nothing was run — the point of it is that it covers packages a full sweep has
// no time for.
func censusReport(results []result, dir string) string {
	var b strings.Builder
	b.WriteString(censusHeading(dir) + `

Every package here had its test binary — the main that ` + "`go list -test`" + ` writes —
loaded, type-checked against rustygo's own standard-library replacements, and
turned into Rust. Nothing was handed to rustc and nothing was run: this measures
what the compiler can and cannot express, which is the part that can be measured
for the whole library at once.

Packages load with cgo off until M4 binds C, so a package whose implementation is
C appears here as its own Go file missing the methods the C side would have
declared, rather than as anything about the compiler.

` + passes(dir) + `Generated by ` + "`go run ./internal/cmd/stdtest -emit`" + ` on ` + platform() + `.

` + module(dir))
	ok := 0
	for _, r := range results {
		if r.Built {
			ok++
		}
	}
	fmt.Fprintf(&b, "| packages | generate Rust |\n|---:|---:|\n| %d | %d |\n\n", len(results), ok)
	sorted := slices.Clone(results)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Pkg < sorted[j].Pkg })

	// The ones that work are a list, not a table: there is nothing to say about
	// them but their names.
	var emits []string
	for _, r := range sorted {
		if r.Built {
			emits = append(emits, "`"+r.Pkg+"`")
		}
	}
	fmt.Fprintf(&b, "Rust comes out for these:\n\n%s.\n\nAnd not for these:\n\n", strings.Join(emits, ", "))

	b.WriteString("| package | what stops it |\n|---|---|\n")
	for _, r := range sorted {
		if !r.Built {
			fmt.Fprintf(&b, "| %s | %s |\n", r.Pkg, truncate(r.Problem, 140))
		}
	}
	b.WriteString(blockers(sorted))
	b.WriteString(order(sorted))
	return b.String()
}

// intro, censusHeading and module are what let one generator write both the
// standard library's report and another module's: the only difference a reader
// needs is which packages were measured, and for a module outside the standard
// library, which versions of it.
func intro(dir string) string {
	what := "The standard library's own tests"
	if dir != "" {
		what = "Third-party packages' own tests"
	}
	s := "# " + what + `

Each package's test binary is the one ` + "`go list -test`" + ` writes — the main that
registers every ` + "`TestXxx`" + ` and calls ` + "`testing.Main`" + ` — built with rustygo and run
with ` + "`-test.v`" + `. The counts are of the tests the package ships, not of tests
written here.

Two kinds of failure are counted apart from the rest, because fixing rustygo
would not change them. A test that fails under gc on this machine as well is the
machine's news: this one's Go has no ` + "`regexp/testdata`" + `, and two of
` + "`regexp`" + `'s tests want it. And a test that counts allocations cannot pass
while rustygo boxes a value on its way into an interface where gc does not, so
every ` + "`testing.AllocsPerRun`" + ` assertion fails by one or two.

Generated by ` + "`go run ./internal/cmd/stdtest`" + ` on ` + platform() + `.

`
	return s + module(dir)
}

// passes points at the document that says what happens once a package does reach
// rustc, which exists for the standard library and not for anything else.
func passes(dir string) string {
	if dir != "" {
		return ""
	}
	return "What passes once it reaches rustc is [STDTEST.md](STDTEST.md).\n\n"
}

// platform is the machine and the toolchain the measurement belongs to.
func platform() string {
	return fmt.Sprintf("%s/%s, %s", runtime.GOOS, runtime.GOARCH, runtime.Version())
}

func censusHeading(dir string) string {
	if dir == "" {
		return "# What rustygo can compile"
	}
	return "# What rustygo can compile outside the standard library"
}

// module describes the module the packages were loaded from, by quoting the
// requirements of the `go.mod` they were measured against. A reader cannot
// reproduce a measurement of `gopkg.in/yaml.v3` without knowing which
// `yaml.v3`.
func module(dir string) string {
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return fmt.Sprintf("Measured in `%s`.\n\n", dir)
	}
	var b strings.Builder
	b.WriteString("Measured from a module requiring:\n\n```\n")
	for line := range strings.Lines(string(data)) {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "require") || strings.Contains(t, " v") {
			if t != "require (" && t != ")" {
				b.WriteString(strings.TrimPrefix(t, "require ") + "\n")
			}
		}
	}
	b.WriteString("```\n\n")
	return b.String()
}

// note is a package's row in the table: its own failing tests, what stopped it,
// and how many of its failures were put aside.
func note(r result) string {
	var parts []string
	if mine := r.ours(); len(mine) > 0 {
		if len(mine) > 6 {
			mine = append(mine[:6:6], "…")
		}
		parts = append(parts, strings.Join(mine, ", "))
	}
	if r.Problem != "" {
		parts = append(parts, r.Problem)
	}
	// A result that came from `-emit` has no verdict to report: the Rust was
	// generated and nothing was built or run. Saying so is what lets one
	// document hold both, the packages a sweep had time for and the rest.
	if r.Built && r.ran() == 0 && r.Problem == "" {
		parts = append(parts, "emits; not run")
	}
	// A binary that ran fewer tests than the package has says so, whether or not
	// it admitted to stopping: the tests it never reached are not passing.
	if ran := r.ran(); ran > 0 && r.Tests > ran {
		parts = append(parts, fmt.Sprintf("reached %d of %d tests", ran, r.Tests))
	}
	if n := len(r.AlsoGc); n > 0 {
		parts = append(parts, fmt.Sprintf("%d gc fails here too", n))
	}
	if counting := r.allocs(); len(counting) > 0 {
		parts = append(parts, "counting allocations: "+strings.Join(counting, ", "))
	}
	return strings.Join(parts, "; ")
}

// blockers is the table that says what to fix first: every reason a package
// could not be built, with the packages it holds up. One missing linkname that
// stops eleven packages is worth more than an emitter case that stops one, and
// this is where that shows.
func blockers(results []result) string {
	byCause := map[string][]string{}
	for _, r := range results {
		for _, c := range r.Causes {
			byCause[c] = append(byCause[c], r.Pkg)
		}
	}
	if len(byCause) == 0 {
		return ""
	}
	causes := make([]string, 0, len(byCause))
	for c := range byCause {
		causes = append(causes, c)
	}
	sort.Slice(causes, func(i, j int) bool {
		if n, m := len(byCause[causes[i]]), len(byCause[causes[j]]); n != m {
			return n > m
		}
		return causes[i] < causes[j]
	})
	var b strings.Builder
	b.WriteString(`
## What the packages that do not build are waiting on

Every diagnostic from a package that failed to build, with the packages it
stops. A package blocked by four things appears against each of them.

| blocked | what | packages |
|---:|---|---|
`)
	for _, c := range causes {
		pkgs := byCause[c]
		shown := pkgs
		if len(shown) > 8 {
			shown = append(shown[:8:8], "…")
		}
		fmt.Fprintf(&b, "| %d | %s | %s |\n", len(pkgs), truncate(c, 100), truncate(strings.Join(shown, ", "), 90))
	}
	return b.String()
}

// order is the blockers in the order worth attacking them: at each step, the one
// thing that would let the most packages build.
//
// Counting the packages that mention a diagnostic overstates the cheap wins,
// because a package is freed only when every diagnostic it reported is gone.
// `text/template` names eleven missing `reflect` members and none of them buys
// anything alone; `reflect.Value.FieldByName` is the whole of what twelve
// packages are waiting for. A greedy pass says which is which: take the fix that
// frees the most packages, strike those off, and look again. A fix that frees
// nothing by itself is carried along until one of them completes a set, and the
// row then names all of them together.
func order(results []result) string {
	need := map[string]map[string]bool{}
	for _, r := range results {
		if r.Built || len(r.Causes) == 0 {
			continue
		}
		cs := map[string]bool{}
		for _, c := range r.Causes {
			cs[c] = true
		}
		need[r.Pkg] = cs
	}
	if len(need) == 0 {
		return ""
	}
	fixed, waiting := map[string]bool{}, []string(nil)
	var b strings.Builder
	for len(need) > 0 {
		mentions := map[string]int{}
		for _, cs := range need {
			for c := range cs {
				if !fixed[c] {
					mentions[c]++
				}
			}
		}
		if len(mentions) == 0 {
			break
		}
		// The best candidate frees the most packages; among equals, the one the
		// most packages mention, and then the first by name, so that the order
		// does not depend on how the maps were walked.
		best, freed := "", []string(nil)
		for c := range mentions {
			var free []string
			for pkg, cs := range need {
				enough := true
				for d := range cs {
					if d != c && !fixed[d] {
						enough = false
						break
					}
				}
				if enough {
					free = append(free, pkg)
				}
			}
			better := false
			switch {
			case best == "", len(free) > len(freed):
				better = true
			case len(free) < len(freed):
			case mentions[c] > mentions[best]:
				better = true
			case mentions[c] < mentions[best]:
			case c < best:
				better = true
			}
			if better {
				best, freed = c, free
			}
		}
		fixed[best] = true
		if len(freed) == 0 {
			waiting = append(waiting, best)
			continue
		}
		sort.Strings(freed)
		for _, pkg := range freed {
			delete(need, pkg)
		}
		shown := freed
		if len(shown) > 8 {
			shown = append(shown[:8:8], "…")
		}
		fmt.Fprintf(&b, "| %d | %s | %s |\n", len(freed),
			truncate(strings.Join(append(waiting, best), ", "), 170),
			truncate(strings.Join(shown, ", "), 120))
		waiting = nil
	}
	return `
## In what order

What each fix would free, taken greedily: the one that unblocks the most
packages first, then the most of what is left. A row with several things in it is
a set that only pays off whole.

| frees | fix | packages |
|---:|---|---|
` + b.String()
}

// readState reads the results a previous sweep recorded. A later entry for a
// package wins, so re-measuring one appends rather than rewrites the file.
func readState(path string) (map[string]result, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]result{}, nil
		}
		return nil, err
	}
	defer f.Close()
	done := map[string]result{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for s.Scan() {
		if strings.TrimSpace(s.Text()) == "" {
			continue
		}
		var r result
		if err := json.Unmarshal(s.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		done[r.Pkg] = r
	}
	return done, s.Err()
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

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return truncate(line, 140)
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
