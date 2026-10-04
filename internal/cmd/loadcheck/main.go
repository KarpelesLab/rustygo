// Command loadcheck says whether a package's test binary would type-check and
// emit, without handing the crate to cargo.
//
//	go run ./internal/cmd/loadcheck os time net/url
//
// The two failures a package hits before anything runs are a load error — its
// source names something rustygo's overlay does not provide — and a missing
// body, which the emitter reports for an assembly function with no Rust
// implementation and no Go fallback. Both come out of the compiler in seconds,
// while the rustc run behind them takes minutes, so this is the loop to work
// in while chasing either one.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/KarpelesLab/rustygo/internal/build"
	"github.com/KarpelesLab/rustygo/internal/load"
)

func main() {
	status := 0
	for _, pkg := range os.Args[1:] {
		if err := check(pkg); err != nil {
			fmt.Printf("%s: %v\n", pkg, err)
			status = 1
			continue
		}
		fmt.Printf("%s: ok\n", pkg)
	}
	os.Exit(status)
}

func check(pkg string) error {
	res, err := load.LoadTests("", pkg)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	// The crate is written to a directory of its own and thrown away: what is
	// wanted here is the errors the emitter finds on the way, not the Rust.
	dir, err := os.MkdirTemp("", "loadcheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := build.Emit(res, filepath.Join(dir, "crate"), build.ConfigFromEnv()); err != nil {
		return fmt.Errorf("emit: %w", err)
	}
	return nil
}
