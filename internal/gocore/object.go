// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocore

import (
	"iter"
	"math/bits"
	"strings"

	"golang.org/x/debug/internal/core"
)

// An Object represents a single reachable object in the Go heap.
// Unreachable (garbage) objects are not represented as Objects.
type Object core.Address

// markObjects finds all the live objects in the heap and marks them
// in the p.heapInfo mark fields.
func (p *Process) markObjects() {
	ptrSize := p.proc.PtrSize()

	// number of live objects found so far
	n := 0
	// total size of live objects
	var live int64

	var q []Object

	// Function to call when we find a new pointer.
	add := func(x core.Address) {
		h := p.heap.get(x)
		if h == nil { // not in heap or not in a valid span
			// Invalid spans can happen with intra-stack pointers.
			return
		}
		// Round down to object start.
		x = h.base.Add(x.Sub(h.base) / h.size * h.size)
		// Object start may map to a different info. Reload heap info.
		h = p.heap.get(x)
		// Find mark bit
		b := uint64(x) % heapInfoSize / 8
		if h.mark&(uint64(1)<<b) != 0 { // already found
			return
		}
		h.mark |= uint64(1) << b
		n++
		live += h.size
		q = append(q, Object(x))
	}

	// Start with scanning all the roots.
	// Note that we don't just use the DWARF roots, just in case DWARF isn't complete.
	// Instead we use exactly what the runtime uses.

	// Goroutine roots
	//
	// BUG: If the process dumps core with a thread in mallocgc at the point
	// where an object is reachable from the stack but it hasn't been zeroed
	// yet, it's possible for us to observe stale pointers here and follow them.
	// Not a huge deal given that we'll just ignore outright bad pointers, but
	// we may accidentally mark some objects as live erroneously.
	for _, g := range p.goroutines {
		for _, r := range g.regRoots {
			add(core.Address(r.RegValue))
		}
		for _, f := range g.frames {
			for a := range f.Live {
				add(p.proc.ReadPtr(a))
			}
		}
	}

	// Global roots
	for _, m := range p.modules {
		for _, s := range [2]string{"data", "bss"} {
			min := core.Address(m.r.Field(s).Uintptr())
			max := core.Address(m.r.Field("e" + s).Uintptr())
			gc := m.r.Field("gc" + s + "mask").Field("bytedata").Address()
			num := max.Sub(min) / ptrSize
			for i := int64(0); i < num; i++ {
				if p.proc.ReadUint8(gc.Add(i/8))>>uint(i%8)&1 != 0 {
					add(p.proc.ReadPtr(min.Add(i * ptrSize)))
				}
			}
		}
	}

	// Finalizers
	for _, r := range p.globals {
		if !strings.HasPrefix(r.Name, "finalizer for ") {
			continue
		}
		for _, f := range r.Type.Fields {
			if f.Type.Kind == KindPtr {
				add(p.proc.ReadPtr(r.Addr.Add(f.Off)))
			}
		}
	}

	// Expand root set to all reachable objects.
	for len(q) > 0 {
		x := q[len(q)-1]
		q = q[:len(q)-1]

		// Scan object for pointers.
		size := p.Size(x)
		for i := int64(0); i < size; i += ptrSize {
			a := core.Address(x).Add(i)
			if p.isPtrFromHeap(a) {
				add(p.proc.ReadPtr(a))
			}
		}
	}

	p.nObj = n

	// Initialize firstIdx fields in the heapInfo, for fast object index lookups.
	n = 0
	p.ForEachObject(func(x Object) bool {
		h := p.heap.get(p.Addr(x))
		if h.firstIdx == -1 {
			h.firstIdx = n
		}
		n++
		return true
	})
	if n != p.nObj {
		panic("object count wrong")
	}

	// Update stats to include the live/garbage distinction.
	allocSize := p.Stats().Sub("heap", "in use spans", "alloc").Value
	p.Stats().Sub("heap", "in use spans").setChild(
		groupStat("alloc",
			leafStat("live", live),
			leafStat("garbage", allocSize-live),
		),
	)
}

// isPtrFromHeap reports whether the inferior at address a contains a pointer.
// a must be somewhere in the heap.
func (p *Process) isPtrFromHeap(a core.Address) bool {
	return p.heap.get(a).isPtr(a, p.proc.PtrSize())
}

