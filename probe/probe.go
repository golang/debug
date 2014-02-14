// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package probe is imported by programs to provide (possibly remote)
// access to a separate debugger program.
package probe

import (
	"unsafe"
)

// Defined in assembler.
func base() uintptr
func etext() uintptr
func edata() uintptr
func end() uintptr

func heapStart() uintptr
func heapUsed() uintptr
func heapEnd() uintptr

// validRead reports whether a read of the specified size can be done at address p.
func validRead(p uintptr, size int) bool {
	// Check for negative size and for (p + size) overflow.
	if size < 0 || uint64(^uintptr(0)-p) < uint64(size) {
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
	// Check for negative size and for (p + size) overflow.
	if size < 0 || uint64(^uintptr(0)-p) < uint64(size) {
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

// read copies into the argument buffer the contents of memory starting at address p.
// Its boolean return tells whether it succeeded. If it fails, no bytes were copied.
func read(p uintptr, buf []byte) bool {
	if !validRead(p, len(buf)) {
		return false
	}
	for i := range buf {
		buf[i] = *(*byte)(unsafe.Pointer(p))
		p++
	}
	return true
}

// write copies the argument buffer to memory starting at address p.
// Its boolean return tells whether it succeeded. If it fails, no bytes were copied.
func write(p uintptr, buf []byte) bool {
	if !validWrite(p, len(buf)) {
		return false
	}
	for i := range buf {
		*(*byte)(unsafe.Pointer(p)) = buf[i]
		p++
	}
	return true
}
