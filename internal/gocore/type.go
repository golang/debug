// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocore

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"

	"golang.org/x/debug/internal/core"
)

// A Type is the representation of the type of a Go object.
// Types are not necessarily canonical.
// Names are opaque; do not depend on the format of the returned name.
type Type struct {
	Name string
	Size int64
	Kind Kind // common dwarf types.
	// Go-specific types obtained from AttrGoKind, such as string and slice.
	// Kind and gokind are not correspond one to one, both need to be preserved now.
	// For example, slices are described in dwarf by a 3-field struct, so its Kind is Struct and its goKind is Slice.
	goKind reflect.Kind
	// Go-specific types obtained from AttrGoRuntimeType.
	// May be nil if this type is not referenced by the DWARF.
	goAddr core.Address

	// Fields only valid for a subset of kinds.
	Count  int64   // for kind == KindArray
	Elem   *Type   // for kind == Kind{Ptr,Array,Slice,String}. nil for unsafe.Pointer. Always uint8 for KindString.
	Fields []Field // for kind == KindStruct
}

type Kind uint8

const (
	KindNone Kind = iota
	KindBool
	KindInt
	KindUint
	KindFloat
	KindComplex
	KindArray
	KindPtr // includes chan, map, unsafe.Pointer
	KindIface
	KindEface
	KindSlice
	KindString
	KindStruct
	KindFunc
)

func (k Kind) String() string {
	return [...]string{
		"KindNone",
		"KindBool",
		"KindInt",
		"KindUint",
		"KindFloat",
		"KindComplex",
		"KindArray",
		"KindPtr",
		"KindIface",
		"KindEface",
		"KindSlice",
		"KindString",
		"KindStruct",
		"KindFunc",
	}[k]
}

// A Field represents a single field of a struct type.
type Field struct {
	Name string
	Off  int64
	Type *Type
}

func (t *Type) String() string {
	return t.Name
}

func (t *Type) field(name string) *Field {
	if t.Kind != KindStruct {
		panic("asking for field of non-struct")
	}
	for i := range t.Fields {
		f := &t.Fields[i]
		if f.Name == name {
			return f
		}
	}
	return nil
}

func (t *Type) HasField(name string) bool {
	return t.field(name) != nil
}

// DynamicType returns the concrete type stored in the interface type t at address a.
// If the interface is nil, returns nil.
func (p *Process) DynamicType(t *Type, a core.Address) *Type {
	switch t.Kind {
	default:
		panic("asking for the dynamic type of a non-interface")
	case KindEface:
		x := p.proc.ReadPtr(a)
		if x == 0 {
			return nil
		}
		return p.runtimeType2Type(x, a.Add(p.proc.PtrSize()))
	case KindIface:
		x := p.proc.ReadPtr(a)
		if x == 0 {
			return nil
		}
		// Read type out of itab.
		x = p.proc.ReadPtr(x.Add(p.proc.PtrSize()))
		return p.runtimeType2Type(x, a.Add(p.proc.PtrSize()))
	}
}

// return the number of bytes of the variable int and its value,
// which means the length of a name.
func readNameLen(p *Process, a core.Address) (int64, int64) {
	v := 0
	for i := 0; ; i++ {
		x := p.proc.ReadUint8(a.Add(int64(i + 1)))
		v += int(x&0x7f) << (7 * i)
		if x&0x80 == 0 {
			return int64(i + 1), int64(v)
		}
	}
}

// runtimeType is a thin wrapper around a runtime._type (AKA abi.Type) region
// that abstracts over the name changes seen in Go 1.21.
type runtimeType struct {
	reg region
}

// findRuntimeType finds either abi.Type (Go 1.21+) or runtime._type.
func (p *Process) findRuntimeType(a core.Address) runtimeType {
	return runtimeType{
		reg: region{p: p.proc, a: a, typ: p.rtTypeByName["internal/abi.Type"]},
	}
}

// Size_ is either abi.Type.Size_ or runtime._type.Size_.
func (r runtimeType) Size_() int64 {
	return int64(r.reg.Field("Size_").Uintptr())
}