// IsPtr reports whether the inferior at address a contains a pointer.
func (p *Process) IsPtr(a core.Address) bool {
	h := p.heap.get(a)
	if h != nil {
		return h.isPtr(a, p.proc.PtrSize())
	}
	for _, m := range p.modules {
		for _, s := range [2]string{"data", "bss"} {
			min := core.Address(m.r.Field(s).Uintptr())
			max := core.Address(m.r.Field("e" + s).Uintptr())
			if a < min || a >= max {
				continue
			}
			gc := m.r.Field("gc" + s + "mask").Field("bytedata").Address()
			i := a.Sub(min)
			return p.proc.ReadUint8(gc.Add(i/8))>>uint(i%8) != 0
		}
	}
	// Everywhere else can't be a pointer. At least, not a pointer into the Go heap.
	// TODO: stacks?
	// TODO: finalizers?
	return false
}

// FindObject finds the object containing a.  Returns that object and the offset within
// that object to which a points.
// Returns 0,0 if a doesn't point to a live heap object.
func (p *Process) FindObject(a core.Address) (Object, int64) {
	// Round down to the start of an object.
	h := p.heap.get(a)
	if h == nil {
		// Not in Go heap, or in a span
		// that doesn't hold Go objects (freed, stacks, ...)
		return 0, 0
	}
	x := h.base.Add(a.Sub(h.base) / h.size * h.size)
	// Check if object is marked.
	h = p.heap.get(x)
	if h.mark>>(uint64(x)%heapInfoSize/8)&1 == 0 { // free or garbage
		return 0, 0
	}
	return Object(x), a.Sub(x)
}

func (p *Process) findObjectIndex(a core.Address) (int, int64) {
	x, off := p.FindObject(a)
	if x == 0 {
		return -1, 0
	}
	h := p.heap.get(core.Address(x))
	return h.firstIdx + bits.OnesCount64(h.mark&(uint64(1)<<(uint64(x)%heapInfoSize/8)-1)), off
}

// ForEachObject calls fn with each object in the Go heap.
// If fn returns false, ForEachObject returns immediately.
func (p *Process) ForEachObject(fn func(x Object) bool) {
	for a, h := range p.heap.all() {
		m := h.mark
		for m != 0 {
			j := bits.TrailingZeros64(m)
			m &= m - 1
			x := Object(a + core.Address(j*8))
			if !fn(x) {
				return
			}
		}
	}
}

// ForEachRoot calls fn with each garbage collection root.
// If fn returns false, ForEachRoot returns immediately.
func (p *Process) ForEachRoot(fn func(r *Root) bool) {
	for _, r := range p.globals {
		if !fn(r) {
			return
		}
	}
	for _, g := range p.goroutines {
		for _, r := range g.regRoots {
			if !fn(r) {
				return
			}
		}
		for _, f := range g.frames {
			for _, r := range f.roots {
				if !fn(r) {
					return
				}
			}
		}
	}
}

// Addr returns the starting address of x.
func (p *Process) Addr(x Object) core.Address {
	return core.Address(x)
}

// Size returns the size of x in bytes.
func (p *Process) Size(x Object) int64 {
	return p.heap.get(core.Address(x)).size
}

// Type returns the type and repeat count for the object x.
// x contains at least repeat copies of the returned type.
func (p *Process) Type(x Object) (*Type, int64) {
	p.typeHeap()

	i, _ := p.findObjectIndex(core.Address(x))
	return p.types[i].t, p.types[i].r
}

// ForEachPtr calls fn for all heap pointers it finds in x.
// It calls fn with:
//
//	the offset of the pointer slot in x
//	the pointed-to object y
//	the offset in y where the pointer points.
//
// If fn returns false, ForEachPtr returns immediately.
// For an edge from an object to its finalizer, the first argument
// passed to fn will be -1. (TODO: implement)
func (p *Process) ForEachPtr(x Object, fn func(int64, Object, int64) bool) {
	size := p.Size(x)
	for i := int64(0); i < size; i += p.proc.PtrSize() {
		a := core.Address(x).Add(i)
		if !p.isPtrFromHeap(a) {
			continue
		}
		ptr := p.proc.ReadPtr(a)
		y, off := p.FindObject(ptr)
		if y != 0 {
			if !fn(i, y, off) {
				return
			}
		}
	}
}

// ForEachRootPtr behaves like ForEachPtr but it starts with a Root instead of an Object.
func (p *Process) ForEachRootPtr(r *Root, fn func(int64, Object, int64) bool) {
	edges1(p, r, 0, r.Type, fn)
}

