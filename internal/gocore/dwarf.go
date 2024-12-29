// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocore

import (
	"debug/dwarf"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"golang.org/x/debug/dwtest"
	"golang.org/x/debug/internal/core"

	"golang.org/x/debug/third_party/delve/dwarf/loclist"
	"golang.org/x/debug/third_party/delve/dwarf/op"
	"golang.org/x/debug/third_party/delve/dwarf/regnum"
)

const (
	AttrGoKind        dwarf.Attr = 0x2900
	AttrGoRuntimeType dwarf.Attr = 0x2904
)

func readDWARFTypes(p *core.Process) (map[dwarf.Type]*Type, map[core.Address]*Type, error) {
	d, err := p.DWARF()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read DWARF: %v", err)
	}
	dwarfMap := make(map[dwarf.Type]*Type)
	addrMap := make(map[core.Address]*Type)
	syms, _ := p.Symbols() // It's OK to ignore the error. If we don't have symbols, that's OK; it's a soft error.
	// It's OK if typBase is 0 and not present. We won't be able to type the heap, probably,
	// but it may still be useful (though painful) to someone to try and debug the core, so don't
	// error out here.
	typBase := syms["runtime.types"]

	// Make one of our own Types for each dwarf type.
	r := d.Reader()
	var types []*Type
	for e, err := r.Next(); e != nil && err == nil; e, err = r.Next() {
		if isNonGoCU(e) {
			r.SkipChildren()
			continue
		}
		switch e.Tag {
		case dwarf.TagArrayType, dwarf.TagPointerType, dwarf.TagStructType, dwarf.TagBaseType, dwarf.TagSubroutineType, dwarf.TagTypedef:
			dt, err := d.Type(e.Offset)
			if err != nil {
				continue
			}
			t := &Type{Name: gocoreName(dt), Size: dwarfSize(dt, p.PtrSize())}
			if goKind, ok := e.Val(AttrGoKind).(int64); ok {
				t.goKind = reflect.Kind(goKind)
			}
			// Guard against typBase being zero. In reality, it's very unlikely for the DWARF
			// to be present but typBase to be zero, but let's just be defensive. addrMap is
			// only really necessary for typing the heap, which is "optional."
			if typBase != 0 {
				if offset, ok := e.Val(AttrGoRuntimeType).(uint64); ok {
					// N.B. AttrGoRuntimeType is not defined for typedefs, so addrMap will
					// always refer to the base type.
					t.goAddr = typBase.Add(int64(offset))
					addrMap[typBase.Add(int64(offset))] = t
				}
			}
			dwarfMap[dt] = t
			types = append(types, t)
		}
	}

	// Fill in fields of types. Postponed until now so we're sure
	// we have all the Types allocated and available.
	for dt, t := range dwarfMap {
		switch x := dt.(type) {
		case *dwarf.ArrayType:
			t.Kind = KindArray
			t.Elem = dwarfMap[x.Type]
			t.Count = x.Count
		case *dwarf.PtrType:
			t.Kind = KindPtr
			// unsafe.Pointer has a void base type.
			if _, ok := x.Type.(*dwarf.VoidType); !ok {
				t.Elem = dwarfMap[x.Type]
			}
		case *dwarf.StructType:
			t.Kind = KindStruct
			for _, f := range x.Field {
				fType := dwarfMap[f.Type]
				if fType == nil {
					// Weird case: arrays of size 0 in structs, like
					// Sysinfo_t.X_f. Synthesize a type so things later don't
					// get sad.
					if arr, ok := f.Type.(*dwarf.ArrayType); ok && arr.Count == 0 {
						fType = &Type{
							Name:  f.Type.String(),
							Kind:  KindArray,
							Count: arr.Count,
							Elem:  dwarfMap[arr.Type],
						}
					} else {
						return nil, nil, fmt.Errorf(
							"found a nil ftype for field %s.%s, type %s (%s) on ",
							x.StructName, f.Name, f.Type, reflect.TypeOf(f.Type))
					}
				}
				t.Fields = append(t.Fields, Field{Name: f.Name, Type: fType, Off: f.ByteOffset})
			}
		case *dwarf.BoolType:
			t.Kind = KindBool
		case *dwarf.IntType:
			t.Kind = KindInt
		case *dwarf.UintType:
			t.Kind = KindUint
		case *dwarf.FloatType:
			t.Kind = KindFloat
		case *dwarf.ComplexType:
			t.Kind = KindComplex
		case *dwarf.FuncType:
			t.Kind = KindFunc
		case *dwarf.TypedefType:
			// handle these types in the loop below
		default:
			return nil, nil, fmt.Errorf("unknown type %s %T", dt, dt)
		}
	}

	// Detect strings & slices
	for _, t := range types {
		if t.Kind != KindStruct {
			continue
		}
		switch t.goKind {
		case reflect.String:
			t.Kind = KindString
			t.Elem = t.Fields[0].Type.Elem // TODO: check that it is always uint8.
			t.Fields = nil
		case reflect.Slice:
			t.Kind = KindSlice
			t.Elem = t.Fields[0].Type.Elem
			t.Fields = nil
		}
	}

	// Copy info from base types into typedefs.
	for dt, t := range dwarfMap {
		tt, ok := dt.(*dwarf.TypedefType)
		if !ok {
			continue
		}
		base := tt.Type
		// Walk typedef chain until we reach a non-typedef type.
		for {
			if x, ok := base.(*dwarf.TypedefType); ok {
				base = x.Type
				continue
			}
			break
		}
		bt := dwarfMap[base]

		// Copy type info from base. Everything except the name.
		name := t.Name
		*t = *bt
		t.Name = name

		// Detect some special types. If the base is some particular type,
		// then the alias gets marked as special.
		// We have aliases like:
		//   interface {}              -> struct runtime.eface
		//   error                     -> struct runtime.iface
		// Note: the base itself does not get marked as special.
		// (Unlike strings and slices, where they do.)
		if bt.Name == "runtime.eface" {
			t.Kind = KindEface
			t.Fields = nil
		}
		if bt.Name == "runtime.iface" {
			t.Kind = KindIface
			t.Fields = nil
		}
	}
	return dwarfMap, addrMap, nil
}