// TFlag is either abi.Type.TFlag or runtime._type.TFlag.
func (r runtimeType) TFlag() uint8 {
	return r.reg.Field("TFlag").Uint8()
}

// Str is either abi.Type.Str or runtime._type.Str.
func (r runtimeType) Str() int64 {
	return int64(r.reg.Field("Str").Int32())
}

// PtrBytes is either abi.Type.PtrBytes or runtime._type.PtrBytes.
func (r runtimeType) PtrBytes() int64 {
	return int64(r.reg.Field("PtrBytes").Uintptr())
}

// Kind_ is either abi.Type.Kind_ or runtime._type.Kind_.
func (r runtimeType) Kind_() uint8 {
	return r.reg.Field("Kind_").Uint8()
}

// GCData is either abi.Type.GCData or runtime._type.GCData.
func (r runtimeType) GCData() core.Address {
	return r.reg.Field("GCData").Address()
}

// runtimeItab is a thin wrapper around a abi.ITab (used to be runtime.itab). It
// abstracts over name/package changes in Go 1.21.
type runtimeItab struct {
	typ *Type
}

// findItab finds either abi.ITab (Go 1.21+) or runtime.itab.
func (p *Process) findItab() runtimeItab {
	return runtimeItab{
		typ: p.rtTypeByName["internal/abi.ITab"],
	}
}

// Type is the field representing either abi.ITab.Type or runtime.itab._type.
func (r runtimeItab) Type() *Field {
	if r.typ == nil {
		return nil
	}
	return r.typ.field("Type")
}

// Convert the address of a runtime._type to a *Type.
// The "d" is the address of the second field of an interface, used to help disambiguate types.
// If "d" is 0, just return *Type and not to do the interface disambiguation.
// Guaranteed to return a non-nil *Type.
func (p *Process) runtimeType2Type(a core.Address, d core.Address) *Type {
	if t := p.rtTypeMap[a]; t != nil {
		return t
	}
	// There's no corresponding DWARF type. Make our own.

	// Read runtime._type.size
	r := p.findRuntimeType(a)
	size := r.Size_()

	// Find module this type is in.
	var m *module
	for _, x := range p.modules {
		if x.types <= a && a < x.etypes {
			m = x
			break
		}
	}

	// Read information out of the runtime._type.
	var name string
	if m != nil {
		x := m.types.Add(r.Str())
		i, n := readNameLen(p, x)
		b := make([]byte, n)
		p.proc.ReadAt(b, x.Add(i+1))
		name = string(b)
		if r.TFlag()&uint8(p.rtConsts.get("internal/abi.TFlagExtraStar")) != 0 {
			name = name[1:]
		}
	} else {
		// A reflect-generated type.
		// TODO: The actual name is in the runtime.reflectOffs map.
		// Too hard to look things up in maps here, just allocate a placeholder for now.
		name = fmt.Sprintf("reflect.generatedType%x", a)
	}

	// Read ptr/nonptr bits
	ptrSize := p.proc.PtrSize()
	nptrs := int64(r.PtrBytes()) / ptrSize
	var ptrs []int64
	kindGCProg, hasGCProgs := p.rtConsts.find("internal/abi.KindGCProg")
	if hasGCProgs && r.Kind_()&uint8(kindGCProg) != 0 {
		// TODO: run GC program. Go 1.23 and earlier only.
	} else {
		gcdata := r.GCData()
		for i := int64(0); i < nptrs; i++ {
			if p.proc.ReadUint8(gcdata.Add(i/8))>>uint(i%8)&1 != 0 {
				ptrs = append(ptrs, i*ptrSize)
			}
		}
	}

	// Build the type from the name, size, and ptr/nonptr bits.
	t := &Type{Name: name, Size: size, Kind: KindStruct}
	n := t.Size / ptrSize

	// Types to use for ptr/nonptr fields of runtime types which
	// have no corresponding DWARF type.
	ptr := p.rtTypeByName["unsafe.Pointer"]
	nonptr := p.rtTypeByName["uintptr"]
	if ptr == nil || nonptr == nil {
		panic("ptr / nonptr standins missing")
	}

	for i := int64(0); i < n; i++ {
		typ := nonptr
		if len(ptrs) > 0 && ptrs[0] == i*ptrSize {
			typ = ptr
			ptrs = ptrs[1:]
		}
		t.Fields = append(t.Fields, Field{
			Name: fmt.Sprintf("f%d", i),
			Off:  i * ptrSize,
			Type: typ,
		})

	}
	if t.Size%ptrSize != 0 {
		// TODO: tail of <ptrSize data.
	}

	// Memoize.
	p.rtTypeMap[a] = t
	return t
}

