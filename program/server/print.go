// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

import (
	"bytes"
	"fmt"
	"math"

	"code.google.com/p/ogle/arch"
	"code.google.com/p/ogle/debug/dwarf"
)

// Routines to print a value using DWARF type descriptions.
// TODO: Does this deserve its own package? It has no dependencies on Server.

// A Printer pretty-prints a values in the target address space.
// It can be reused after each printing operation to avoid unnecessary
// allocations. However, it is not safe for concurrent access.
type Printer struct {
	err      error // Sticky error value.
	peeker   Peeker
	dwarf    *dwarf.Data
	arch     *arch.Architecture
	printBuf bytes.Buffer     // Accumulates the output.
	tmp      []byte           // Temporary used for I/O.
	visited  map[uintptr]bool // Prevents looping on cyclic data.
}

// printf prints to printBuf, unless there has been an error.
func (p *Printer) printf(format string, args ...interface{}) {
	if p.err != nil {
		return
	}
	fmt.Fprintf(&p.printBuf, format, args...)
}

// errorf sets the sticky error for the printer, if not already set.
func (p *Printer) errorf(format string, args ...interface{}) {
	if p.err != nil {
		return
	}
	p.err = fmt.Errorf(format, args...)
}

// ok checks the error. If it is the first non-nil error encountered,
// it is printed to printBuf, parenthesized for discrimination, and remembered.
func (p *Printer) ok(err error) bool {
	if p.err == nil && err != nil {
		p.printf("(%s)", err)
		p.err = err
	}
	return p.err == nil
}

// peek reads len bytes at offset, leaving p.tmp with the data and sized appropriately.
func (p *Printer) peek(offset uintptr, len int64) bool {
	p.tmp = p.tmp[:len]
	return p.ok(p.peeker.peek(offset, p.tmp))
}

// Peeker is like a read that probes the remote address space.
type Peeker interface {
	peek(offset uintptr, buf []byte) error
}

// NewPrinter returns a printer that can use the Peeker to access and print
// values of the specified architecture described by the provided DWARF data.
func NewPrinter(arch *arch.Architecture, dwarf *dwarf.Data, peeker Peeker) *Printer {
	return &Printer{
		peeker:  peeker,
		arch:    arch,
		dwarf:   dwarf,
		visited: make(map[uintptr]bool),
		tmp:     make([]byte, 100), // Enough for a largish string.
	}
}

// reset resets the Printer. It must be called before starting a new
// printing operation.
func (p *Printer) reset() {
	p.err = nil
	p.printBuf.Reset()
	// Just wipe the map rather than reallocating. It's almost always tiny.
	for k := range p.visited {
		delete(p.visited, k)
	}
}

// Sprint returns the pretty-printed value of the item with the given name, such as "main.global".
func (p *Printer) Sprint(name string) (string, error) {
	entry, err := p.dwarf.LookupEntry(name)
	if err != nil {
		return "", err
	}
	p.reset()
	switch entry.Tag {
	case dwarf.TagVariable: // TODO: What other entries have global location attributes?
		addr := uintptr(0)
		iface := entry.Val(dwarf.AttrLocation)
		if iface != nil {
			addr = p.decodeLocation(iface.([]byte))
		}
		p.printEntryValueAt(entry, addr)
	default:
		p.errorf("unrecognized entry type %s", entry.Tag)
	}
	return p.printBuf.String(), p.err
}

// Figure 24 of DWARF v4.
const (
	locationAddr = 0x03
)

// decodeLocation decodes the dwarf data describing an address.
func (p *Printer) decodeLocation(data []byte) uintptr {
	switch data[0] {
	case locationAddr:
		return uintptr(p.arch.Uintptr(data[1:]))
	default:
		p.errorf("unimplemented location type %#x", data[0])
	}
	return 0
}

// SprintEntry returns the pretty-printed value of the item with the specified DWARF Entry and address.
func (p *Printer) SprintEntry(entry *dwarf.Entry, addr uintptr) (string, error) {
	p.reset()
	p.printEntryValueAt(entry, addr)
	return p.printBuf.String(), p.err
}

