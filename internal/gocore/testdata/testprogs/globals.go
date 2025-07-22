// Copyright 2025 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

// Tests to make sure we can tell which globals have pointers.

package main

import (
	"os"
	"unsafe"

	"golang.org/x/debug/internal/testenv"
)

var string_ string = "foo" // string type, in data section
var slice []byte           // slice type, in bss section
var struct_ struct {       // a more complicated layout
	a, b, c uintptr
	d       *byte
	e       uintptr
	f       unsafe.Pointer
	g, h    uintptr
}

func main() {
	testenv.RunThenCrash(os.Getenv("GO_DEBUG_TEST_COREDUMP_FILTER"), func() any {
		// Reference globals so they don't get deadcoded.
		string_ = "bar"
		slice = []byte{1, 2, 3}
		struct_.a = 3
		return nil
	})
}