// ptrs returns a sorted list of pointer offsets in t.
func (t *Type) ptrs() []int64 {
	return t.ptrs1(nil, 0)
}
func (t *Type) ptrs1(s []int64, off int64) []int64 {
	switch t.Kind {
	case KindPtr, KindFunc, KindSlice, KindString:
		s = append(s, off)
	case KindIface, KindEface:
		s = append(s, off, off+t.Size/2)
	case KindArray:
		if t.Count > 10000 {
			// Be careful about really large types like [1e9]*byte.
			// To process such a type we'd make a huge ptrs list.
			// The ptrs list here is only used for matching
			// a runtime type with a dwarf type, and for making
			// fields for types with no dwarf type.
			// Both uses can fail with no terrible repercussions.
			// We still will scan the whole object during markObjects, for example.
			// TODO: make this more robust somehow.
			break
		}
		for i := int64(0); i < t.Count; i++ {
			s = t.Elem.ptrs1(s, off)
			off += t.Elem.Size
		}
	case KindStruct:
		for _, f := range t.Fields {
			s = f.Type.ptrs1(s, off+f.Off)
		}
	default:
		// no pointers
	}
	return s
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i, x := range a {
		if x != b[i] {
			return false
		}
	}
	return true
}

// A typeInfo contains information about the type of an object.
// A slice of these hold the results of typing the heap.
type typeInfo struct {
	// This object has an effective type of [r]t.
	// Parts of the object beyond the first r*t.Size bytes have unknown type.
	// If t == nil, the type is unknown. (TODO: provide access to ptr/nonptr bits in this case.)
	t *Type
	r int64
}

// A typeChunk records type information for a portion of an object.
// Similar to a typeInfo, but it has an offset so it can be used for interior typings.
type typeChunk struct {
	off int64
	t   *Type
	r   int64
}

func (c typeChunk) min() int64 {
	return c.off
}
func (c typeChunk) max() int64 {
	return c.off + c.r*c.t.Size
}
func (c typeChunk) size() int64 {
	return c.r * c.t.Size
}
func (c typeChunk) matchingAlignment(d typeChunk) bool {
	if c.t != d.t {
		panic("can't check alignment of differently typed chunks")
	}
	return (c.off-d.off)%c.t.Size == 0
}

func (c typeChunk) merge(d typeChunk) typeChunk {
	t := c.t
	if t != d.t {
		panic("can't merge chunks with different types")
	}
	size := t.Size
	if (c.off-d.off)%size != 0 {
		panic("can't merge poorly aligned chunks")
	}
	min := c.min()
	max := c.max()
	if max < d.min() || min > d.max() {
		panic("can't merge chunks which don't overlap or abut")
	}
	if x := d.min(); x < min {
		min = x
	}
	if x := d.max(); x > max {
		max = x
	}
	return typeChunk{off: min, t: t, r: (max - min) / size}
}
func (c typeChunk) String() string {
	return fmt.Sprintf("%x[%d]%s", c.off, c.r, c.t)
}

// typeHeap tries to label all the heap objects with types.
func (p *Process) typeHeap() {
	p.initTypeHeap.Do(p.doTypeHeap)
}

