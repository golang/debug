// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

import (
	"bytes"
	"fmt"
	"math"

	"golang.org/x/debug/dwarf"
	"golang.org/x/debug/ogle/arch"
)

// address is a type denoting addresses in the tracee.
type address uintptr

// typeAndAddress associates an address in the target with a DWARF type.
type typeAndAddress struct {
	Type    dwarf.Type
	Address address
}

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
	printBuf bytes.Buffer            // Accumulates the output.
	tmp      []byte                  // Temporary used for I/O.
	visited  map[typeAndAddress]bool // Prevents looping on cyclic data.
	// The cache stores a local copy of part of the address space.
	// Saves I/O overhead when scanning map buckets by letting
	// printValueAt use the contents of already-read bucket data.
	cache     []byte  // Already-read data.
	cacheAddr address // Starting address of cache.
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
// It uses the cache if the request is within it.
func (p *Printer) peek(a address, length int64) bool {
	p.tmp = p.tmp[:length]
	if p.cacheAddr <= a && a+address(length) <= p.cacheAddr+address(len(p.cache)) {
		copy(p.tmp, p.cache[a-p.cacheAddr:])
		return true
	}
	return p.ok(p.peeker.peek(uintptr(a), p.tmp))
}

// setCache initializes the cache to contain the contents of the
// address space at the specified offset.
func (p *Printer) setCache(a, length address) bool {
	if address(cap(p.cache)) >= length {
		p.cache = p.cache[:length]
	} else {
		p.cache = make([]byte, length)
	}
	p.cacheAddr = a
	ok := p.ok(p.peeker.peek(uintptr(a), p.cache))
	if !ok {
		p.resetCache()
	}
	return ok
}

func (p *Printer) resetCache() {
	p.cache = p.cache[0:0]
	p.cacheAddr = 0
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
		visited: make(map[typeAndAddress]bool),
		tmp:     make([]byte, 100), // Enough for a largish string.
	}
}

