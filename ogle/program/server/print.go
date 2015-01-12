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

// printf prints to printBuf.
func (p *Printer) printf(format string, args ...interface{}) {
	fmt.Fprintf(&p.printBuf, format, args...)
}

// errorf prints the error to printBuf, then sets the sticky error for the
// printer, if not already set.
func (p *Printer) errorf(format string, args ...interface{}) {
	fmt.Fprintf(&p.printBuf, "<"+format+">", args...)
	if p.err != nil {
		return
	}
	p.err = fmt.Errorf(format, args...)
}

// peek reads len bytes at addr, leaving p.tmp with the data and sized appropriately.
// It uses the cache if the request is within it.
func (p *Printer) peek(addr address, length int64) bool {
	p.tmp = p.tmp[:length]
	if p.cacheAddr <= addr && addr+address(length) <= p.cacheAddr+address(len(p.cache)) {
		copy(p.tmp, p.cache[addr-p.cacheAddr:])
		return true
	}
	err := p.peeker.peek(uintptr(addr), p.tmp)
	return err == nil
}

// peekPtr reads a pointer at addr.
func (p *Printer) peekPtr(addr address) (address, bool) {
	if p.peek(addr, int64(p.arch.PointerSize)) {
		return address(p.arch.Uintptr(p.tmp)), true
	}
	return 0, false
}

// peekUint8 reads a uint8 at addr.
func (p *Printer) peekUint8(addr address) (uint8, bool) {
	if p.peek(addr, 1) {
		return p.tmp[0], true
	}
	return 0, false
}

// peekInt reads an int of size s at addr.
func (p *Printer) peekInt(addr address, s int64) (int64, bool) {
	if p.peek(addr, s) {
		return p.arch.IntN(p.tmp), true
	}
	return 0, false
}

// peekUint reads a uint of size s at addr.
func (p *Printer) peekUint(addr address, s int64) (uint64, bool) {
	if p.peek(addr, s) {
		return p.arch.UintN(p.tmp), true
	}
	return 0, false
}

// peekPtrStructField reads a pointer in the field fieldName of the struct
// of type t at address addr.
func (p *Printer) peekPtrStructField(t *dwarf.StructType, addr address, fieldName string) (address, bool) {
	f, err := getField(t, fieldName)
	if err != nil {
		p.errorf("%s", err)
		return 0, false
	}
	_, ok := f.Type.(*dwarf.PtrType)
	if !ok {
		p.errorf("struct field %s is not a pointer", fieldName)
		return 0, false
	}
	return p.peekPtr(addr + address(f.ByteOffset))
}

// peekUintStructField reads a uint in the field fieldName of the struct
// of type t at address addr.  The size of the uint is determined by the field.
func (p *Printer) peekUintStructField(t *dwarf.StructType, addr address, fieldName string) (uint64, bool) {
	f, err := getField(t, fieldName)
	if err != nil {
		p.errorf("%s", err)
		return 0, false
	}
	ut, ok := f.Type.(*dwarf.UintType)
	if !ok {
		p.errorf("struct field %s is not a uint", fieldName)
		return 0, false
	}
	return p.peekUint(addr+address(f.ByteOffset), ut.ByteSize)
}

