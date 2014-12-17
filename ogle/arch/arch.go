// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package arch contains architecture-specific definitions.
package arch // import "golang.org/x/debug/ogle/arch"

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

func (a *Architecture) Int16(buf []byte) int16 {
	return int16(a.Uint16(buf))
}

func (a *Architecture) Int32(buf []byte) int32 {
	return int32(a.Uint32(buf))
}

func (a *Architecture) Int64(buf []byte) int64 {
	return int64(a.Uint64(buf))
}

func (a *Architecture) Uint16(buf []byte) uint16 {
	return a.ByteOrder.Uint16(buf)
}

func (a *Architecture) Uint32(buf []byte) uint32 {
	return a.ByteOrder.Uint32(buf)
}

func (a *Architecture) Uint64(buf []byte) uint64 {
	return a.ByteOrder.Uint64(buf)
}

func (a *Architecture) IntN(buf []byte) int64 {
	return int64(a.UintN(buf))
}

func (a *Architecture) UintN(buf []byte) uint64 {
	u := uint64(0)
	if a.ByteOrder == binary.LittleEndian {
		shift := uint(0)
		for _, c := range buf {
			u |= uint64(c) << shift
			shift += 8
		}
	} else {
		for _, c := range buf {
			u <<= 8
			u |= uint64(c)
		}
	}
	return u
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
