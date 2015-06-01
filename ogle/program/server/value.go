// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

import (
	"fmt"
	"golang.org/x/debug/dwarf"
	"golang.org/x/debug/ogle/program"
)

// value peeks the program's memory at the given address, parsing it as a value of type t.
func (s *Server) value(t dwarf.Type, addr uint64) (program.Value, error) {
	// readBasic reads the memory for a basic type of size n bytes.
	readBasic := func(n int64) ([]byte, error) {
		switch n {
		case 1, 2, 4, 8, 16:
		default:
			return nil, fmt.Errorf("invalid size: %d", n)
		}
		buf := make([]byte, n)
		if err := s.peek(uintptr(addr), buf); err != nil {
			return nil, err
		}
		return buf, nil
	}

	switch t := t.(type) {
	case *dwarf.CharType, *dwarf.IntType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading integer: %s", err)
		}
		x := s.arch.IntN(buf)
		switch bs {
		case 1:
			return int8(x), nil
		case 2:
			return int16(x), nil
		case 4:
			return int32(x), nil
		case 8:
			return int64(x), nil
		default:
			return nil, fmt.Errorf("invalid integer size: %d", bs)
		}
	case *dwarf.UcharType, *dwarf.UintType, *dwarf.AddrType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading unsigned integer: %s", err)
		}
		x := s.arch.UintN(buf)
		switch bs {
		case 1:
			return uint8(x), nil
		case 2:
			return uint16(x), nil
		case 4:
			return uint32(x), nil
		case 8:
			return uint64(x), nil
		default:
			return nil, fmt.Errorf("invalid unsigned integer size: %d", bs)
		}
	case *dwarf.BoolType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading boolean: %s", err)
		}
		for _, b := range buf {
			if b != 0 {
				return true, nil
			}
		}
		return false, nil
	case *dwarf.FloatType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading float: %s", err)
		}
		switch bs {
		case 4:
			return s.arch.Float32(buf), nil
		case 8:
			return s.arch.Float64(buf), nil
		default:
			return nil, fmt.Errorf("invalid float size: %d", bs)
		}
	case *dwarf.ComplexType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading complex: %s", err)
		}
		switch bs {
		case 8:
			return s.arch.Complex64(buf), nil
		case 16:
			return s.arch.Complex128(buf), nil
		default:
			return nil, fmt.Errorf("invalid complex size: %d", bs)
		}
	case *dwarf.PtrType:
		bs := t.Common().ByteSize
		if bs != int64(s.arch.PointerSize) {
			return nil, fmt.Errorf("invalid pointer size: %d", bs)
		}
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading pointer: %s", err)
		}
		return program.Pointer{
			TypeID:  uint64(t.Type.Common().Offset),
			Address: uint64(s.arch.Uintptr(buf)),
		}, nil
	case *dwarf.ArrayType:
		length := t.Count
		stride := t.StrideBitSize
		if stride%8 != 0 {
			return nil, fmt.Errorf("array is not byte-aligned")
		}
		return program.Array{
			ElementTypeID: uint64(t.Type.Common().Offset),
			Address:       uint64(addr),
			Length:        uint64(length),
			StrideBits:    uint64(stride),
		}, nil
	case *dwarf.StructType:
		fields := make([]program.StructField, len(t.Field))
		for i, field := range t.Field {
			fields[i] = program.StructField{
				Name: field.Name,
				Var: program.Var{
					TypeID:  uint64(field.Type.Common().Offset),
					Address: uint64(addr) + uint64(field.ByteOffset),
				},
			}
		}
		return program.Struct{fields}, nil
	case *dwarf.TypedefType:
		return s.value(t.Type, addr)
		// TODO: more types
	}
	return nil, fmt.Errorf("Unsupported type %T", t)
}
