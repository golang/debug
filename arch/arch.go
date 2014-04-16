// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package arch contains architecture-specific definitions.
package arch

import (
	"encoding/binary"
)

const MaxBreakpointSize = 4 // TODO

// Architecture defines the architecture-specific details for a given machine.
type Architecture struct {
	// BreakpointSize is the size of a breakpoint instruction, in bytes.
	BreakpointSize int
	// IntSize is the size of the int type, in bytes.
	IntSize int
	// PointerSize is the size of a pointer, in bytes.
	PointerSize int
	// ByteOrder is the byte order for ints and pointers.
	ByteOrder       binary.ByteOrder
	BreakpointInstr [MaxBreakpointSize]byte
}

func (a *Architecture) Int(buf []byte) int64 {
	return int64(a.Uint(buf))
}

func (a *Architecture) Uint(buf []byte) uint64 {
	if len(buf) != a.IntSize {
		panic("bad IntSize")
	}
	switch a.IntSize {
	case 4:
		return uint64(a.ByteOrder.Uint32(buf[:4]))
	case 8:
		return a.ByteOrder.Uint64(buf[:8])
	}
	panic("no IntSize")
}

func (a *Architecture) Uintptr(buf []byte) uint64 {
	if len(buf) != a.PointerSize {
		panic("bad PointerSize")
	}
	switch a.PointerSize {
	case 4:
		return uint64(a.ByteOrder.Uint32(buf[:4]))
	case 8:
		return a.ByteOrder.Uint64(buf[:8])
	}
	panic("no PointerSize")
}

var AMD64 = Architecture{
	BreakpointSize:  1,
	IntSize:         8,
	PointerSize:     8,
	ByteOrder:       binary.LittleEndian,
	BreakpointInstr: [MaxBreakpointSize]byte{0xCC}, // INT 3
}

var X86 = Architecture{
	BreakpointSize:  1,
	IntSize:         4,
	PointerSize:     4,
	ByteOrder:       binary.LittleEndian,
	BreakpointInstr: [MaxBreakpointSize]byte{0xCC}, // INT 3
}

var ARM = Architecture{
	BreakpointSize:  4, // TODO
	IntSize:         4,
	PointerSize:     4,
	ByteOrder:       binary.LittleEndian,
	BreakpointInstr: [MaxBreakpointSize]byte{0x00, 0x00, 0x00, 0x00}, // TODO
}
