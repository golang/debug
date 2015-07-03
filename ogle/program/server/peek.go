// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Functions for reading values of various types from a program's memory.

package server

import (
	"fmt"

	"golang.org/x/debug/dwarf"
)

// peekBytes reads len(buf) bytes at addr.
func (s *Server) peekBytes(addr uint64, buf []byte) error {
	return s.ptracePeek(s.stoppedPid, uintptr(addr), buf)
}

// peekPtr reads a pointer at addr.
func (s *Server) peekPtr(addr uint64) (uint64, error) {
	buf := make([]byte, s.arch.PointerSize)
	if err := s.peekBytes(addr, buf); err != nil {
		return 0, err
	}
	return s.arch.Uintptr(buf), nil
}

// peekUint8 reads a single byte at addr.
func (s *Server) peekUint8(addr uint64) (byte, error) {
	buf := make([]byte, 1)
	if err := s.peekBytes(addr, buf); err != nil {
		return 0, err
	}
	return uint8(s.arch.UintN(buf)), nil
}

// peekInt reads an int of size n bytes at addr.
func (s *Server) peekInt(addr uint64, n int64) (int64, error) {
	buf := make([]byte, n)
	if err := s.peekBytes(addr, buf); err != nil {
		return 0, err
	}
	return s.arch.IntN(buf), nil
}

// peekUint reads a uint of size n bytes at addr.
func (s *Server) peekUint(addr uint64, n int64) (uint64, error) {
	buf := make([]byte, n)
	if err := s.peekBytes(addr, buf); err != nil {
		return 0, err
	}
	return s.arch.UintN(buf), nil
}

// peekPtrStructField reads a pointer in the field fieldName of the struct
// of type t at addr.
func (s *Server) peekPtrStructField(t *dwarf.StructType, addr uint64, fieldName string) (uint64, error) {
	f, err := getField(t, fieldName)
	if err != nil {
		return 0, fmt.Errorf("reading field %s: %s", fieldName, err)
	}
	if _, ok := f.Type.(*dwarf.PtrType); !ok {
		return 0, fmt.Errorf("field %s is not a pointer", fieldName)
	}
	return s.peekPtr(addr + uint64(f.ByteOffset))
}

// peekUintStructField reads a uint in the field fieldName of the struct
// of type t at addr.  The size of the uint is determined by the field.
func (s *Server) peekUintStructField(t *dwarf.StructType, addr uint64, fieldName string) (uint64, error) {
	f, err := getField(t, fieldName)
	if err != nil {
		return 0, fmt.Errorf("reading field %s: %s", fieldName, err)
	}
	ut, ok := f.Type.(*dwarf.UintType)
	if !ok {
		return 0, fmt.Errorf("field %s is not an unsigned integer", fieldName)
	}
	return s.peekUint(addr+uint64(f.ByteOffset), ut.ByteSize)
}

// peekIntStructField reads an int in the field fieldName of the struct
// of type t at addr.  The size of the int is determined by the field.
func (s *Server) peekIntStructField(t *dwarf.StructType, addr uint64, fieldName string) (int64, error) {
	f, err := getField(t, fieldName)
	if err != nil {
		return 0, fmt.Errorf("reading field %s: %s", fieldName, err)
	}
	it, ok := f.Type.(*dwarf.IntType)
	if !ok {
		return 0, fmt.Errorf("field %s is not a signed integer", fieldName)
	}
	return s.peekInt(addr+uint64(f.ByteOffset), it.ByteSize)
}