func isNonGoCU(e *dwarf.Entry) bool {
	if e.Tag != dwarf.TagCompileUnit {
		return false
	}
	prod, ok := e.Val(dwarf.AttrProducer).(string)
	if !ok {
		return true
	}
	return !strings.Contains(prod, "Go cmd/compile")
}

// dwarfSize is used to compute the size of a DWARF type.
// dt.Size() is wrong when it returns a negative number.
// This function implements just enough to correct the bad behavior.
func dwarfSize(dt dwarf.Type, ptrSize int64) int64 {
	s := dt.Size()
	if s >= 0 {
		return s
	}
	switch x := dt.(type) {
	case *dwarf.FuncType:
		return ptrSize // Fix for issue 21097.
	case *dwarf.ArrayType:
		return x.Count * dwarfSize(x.Type, ptrSize)
	case *dwarf.TypedefType:
		return dwarfSize(x.Type, ptrSize)
	default:
		panic(fmt.Sprintf("unhandled: %s, %T", x, x))
	}
}

// gocoreName generates the name this package uses to refer to a dwarf type.
// This name differs from the dwarf name in that it stays closer to the Go name for the type.
// For instance (dwarf name -> gocoreName)
//
//	struct runtime.siginfo -> runtime.siginfo
//	*void -> unsafe.Pointer
//	struct struct { runtime.signalLock uint32; runtime.hz int32 } -> struct { signalLock uint32; hz int32 }
func gocoreName(dt dwarf.Type) string {
	switch x := dt.(type) {
	case *dwarf.PtrType:
		if _, ok := x.Type.(*dwarf.VoidType); ok {
			return "unsafe.Pointer"
		}
		return "*" + gocoreName(x.Type)
	case *dwarf.ArrayType:
		return fmt.Sprintf("[%d]%s", x.Count, gocoreName(x.Type))
	case *dwarf.StructType:
		if !strings.HasPrefix(x.StructName, "struct {") {
			// This is a named type, return that name.
			return x.StructName
		}
		// Build gocore name from the DWARF fields.
		s := "struct {"
		first := true
		for _, f := range x.Field {
			if !first {
				s += ";"
			}
			name := f.Name
			if i := strings.Index(name, "."); i >= 0 {
				// Remove pkg path from field names.
				name = name[i+1:]
			}
			s += fmt.Sprintf(" %s %s", name, gocoreName(f.Type))
			first = false
		}
		s += " }"
		return s
	default:
		return dt.String()
	}
}

