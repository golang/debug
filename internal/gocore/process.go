// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocore

import (
	"debug/dwarf"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"math/bits"
	"sort"
	"strings"
	"sync"

	"golang.org/x/debug/internal/core"

	"github.com/go-delve/delve/pkg/dwarf/op"
	"github.com/go-delve/delve/pkg/dwarf/regnum"
)

// A Process represents the state of a Go process that core dumped.
type Process struct {
	proc         *core.Process
	buildVersion string

	// Index of heap objects and pointers.
	heap *heapTable

	// number of live objects
	nObj int

	goroutines []*Goroutine

	// Runtime info for easier lookup.
	rtGlobals map[string]region
	rtConsts  map[string]int64

	// A module is a loadable unit. Most Go programs have 1, programs
	// which load plugins will have more.
	modules []*module

	// Maps core.Address to functions.
	funcTab *funcTab

	// Fundamental type mappings extracted from the core.
	dwarfTypeMap map[dwarf.Type]*Type
	rtTypeByName map[string]*Type

	// rtTypeMap maps a core.Address to a *Type. Constructed incrementally, on-demand.
	rtTypeMap map[core.Address]*Type

	// Memory usage breakdown.
	stats *Statistic

	// Global roots.
	globals []*Root

	// Types of each object, indexed by object index.
	initTypeHeap sync.Once
	types        []typeInfo

	// Reverse edges.
	// The reverse edges for object #i are redge[ridx[i]:ridx[i+1]].
	// A "reverse edge" for object #i is a location in memory where a pointer
	// to object #i lives.
	initReverseEdges sync.Once
	redge            []core.Address
	ridx             []int64
	// Sorted list of all roots.
	// Only initialized if FlagReverse is passed to Core.
	rootIdx []*Root
}

// Core takes a loaded core file and extracts Go information from it.
func Core(proc *core.Process) (p *Process, err error) {
	p = &Process{proc: proc}

	// Initialize everything that just depends on DWARF.
	p.dwarfTypeMap, err = readDWARFTypes(proc)
	if err != nil {
		return nil, err
	}
	p.rtTypeByName = make(map[string]*Type)
	for dt, t := range p.dwarfTypeMap {
		name := gocoreName(dt)
		if _, ok := p.rtTypeByName[name]; ok {
			// If a runtime type matches more than one DWARF type,
			// pick one arbitrarily.
			//
			// This looks mostly harmless. DWARF has some redundant entries.
			// For example, [32]uint8 appears twice.
			//
			// TODO(mknyszek): Investigate the reason for this duplication.
			continue
		}
		p.rtTypeByName[name] = t
	}
	p.rtConsts, err = readRuntimeConstants(proc)
	if err != nil {
		return nil, err
	}
	p.globals, err = readGlobals(proc, p.dwarfTypeMap)
	if err != nil {
		return nil, err
	}

	// Find runtime globals we care about. Initialize regions for them.
	p.rtGlobals = make(map[string]region)
	for _, g := range p.globals {
		if strings.HasPrefix(g.Name, "runtime.") {
			p.rtGlobals[g.Name[8:]] = region{p: proc, a: g.Addr, typ: g.Type}
		}
	}

	// Read all the data that depend on runtime globals.
	p.buildVersion = p.rtGlobals["buildVersion"].String()

	// Read modules and function data.
	p.modules, p.funcTab, err = readModules(p.rtTypeByName, p.rtConsts, p.rtGlobals)
	if err != nil {
		return nil, err
	}

	// Initialize the heap data structures.
	p.heap, p.stats, err = readHeap(p)
	if err != nil {
		return nil, err
	}

	// Read stack and register variables from DWARF.
	dwarfVars, err := readDWARFVars(proc, p.funcTab, p.dwarfTypeMap)
	if err != nil {
		return nil, err
	}

	// Read goroutines.
	p.goroutines, err = readGoroutines(p, dwarfVars)
	if err != nil {
		return nil, err
	}

	p.markObjects() // needs to be after readGlobals, readGs.
	return p, nil
}

// Process returns the core.Process used to construct this Process.
func (p *Process) Process() *core.Process {
	return p.proc
}

func (p *Process) Goroutines() []*Goroutine {
	return p.goroutines
}

// Stats returns a breakdown of the program's memory use by category.
func (p *Process) Stats() *Statistic {
	return p.stats
}

