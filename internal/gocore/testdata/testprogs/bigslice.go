// Copyright 2025 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

// Tests to make sure pointers in big slices are handled correctly.

package main

import (
	"os"

	"golang.org/x/debug/internal/testenv"
)

type bigSliceElem struct {
	x, y, z float64
}

var globalBigSlice []*bigSliceElem

func main() {
	testenv.RunThenCrash(os.Getenv("GO_DEBUG_TEST_COREDUMP_FILTER"), func() any {
		globalBigSlice = make([]*bigSliceElem, 32<<10)
		for i := range globalBigSlice {
			globalBigSlice[i] = &bigSliceElem{float64(i), float64(i) - 0.5, float64(i * 124)}
		}
		return nil
	})
}
