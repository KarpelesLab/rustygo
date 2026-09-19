// Package goroot builds the overlay that swaps rustygo's own versions of a
// few standard-library packages in for gc's, without copying GOROOT.
//
// The packages at the bottom of the standard library are written for gc: the
// `runtime` itself, and the packages that read gc's type descriptors or call
// into it through `go:linkname`. rustygo ships replacements for those under
// _overlay/<import path>/ (DESIGN §8). The go command's file overlay then
// hides every file of the real package and adds the replacement's files in
// its place, so the go/packages load sees rustygo's package at the real
// import path and every importer compiles against it unchanged.
//
// A replacement is total unless its directory has a hide.txt, which then
// lists the only files of gc's package to hide: sync/atomic keeps gc's types
// and replaces just the files that declare assembly.
//
// The directory starts with `_` so the go tool never builds it as part of
// this module: replacements may declare functions without bodies, which the
// emitter resolves to runtime intrinsics.
package goroot

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

//go:embed all:_overlay
var overlayFS embed.FS

// ignored stands in for every file of a replaced package: excluded by its
// build constraint, so the package consists of the replacement alone.
const ignored = "//go:build ignore\n\npackage ignored\n"

// Replaced lists the import paths rustygo replaces, sorted.
func Replaced() []string {
	var pkgs []string
	fs.WalkDir(overlayFS, "_overlay", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if hasGoFiles(p) {
			pkgs = append(pkgs, strings.TrimPrefix(p, "_overlay/"))
		}
		return nil
	})
	sort.Strings(pkgs)
	return pkgs
}

func hasGoFiles(dir string) bool {
	entries, _ := overlayFS.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// Overlay returns the file overlay for go/packages: every Go file of each
// replaced package hidden, and the replacement's files added.
func Overlay() (map[string][]byte, error) {
	root, err := GOROOT()
	if err != nil {
		return nil, err
	}
	ov := map[string][]byte{}
	for _, pkg := range Replaced() {
		real := filepath.Join(root, "src", filepath.FromSlash(pkg))
		entries, err := os.ReadDir(real)
		if err != nil {
			return nil, fmt.Errorf("replacing %s: %w", pkg, err)
		}
		hide := func(string) bool { return true }
		if list, err := overlayFS.ReadFile(path.Join("_overlay", pkg, "hide.txt")); err == nil {
			only := map[string]bool{}
			for _, name := range strings.Fields(string(list)) {
				only[name] = true
			}
			hide = func(name string) bool { return only[name] }
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && hide(e.Name()) {
				ov[filepath.Join(real, e.Name())] = []byte(ignored)
			}
		}
		files, err := overlayFS.ReadDir(path.Join("_overlay", pkg))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".go") {
				continue
			}
			data, err := overlayFS.ReadFile(path.Join("_overlay", pkg, f.Name()))
			if err != nil {
				return nil, err
			}
			// A name gc's package cannot have, so it never collides with a
			// hidden file.
			ov[filepath.Join(real, "zz_rustygo_"+f.Name())] = data
		}
	}
	return ov, nil
}

var goroot struct {
	once sync.Once
	dir  string
	err  error
}

// GOROOT is the go command's GOROOT, which is the standard library the load
// sees.
func GOROOT() (string, error) {
	goroot.once.Do(func() {
		out, err := exec.Command("go", "env", "GOROOT").Output()
		if err != nil {
			goroot.err = fmt.Errorf("go env GOROOT: %w", err)
			return
		}
		goroot.dir = strings.TrimSpace(string(out))
	})
	return goroot.dir, goroot.err
}