// BuildVersion returns the Go version that was used to build the inferior binary.
func (p *Process) BuildVersion() string {
	return p.buildVersion
}

func (p *Process) Globals() []*Root {
	return p.globals
}

// FindFunc returns the function which contains the code at address pc, if any.
func (p *Process) FindFunc(pc core.Address) *Func {
	return p.funcTab.find(pc)
}

func (p *Process) findType(name string) *Type {
	typ := p.tryFindType(name)
	if typ == nil {
		panic("can't find type " + name)
	}
	return typ
}

func (p *Process) tryFindType(name string) *Type {
	return p.rtTypeByName[name]
}

// arena is a summary of the size of components of a heapArena.
type arena struct {
	heapMin      core.Address
	heapMax      core.Address
	spanTableMin core.Address
	spanTableMax core.Address
}

func readHeap(p *Process) (*heapTable, *Statistic, error) {
	mheap := p.rtGlobals["mheap_"]

	var arenas []arena
	arenaSize := p.rtConsts["heapArenaBytes"]
	if arenaSize%heapInfoSize != 0 {
		panic("arenaSize not a multiple of heapInfoSize")
	}
	arenaBaseOffset := -p.rtConsts["arenaBaseOffsetUintptr"]
	if p.proc.PtrSize() == 4 && arenaBaseOffset != 0 {
		panic("arenaBaseOffset must be 0 for 32-bit inferior")
	}

	level1Table := mheap.Field("arenas")
	level1size := level1Table.ArrayLen()
	for level1 := int64(0); level1 < level1size; level1++ {
		ptr := level1Table.ArrayIndex(level1)
		if ptr.Address() == 0 {
			continue
		}
		level2table := ptr.Deref()
		level2size := level2table.ArrayLen()
		for level2 := int64(0); level2 < level2size; level2++ {
			ptr = level2table.ArrayIndex(level2)
			if ptr.Address() == 0 {
				continue
			}
			a := ptr.Deref()

			min := core.Address(arenaSize*(level2+level1*level2size) - arenaBaseOffset)
			max := min.Add(arenaSize)

			arenas = append(arenas, readArena(a, min, max))
		}
	}
	return readHeap0(p, mheap, arenas, arenaBaseOffset)
}

// Read a single heapArena. Go 1.11+, which has multiple areans. Record heap
// pointers and return the arena size summary.
func readArena(a region, min, max core.Address) arena {
	ptrSize := a.p.PtrSize()
	spans := a.Field("spans")
	arena := arena{
		heapMin:      min,
		heapMax:      max,
		spanTableMin: spans.a,
		spanTableMax: spans.a.Add(spans.ArrayLen() * ptrSize),
	}
	return arena
}

