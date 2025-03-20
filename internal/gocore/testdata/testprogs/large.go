// Copyright 2025 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

// Tests to make sure a large object referenced only from a stack
// can be found.

package main

import (
	"os"

	"golang.org/x/debug/internal/testenv"
)

// Large is an object that (since Go 1.22) is allocated in a span that has a
// non-nil largeType field. Meaning it must be (>maxSmallSize-mallocHeaderSize).
// At the time of writing this is (32768 - 8).
type Large struct {
	ptr *uint8 // Object must contain a pointer to trigger code path.
	arr [32768 - 8]uint8
}

func useLarge(o *Large, ready chan<- struct{}) {
	o.ptr = &o.arr[5]
	o.arr[5] = 0xCA
	ready <- struct{}{}
	<-block
}

var block = make(chan struct{})

func main() {
	testenv.RunThenCrash(os.Getenv("GO_DEBUG_TEST_COREDUMP_FILTER"), func() any {
		ready := make(chan struct{})

		// Create a large value and reference
		var o Large
		go useLarge(&o, ready) // Force an escape of o.
		o.arr[14] = 0xDE       // Prevent a future smart compiler from allocating o directly on useLarge's stack.

		<-ready
		return &o
	})
}
