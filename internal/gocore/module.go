// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocore

import (
	"fmt"
	"math"
	"sort"

	"golang.org/x/debug/internal/core"
)

type module struct {
	r             region       // inferior region holding a runtime.moduledata
	types, etypes core.Address // range that holds all the runtime._type data in this module
}

func readModules(rtTypeByName map[string]*Type, rtConsts map[string]int64, rtGlobals map[string]region) ([]*module, *funcTab, error) {
	ms := rtGlobals["modulesSlice"].Deref()
	n := ms.SliceLen()
	var modules []*module
	var fnTab funcTab
	fnTab.byName = make(map[string]*Func)
	for i := int64(0); i < n; i++ {
		md := ms.SliceIndex(i).Deref()
		modules = append(modules, readModule(md, &fnTab, rtTypeByName, rtConsts))
	}
	fnTab.sort()
	return modules, &fnTab, nil
}

func readModule(r region, fns *funcTab, rtTypeByName map[string]*Type, rtConsts map[string]int64) *module {
	m := &module{r: r}
	m.types = core.Address(r.Field("types").Uintptr())
	m.etypes = core.Address(r.Field("etypes").Uintptr())

	// Read the pc->function table
	pcln := r.Field("pclntable")
	var pctab, funcnametab region
	havePCtab := r.HasField("pctab")
	if havePCtab {
		// In 1.16, pclntable was split up into pctab and funcnametab.
		pctab = r.Field("pctab")
		funcnametab = r.Field("funcnametab")
	}
	ftab := r.Field("ftab")
	n := ftab.SliceLen() - 1 // last slot is a dummy, just holds entry
	for i := int64(0); i < n; i++ {
		ft := ftab.SliceIndex(i)
		min := m.textAddr(ft.Field("entryoff").Uint32())
		max := m.textAddr(ftab.SliceIndex(i + 1).Field("entryoff").Uint32())
		funcoff := int64(ft.Field("funcoff").Uint32())
		fr := pcln.SliceIndex(funcoff).Cast(rtTypeByName["runtime._func"])
		var f *Func
		if havePCtab {
			f = m.readFunc(fr, pctab, funcnametab, rtConsts)
		} else {
			f = m.readFunc(fr, pcln, pcln, rtConsts)
		}
		if f.entry != min {
			panic(fmt.Errorf("entry %x and min %x don't match for %s", f.entry, min, f.name))
		}
		fns.add(min, max, f)
	}

	return m
}

// readFunc parses a runtime._func and returns a *Func.
// r must have type runtime._func.
// pcln must have type []byte and represent the module's pcln table region.
func (m *module) readFunc(r region, pctab region, funcnametab region, rtConsts constsMap) *Func {
	f := &Func{module: m, r: r}
	f.entry = m.textAddr(r.Field("entryOff").Uint32())
	nameOff := r.Field("nameOff").Int32()
	f.name = r.p.ReadCString(funcnametab.SliceIndex(int64(nameOff)).a)
	pcspIdx := int64(r.Field("pcsp").Uint32())
	f.frameSize.read(r.p, pctab.SliceIndex(pcspIdx).a)

	// Parse pcdata and funcdata, which are laid out beyond the end of the _func.
	npcdata := r.Field("npcdata")
	n := npcdata.Uint32()
	nfd := r.Field("nfuncdata")
	a := nfd.a.Add(nfd.typ.Size)

	for i := uint32(0); i < n; i++ {
		f.pcdata = append(f.pcdata, r.p.ReadInt32(a))
		a = a.Add(4)
	}

	n = uint32(nfd.Uint8())
	for i := uint32(0); i < n; i++ {
		// Since 1.18, funcdata contains offsets from go.func.*.
		off := r.p.ReadUint32(a)
		if off == ^uint32(0) {
			// No entry.
			f.funcdata = append(f.funcdata, 0)
		} else {
			f.funcdata = append(f.funcdata, core.Address(m.r.Field("gofunc").Uintptr()+uint64(off)))
		}
		a = a.Add(4)
	}

	// Read pcln tables we need.
	if stackmap := int(rtConsts.get("internal/abi.PCDATA_StackMapIndex")); stackmap < len(f.pcdata) {
		f.stackMap.read(r.p, pctab.SliceIndex(int64(f.pcdata[stackmap])).a)
	} else {
		f.stackMap.setEmpty()
	}

	return f
}