func (p *Process) doTypeHeap() {
	// Type info for the start of each object. a.k.a. "0 offset" typings.
	p.types = make([]typeInfo, p.nObj)

	// Type info for the interior of objects, a.k.a. ">0 offset" typings.
	// Type information is arranged in chunks. Chunks are stored in an
	// arbitrary order, and are guaranteed to not overlap. If types are
	// equal, chunks are also guaranteed not to abut.
	// Interior typings are kept separate because they hopefully are rare.
	// TODO: They aren't really that rare. On some large heaps I tried
	// ~50% of objects have an interior pointer into them.
	// Keyed by object index.
	interior := map[int][]typeChunk{}

	// Typings we know about but haven't scanned yet.
	type workRecord struct {
		a core.Address
		t *Type
		r int64
	}
	var work []workRecord

	// add records the fact that we know the object at address a has
	// r copies of type t.
	add := func(a core.Address, t *Type, r int64) {
		if a == 0 { // nil pointer
			return
		}
		i, off := p.findObjectIndex(a)
		if i < 0 { // pointer doesn't point to an object in the Go heap
			return
		}
		if off == 0 {
			// We have a 0-offset typing. Replace existing 0-offset typing
			// if the new one is larger.
			ot := p.types[i].t
			or := p.types[i].r
			if ot == nil || r*t.Size > or*ot.Size {
				if t == ot {
					// Scan just the new section.
					work = append(work, workRecord{
						a: a.Add(or * ot.Size),
						t: t,
						r: r - or,
					})
				} else {
					// Rescan the whole typing using the updated type.
					work = append(work, workRecord{
						a: a,
						t: t,
						r: r,
					})
				}
				p.types[i].t = t
				p.types[i].r = r
			}
			return
		}

		// Add an interior typing to object #i.
		c := typeChunk{off: off, t: t, r: r}

		// Merge the given typing into the chunks we already know.
		// TODO: this could be O(n) per insert if there are lots of internal pointers.
		chunks := interior[i]
		newchunks := chunks[:0]
		addWork := true
		for _, d := range chunks {
			if c.max() <= d.min() || c.min() >= d.max() {
				// c does not overlap with d.
				if c.t == d.t && (c.max() == d.min() || c.min() == d.max()) {
					// c and d abut and share the same base type. Merge them.
					c = c.merge(d)
					continue
				}
				// Keep existing chunk d.
				// Overwrites chunks slice, but we're only merging chunks so it
				// can't overwrite to-be-processed elements.
				newchunks = append(newchunks, d)
				continue
			}
			// There is some overlap. There are a few possibilities:
			// 1) One is completely contained in the other.
			// 2) Both are slices of a larger underlying array.
			// 3) Some unsafe trickery has happened. Non-containing overlap
			//    can only happen in safe Go via case 2.
			if c.min() >= d.min() && c.max() <= d.max() {
				// 1a: c is contained within the existing chunk d.
				// Note that there can be a type mismatch between c and d,
				// but we don't care. We use the larger chunk regardless.
				c = d
				addWork = false // We've already scanned all of c.
				continue
			}
			if d.min() >= c.min() && d.max() <= c.max() {
				// 1b: existing chunk d is completely covered by c.
				continue
			}
			if c.t == d.t && c.matchingAlignment(d) {
				// Union two regions of the same base type. Case 2 above.
				c = c.merge(d)
				continue
			}
			if c.size() < d.size() {
				// Keep the larger of the two chunks.
				c = d
				addWork = false
			}
		}
		// Add new chunk to list of chunks for object.
		newchunks = append(newchunks, c)
		interior[i] = newchunks
		// Also arrange to scan the new chunk. Note that if we merged
		// with an existing chunk (or chunks), those will get rescanned.
		// Duplicate work, but that's ok. TODO: but could be expensive.
		if addWork {
			work = append(work, workRecord{
				a: a.Add(c.off - off),
				t: c.t,
				r: c.r,
			})
		}
	}

	// Get typings starting at roots.
	p.ForEachRoot(func(r *Root) bool {
		rr := &rootReaderAt{p: p, r: r}
		if r.Frame != nil {
			rr.frameLive = r.Frame.Live
			p.typeObject(rr, r.Type, add)
		} else {
			p.typeObject(rr, r.Type, add)
		}
		return true
	})

	// Propagate typings through the heap.
	for len(work) > 0 {
		c := work[len(work)-1]
		work = work[:len(work)-1]
		switch c.t.Kind {
		case KindBool, KindInt, KindUint, KindFloat, KindComplex:
			// Don't do O(n) function calls for big primitive slices
			continue
		}
		for i := int64(0); i < c.r; i++ {
			p.typeObject(&addrReaderAt{p, c.a.Add(i * c.t.Size)}, c.t, add)
		}
	}

	// Merge any interior typings with the 0-offset typing.
	for i, chunks := range interior {
		t := p.types[i].t
		r := p.types[i].r
		if t == nil {
			continue // We have no type info at offset 0.
		}
		for _, c := range chunks {
			if c.max() <= r*t.Size {
				// c is completely contained in the 0-offset typing. Ignore it.
				continue
			}
			if c.min() <= r*t.Size {
				// Typings overlap or abut. Extend if we can.
				if c.t == t && c.min()%t.Size == 0 {
					r = c.max() / t.Size
					p.types[i].r = r
				}
				continue
			}
			// Note: at this point we throw away any interior typings that weren't
			// merged with the 0-offset typing.  TODO: make more use of this info.
		}
	}
}

