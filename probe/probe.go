// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package probe is imported by programs to provide (possibly remote)
// access to a separate debugger program.
package main

func base() uintptr
func etext() uintptr
func edata() uintptr
func end() uintptr

func heapStart() uintptr
func heapUsed() uintptr
func heapEnd() uintptr

// validRead reports whether a read of the specified size can be done at address p.
func validRead(p uintptr, size int) bool {
	if size <= 0 {
		return false
	}
	// The read must be in a single contiguous valid region.
	switch {
	case base() <= p && p < end():
		// Assumes text is before data, but ld's binaries always satisfy that constraint.
		p += uintptr(size)
		return base() <= p && p <= end()
	case heapStart() <= p && p < heapUsed(): // Don't allow reads past the used part of the heap.
		p += uintptr(size)
		return heapStart() <= p && p <= heapUsed()
	}
	return false
}

// validWrite reports whether a write of the specified size can be done at address p.
func validWrite(p uintptr, size int) bool {
	if size <= 0 {
		return false
	}
	// The write must be in a single contiguous valid region.
	switch {
	case etext() <= p && p < end():
		// Assumes text is before data, but ld's binaries always satisfy that constraint.
		p += uintptr(size)
		return etext() <= p && p <= end()
	case heapStart() <= p && p < heapUsed(): // Don't allow writes past the used part of the heap.
		p += uintptr(size)
		return heapStart() <= p && p <= heapUsed()
	}
	return false
}