type constsMap map[string]int64

func (c constsMap) get(s string) int64 {
	v, ok := c[s]
	if !ok {
		panic("failed to find constant " + s)
	}
	return v
}

func (c constsMap) find(s string) (int64, bool) {
	v, ok := c[s]
	return v, ok
}

func readDWARFConstants(p *core.Process) (constsMap, error) {
	d, err := p.DWARF()
	if err != nil {
		return nil, fmt.Errorf("failed to read DWARF: %v", err)
	}
	consts := map[string]int64{}

	r := d.Reader()
	for e, err := r.Next(); e != nil && err == nil; e, err = r.Next() {
		if e.Tag != dwarf.TagConstant {
			continue
		}
		f := e.AttrField(dwarf.AttrName)
		if f == nil {
			continue
		}
		name := f.Val.(string)
		c := e.AttrField(dwarf.AttrConstValue)
		if c == nil {
			continue
		}
		consts[name] = c.Val.(int64)
	}
	return consts, nil
}

func readDWARFGlobals(p *core.Process, nRoots *int, dwarfTypeMap map[dwarf.Type]*Type) ([]*Root, error) {
	d, err := p.DWARF()
	if err != nil {
		return nil, fmt.Errorf("failed to read DWARF: %v", err)
	}
	var roots []*Root
	r := d.Reader()
	for e, err := r.Next(); e != nil && err == nil; e, err = r.Next() {
		if isNonGoCU(e) {
			r.SkipChildren()
			continue
		}

		if e.Tag != dwarf.TagVariable {
			continue
		}
		f := e.AttrField(dwarf.AttrLocation)
		if f == nil {
			continue
		}
		if f.Class != dwarf.ClassExprLoc {
			// Globals are all encoded with this class.
			continue
		}
		loc := f.Val.([]byte)
		if len(loc) == 0 || loc[0] != byte(op.DW_OP_addr) {
			continue
		}
		var a core.Address
		if p.PtrSize() == 8 {
			a = core.Address(p.ByteOrder().Uint64(loc[1:]))
		} else {
			a = core.Address(p.ByteOrder().Uint32(loc[1:]))
		}
		a = a.Add(int64(p.StaticBase()))
		if !p.Writeable(a) {
			// Read-only globals can't have heap pointers.
			// TODO: keep roots around anyway?
			continue
		}
		f = e.AttrField(dwarf.AttrType)
		if f == nil {
			continue
		}
		dt, err := d.Type(f.Val.(dwarf.Offset))
		if err != nil {
			return nil, err
		}
		if _, ok := dt.(*dwarf.UnspecifiedType); ok {
			continue // Ignore markers like data/edata.
		}
		nf := e.AttrField(dwarf.AttrName)
		if nf == nil {
			continue
		}
		typ := dwarfTypeMap[dt]
		roots = append(roots, makeMemRoot(nRoots, nf.Val.(string), typ, nil, a))
	}
	return roots, nil
}

type dwarfVarKind int

const (
	dwarfVarUnknown dwarfVarKind = iota
	dwarfParam
	dwarfLocal
)

type dwarfVar struct {
	lowPC, highPC core.Address
	kind          dwarfVarKind
	name          string
	instr         []byte
	typ           *Type
}

