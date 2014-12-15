// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This program prints to stdout from multiple goroutines multiplexed onto
// multiple threads. It is used as the tracee program by the debugger (tracer)
// program in the parent directory.
package main

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
)

// ansiColor is whether to display ANSI color codes in the program's output.
// This distinguishes this program's output (the tracee's) from the debugger
// program's output (the tracer's).
const ansiColor = false

var prefix, suffix string

func run(base int, sleep time.Duration, lockOSThread bool) {
	if lockOSThread {
		runtime.LockOSThread()
	}
	for i := 0; ; i++ {
		fmt.Printf("%sx=%5d tid=%d%s\n", prefix, base+i, syscall.Gettid(), suffix)
		time.Sleep(sleep)
	}
}

func main() {
	if ansiColor {
		prefix, suffix = "\x1b[36m", "\x1b[0m"
	}
	go run(0, 300*time.Millisecond, false)
	go run(100, 500*time.Millisecond, false)
	go run(10000, 700*time.Millisecond, true)
	time.Sleep(5 * time.Second)
}