func readHeap0(p *Process, mheap region, arenas []arena, arenaBaseOffset int64) (*heapTable, *Statistic, error) {
	// TODO(mknyszek): Break up this function into heapTable setup and statistics collection,
	// at the very least...

	// The main goal of this function is to initialize this data structure.
	heap := &heapTable{
		table:   make(map[heapTableID]*heapTableEntry),
		ptrSize: uint64(p.proc.PtrSize()),
	}

	// ... But while we're here, we'll be collecting stats.
	var stats struct {
		all              int64
		text             int64
		readOnly         int64
		spanTable        int64
		data             int64
		bss              int64
		freeSpanSize     int64
		releasedSpanSize int64
		manualSpanSize   int64
		inUseSpanSize    int64
		allocSize        int64
		freeSize         int64
		spanRoundSize    int64
		manualAllocSize  int64
		manualFreeSize   int64
	}
	for _, m := range p.proc.Mappings() {
		size := m.Size()
		stats.all += size
		switch m.Perm() {
		case core.Read:
			stats.readOnly += size
		case core.Read | core.Exec:
			stats.text += size
		case core.Read | core.Write:
			if m.CopyOnWrite() {
				// Check if m.file == text's file? That could distinguish
				// data segment from mmapped file.
				stats.data += size
				break
			}
			attribute := func(x, y core.Address, p *int64) {
				a := x.Max(m.Min())
				b := y.Min(m.Max())
				if a < b {
					*p += b.Sub(a)
					size -= b.Sub(a)
				}
			}
			for _, a := range arenas {
				attribute(a.spanTableMin, a.spanTableMax, &stats.spanTable)
			}
			// Any other anonymous mapping is bss.
			// TODO: how to distinguish original bss from anonymous mmap?
			stats.bss += size
		case core.Exec: // Ignore --xp mappings, like Linux's vsyscall=xonly.
			stats.all -= size // Make the total match again.
		default:
			return nil, nil, errors.New("weird mapping " + m.Perm().String())
		}
	}
	pageSize := p.rtConsts["_PageSize"]

	// Span types.
	spanInUse := uint8(p.rtConsts["mSpanInUse"])
	spanManual := uint8(p.rtConsts["mSpanManual"])
	spanDead := uint8(p.rtConsts["mSpanDead"])

	// Malloc header constants (go 1.22+)
	minSizeForMallocHeader := int64(p.rtConsts["minSizeForMallocHeader"])
	mallocHeaderSize := int64(p.rtConsts["mallocHeaderSize"])
	maxSmallSize := int64(p.rtConsts["maxSmallSize"])

	abiType := p.tryFindType("internal/abi.Type")

	// Process spans.
	if pageSize%heapInfoSize != 0 {
		return nil, nil, fmt.Errorf("page size not a multiple of %d", heapInfoSize)
	}
	allspans := mheap.Field("allspans")
	n := allspans.SliceLen()
	for i := int64(0); i < n; i++ {
		s := allspans.SliceIndex(i).Deref()
		min := core.Address(s.Field("startAddr").Uintptr())
		elemSize := int64(s.Field("elemsize").Uintptr())
		nPages := int64(s.Field("npages").Uintptr())
		spanSize := nPages * pageSize
		max := min.Add(spanSize)
		for a := min; a != max; a = a.Add(pageSize) {
			if !p.proc.Readable(a) {
				// Sometimes allocated but not yet touched pages or
				// MADV_DONTNEEDed pages are not written
				// to the core file.  Don't count these pages toward
				// space usage (otherwise it can look like the heap
				// is larger than the total memory used).
				spanSize -= pageSize
			}
		}
		st := s.Field("state")
		if st.IsStruct() && st.HasField("s") { // go1.14+
			st = st.Field("s")
		}
		if st.IsStruct() && st.HasField("value") { // go1.20+
			st = st.Field("value")
		}
		switch st.Uint8() {
		case spanInUse:
			stats.inUseSpanSize += spanSize
			nelems := s.Field("nelems")
			var n int64
			if nelems.IsUint16() { // go1.22+
				n = int64(nelems.Uint16())
			} else {
				n = int64(nelems.Uintptr())
			}
			// An object is allocated if it is marked as
			// allocated or it is below freeindex.
			x := s.Field("allocBits").Address()
			alloc := make([]bool, n)
			for i := int64(0); i < n; i++ {
				alloc[i] = p.proc.ReadUint8(x.Add(i/8))>>uint(i%8)&1 != 0
			}
			freeindex := s.Field("freeindex")
			var k int64
			if freeindex.IsUint16() { // go1.22+
				k = int64(freeindex.Uint16())
			} else {
				k = int64(freeindex.Uintptr())
			}
			for i := int64(0); i < k; i++ {
				alloc[i] = true
			}
			for i := int64(0); i < n; i++ {
				if alloc[i] {
					stats.allocSize += elemSize
				} else {
					stats.freeSize += elemSize
				}
			}
			stats.spanRoundSize += spanSize - n*elemSize

			// initialize heap info records for all inuse spans.
			for a := min; a < max; a += heapInfoSize {
				h := heap.getOrCreate(a)
				h.base = min
				h.size = elemSize
			}

			// Process special records.
			for sp := s.Field("specials"); sp.Address() != 0; sp = sp.Field("next") {
				sp = sp.Deref() // *special to special
				if sp.Field("kind").Uint8() != uint8(p.rtConsts["_KindSpecialFinalizer"]) {
					// All other specials (just profile records) can't point into the heap.
					continue
				}
				obj := min.Add(int64(sp.Field("offset").Uint64()))
				p.globals = append(p.globals,
					&Root{
						Name:  fmt.Sprintf("finalizer for %x", obj),
						Addr:  sp.a,
						Type:  p.findType("runtime.specialfinalizer"),
						Frame: nil,
					})
				// TODO: these aren't really "globals", as they
				// are kept alive by the object they reference being alive.
				// But we have no way of adding edges from an object to
				// the corresponding finalizer data, so we punt on that thorny
				// issue for now.
			}
			if noscan := s.Field("spanclass").Uint8()&1 != 0; noscan {
				// No pointers.
				continue
			}
			if elemSize <= minSizeForMallocHeader {
				// Heap bits in span.
				bitmapSize := spanSize / int64(heap.ptrSize) / 8
				bitmapAddr := min.Add(spanSize - bitmapSize)
				for i := int64(0); i < bitmapSize; i++ {
					bits := p.proc.ReadUint8(bitmapAddr.Add(int64(i)))
					for j := int64(0); j < 8; j++ {
						if bits&(uint8(1)<<j) != 0 {
							heap.setIsPointer(min.Add(int64(heap.ptrSize) * (i*8 + j)))
						}
					}
				}
			} else if elemSize <= maxSmallSize-mallocHeaderSize {
				// Allocation headers.
				//
				// These will always point to real abi.Type values that, once allocated,
				// are never freed, so it's safe to observe them even if the object is
				// dead. We may note down pointers that are invalid if the object is not
				// allocated (or live) but that's no different from reading stale bits
				// out of the bitmap in older Go versions.
				for e, off := 0, int64(0); int64(e) < n; e, off = e+1, off+elemSize {
					// We need to be careful to only check space that's actually marked
					// allocated, otherwise it can contain junk, including an invalid
					// header.
					if !alloc[e] {
						continue
					}
					typeAddr := p.proc.ReadPtr(min.Add(off))
					if typeAddr == 0 {
						continue
					}
					typ := region{p: p.proc, a: typeAddr, typ: abiType}
					nptrs := int64(typ.Field("PtrBytes").Uintptr()) / int64(heap.ptrSize)
					if typ.Field("Kind_").Uint8()&uint8(p.rtConsts["kindGCProg"]) != 0 {
						panic("unexpected GC prog on small allocation")
					}
					gcdata := typ.Field("GCData").Address()
					for i := int64(0); i < nptrs; i++ {
						if p.proc.ReadUint8(gcdata.Add(i/8))>>uint(i%8)&1 != 0 {
							heap.setIsPointer(min.Add(off + mallocHeaderSize + i*int64(heap.ptrSize)))
						}
					}
				}
			} else {
				// Large object (header in span).
				//
				// These will either point to a real type or a "dummy" type whose storage
				// is not valid if the object is dead. However, because large objects are
				// 1:1 with spans, we can be certain largeType is valid as long as the span
				// is in use.
				typ := s.Field("largeType").Deref()
				nptrs := int64(typ.Field("PtrBytes").Uintptr()) / int64(heap.ptrSize)
				if typ.Field("Kind_").Uint8()&uint8(p.rtConsts["kindGCProg"]) != 0 {
					panic("large object's GCProg was not unrolled")
				}
				gcdata := typ.Field("GCData").Address()
				for i := int64(0); i < nptrs; i++ {
					if p.proc.ReadUint8(gcdata.Add(i/8))>>uint(i%8)&1 != 0 {
						heap.setIsPointer(min.Add(i * int64(heap.ptrSize)))
					}
				}
			}
		case spanDead:
			// These are just deallocated span descriptors. They use no heap.
		case spanManual:
			stats.manualSpanSize += spanSize
			stats.manualAllocSize += spanSize
			for x := core.Address(s.Field("manualFreeList").Cast(p.findType("uintptr")).Uintptr()); x != 0; x = p.proc.ReadPtr(x) {
				stats.manualAllocSize -= elemSize
				stats.manualFreeSize += elemSize
			}
		}
	}

	// There are no longer "free" mspans to represent unused pages.
	// Instead, there are just holes in the pagemap into which we can allocate.
	// Look through the page allocator and count the total free space.
	// Also keep track of how much has been scavenged.
	pages := mheap.Field("pages")
	chunks := pages.Field("chunks")
	pallocChunkBytes := p.rtConsts["pallocChunkBytes"]
	pallocChunksL1Bits := p.rtConsts["pallocChunksL1Bits"]
	pallocChunksL2Bits := p.rtConsts["pallocChunksL2Bits"]
	inuse := pages.Field("inUse")
	ranges := inuse.Field("ranges")
	for i := int64(0); i < ranges.SliceLen(); i++ {
		r := ranges.SliceIndex(i)
		baseField := r.Field("base").Field("a")
		base := core.Address(baseField.Uintptr())
		limitField := r.Field("limit").Field("a")
		limit := core.Address(limitField.Uintptr())
		chunkBase := (int64(base) + arenaBaseOffset) / pallocChunkBytes
		chunkLimit := (int64(limit) + arenaBaseOffset) / pallocChunkBytes
		for chunkIdx := chunkBase; chunkIdx < chunkLimit; chunkIdx++ {
			var l1, l2 int64
			if pallocChunksL1Bits == 0 {
				l2 = chunkIdx
			} else {
				l1 = chunkIdx >> uint(pallocChunksL2Bits)
				l2 = chunkIdx & (1<<uint(pallocChunksL2Bits) - 1)
			}
			chunk := chunks.ArrayIndex(l1).Deref().ArrayIndex(l2)
			// Count the free bits in this chunk.
			alloc := chunk.Field("pallocBits")
			for i := int64(0); i < pallocChunkBytes/pageSize/64; i++ {
				stats.freeSpanSize += int64(bits.OnesCount64(^alloc.ArrayIndex(i).Uint64())) * pageSize
			}
			// Count the scavenged bits in this chunk.
			scavenged := chunk.Field("scavenged")
			for i := int64(0); i < pallocChunkBytes/pageSize/64; i++ {
				stats.releasedSpanSize += int64(bits.OnesCount64(scavenged.ArrayIndex(i).Uint64())) * pageSize
			}
		}
	}

	// Also count pages in the page cache for each P.
	allp := p.rtGlobals["allp"]
	for i := int64(0); i < allp.SliceLen(); i++ {
		pcache := allp.SliceIndex(i).Deref().Field("pcache")
		stats.freeSpanSize += int64(bits.OnesCount64(pcache.Field("cache").Uint64())) * pageSize
		stats.releasedSpanSize += int64(bits.OnesCount64(pcache.Field("scav").Uint64())) * pageSize
	}

	// Create stats.
	//
	// TODO(mknyszek): Double-check that our own computations of the group stats match the sums here.
	return heap, groupStat("all",
		leafStat("text", stats.text),
		leafStat("readonly", stats.readOnly),
		leafStat("data", stats.data),
		leafStat("bss", stats.bss),
		groupStat("heap",
			groupStat("in use spans",
				leafStat("alloc", stats.allocSize),
				leafStat("free", stats.freeSize),
				leafStat("round", stats.spanRoundSize),
			),
			groupStat("manual spans",
				leafStat("alloc", stats.manualAllocSize),
				leafStat("free", stats.manualFreeSize),
			),
			groupStat("free spans",
				leafStat("retained", stats.freeSpanSize-stats.releasedSpanSize),
				leafStat("released", stats.releasedSpanSize),
			),
		),
		leafStat("span table", stats.spanTable),
	), nil
}

