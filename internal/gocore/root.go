// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocore

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/debug/internal/core"
)

// A Root is an area of memory that might have pointers into the Go heap.
type Root struct {
	Name string
	Type *Type // always non-nil
	// Frame, if non-nil, points to the frame in which this root lives.
	// Roots with non-nil Frame fields refer to local variables on a stack.
	// A stack root might be a large type, with some of its fields live and
	// others dead. Consult Frame.Live to find out which pointers in a stack
	// root are live.
	Frame *Frame

	pieces []rootPiece
	id     int
}

// HasAddress returns true if the root is simple and contiguous, and can be
// described with just a single address.
func (r *Root) HasAddress() bool {
	return len(r.pieces) == 1 && r.pieces[0].kind == addrPiece
}

// Addr returns the address of the root, if it has one.
func (r *Root) Addr() core.Address {
	if r.HasAddress() {
		return core.Address(r.pieces[0].value)
	}
	return 0
}

func (p *Process) makeMemRoot(name string, typ *Type, fr *Frame, addr core.Address) *Root {
	return makeMemRoot(&p.nRoots, name, typ, fr, addr)
}

func makeMemRoot(nRoots *int, name string, typ *Type, fr *Frame, addr core.Address) *Root {
	r := &Root{
		Name:   name,
		Type:   typ,
		Frame:  fr,
		pieces: []rootPiece{{size: typ.Size, kind: addrPiece, value: uint64(addr)}},
		id:     *nRoots,
	}
	*nRoots += 1
	return r
}

func (p *Process) makeCompositeRoot(name string, typ *Type, fr *Frame, pieces []rootPiece) *Root {
	r := &Root{
		Name:   name,
		Type:   typ,
		Frame:  fr,
		pieces: pieces,
		id:     p.nRoots,
	}
	p.nRoots++
	return r
}

func (pr *Process) readRootPtr(r *Root, offset int64) core.Address {
	// TODO(mknyszek): Little-endian only.
	ptrBuf := make([]byte, pr.proc.PtrSize())
	pr.readRootAt(r, ptrBuf, offset)
	if pr.proc.PtrSize() == 4 {
		return core.Address(binary.LittleEndian.Uint32(ptrBuf))
	}
	return core.Address(binary.LittleEndian.Uint64(ptrBuf))
}

// ReadRootAt reads data out of this root. offset+len(b) must be less than r.Type.Size.
// Returns the address read from, if the read was contiguous and from memory.
func (pr *Process) readRootAt(r *Root, b []byte, offset int64) core.Address {
	if offset+int64(len(b)) > r.Type.Size {
		panic("invalid range to read from root")
	}
	if len(b) == 0 {
		return 0
	}
	bOff := int64(0)
	var addr core.Address
	first := true
	for _, p := range r.pieces {
		if offset > p.off+p.size {
			continue
		}
		if offset+int64(len(b)) <= p.off {
			break
		}
		pOff := max(p.off, offset)
		base := pOff - p.off
		rlen := min(int64(len(b))-bOff, p.size-base)
		switch p.kind {
		case addrPiece:
			pr.proc.ReadAt(b[bOff:bOff+rlen], core.Address(p.value).Add(base))
			if first {
				addr = core.Address(p.value).Add(base)
			} else {
				addr = 0
			}
		case regPiece, immPiece:
			// TODO(mknyszek): Supports little-endian only.
			v := ((*[8]byte)(unsafe.Pointer(&p.value)))[:p.size]
			copy(b[bOff:bOff+rlen], v[base:base+rlen])
			addr = 0
		}
		if first {
			first = false
		}
		bOff += rlen
		if bOff == int64(len(b)) {
			break
		}
	}
	return addr
}

// walkRootTypePtrs calls fn for the edges found in an object of type t living at offset off in the root r.
// If fn returns false, return immediately with false.
func walkRootTypePtrs(p *Process, r *Root, ptrBuf []byte, off int64, t *Type, fn func(int64, core.Address) bool) bool {
	switch t.Kind {
	case KindBool, KindInt, KindUint, KindFloat, KindComplex:
		// no edges here
	case KindIface, KindEface:
		// The first word is a type or itab.
		// Itabs are never in the heap.
		// Types might be, though.
		// We have no idea about the liveness of registers, when a == 0.
		a := p.readRootAt(r, ptrBuf[:p.proc.PtrSize()], off)
		if a != 0 && (r.Frame == nil || r.Frame.Live[a]) {
			var ptr core.Address
			if p.proc.PtrSize() == 4 {
				ptr = core.Address(binary.LittleEndian.Uint32(ptrBuf[:]))
			} else {
				ptr = core.Address(binary.LittleEndian.Uint64(ptrBuf[:]))
			}
			if !fn(off, ptr) {
				return false
			}
		}
		// Treat second word like a pointer.
		off += p.proc.PtrSize()
		fallthrough
	case KindPtr, KindString, KindSlice, KindFunc:
		a := p.readRootAt(r, ptrBuf[:p.proc.PtrSize()], off)
		if a != 0 && (r.Frame == nil || r.Frame.Live[a]) {
			var ptr core.Address
			if p.proc.PtrSize() == 4 {
				ptr = core.Address(binary.LittleEndian.Uint32(ptrBuf[:]))
			} else {
				ptr = core.Address(binary.LittleEndian.Uint64(ptrBuf[:]))
			}
			if !fn(off, ptr) {
				return false
			}
		}
	case KindArray:
		s := t.Elem.Size
		for i := int64(0); i < t.Count; i++ {
			if !walkRootTypePtrs(p, r, ptrBuf, off+i*s, t.Elem, fn) {
				return false
			}
		}
	case KindStruct:
		for _, f := range t.Fields {
			if !walkRootTypePtrs(p, r, ptrBuf, off+f.Off, f.Type, fn) {
				return false
			}
		}
	}
	return true
}

type rootPieceKind int

const (
	addrPiece rootPieceKind = iota
	regPiece
	immPiece
)

type rootPiece struct {
	off   int64         // Logical offset into the root, or specifically the root's Type.
	size  int64         // Size of the piece.
	kind  rootPieceKind // Where the piece is.
	value uint64        // Address if kind == AddrPiece, value if kind == RegPiece or kind == ImmPiece.
}