// printEntryValueAt pretty-prints the data at the specified addresss
// using the type information in the Entry.
func (p *Printer) printEntryValueAt(entry *dwarf.Entry, addr uintptr) {
	if addr == 0 {
		p.printf("<nil>")
		return
	}
	switch entry.Tag {
	case dwarf.TagVariable, dwarf.TagFormalParameter:
		// OK
	default:
		p.errorf("unrecognized entry type %s", entry.Tag)
		return
	}
	iface := entry.Val(dwarf.AttrType)
	if iface == nil {
		p.errorf("no type")
		return
	}
	typ, err := p.dwarf.Type(iface.(dwarf.Offset))
	if err != nil {
		p.errorf("type lookup: %v", err)
		return
	}
	p.printValueAt(typ, addr)
}

// printValueAt pretty-prints the data at the specified addresss
// using the provided type information.
func (p *Printer) printValueAt(typ dwarf.Type, addr uintptr) {
	// Make sure we don't recur forever.
	if p.visited[addr] {
		p.printf("(@%x...)", addr)
		return
	}
	switch typ := typ.(type) {
	case *dwarf.BoolType:
		if typ.ByteSize != 1 {
			p.errorf("unrecognized bool size %d", typ.ByteSize)
			return
		}
		if p.peek(addr, 1) {
			p.printf("%t", p.tmp[0] != 0)
		}
	case *dwarf.PtrType:
		// This type doesn't define the ByteSize attribute.
		if p.peek(addr, int64(p.arch.PointerSize)) {
			p.printf("%#x", p.arch.Uintptr(p.tmp))
		}
	case *dwarf.IntType:
		// Sad we can't tell a rune from an int32.
		if p.peek(addr, typ.ByteSize) {
			p.printf("%d", p.arch.IntN(p.tmp))
		}
	case *dwarf.UintType:
		if p.peek(addr, typ.ByteSize) {
			p.printf("%d", p.arch.UintN(p.tmp))
		}
	case *dwarf.FloatType:
		if !p.peek(addr, typ.ByteSize) {
			return
		}
		switch typ.ByteSize {
		case 4:
			p.printf("%g", math.Float32frombits(uint32(p.arch.UintN(p.tmp))))
		case 8:
			p.printf("%g", math.Float64frombits(p.arch.UintN(p.tmp)))
		default:
			p.errorf("unrecognized float size %d", typ.ByteSize)
		}
	case *dwarf.ComplexType:
		if !p.peek(addr, typ.ByteSize) {
			return
		}
		switch typ.ByteSize {
		case 8:
			r := math.Float32frombits(uint32(p.arch.UintN(p.tmp[:4])))
			i := math.Float32frombits(uint32(p.arch.UintN(p.tmp[4:8])))
			p.printf("%g", complex(r, i))
		case 16:
			r := math.Float64frombits(p.arch.UintN(p.tmp[:8]))
			i := math.Float64frombits(p.arch.UintN(p.tmp[8:16]))
			p.printf("%g", complex(r, i))
		default:
			p.errorf("unrecognized complex size %d", typ.ByteSize)
		}
	case *dwarf.StructType:
		p.visited[addr] = true
		if typ.Kind != "struct" {
			// Could be "class" or "union".
			p.errorf("can't handle struct type %s", typ.Kind)
			return
		}
		p.printf("%s {", typ.String())
		for i, field := range typ.Field {
			if i != 0 {
				p.printf(", ")
			}
			p.printValueAt(field.Type, addr+uintptr(field.ByteOffset))
		}
		p.printf("}")
	case *dwarf.ArrayType:
		p.printArrayAt(typ, addr)
	case *dwarf.MapType:
		p.visited[addr] = true
		p.printMapAt(typ, addr)
	case *dwarf.SliceType:
		p.visited[addr] = true
		p.printSliceAt(typ, addr)
	case *dwarf.StringType:
		p.printStringAt(typ, addr)
	case *dwarf.TypedefType:
		p.errorf("unimplemented typedef type %T %v", typ, typ)
	default:
		// TODO: chan func interface
		p.errorf("unimplemented type %v", typ)
	}
}

