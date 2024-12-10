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

	"golang.org/x/debug/internal/core"

	"github.com/go-delve/delve/pkg/dwarf/loclist"
	"github.com/go-delve/delve/pkg/dwarf/op"
	"github.com/go-delve/delve/pkg/dwarf/regnum"
)

const (
	AttrGoKind dwarf.Attr = 0x2900
)

// read DWARF types from core dump.
func readDWARFTypes(p *core.Process) (map[dwarf.Type]*Type, error) {
	d, err := p.DWARF()
	if err != nil {
		return nil, fmt.Errorf("failed to read DWARF: %v", err)
	}
	dwarfMap := make(map[dwarf.Type]*Type)

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
						return nil, fmt.Errorf(
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
			return nil, fmt.Errorf("unknown type %s %T", dt, dt)
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
	return dwarfMap, nil
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

func readRuntimeConstants(p *core.Process) (map[string]int64, error) {
	d, err := p.DWARF()
	if err != nil {
		return nil, fmt.Errorf("failed to read DWARF: %v", err)
	}
	consts := map[string]int64{}

	// From 1.10, these constants are recorded in DWARF records.
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
		if !strings.HasPrefix(name, "runtime.") {
			continue
		}
		name = name[8:]
		c := e.AttrField(dwarf.AttrConstValue)
		if c == nil {
			continue
		}
		consts[name] = c.Val.(int64)
	}
	return consts, nil
}

const (
	_DW_OP_addr           = 0x03
	_DW_OP_call_frame_cfa = 0x9c
	_DW_OP_plus           = 0x22
	_DW_OP_consts         = 0x11
)

func readGlobals(p *core.Process, dwarfTypeMap map[dwarf.Type]*Type) ([]*Root, error) {
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
		if len(loc) == 0 || loc[0] != _DW_OP_addr {
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
		roots = append(roots, &Root{
			Name:  nf.Val.(string),
			Addr:  a,
			Type:  dwarfTypeMap[dt],
			Frame: nil,
		})
	}
	return roots, nil
}

type dwarfVar struct {
	lowPC, highPC core.Address
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
			lowpc := e.AttrField(dwarf.AttrLowpc)
			highpc := e.AttrField(dwarf.AttrHighpc)
			if lowpc == nil || highpc == nil {
				continue
			}
			min := core.Address(lowpc.Val.(uint64) + p.StaticBase())
			max := core.Address(highpc.Val.(uint64) + p.StaticBase())
			f := fns.find(min)
			if f == nil {
				// some func Go doesn't know about. C?
				curfn = nil
			} else {
				if f.entry != min {
					return nil, errors.New("dwarf and runtime don't agree about start of " + f.name)
				}
				if fns.find(max-1) != f {
					return nil, errors.New("function ranges don't match for " + f.name)
				}
				curfn = f
			}
			continue
		}
		if e.Tag != dwarf.TagVariable && e.Tag != dwarf.TagFormalParameter {
			continue
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