// reset resets the Printer. It must be called before starting a new
// printing operation.
func (p *Printer) reset() {
	p.err = nil
	p.printBuf.Reset()
	p.resetCache()
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
		var a address
		iface := entry.Val(dwarf.AttrLocation)
		if iface != nil {
			a = p.decodeLocation(iface.([]byte))
		}
		p.printEntryValueAt(entry, a)
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
func (p *Printer) decodeLocation(data []byte) address {
	switch data[0] {
	case locationAddr:
		return address(p.arch.Uintptr(data[1:]))
	default:
		p.errorf("unimplemented location type %#x", data[0])
	}
	return 0
}

// SprintEntry returns the pretty-printed value of the item with the specified DWARF Entry and address.
func (p *Printer) SprintEntry(entry *dwarf.Entry, a address) (string, error) {
	p.reset()
	p.printEntryValueAt(entry, a)
	return p.printBuf.String(), p.err
}

// printEntryValueAt pretty-prints the data at the specified addresss
// using the type information in the Entry.
func (p *Printer) printEntryValueAt(entry *dwarf.Entry, a address) {
	if a == 0 {
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
	p.printValueAt(typ, a)
}

// printValueAt pretty-prints the data at the specified addresss
// using the provided type information.
func (p *Printer) printValueAt(typ dwarf.Type, a address) {
	if a != 0 {
		// Check if we are repeating the same type and address.
		ta := typeAndAddress{typ, a}
		if p.visited[ta] {
			p.printf("(%v %#x)", typ, a)
			return
		}
		p.visited[ta] = true
	}
	switch typ := typ.(type) {
	case *dwarf.BoolType:
		if typ.ByteSize != 1 {
			p.errorf("unrecognized bool size %d", typ.ByteSize)
			return
		}
		if p.peek(a, 1) {
			p.printf("%t", p.tmp[0] != 0)
		}
	case *dwarf.PtrType:
		// This type doesn't define the ByteSize attribute.
		if p.peek(a, int64(p.arch.PointerSize)) {
			p.printf("%#x", p.arch.Uintptr(p.tmp))
		}
	case *dwarf.IntType:
		// Sad we can't tell a rune from an int32.
		if p.peek(a, typ.ByteSize) {
			p.printf("%d", p.arch.IntN(p.tmp))
		}
	case *dwarf.UintType:
		if p.peek(a, typ.ByteSize) {
			p.printf("%d", p.arch.UintN(p.tmp))
		}
	case *dwarf.FloatType:
		if !p.peek(a, typ.ByteSize) {
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
		if !p.peek(a, typ.ByteSize) {
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
			p.printValueAt(field.Type, a+address(field.ByteOffset))
		}
		p.printf("}")
	case *dwarf.ArrayType:
		p.printArrayAt(typ, a)
	case *dwarf.MapType:
		p.printMapAt(typ, a)
	case *dwarf.SliceType:
		p.printSliceAt(typ, a)
	case *dwarf.StringType:
		p.printStringAt(typ, a)
	case *dwarf.TypedefType:
		p.printValueAt(typ.Type, a)
	case *dwarf.FuncType:
		p.printf("%v @%#x ", typ, a)
	case *dwarf.VoidType:
		p.printf("void")
	default:
		// TODO: chan interface
		p.errorf("unimplemented type %v", typ)
	}
}

func (p *Printer) printArrayAt(typ *dwarf.ArrayType, a address) {
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
		p.printValueAt(elemType, a)
		a += address(stride) // TODO: Alignment and padding - not given by Type
	}
	if n < length {
		p.printf(", ...")
	}
	p.printf("}")
}

// mapDesc collects the information necessary to print a map.
type mapDesc struct {
	typ        *dwarf.MapType
	count      int
	numBuckets int
	keySize    address
	elemSize   address
	bucketSize address
}

func (p *Printer) printMapAt(typ *dwarf.MapType, a address) {
	// Maps are pointers to a struct type.
	structType := typ.Type.(*dwarf.PtrType).Type.(*dwarf.StructType)
	// Indirect through the pointer.
	if !p.peek(a, int64(p.arch.PointerSize)) {
		return
	}
	a = address(p.arch.Uintptr(p.tmp[:p.arch.PointerSize]))
	// Now read the struct.
	if !p.peek(a, structType.ByteSize) {
		return
	}
	// From runtime/hashmap.go; We need to walk the map data structure.
	// Load the struct, then iterate over the buckets.
	// uintgo count (occupancy).
	offset := int(structType.Field[0].ByteOffset)
	count := int(p.arch.Uint(p.tmp[offset : offset+p.arch.IntSize]))
	// uint8 Log2 of number of buckets.
	b := uint(p.tmp[structType.Field[3].ByteOffset])
	// uint8 key size in bytes.
	keySize := address(p.tmp[structType.Field[4].ByteOffset])
	// uint8 element size in bytes.
	elemSize := address(p.tmp[structType.Field[5].ByteOffset])
	// uint16 bucket size in bytes.
	bucketSize := address(p.arch.Uint16(p.tmp[structType.Field[6].ByteOffset:]))
	// pointer to buckets
	offset = int(structType.Field[7].ByteOffset)
	bucketPtr := address(p.arch.Uintptr(p.tmp[offset : offset+p.arch.PointerSize]))
	// pointer to old buckets.
	offset = int(structType.Field[8].ByteOffset)
	oldBucketPtr := address(p.arch.Uintptr(p.tmp[offset : offset+p.arch.PointerSize]))
	// Ready to print.
	p.printf("%s{", typ)
	desc := mapDesc{
		typ:        typ,
		count:      count,
		numBuckets: 1 << b,
		keySize:    keySize,
		elemSize:   elemSize,
		bucketSize: bucketSize,
	}
	p.printMapBucketsAt(desc, bucketPtr)
	p.printMapBucketsAt(desc, oldBucketPtr)
	p.printf("}")
}

// Map bucket layout from runtime/hashmap.go
const (
	bucketCnt  = 8
	minTopHash = 4
)

func (p *Printer) printMapBucketsAt(desc mapDesc, a address) {
	if a == 0 {
		return
	}
	for i := 0; desc.count > 0 && i < desc.numBuckets; i++ {
		// After the header, the bucket struct has an array of keys followed by an array of elements.
		// Load this bucket struct into p's cache and initialize "pointers" to the key and value slices.
		if !p.setCache(a, desc.bucketSize) {
			return
		}
		keyAddr := a + bucketCnt + address(p.arch.PointerSize)
		elemAddr := keyAddr + bucketCnt*desc.keySize
		a += desc.bucketSize // Advance to next bucket; keyAddr and elemAddr are all we need now.
		// tophash uint8 [bucketCnt] tells us which buckets are occupied.
		// p.cache has the data but calls to printValueAt below may overwrite the
		// cache, so grab a copy of the relevant data.
		var tophash [bucketCnt]byte
		if copy(tophash[:], p.cache) != bucketCnt {
			p.errorf("bad count copying hash flags")
			return
		}
		overflow := address(p.arch.Uintptr(p.cache[bucketCnt : bucketCnt+p.arch.PointerSize]))
		for j := 0; desc.count > 0 && j < bucketCnt; j++ {
			if tophash[j] >= minTopHash {
				p.printValueAt(desc.typ.KeyType, keyAddr)
				p.printf(":")
				p.printValueAt(desc.typ.ElemType, elemAddr)
				desc.count--
				if desc.count > 0 {
					p.printf(", ")
				}
			}
			keyAddr += desc.keySize
			elemAddr += desc.elemSize
		}
		// pointer to overflow bucket, if any.
		p.printMapBucketsAt(desc, overflow)
	}
}

func (p *Printer) printSliceAt(typ *dwarf.SliceType, a address) {
	// Slices look like a struct with fields array *elemtype, len uint32/64, cap uint32/64.
	// BUG: Slice header appears to have fields with ByteSize == 0
	if !p.peek(a, typ.ByteSize) {
		p.errorf("slice header has no known size")
		return
	}
	lo := typ.Field[0].ByteOffset
	hi := lo + int64(p.arch.PointerSize)
	ptr := address(p.arch.UintN(p.tmp[lo:hi]))
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
		ptr += address(size) // TODO: Alignment and padding - not given by Type
	}
	p.printf("}")
}

func (p *Printer) printStringAt(typ *dwarf.StringType, a address) {
	// Strings look like a struct with fields array *elemtype, len uint64.
	// TODO uint64 on 386 too?
	if !p.peek(a, typ.ByteSize) {
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
		if p.peek(address(ptr), int64(cap(p.tmp))) {
			p.printf("%q...", p.tmp)
		}
	} else {
		if p.peek(address(ptr), int64(length)) {
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