func (p *Printer) printArrayAt(typ *dwarf.ArrayType, addr uintptr) {
	elemType := typ.Type
	length := typ.Count
	stride := typ.StrideBitSize
	if stride > 0 {
		stride /= 8
	} else {
		stride = p.sizeof(elemType)
		if stride < 0 {
			p.errorf("array elements have no known size")
		}
	}
	p.printf("%s{", typ)
	n := length
	if n > 100 {
		n = 100 // TODO: Have a way to control this?
	}
	for i := int64(0); i < n; i++ {
		if i != 0 {
			p.printf(", ")
		}
		p.printValueAt(elemType, addr)
		addr += uintptr(stride) // TODO: Alignment and padding - not given by Type
	}
	if n < length {
		p.printf(", ...")
	}
	p.printf("}")
}

// TODO: Unimplemented.
func (p *Printer) printMapAt(typ *dwarf.MapType, addr uintptr) {
	// Maps are pointers to a struct type.
	structType := typ.Type.(*dwarf.PtrType).Type.(*dwarf.StructType)
	// Indirect through the pointer.
	if !p.peek(addr, int64(p.arch.PointerSize)) {
		return
	}
	addr = uintptr(p.arch.Uintptr(p.tmp[:p.arch.PointerSize]))
	// Now read the struct.
	if !p.peek(addr, structType.ByteSize) {
		return
	}
	// TODO: unimplemented. Hard.
	countField := structType.Field[0]
	p.printf("%s with ", typ)
	p.printValueAt(countField.Type, addr+uintptr(countField.ByteOffset))
	p.printf(" elements")
}

func (p *Printer) printSliceAt(typ *dwarf.SliceType, addr uintptr) {
	// Slices look like a struct with fields array *elemtype, len uint32/64, cap uint32/64.
	// BUG: Slice header appears to have fields with ByteSize == 0
	if !p.peek(addr, typ.ByteSize) {
		p.errorf("slice header has no known size")
		return
	}
	lo := typ.Field[0].ByteOffset
	hi := lo + int64(p.arch.PointerSize)
	ptr := uintptr(p.arch.UintN(p.tmp[lo:hi]))
	lo = typ.Field[1].ByteOffset
	hi = lo + int64(p.arch.IntSize)
	length := p.arch.UintN(p.tmp[lo:hi])
	// Capacity is unused here.
	elemType := typ.ElemType
	size := p.sizeof(elemType)
	if size < 0 {
		return
	}
	p.printf("%s{", typ)
	for i := uint64(0); i < length; i++ {
		if i != 0 {
			p.printf(", ")
		}
		p.printValueAt(elemType, ptr)
		ptr += uintptr(size) // TODO: Alignment and padding - not given by Type
	}
	p.printf("}")
}

func (p *Printer) printStringAt(typ *dwarf.StringType, addr uintptr) {
	// Strings look like a struct with fields array *elemtype, len uint64.
	// TODO uint64 on 386 too?
	if !p.peek(addr, typ.ByteSize) {
		p.errorf("string header has no known size")
		return
	}
	// BUG: String header appears to have fields with ByteSize == 0
	lo := typ.Field[0].ByteOffset
	hi := lo + int64(p.arch.PointerSize)
	ptr := p.arch.UintN(p.tmp[lo:hi])
	lo = typ.Field[1].ByteOffset
	hi = lo + int64(p.arch.IntSize) // TODO?
	length := p.arch.UintN(p.tmp[lo:hi])
	if length > uint64(cap(p.tmp)) {
		if p.peek(uintptr(ptr), int64(cap(p.tmp))) {
			p.printf("%q...", p.tmp)
		}
	} else {
		if p.peek(uintptr(ptr), int64(length)) {
			p.printf("%q", p.tmp[:length])
		}
	}
}

// sizeof returns the byte size of the type. It returns -1 if no size can be found.
func (p *Printer) sizeof(typ dwarf.Type) int64 {
	size := typ.Size() // Will be -1 if ByteSize is not set.
	if size >= 0 {
		return size
	}
	switch typ.(type) {
	case *dwarf.PtrType:
		// This is the only one we know of, but more may arise.
		return int64(p.arch.PointerSize)
	}
	p.errorf("unknown size for %s", typ)
	return -1
}