func readGoroutines(p *Process, dwarfVars map[*Func][]dwarfVar) ([]*Goroutine, error) {
	allgs := p.rtGlobals["allgs"]
	n := allgs.SliceLen()
	var goroutines []*Goroutine
	for i := int64(0); i < n; i++ {
		r := allgs.SliceIndex(i).Deref()
		g, err := readGoroutine(p, r, dwarfVars)
		if err != nil {
			return nil, fmt.Errorf("reading goroutine: %v", err)
		}
		if g == nil {
			continue
		}
		goroutines = append(goroutines, g)
	}
	return goroutines, nil
}

func readGoroutine(p *Process, r region, dwarfVars map[*Func][]dwarfVar) (*Goroutine, error) {
	// Set up register descriptors for DWARF stack programs to be executed.
	g := &Goroutine{r: r}
	stk := r.Field("stack")
	g.stackSize = int64(stk.Field("hi").Uintptr() - stk.Field("lo").Uintptr())

	var osT *core.Thread // os thread working on behalf of this G (if any).
	mp := r.Field("m")
	if mp.Address() != 0 {
		m := mp.Deref()
		pid := m.Field("procid").Uint64()
		// TODO check that m.curg points to g?
		for _, t := range p.proc.Threads() {
			if t.Pid() == pid {
				osT = t
			}
		}
	}
	st := r.Field("atomicstatus").Field("value")
	status := st.Uint32()
	status &^= uint32(p.rtConsts["_Gscan"])
	var sp, pc core.Address
	switch status {
	case uint32(p.rtConsts["_Gidle"]):
		return g, nil
	case uint32(p.rtConsts["_Grunnable"]), uint32(p.rtConsts["_Gwaiting"]):
		sched := r.Field("sched")
		sp = core.Address(sched.Field("sp").Uintptr())
		pc = core.Address(sched.Field("pc").Uintptr())
	case uint32(p.rtConsts["_Grunning"]):
		sp = osT.SP()
		pc = osT.PC()
		// TODO: back up to the calling frame?
	case uint32(p.rtConsts["_Gsyscall"]):
		sp = core.Address(r.Field("syscallsp").Uintptr())
		pc = core.Address(r.Field("syscallpc").Uintptr())
		// TODO: or should we use the osT registers?
	case uint32(p.rtConsts["_Gdead"]):
		return nil, nil
		// TODO: copystack, others?
	default:
		// Unknown state. We can't read the frames, so just bail now.
		// TODO: make this switch complete and then panic here.
		// TODO: or just return nil?
		return g, nil
	}

	// Set up register context.
	var dregs []*op.DwarfRegister
	if osT != nil {
		dregs = hardwareRegs2DWARF(osT.Regs())
	} else {
		dregs = hardwareRegs2DWARF(nil)
	}
	regs := op.NewDwarfRegisters(p.proc.StaticBase(), dregs, binary.LittleEndian, regnum.AMD64_Rip, regnum.AMD64_Rsp, regnum.AMD64_Rbp, 0)

	// Read all the frames.
	isCrashFrame := false
	for {
		f, err := readFrame(p, sp, pc)
		if err != nil {
			goid := r.Field("goid").Uint64()
			fmt.Printf("warning: giving up on backtrace for %d after %d frames: %v\n", goid, len(g.frames), err)
			break
		}
		if f.f.name == "runtime.goexit" {
			break
		}
		if len(g.frames) > 0 {
			g.frames[len(g.frames)-1].parent = f
		}
		g.frames = append(g.frames, f)

		regs.CFA = int64(f.max)
		regs.FrameBase = int64(f.max)

		// Start with all pointer slots as unnamed.
		unnamed := map[core.Address]bool{}
		for a := range f.Live {
			unnamed[a] = true
		}
		conservativeLiveness := isCrashFrame && len(f.Live) == 0

		// Emit roots for DWARF entries.
		for _, v := range dwarfVars[f.f] {
			if !(v.lowPC <= f.pc && f.pc < v.highPC) {
				continue
			}
			addr, pieces, err := op.ExecuteStackProgram(*regs, v.instr, int(p.proc.PtrSize()), func(buf []byte, addr uint64) (int, error) {
				p.proc.ReadAt(buf, core.Address(addr))
				return len(buf), nil
			})
			if err != nil {
				return nil, fmt.Errorf("failed to execute DWARF stack program for variable %s: %v", v.name, err)
			}
			if addr == 0 {
				// TODO(mknyszek): Handle composites via pieces returned by the stack program.
				continue
			}
			if addr != 0 && len(pieces) == 1 && v.typ.Kind == KindPtr {
				r := &Root{
					Name:     v.name,
					RegName:  regnum.AMD64ToName(pieces[0].Val),
					RegValue: core.Address(addr),
					Type:     v.typ,
					Frame:    f,
				}
				g.regRoots = append(g.regRoots, r)
			} else {
				r := &Root{
					Name:  v.name,
					Addr:  core.Address(addr),
					Type:  v.typ,
					Frame: f,
				}
				f.roots = append(f.roots, r)

				if conservativeLiveness {
					// This is a frame that is likely not at a safe point, so the liveness
					// map is almost certainly empty. If it's not, then great, we can use
					// that for precise information, but otherwise, let's be conservative
					// and populate liveness data from the DWARF.
					r.Type.forEachPointer(0, p.proc.PtrSize(), func(off int64) {
						f.Live[r.Addr.Add(off)] = true
					})
				}

				// Remove this variable from the set of unnamed pointers.
				for a := r.Addr; a < r.Addr.Add(r.Type.Size); a = a.Add(p.proc.PtrSize()) {
					delete(unnamed, a)
				}
			}
		}

		// Emit roots for unnamed pointer slots in the frame.
		// Make deterministic by sorting first.
		s := make([]core.Address, 0, len(unnamed))
		for a := range unnamed {
			s = append(s, a)
		}
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		for _, a := range s {
			r := &Root{
				Name:  "unk",
				Addr:  a,
				Type:  p.findType("unsafe.Pointer"),
				Frame: f,
			}
			f.roots = append(f.roots, r)
		}

		// Figure out how to unwind to the next frame.
		if f.f.name == "runtime.sigtrampgo" {
			if osT == nil {
				// No thread attached to a goroutine in sigtrampgo?
				break
			}
			var ctxt core.Address
			for _, v := range dwarfVars[f.f] {
				if v.name != "ctx" {
					continue
				}
				if !(v.lowPC <= f.pc && f.pc < v.highPC) {
					continue
				}
				addr, pieces, err := op.ExecuteStackProgram(*regs, v.instr, int(p.proc.PtrSize()), func(buf []byte, addr uint64) (int, error) {
					p.proc.ReadAt(buf, core.Address(addr))
					return len(buf), nil
				})
				if err != nil {
					continue
				}
				if addr == 0 {
					continue
				}
				if addr != 0 && len(pieces) == 1 && v.typ.Kind == KindPtr {
					ctxt = core.Address(addr)
				} else {
					ctxt = p.proc.ReadPtr(core.Address(addr))
				}
			}
			if ctxt == 0 {
				// No way to unwind further.
				break
			}
			// Continue traceback at location where the signal
			// interrupted normal execution.

			// ctxt is a *ucontext
			mctxt := ctxt.Add(5 * 8)
			// mctxt is a *mcontext
			// TODO: totally arch-dependent!

			// Read new register context out of mcontext, before we continue.
			//
			// type mcontext struct {
			//     r8          uint64
			//     r9          uint64
			//     r10         uint64
			//     r11         uint64
			//     r12         uint64
			//     r13         uint64
			//     r14         uint64
			//     r15         uint64
			//     rdi         uint64
			//     rsi         uint64
			//     rbp         uint64
			//     rbx         uint64
			//     rdx         uint64
			//     rax         uint64
			//     rcx         uint64
			//     rsp         uint64
			//     rip         uint64
			//     eflags      uint64
			//     cs          uint16
			//     gs          uint16
			//     fs          uint16
			//     __pad0      uint16
			//     err         uint64
			//     trapno      uint64
			//     oldmask     uint64
			//     cr2         uint64
			//     fpstate     uint64 // pointer
			//     __reserved1 [8]uint64
			// }
			var hregs []core.Register
			i := int64(0)
			readReg := func(name string) uint64 {
				value := p.proc.ReadUint64(mctxt.Add(i))
				hregs = append(hregs, core.Register{Name: name, Value: value})
				i += 8
				return value
			}
			readReg("r8")
			readReg("r9")
			readReg("r10")
			readReg("r11")
			readReg("r12")
			readReg("r13")
			readReg("r14")
			readReg("r15")
			readReg("rdi")
			readReg("rsi")
			readReg("rbp")
			readReg("rbx")
			readReg("rdx")
			readReg("rax")
			readReg("rcx")
			sp = core.Address(readReg("rsp"))
			pc = core.Address(readReg("rip"))
			readReg("eflags")
			readReg("cs")
			readReg("gs")
			readReg("fs")

			// Update register state.
			dregs := hardwareRegs2DWARF(hregs)
			regs = op.NewDwarfRegisters(p.proc.StaticBase(), dregs, binary.LittleEndian, regnum.AMD64_Rip, regnum.AMD64_Rsp, regnum.AMD64_Rbp, 0)

			isCrashFrame = true
		} else {
			sp = f.max
			pc = core.Address(p.proc.ReadUintptr(sp - 8)) // TODO:amd64 only
		}
		if pc == 0 {
			// TODO: when would this happen?
			break
		}
		if f.f.name == "runtime.systemstack" {
			// switch over to goroutine stack
			sched := r.Field("sched")
			sp = core.Address(sched.Field("sp").Uintptr())
			pc = core.Address(sched.Field("pc").Uintptr())
		}
	}
	return g, nil
}

