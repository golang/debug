// Copyright 2025 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

// Tests to make sure pointers in big slices are handled correctly.

package main

import (
	"os"
	"runtime"

	"golang.org/x/debug/internal/testenv"
)

type bigSliceElem struct {
	x, y, z float64
}

var globalBigSlice []*bigSliceElem
var block chan struct{}

func main() {
	testenv.RunThenCrash(os.Getenv("GO_DEBUG_TEST_COREDUMP_FILTER"), func() any {
		globalBigSlice = *makeBigSlice()
		ready := make(chan []*bigSliceElem)
		go func() {
			// This funny incantation exists to force the bs0 and bs1 slice
			// headers to be deconstructed by the compiler and stored in pieces
			// on the stack. This tests whether gocore can piece it back
			// together.
			bsp0 := makeBigSlice()
			bsp1 := makeBigSlice()
			bs0 := *bsp0
			bs1 := *bsp1
			runtime.GC()
			ready <- bs0
			ready <- bs1
			runtime.KeepAlive(bs0)
			runtime.KeepAlive(bs1)
		}()
		<-ready

		return nil
	})
}

// This function signature looks weird, returning a pointer to a slice, but it's
// to try and force deconstruction of the slice value by the compiler in the caller.
// See callers of makeBigSlice.
//
//go:noinline
func makeBigSlice() *[]*bigSliceElem {
	bs := make([]*bigSliceElem, 32<<10)
	for i := range bs {
		bs[i] = &bigSliceElem{float64(i), float64(i) - 0.5, float64(i * 124)}
	}
	return &bs
}