// peekIntStructField reads an int in the field fieldName of the struct
// of type t at address addr.  The size of the int is determined by the field.
func (p *Printer) peekIntStructField(t *dwarf.StructType, addr address, fieldName string) (int64, bool) {
	f, err := getField(t, fieldName)
	if f == nil {
		p.errorf("%s", err)
		return 0, false
	}
	it, ok := f.Type.(*dwarf.IntType)
	if !ok {
		p.errorf("struct field %s is not an int", fieldName)
		return 0, false
	}
	return p.peekInt(addr+address(f.ByteOffset), it.ByteSize)
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
	err := p.peeker.peek(uintptr(a), p.cache)
	if err != nil {
		// If the peek failed, don't cache anything.
		p.resetCache()
		return false
	}
	return true
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
		if b, ok := p.peekUint8(a); ok {
			p.printf("%t", b != 0)
		} else {
			p.errorf("couldn't read bool")
		}
	case *dwarf.PtrType:
		if ptr, ok := p.peekPtr(a); ok {
			p.printf("%#x", ptr)
		} else {
			p.errorf("couldn't read pointer")
		}
	case *dwarf.IntType:
		// Sad we can't tell a rune from an int32.
		if i, ok := p.peekInt(a, typ.ByteSize); ok {
			p.printf("%d", i)
		} else {
			p.errorf("couldn't read int")
		}
	case *dwarf.UintType:
		if u, ok := p.peekUint(a, typ.ByteSize); ok {
			p.printf("%d", u)
		} else {
			p.errorf("couldn't read uint")
		}
	case *dwarf.FloatType:
		if !p.peek(a, typ.ByteSize) {
			p.errorf("couldn't read float")
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
			p.errorf("couldn't read complex")
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
	stride, ok := p.arrayStride(typ)
	if !ok {
		p.errorf("can't determine element size")
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
		p.errorf("couldn't read map")
		return
	}
	a = address(p.arch.Uintptr(p.tmp[:p.arch.PointerSize]))
	// Now read the struct.
	if !p.peek(a, structType.ByteSize) {
		p.errorf("couldn't read map")
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
			p.errorf("couldn't read map")
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
	ptr, ok1 := p.peekPtrStructField(&typ.StructType, a, "array")
	length, ok2 := p.peekUintStructField(&typ.StructType, a, "len")
	// Capacity is not used yet.
	_, ok3 := p.peekUintStructField(&typ.StructType, a, "cap")
	if !ok1 || !ok2 || !ok3 {
		p.errorf("couldn't read slice")
		return
	}
	elemType := typ.ElemType
	size, ok := p.sizeof(typ.ElemType)
	if !ok {
		p.errorf("can't determine element size")
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
	// BUG: String header appears to have fields with ByteSize == 0
	ptr, ok := p.peekPtrStructField(&typ.StructType, a, "str")
	if !ok {
		p.errorf("couldn't read string")
		return
	}
	length, ok := p.peekIntStructField(&typ.StructType, a, "len")
	if !ok {
		p.errorf("couldn't read string")
		return
	}
	if length > int64(cap(p.tmp)) {
		if p.peek(address(ptr), int64(cap(p.tmp))) {
			p.printf("%q...", p.tmp)
		} else {
			p.errorf("couldn't read string")
			return
		}
	} else {
		if p.peek(address(ptr), int64(length)) {
			p.printf("%q", p.tmp[:length])
		} else {
			p.errorf("couldn't read string")
			return
		}
	}
}

// sizeof returns the byte size of the type.
func (p *Printer) sizeof(typ dwarf.Type) (address, bool) {
	size := typ.Size() // Will be -1 if ByteSize is not set.
	if size >= 0 {
		return address(size), true
	}
	switch typ.(type) {
	case *dwarf.PtrType:
		// This is the only one we know of, but more may arise.
		return address(p.arch.PointerSize), true
	}
	return 0, false
}

// arrayStride returns the stride of a dwarf.ArrayType in bytes.
func (p *Printer) arrayStride(t *dwarf.ArrayType) (address, bool) {
	stride := t.StrideBitSize
	if stride > 0 {
		return address(stride / 8), true
	}
	return p.sizeof(t.Type)
}

// getField finds the *dwarf.StructField in a dwarf.StructType with name fieldName.
func getField(t *dwarf.StructType, fieldName string) (*dwarf.StructField, error) {
	var r *dwarf.StructField
	for _, f := range t.Field {
		if f.Name == fieldName {
			if r != nil {
				return nil, fmt.Errorf("struct definition repeats field %s", fieldName)
			}
			r = f
		}
	}
	if r == nil {
		return nil, fmt.Errorf("struct field %s missing", fieldName)
	}
	return r, nil
}