func readFrame(p *Process, sp, pc core.Address) (*Frame, error) {
	f := p.funcTab.find(pc)
	if f == nil {
		return nil, fmt.Errorf("cannot find func for pc=%#x", pc)
	}
	off := pc.Sub(f.entry)
	size, err := f.frameSize.find(off)
	if err != nil {
		return nil, fmt.Errorf("cannot read frame size at pc=%#x: %v", pc, err)
	}
	size += p.proc.PtrSize() // TODO: on amd64, the pushed return address

	frame := &Frame{f: f, pc: pc, min: sp, max: sp.Add(size)}

	// Find live ptrs in locals
	live := map[core.Address]bool{}
	if x := int(p.rtConsts["_FUNCDATA_LocalsPointerMaps"]); x < len(f.funcdata) {
		addr := f.funcdata[x]
		// TODO: Ideally we should have the same frame size check as
		// runtime.getStackSize to detect errors when we are missing
		// the stackmap.
		if addr != 0 {
			locals := region{p: p.proc, a: addr, typ: p.findType("runtime.stackmap")}
			n := locals.Field("n").Int32()       // # of bitmaps
			nbit := locals.Field("nbit").Int32() // # of bits per bitmap
			idx, err := f.stackMap.find(off)
			if err != nil {
				return nil, fmt.Errorf("cannot read stack map at pc=%#x: %v", pc, err)
			}
			if idx < 0 {
				idx = 0
			}
			if idx < int64(n) {
				bits := locals.Field("bytedata").a.Add(int64(nbit+7) / 8 * idx)
				base := frame.max.Add(-16).Add(-int64(nbit) * p.proc.PtrSize())
				// TODO: -16 for amd64. Return address and parent's frame pointer
				for i := int64(0); i < int64(nbit); i++ {
					if p.proc.ReadUint8(bits.Add(i/8))>>uint(i&7)&1 != 0 {
						live[base.Add(i*p.proc.PtrSize())] = true
					}
				}
			}
		}
	}
	// Same for args
	if x := int(p.rtConsts["_FUNCDATA_ArgsPointerMaps"]); x < len(f.funcdata) {
		addr := f.funcdata[x]
		if addr != 0 {
			args := region{p: p.proc, a: addr, typ: p.findType("runtime.stackmap")}
			n := args.Field("n").Int32()       // # of bitmaps
			nbit := args.Field("nbit").Int32() // # of bits per bitmap
			idx, err := f.stackMap.find(off)
			if err != nil {
				return nil, fmt.Errorf("cannot read stack map at pc=%#x: %v", pc, err)
			}
			if idx < 0 {
				idx = 0
			}
			if idx < int64(n) {
				bits := args.Field("bytedata").a.Add(int64(nbit+7) / 8 * idx)
				base := frame.max
				// TODO: add to base for LR archs.
				for i := int64(0); i < int64(nbit); i++ {
					if p.proc.ReadUint8(bits.Add(i/8))>>uint(i&7)&1 != 0 {
						live[base.Add(i*p.proc.PtrSize())] = true
					}
				}
			}
		}
	}
	frame.Live = live

	return frame, nil
}

