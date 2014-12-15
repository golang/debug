// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package probe is imported by programs to provide (possibly remote)
// access to a separate debugger program.
package probe

import (
	"runtime/debug"
	"unsafe"
)

// catchFault is used by the read and write routines to turn a panic into an error return.
func catchFault(ok *bool) {
	if e := recover(); e != nil {
		*ok = false
	}
}

// validRead reports whether a read of the specified size can be done at address p.
// TODO: It does this by actually doing the read and seeing if it succeeds. Do better.
func validRead(p uintptr, size int) (ok bool) {
	// Check for negative size and for (p + size) overflow.
	if size < 0 || uint64(^uintptr(0)-p) < uint64(size) {
		return false
	}
	defer catchFault(&ok)
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	ep := p + uintptr(size)
	var b byte
	for p < ep {
		b = *(*byte)(unsafe.Pointer(p))
		_ = b
		p++
	}
	return true
}

// validWrite reports whether a write of the specified size can be done at address p.
// TODO: It does this by actually doing a write and seeing if it succeeds. Do better.
func validWrite(p uintptr, size int) (ok bool) {
	// Check for negative size and for (p + size) overflow.
	if size < 0 || uint64(^uintptr(0)-p) < uint64(size) {
		return false
	}
	defer catchFault(&ok)
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	ep := p + uintptr(size)
	for p < ep {
		*(*byte)(unsafe.Pointer(p)) = *(*byte)(unsafe.Pointer(p))
		p++
	}
	return true
}

// read copies into the argument buffer the contents of memory starting at address p.
// Its boolean return tells whether it succeeded. If it fails, no bytes were copied.
func read(p uintptr, buf []byte) (ok bool) {
	defer catchFault(&ok)
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	for i := range buf {
		buf[i] = *(*byte)(unsafe.Pointer(p))
		p++
	}
	return true
}

// write copies the argument buffer to memory starting at address p.
// Its boolean return tells whether it succeeded. If it fails, no bytes were copied.
func write(p uintptr, buf []byte) (ok bool) {
	defer catchFault(&ok)
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	for i := range buf {
		*(*byte)(unsafe.Pointer(p)) = buf[i]
		p++
	}
	return true
}