// valueReaderAt is an interface that abstracts over reading logically contiguous
// values. This is to allow contiguously reading values that are not actually stored
// contiguously.
type valueReaderAt interface {
	// ReadPtrAt loads a core.Address at offset bytes from the start of the value.
	//
	// Returns the code.Address read and the address it was read from, if there exists
	// such a meaningful address (that is, from might be zero if, for example, the
	// core.Address was read out of a register).
	ReadPtrAt(offset int64) (value, from core.Address)

	// ReadIntAt loads a Go 'int' at offset bytes from the start of the value.
	//
	// Returns the 'int' read and the address it was read from, if there exists
	// such a meaningful address (that is, from might be zero if, for example, the
	// 'int' was read out of a register).
	ReadIntAt(offset int64) (value int64, from core.Address)

	// Advance returns a new valueReaderAt that starts at an offset into the value
	// represented by this valueReaderAt. This is useful for reading, for example,
	// struct values. Each field value in the struct can be referenced by advancing
	// to the field offset.
	Advance(offset int64) valueReaderAt
}

// addrReaderAt is a trivial valueReaderAt that reads a value stored contiguously
// in memory at some address.
type addrReaderAt struct {
	p *Process
	a core.Address
}

func (ar *addrReaderAt) ReadPtrAt(offset int64) (value, from core.Address) {
	return ar.p.proc.ReadPtr(ar.a.Add(offset)), ar.a.Add(offset)
}

func (ar *addrReaderAt) ReadIntAt(offset int64) (value int64, from core.Address) {
	return ar.p.proc.ReadInt(ar.a.Add(offset)), ar.a.Add(offset)
}

func (ar *addrReaderAt) Advance(offset int64) valueReaderAt {
	nar := *ar
	nar.a = nar.a.Add(offset)
	return &nar
}

// rootReaderAt is a valueReaderAt that reads a value stored logically contiguously
// in a Root.
type rootReaderAt struct {
	p         *Process
	r         *Root
	offset    int64
	frameLive map[core.Address]bool
}

func (rr *rootReaderAt) ReadPtrAt(offset int64) (value, from core.Address) {
	ptr, from := rr.p.readRootPtr(rr.r, offset+rr.offset)
	if from != 0 && rr.frameLive != nil && !rr.frameLive[from] {
		return 0, from
	}
	return ptr, from
}

func (rr *rootReaderAt) ReadIntAt(offset int64) (value int64, from core.Address) {
	var i [8]byte
	from = rr.p.readRootAt(rr.r, i[:], offset+rr.offset)
	if rr.p.proc.PtrSize() == 4 {
		return int64(binary.LittleEndian.Uint32(i[:])), from
	}
	return int64(binary.LittleEndian.Uint64(i[:])), from
}

