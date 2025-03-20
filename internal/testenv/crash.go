// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package testenv

import (
	"os"
	"runtime"
)

// RunThenCrash sets the provided core dump filter (optional) for
// the process, runs f, then crashes.
//
// The slice returned by f is kept alive across the crash.
func RunThenCrash(coredumpFilter string, f func() any) {
	// Set coredump filter (Linux only).
	if runtime.GOOS == "linux" && coredumpFilter != "" {
		if err := os.WriteFile("/proc/self/coredump_filter", []byte(coredumpFilter), 0600); err != nil {
			os.Stderr.WriteString("crash: unable to set coredump_filter: ")
			os.Stderr.WriteString(err.Error())
			os.Stderr.WriteString("\n")
			os.Exit(0) // Don't crash (which is an error for the called).
		}
	}

	// Run f.
	result := f()
	crash()
	runtime.KeepAlive(result)
}

// Crash crashes the program.
//
// Make it noinline so registers are spilled before entering, otherwise imprecise DWARF will be our doom.
// Delve has trouble with this too; 'result' in RunThenCrash won't be visible otherwise.
//
//go:noinline
func crash() {
	_ = *(*int)(nil)
}