// textAddr returns the address of a text offset.
//
// Equivalent to runtime.moduledata.textAddr.
func (m *module) textAddr(off32 uint32) core.Address {
	off := uint64(off32)
	res := m.r.Field("text").Uintptr() + off

	textsectmap := m.r.Field("textsectmap")
	length := textsectmap.SliceLen()
	if length > 1 {
		for i := int64(0); i < length; i++ {
			sect := textsectmap.SliceIndex(i)

			vaddr := sect.Field("vaddr").Uintptr()
			end := sect.Field("end").Uintptr()
			baseaddr := sect.Field("baseaddr").Uintptr()

			if off >= vaddr && off < end || (i == length-1 && off == end) {
				res = baseaddr + off - vaddr
			}
		}
	}

	return core.Address(res)
}

type funcTabEntry struct {
	min, max core.Address
	f        *Func
}

type funcTab struct {
	entries []funcTabEntry
	byName  map[string]*Func
}

// add records that PCs in the range [min,max) map to function f.
func (t *funcTab) add(min, max core.Address, f *Func) {
	t.byName[f.name] = f
	t.entries = append(t.entries, funcTabEntry{min: min, max: max, f: f})
}

// sort must be called after all the adds, but before any find.
func (t *funcTab) sort() {
	sort.Slice(t.entries, func(i, j int) bool {
		return t.entries[i].min < t.entries[j].min
	})
}

// find finds a Func for the given address. sort must have been called already.
func (t *funcTab) find(pc core.Address) *Func {
	n := sort.Search(len(t.entries), func(i int) bool {
		return t.entries[i].max > pc
	})
	if n == len(t.entries) || pc < t.entries[n].min || pc >= t.entries[n].max {
		return nil
	}
	return t.entries[n].f
}

// findByName finds a Func for the given name.
func (t *funcTab) findByName(name string) *Func {
	return t.byName[name]
}

// a pcTab maps from an offset in a function to an int64.
type pcTab struct {
	entries []pcTabEntry
}

type pcTabEntry struct {
	bytes int64 // # of bytes this entry covers
	val   int64 // value over that range of bytes
}

// read parses a pctab from the core file at address data.
func (t *pcTab) read(core *core.Process, data core.Address) {
	var pcQuantum int64
	switch core.Arch() {
	case "386", "amd64", "amd64p32":
		pcQuantum = 1
	case "s390x":
		pcQuantum = 2
	case "arm", "arm64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le":
		pcQuantum = 4
	default:
		panic("unknown architecture " + core.Arch())
	}
	val := int64(-1)
	first := true
	for {
		// Advance value.
		v, n := readVarint(core, data)
		if v == 0 && !first {
			return
		}
		data = data.Add(n)
		if v&1 != 0 {
			val += ^(v >> 1)
		} else {
			val += v >> 1
		}

		// Advance pc.
		v, n = readVarint(core, data)
		data = data.Add(n)
		t.entries = append(t.entries, pcTabEntry{bytes: v * pcQuantum, val: val})
		first = false
	}
}

func (t *pcTab) setEmpty() {
	t.entries = []pcTabEntry{{bytes: math.MaxInt64, val: -1}}
}

func (t *pcTab) find(off int64) (int64, error) {
	for _, e := range t.entries {
		if off < e.bytes {
			return e.val, nil
		}
		off -= e.bytes
	}
	return 0, fmt.Errorf("can't find pctab entry for offset %#x", off)
}

// readVarint reads a varint from the core file.
// val is the value, n is the number of bytes consumed.
func readVarint(core *core.Process, a core.Address) (val, n int64) {
	for {
		b := core.ReadUint8(a)
		val |= int64(b&0x7f) << uint(n*7)
		n++
		a++
		if b&0x80 == 0 {
			return
		}
	}
}
