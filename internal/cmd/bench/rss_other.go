//go:build !linux && !darwin

package main

import "os"

func watchRSS(int) func() int64 { return func() int64 { return 0 } }

func rusagePeak(*os.ProcessState) int64 { return 0 }