func (rr *rootReaderAt) Advance(offset int64) valueReaderAt {
	nrr := *rr
	nrr.offset += offset
	return &nrr
}

func methodFromMethodValueWrapper(name string) (string, bool) {
	return strings.CutSuffix(name, "-fm")
}

// ifaceIndir reports whether t is stored indirectly in an interface value.
func ifaceIndir(t core.Address, p *Process) bool {
	typr := p.findRuntimeType(t)
	if tflagDirectIface, ok := p.rtConsts.find("internal/abi.TFlagDirectIface"); ok {
		// 1.26 and later, direct bit stored in tflags
		return typr.TFlag()&uint8(tflagDirectIface) == 0
	}
	// 1.25 and earlier, direct bit stored in kind field
	return typr.Kind_()&uint8(p.rtConsts.get("internal/abi.KindDirectIface")) == 0
}

// typeObject takes an address and a type for the data at that address.
// For each pointer it finds in the memory at that address, it calls add with the pointer
// and the type + repeat count of the thing that it points to.
func (p *Process) typeObject(r valueReaderAt, t *Type, add func(core.Address, *Type, int64)) {
	ptrSize := p.proc.PtrSize()

	switch t.Kind {
	case KindBool, KindInt, KindUint, KindFloat, KindComplex:
		// Nothing to do
	case KindEface, KindIface:
		// Interface.
		//
		// Load the data word first and back out if its a nil
		// or typed-nil interface. We must do this because it's
		// our only signal that this value is dead. Consider
		// a dead eface variable on the stack: the data field
		// will return nil because it's dead, but the type pointer
		// will likely be bogus.
		if value, _ := r.ReadPtrAt(ptrSize); value == 0 { // nil interface
			return
		}
		// Use the type word to determine the type
		// of the pointed-to object.
		typPtr, data := r.ReadPtrAt(0)
		if typPtr == 0 { // nil interface
			return
		}
		if t.Kind == KindIface {
			if p.findItab().Type() == nil {
				return
			}
			typPtr = p.proc.ReadPtr(typPtr.Add(p.findItab().Type().Off))
		}
		// TODO: for KindEface, type typPtr. It might point to the heap
		// if the type was allocated with reflect.
		typ := p.runtimeType2Type(typPtr, data)
		if ifaceIndir(typPtr, p) {
			// Indirect interface: the interface introduced a new
			// level of indirection, not reflected in the type.
			// Read through it.
			value, _ := r.ReadPtrAt(ptrSize)
			add(value, typ, 1)
			return
		}

		// Direct interface: the contained type is a single pointer.
		// Figure out what it is and type it. See isdirectiface() for the rules.
		directTyp := typ
	findDirect:
		for {
			if directTyp.Kind == KindArray {
				directTyp = typ.Elem
				continue findDirect
			}
			if directTyp.Kind == KindStruct {
				for _, f := range directTyp.Fields {
					if f.Type.Size != 0 {
						directTyp = f.Type
						continue findDirect
					}
				}
			}
			if directTyp.Kind == KindPtr || directTyp.Kind == KindFunc {
				p.typeObject(r.Advance(ptrSize), directTyp, add)
				break
			}
			panic(fmt.Sprintf("type of direct interface, originally %s (kind %s), isn't a pointer: %s (kind %s)", typ, typ.Kind, directTyp, directTyp.Kind))
		}
	case KindString:
		ptr, _ := r.ReadPtrAt(0)
		len, _ := r.ReadIntAt(ptrSize)
		add(ptr, t.Elem, len)
	case KindSlice:
		ptr, _ := r.ReadPtrAt(0)
		cap, _ := r.ReadIntAt(2 * ptrSize)
		add(ptr, t.Elem, cap)
	case KindPtr:
		if t.Elem != nil { // unsafe.Pointer has a nil Elem field.
			ptr, _ := r.ReadPtrAt(0)
			add(ptr, t.Elem, 1)
		}
	case KindFunc:
		// The referent is a closure. We don't know much about the
		// type of the referent. Its first entry is a code pointer.
		// The runtime._type we want exists in the binary (for all
		// heap-allocated closures, anyway) but it would be hard to find
		// just given the pc.
		closure, _ := r.ReadPtrAt(0)
		if closure == 0 {
			break
		}
		pc := p.proc.ReadPtr(closure)
		f := p.funcTab.find(pc)
		if f == nil {
			panic(fmt.Sprintf("can't find func for closure pc %x", pc))
		}
		ft := f.closure
		if ft == nil {
			ft = &Type{Name: "closure for " + f.name, Size: ptrSize, Kind: KindPtr}
			// For now, treat a closure like an unsafe.Pointer.
			// TODO: better value for size?
			f.closure = ft
		}
		p.typeObject(&addrReaderAt{p, closure}, ft, add)

		// Handle the special case for method value.
		// It's a single-entry closure laid out like {pc uintptr, x T}.
		if method, ok := methodFromMethodValueWrapper(f.name); ok {
			mf := p.funcTab.findByName(method)
			if mf != nil {
				for _, v := range p.dwarfVars[mf] {
					if v.kind == dwarfParam {
						ptr := closure.Add(p.proc.PtrSize())
						p.typeObject(&addrReaderAt{p, ptr}, v.typ, add)
						break
					}
				}
			}
		}
	case KindArray:
		n := t.Elem.Size
		for i := int64(0); i < t.Count; i++ {
			p.typeObject(r.Advance(i*n), t.Elem, add)
		}
	case KindStruct:
		if strings.HasPrefix(t.Name, "hash<") {
			// Special case - maps have a pointer to the first bucket
			// but it really types all the buckets (like a slice would).
			var bPtr core.Address
			var bTyp *Type
			var n int64
			for _, f := range t.Fields {
				if f.Name == "buckets" {
					bPtr, _ = r.ReadPtrAt(f.Off)
					bTyp = f.Type.Elem
				}
				if f.Name == "B" {
					shift, _ := r.ReadIntAt(f.Off)
					n = int64(1) << uint8(shift)
				}
			}
			add(bPtr, bTyp, n)
			// TODO: also oldbuckets
		}
		for _, f := range t.Fields {
			// hchan.buf(in chan) is an unsafe.pointer to an [dataqsiz]elemtype.
			if strings.HasPrefix(t.Name, "hchan<") && f.Name == "buf" && f.Type.Kind == KindPtr {
				elemType, _ := r.ReadPtrAt(t.field("elemtype").Off)
				bufPtr, _ := r.ReadPtrAt(t.field("buf").Off)
				rTyp := p.runtimeType2Type(elemType, 0)
				dataqsiz, _ := r.ReadPtrAt(t.field("dataqsiz").Off)
				add(bufPtr, rTyp, int64(dataqsiz))
			}
			p.typeObject(r.Advance(f.Off), f.Type, add)
		}
	default:
		panic(fmt.Sprintf("unknown type kind %s\n", t.Kind))
	}
}

// forEachPointer iterates over each pointer in the type, emitting the offset of the
// pointer in the type.
func (t *Type) forEachPointer(baseOffset, ptrSize int64, yield func(off int64)) {
	switch t.Kind {
	case KindBool, KindInt, KindUint, KindFloat, KindComplex:
		// Nothing to do
	case KindEface, KindIface:
		yield(ptrSize)
	case KindString, KindSlice, KindPtr, KindFunc:
		yield(baseOffset)
	case KindArray:
		n := t.Elem.Size
		for i := int64(0); i < t.Count; i++ {
			t.Elem.forEachPointer(baseOffset+i*n, ptrSize, yield)
		}
	case KindStruct:
		for _, f := range t.Fields {
			f.Type.forEachPointer(baseOffset+f.Off, ptrSize, yield)
		}
	default:
		panic(fmt.Sprintf("unknown type kind %s\n", t.Kind))
	}
}