func readDWARFVars(p *core.Process, fns *funcTab, dwarfTypeMap map[dwarf.Type]*Type) (map[*Func][]dwarfVar, error) {
	d, err := p.DWARF()
	if err != nil {
		return nil, fmt.Errorf("failed to read DWARF: %v", err)
	}
	dLocSec, err := p.DWARFLoc()
	if err != nil {
		return nil, fmt.Errorf("failed to read DWARF: %v", err)
	}
	vars := make(map[*Func][]dwarfVar)
	var curfn *Func
	r := d.Reader()
	for e, err := r.Next(); e != nil && err == nil; e, err = r.Next() {
		if isNonGoCU(e) {
			r.SkipChildren()
			continue
		}

		if e.Tag == dwarf.TagSubprogram {
			if e.AttrField(dwarf.AttrLowpc) == nil ||
				e.AttrField(dwarf.AttrHighpc) == nil {
				continue
			}

			// Collect the start/end PC for the func. The format/class of
			// the high PC attr may vary depending on which DWARF version
			// we're generating; invoke a helper to handle the various
			// possibilities.
			lowpc, highpc, perr := dwtest.SubprogLoAndHighPc(e)
			if perr != nil {
				return nil, fmt.Errorf("subprog die malformed: %v", perr)
			}
			fmin := core.Address(lowpc + p.StaticBase())
			fmax := core.Address(highpc + p.StaticBase())
			f := fns.find(fmin)
			if f == nil {
				// some func Go doesn't know about. C?
				curfn = nil
			} else {
				if f.entry != fmin {
					return nil, errors.New("dwarf and runtime don't agree about start of " + f.name)
				}
				if fns.find(fmax-1) != f {
					return nil, errors.New("function ranges don't match for " + f.name)
				}
				curfn = f
			}
			continue
		}
		if e.Tag != dwarf.TagVariable && e.Tag != dwarf.TagFormalParameter {
			continue
		}
		var kind dwarfVarKind
		switch e.Tag {
		case dwarf.TagFormalParameter:
			kind = dwarfParam
		case dwarf.TagVariable:
			kind = dwarfLocal
		}
		aloc := e.AttrField(dwarf.AttrLocation)
		if aloc == nil {
			continue
		}
		if aloc.Class != dwarf.ClassLocListPtr {
			continue
		}

		// Read attributes for some high-level information.
		f := e.AttrField(dwarf.AttrType)
		if f == nil {
			continue
		}
		dt, err := d.Type(f.Val.(dwarf.Offset))
		if err != nil {
			return nil, err
		}
		nf := e.AttrField(dwarf.AttrName)
		if nf == nil {
			continue
		}
		name := nf.Val.(string)

		// No .debug_loc section, can't make progress.
		if len(dLocSec) == 0 {
			continue
		}

		// Read the location list.
		locListOff := aloc.Val.(int64)
		dr := loclist.NewDwarf2Reader(dLocSec, int(p.PtrSize()))
		dr.Seek(int(locListOff))
		var base uint64
		var e loclist.Entry
		for dr.Next(&e) {
			if e.BaseAddressSelection() {
				base = e.HighPC + p.StaticBase()
				continue
			}
			vars[curfn] = append(vars[curfn], dwarfVar{
				lowPC:  core.Address(e.LowPC + base),
				highPC: core.Address(e.HighPC + base),
				kind:   kind,
				instr:  e.Instr,
				name:   name,
				typ:    dwarfTypeMap[dt],
			})
		}
	}
	return vars, nil
}

func hardwareRegs2DWARF(hregs []core.Register) []*op.DwarfRegister {
	n := regnum.AMD64MaxRegNum()
	dregs := make([]*op.DwarfRegister, n)
	for _, hreg := range hregs {
		dwn, ok := regnum.AMD64NameToDwarf[hreg.Name]
		if !ok {
			continue
		}
		dreg := op.DwarfRegisterFromUint64(hreg.Value)
		dreg.FillBytes()
		dregs[dwn] = dreg
	}
	return dregs
}

/* Dwarf encoding notes

type XXX sss

translates to a dwarf type pkg.XXX of the type of sss (uint, float, ...)

exception: if sss is a struct or array, then we get two types, the "unnamed" and "named" type.
The unnamed type is a dwarf struct type with name "struct pkg.XXX" or a dwarf array type with
name [N]elem.
Then there is a typedef with pkg.XXX pointing to "struct pkg.XXX" or [N]elem.

For structures, lowercase field names are prepended with the package name (pkg path?).

type XXX interface{}
pkg.XXX is a typedef to "struct runtime.eface"
type XXX interface{f()}
pkg.XXX is a typedef to "struct runtime.iface"

Sometimes there is even a chain of identically-named typedefs. I have no idea why.
main.XXX -> main.XXX -> struct runtime.iface

*/