// edges1 calls fn for the edges found in an object of type t living at offset off in the root r.
// If fn returns false, return immediately with false.
func edges1(p *Process, r *Root, off int64, t *Type, fn func(int64, Object, int64) bool) bool {
	switch t.Kind {
	case KindBool, KindInt, KindUint, KindFloat, KindComplex:
		// no edges here
	case KindIface, KindEface:
		// The first word is a type or itab.
		// Itabs are never in the heap.
		// Types might be, though.
		a := r.Addr.Add(off)
		if r.Frame == nil || r.Frame.Live[a] {
			dst, off2 := p.FindObject(p.proc.ReadPtr(a))
			if dst != 0 {
				if !fn(off, dst, off2) {
					return false
				}
			}
		}
		// Treat second word like a pointer.
		off += p.proc.PtrSize()
		fallthrough
	case KindPtr, KindString, KindSlice, KindFunc:
		if t.Kind == KindPtr && r.Addr == 0 {
			dst, off2 := p.FindObject(core.Address(r.RegValue))
			if dst != 0 {
				if !fn(off, dst, off2) {
					return false
				}
			}
			break
		}
		a := r.Addr.Add(off)
		if r.Frame == nil || r.Frame.Live[a] {
			dst, off2 := p.FindObject(p.proc.ReadPtr(a))
			if dst != 0 {
				if !fn(off, dst, off2) {
					return false
				}
			}
		}
	case KindArray:
		s := t.Elem.Size
		for i := int64(0); i < t.Count; i++ {
			if !edges1(p, r, off+i*s, t.Elem, fn) {
				return false
			}
		}
	case KindStruct:
		for _, f := range t.Fields {
			if !edges1(p, r, off+f.Off, f.Type, fn) {
				return false
			}
		}
	}
	return true
}

const heapInfoSize = 512

// Information for heapInfoSize bytes of heap.
type heapInfo struct {
	base     core.Address // start of the span containing this heap region
	size     int64        // size of objects in the span
	mark     uint64       // 64 mark bits, one for every 8 bytes
	firstIdx int          // the index of the first object that starts in this region, or -1 if none
	// For 64-bit inferiors, ptr[0] contains 64 pointer bits, one
	// for every 8 bytes.  On 32-bit inferiors, ptr contains 128
	// pointer bits, one for every 4 bytes.
	ptr [2]uint64
}

func (h *heapInfo) isPtr(a core.Address, ptrSize int64) bool {
	if ptrSize == 8 {
		i := uint(a%heapInfoSize) / 8
		return h.ptr[0]>>i&1 != 0
	}
	i := a % heapInfoSize / 4
	return h.ptr[i/64]>>(i%64)&1 != 0
}

type heapTableID uint64

func (id heapTableID) addr() core.Address {
	return core.Address(id * heapInfoSize * heapTableSize)
}

type heapTable struct {
	table   map[heapTableID]*heapTableEntry
	entries []heapTableID
	ptrSize uint64
}

func heapTableIndex(a core.Address) (heapTableID, uint64) {
	return heapTableID(a / heapInfoSize / heapTableSize), uint64(a / heapInfoSize % heapTableSize)
}

// Heap info structures cover 9 bits of address.
// A page table entry covers 20 bits of address (1MB).
const heapTableSize = 1 << 11

type heapTableEntry [heapTableSize]heapInfo

func (ht *heapTable) setIsPointer(a core.Address) {
	h := ht.getOrCreate(a)
	ptrSize := ht.ptrSize
	if ptrSize == 8 {
		i := uint64(a%heapInfoSize) / 8
		h.ptr[0] |= uint64(1) << i
		return
	}
	i := uint64(a%heapInfoSize) / 4
	h.ptr[i/64] |= uint64(1) << (i % 64)
}

// get returns the heap info for an address. Returns nil
// if a is not a heap address.
func (ht *heapTable) get(a core.Address) *heapInfo {
	k, i := heapTableIndex(a)
	t := ht.table[k]
	if t == nil {
		return nil
	}
	h := &t[i]
	if h.base == 0 {
		return nil
	}
	return h
}

// getOrCreate returns the heap info for an address, or if nil,
// creates it, marking it as a heap address.
func (ht *heapTable) getOrCreate(a core.Address) *heapInfo {
	k, i := heapTableIndex(a)
	t := ht.table[k]
	if t == nil {
		t = new(heapTableEntry)
		for j := 0; j < heapTableSize; j++ {
			t[j].firstIdx = -1
		}
		ht.table[k] = t
		ht.entries = append(ht.entries, k)
	}
	return &t[i]
}

func (ht *heapTable) all() iter.Seq2[core.Address, *heapInfo] {
	return func(yield func(core.Address, *heapInfo) bool) {
		for _, k := range ht.entries {
			e := ht.table[k]
			for i := range e {
				h := &e[i]
				a := k.addr() + core.Address(uint64(i)*heapInfoSize)
				if !yield(a, h) {
					return
				}
			}
		}
	}
}