// A Stats struct is the node of a tree representing the entire memory
// usage of the Go program. Children of a node break its usage down
// by category.
// We maintain the invariant that, if there are children,
// Size == sum(c.Size for c in Children).
type Statistic struct {
	Name  string
	Value int64

	children map[string]*Statistic
}

func leafStat(name string, value int64) *Statistic {
	return &Statistic{Name: name, Value: value}
}

func groupStat(name string, children ...*Statistic) *Statistic {
	var cmap map[string]*Statistic
	var value int64
	if len(children) != 0 {
		cmap = make(map[string]*Statistic)
		for _, child := range children {
			cmap[child.Name] = child
			value += child.Value
		}
	}
	return &Statistic{
		Name:     name,
		Value:    value,
		children: cmap,
	}
}

func (s *Statistic) Sub(chain ...string) *Statistic {
	for _, name := range chain {
		if s == nil {
			return nil
		}
		s = s.children[name]
	}
	return s
}

func (s *Statistic) setChild(child *Statistic) {
	if len(s.children) == 0 {
		panic("cannot add children to leaf statistic")
	}
	if oldChild, ok := s.children[child.Name]; ok {
		s.Value -= oldChild.Value
	}
	s.children[child.Name] = child
	s.Value += child.Value
}

func (s *Statistic) Children() iter.Seq[*Statistic] {
	return func(yield func(*Statistic) bool) {
		for _, child := range s.children {
			if !yield(child) {
				return
			}
		}
	}
}
