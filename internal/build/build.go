// Package build drives a whole compilation: emit the Rust crate, then build
// it with cargo.
package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/KarpelesLab/rustygo"
	"github.com/KarpelesLab/rustygo/internal/emit"
	"github.com/KarpelesLab/rustygo/internal/load"
)

// CacheDir is where rustygo keeps the extracted runtime, generated crates and
// the shared cargo target directory: $RUSTYGO_CACHE, or rustygo/ under the
// user cache directory.
func CacheDir() (string, error) { return Config{}.cacheDir() }

func (cfg Config) cacheDir() (string, error) {
	if cfg.Cache != "" {
		return cfg.Cache, nil
	}
	if d := os.Getenv("RUSTYGO_CACHE"); d != "" {
		return d, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "rustygo"), nil
}

// RuntimeDir extracts the embedded runtime crate, once per content hash, and
// returns its directory.
func RuntimeDir() (string, error) { return Config{}.RuntimeDir() }

// RuntimeDir extracts the runtime into this configuration's cache.
func (cfg Config) RuntimeDir() (string, error) {
	cache, err := cfg.cacheDir()
	if err != nil {
		return "", err
	}
	files := map[string][]byte{}
	err = fs.WalkDir(rustygo.Runtime, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files[path], err = fs.ReadFile(rustygo.Runtime, path)
		return err
	})
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(files[n]))
		h.Write(files[n])
	}
	dir := filepath.Join(cache, "runtime-"+hex.EncodeToString(h.Sum(nil))[:16])
	if _, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(cache, "runtime-tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	for _, n := range names {
		p := filepath.Join(tmp, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, files[n], 0o644); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Lost a race with another extraction of the same content.
		if _, statErr := os.Stat(filepath.Join(dir, "Cargo.toml")); statErr == nil {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}

// Config selects code-generation options. The shadow stack, which M0
// measured behind a switch, is mandatory now that the collector reads it.
type Config struct {
	// GcTorture builds against the runtime's debug collector, which
	// collects at every allocation.
	GcTorture bool
	// Cache overrides the cache directory. Builds running at the same time
	// need one each: cargo locks its target directory.
	Cache string
}

// ConfigFromEnv reads RUSTYGO_GCTORTURE=1.
func ConfigFromEnv() Config {
	return Config{GcTorture: os.Getenv("RUSTYGO_GCTORTURE") == "1"}
}

// Emit writes the crate for res into dir.
func Emit(res *load.Result, dir string, cfg Config) error {
	rt, err := cfg.RuntimeDir()
	if err != nil {
		return err
	}
	return emit.Crate(res, emit.Options{OutDir: dir, RuntimePath: rt, BinName: "main", GcTorture: cfg.GcTorture})
}

// Binary compiles res to a native executable at output.
func Binary(res *load.Result, output string, cfg Config) error {
	rt, err := cfg.RuntimeDir()
	if err != nil {
		return err
	}
	cache, err := cfg.cacheDir()
	if err != nil {
		return err
	}
	work, id := workDir(cache, res, cfg)
	// One binary name per work directory, so builds sharing the cargo
	// target directory never overwrite each other's output.
	bin := "p" + id
	if err := emit.Crate(res, emit.Options{OutDir: work, RuntimePath: rt, BinName: bin, GcTorture: cfg.GcTorture}); err != nil {
		return err
	}

	target := filepath.Join(cache, "target")
	cmd := exec.Command("cargo", "build", "--release", "--quiet", "--manifest-path", filepath.Join(work, "Cargo.toml"))
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cargo build failed (crate in %s): %w\n%s", work, err, stderr.Bytes())
	}
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return copyFile(filepath.Join(target, "release", bin), output)
}

// WorkDir is where Binary writes the crate for res.
func WorkDir(res *load.Result, cfg Config) (string, error) {
	cache, err := cfg.cacheDir()
	if err != nil {
		return "", err
	}
	dir, _ := workDir(cache, res, cfg)
	return dir, nil
}

// workDir picks one work directory per set of main packages and config.
func workDir(cache string, res *load.Result, cfg Config) (dir, id string) {
	key := fmt.Sprintf("%+v\x00", cfg)
	for _, p := range res.Pkgs {
		key += p.Pkg.Path() + "\x00"
	}
	sum := sha256.Sum256([]byte(key))
	id = hex.EncodeToString(sum[:])[:16]
	return filepath.Join(cache, "work", id), id
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	// Remove first: overwriting a running or read-only binary in place fails.
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}
