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
	// readInt reads the memory for an n-byte integer or unsigned integer.
	readInt := func(n int64) ([]byte, error) {
		switch n {
		case 1, 2, 4, 8:
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
	case *dwarf.IntType:
		bs := t.Common().ByteSize
		buf, err := readInt(bs)
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
		}
	case *dwarf.UintType:
		bs := t.Common().ByteSize
		buf, err := readInt(bs)
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
		}
		// TODO: more types
	}
	return nil, fmt.Errorf("Unsupported type %T", t)
}
