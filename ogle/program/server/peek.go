// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Functions for reading values of various types from a program's memory.

package server

import (
	"errors"
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

// peekUintOrIntStructField reads a signed or unsigned integer in the field fieldName
// of the struct of type t at addr. If the value is negative, it returns an error.
// This function is used when the value should be non-negative, but the DWARF
// type of the field may be signed or unsigned.
func (s *Server) peekUintOrIntStructField(t *dwarf.StructType, addr uint64, fieldName string) (uint64, error) {
	f, err := getField(t, fieldName)
	if err != nil {
		return 0, fmt.Errorf("reading field %s: %s", fieldName, err)
	}
	ut, ok := f.Type.(*dwarf.UintType)
	if ok {
		return s.peekUint(addr+uint64(f.ByteOffset), ut.ByteSize)
	}
	it, ok := f.Type.(*dwarf.IntType)
	if !ok {
		return 0, fmt.Errorf("field %s is not an integer", fieldName)
	}
	i, err := s.peekInt(addr+uint64(f.ByteOffset), it.ByteSize)
	if err != nil {
		return 0, err
	}
	if i < 0 {
		return 0, fmt.Errorf("field %s is negative", fieldName)
	}
	return uint64(i), nil
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

// peekMapValues reads a map at the given address and calls fn with the addresses for each (key, value) pair.
// If fn returns false, peekMapValues stops.
func (s *Server) peekMapValues(t *dwarf.MapType, a uint64, fn func(keyAddr, valAddr uint64, keyType, valType dwarf.Type) bool) error {
	pt, ok := t.Type.(*dwarf.PtrType)
	if !ok {
		return errors.New("bad map type: not a pointer")
	}
	st, ok := pt.Type.(*dwarf.StructType)
	if !ok {
		return errors.New("bad map type: not a pointer to a struct")
	}
	// a is the address of a pointer to a struct.  Get the pointer's value.
	a, err := s.peekPtr(a)
	if err != nil {
		return fmt.Errorf("reading map pointer: %s", err)
	}
	if a == 0 {
		// The pointer was nil, so the map is empty.
		return nil
	}
	// Gather information about the struct type and the map bucket type.
	b, err := s.peekUintStructField(st, a, "B")
	if err != nil {
		return fmt.Errorf("reading map: %s", err)
	}
	buckets, err := s.peekPtrStructField(st, a, "buckets")
	if err != nil {
		return fmt.Errorf("reading map: %s", err)
	}
	oldbuckets, err := s.peekPtrStructField(st, a, "oldbuckets")
	if err != nil {
		return fmt.Errorf("reading map: %s", err)
	}
	bf, err := getField(st, "buckets")
	if err != nil {
		return fmt.Errorf("reading map: %s", err)
	}
	bucketPtrType, ok := bf.Type.(*dwarf.PtrType)
	if !ok {
		return errors.New("bad map bucket type: not a pointer")
	}
	bt, ok := bucketPtrType.Type.(*dwarf.StructType)
	if !ok {
		return errors.New("bad map bucket type: not a pointer to a struct")
	}
	bucketSize := uint64(bucketPtrType.Type.Size())
	tophashField, err := getField(bt, "tophash")
	if err != nil {
		return fmt.Errorf("reading map: %s", err)
	}
	bucketCnt := uint64(tophashField.Type.Size())
	tophashFieldOffset := uint64(tophashField.ByteOffset)
	keysField, err := getField(bt, "keys")
	if err != nil {
		return fmt.Errorf("reading map: %s", err)
	}
	keysType, ok := keysField.Type.(*dwarf.ArrayType)
	if !ok {
		return errors.New(`bad map bucket type: "keys" is not an array`)
	}
	keyType := keysType.Type
	keysStride := uint64(keysType.StrideBitSize / 8)
	keysFieldOffset := uint64(keysField.ByteOffset)
	valuesField, err := getField(bt, "values")
	if err != nil {
		return fmt.Errorf("reading map: %s", err)
	}
	valuesType, ok := valuesField.Type.(*dwarf.ArrayType)
	if !ok {
		return errors.New(`bad map bucket type: "values" is not an array`)
	}
	valueType := valuesType.Type
	valuesStride := uint64(valuesType.StrideBitSize / 8)
	valuesFieldOffset := uint64(valuesField.ByteOffset)
	overflowField, err := getField(bt, "overflow")
	if err != nil {
		return fmt.Errorf("reading map: %s", err)
	}
	overflowFieldOffset := uint64(overflowField.ByteOffset)

	// Iterate through the two arrays of buckets.
	bucketArrays := [2]struct {
		addr uint64
		size uint64
	}{
		{buckets, 1 << b},
		{oldbuckets, 1 << (b - 1)},
	}
	for _, bucketArray := range bucketArrays {
		if bucketArray.addr == 0 {
			continue
		}
		for i := uint64(0); i < bucketArray.size; i++ {
			bucketAddr := bucketArray.addr + i*bucketSize
			// Iterate through the linked list of buckets.
			// TODO: check for repeated bucket pointers.
			for bucketAddr != 0 {
				// Iterate through each entry in the bucket.
				for j := uint64(0); j < bucketCnt; j++ {
					tophash, err := s.peekUint8(bucketAddr + tophashFieldOffset + j)
					if err != nil {
						return errors.New("reading map: " + err.Error())
					}
					// From runtime/hashmap.go
					const minTopHash = 4
					if tophash < minTopHash {
						continue
					}
					keyAddr := bucketAddr + keysFieldOffset + j*keysStride
					valAddr := bucketAddr + valuesFieldOffset + j*valuesStride
					if !fn(keyAddr, valAddr, keyType, valueType) {
						return nil
					}
				}
				var err error
				bucketAddr, err = s.peekPtr(bucketAddr + overflowFieldOffset)
				if err != nil {
					return errors.New("reading map: " + err.Error())
				}
			}
		}
	}

	return nil
}
