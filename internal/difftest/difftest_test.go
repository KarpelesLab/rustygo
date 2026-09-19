// Package difftest is the differential harness: every program under
// testdata/programs is built with gc and with rustygo, both binaries run, and
// their stdout, stderr and exit status must match.
//
// gc's goroutine traces are not compared. stderr is cut at the first
// "\n[signal " or "\n\ngoroutine " line, which keeps the whole panic message
// and everything printed before it.
//
// testdata/programs/PASSING is a ratchet: a listed program that stops passing
// fails the test; an unlisted program that passes is reported so it can be
// added. The list only grows.
package difftest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KarpelesLab/rustygo/internal/build"
	"github.com/KarpelesLab/rustygo/internal/load"
)

const (
	moduleRoot  = "../.."
	programsDir = "testdata/programs"
	runTimeout  = 30 * time.Second
)

type outcome struct {
	stdout, stderr string
	exit           int
}

func TestPrograms(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not found")
	}
	dir := filepath.Join(moduleRoot, programsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	passing, err := readPassing(filepath.Join(dir, "PASSING"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	for name := range passing {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("PASSING lists %q, which does not exist", name)
		}
	}

	tmp := t.TempDir()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	var pass, newlyPassing []string
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			pkg := "./" + programsDir + "/" + name
			gcBin := filepath.Join(tmp, name+"-gc"+exe)
			goBuild := exec.Command("go", "build", "-o", gcBin, pkg)
			goBuild.Dir = moduleRoot
			if out, err := goBuild.CombinedOutput(); err != nil {
				t.Fatalf("gc build failed: %v\n%s", err, out)
			}
			want := run(t, gcBin, false)

			problem := ""
			rgBin := filepath.Join(tmp, name+"-rustygo"+exe)
			res, err := load.Load(moduleRoot, pkg)
			if err == nil {
				err = build.Binary(res, rgBin, build.ConfigFromEnv())
			}
			if err != nil {
				problem = "rustygo build failed: " + err.Error()
			} else {
				problem = diff(want, run(t, rgBin, true))
			}

			switch {
			case problem == "":
				pass = append(pass, name)
				if !passing[name] {
					newlyPassing = append(newlyPassing, name)
				}
			case passing[name]:
				t.Errorf("%s regressed (listed in PASSING):\n%s", name, problem)
			default:
				t.Skipf("not passing yet: %s", firstLine(problem))
			}
		})
	}
	t.Logf("%d/%d programs pass", len(pass), len(names))
	if len(newlyPassing) > 0 {
		sort.Strings(newlyPassing)
		t.Logf("now passing, add to %s/PASSING: %s", programsDir, strings.Join(newlyPassing, " "))
	}
}

// run executes a test binary. A rustygo-built one runs under a memory cap
// (build.TestCommand), so a bug that computes a wild allocation size fails
// in the child rather than asking the host for it.
func run(t *testing.T, bin string, capped bool) outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	if capped {
		cmd = build.TestCommand(ctx, bin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exit := 0
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		t.Fatalf("%s: timed out after %v", filepath.Base(bin), runTimeout)
	case errors.As(err, &ee):
		exit = ee.ExitCode()
	case err != nil:
		t.Fatalf("%s: %v", filepath.Base(bin), err)
	}
	return outcome{stdout: stdout.String(), stderr: normalizeStderr(stderr.String()), exit: exit}
}

// normalizeStderr drops gc's signal line and goroutine traces, which rustygo
// does not reproduce.
func normalizeStderr(s string) string {
	for _, marker := range []string{"\n[signal ", "\n\ngoroutine "} {
		if i := strings.Index(s, marker); i >= 0 {
			s = s[:i+1]
		}
	}
	return s
}

func diff(want, got outcome) string {
	var b strings.Builder
	if want.exit != got.exit {
		fmt.Fprintf(&b, "exit status: gc %d, rustygo %d\n", want.exit, got.exit)
	}
	if want.stdout != got.stdout {
		fmt.Fprintf(&b, "stdout differs:\n--- gc\n%s--- rustygo\n%s", want.stdout, got.stdout)
	}
	if want.stderr != got.stderr {
		fmt.Fprintf(&b, "stderr differs:\n--- gc\n%s--- rustygo\n%s", want.stderr, got.stderr)
	}
	return b.String()
}

func readPassing(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			m[line] = true
		}
	}
	return m, sc.Err()
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
