// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

import (
	"errors"
	"regexp"

	"golang.org/x/debug/dwarf"
)

func (s *Server) lookupRE(re *regexp.Regexp) (result []string, err error) {
	r := s.dwarfData.Reader()
	for {
		entry, err := r.Next()
		if err != nil {
			return nil, err
		}
		if entry == nil {
			// TODO: why don't we get an error here.
			break
		}
		nameAttr := entry.Val(dwarf.AttrName)
		if nameAttr == nil {
			// TODO: this shouldn't be possible.
			continue
		}
		name, ok := nameAttr.(string)
		if !ok || !re.MatchString(name) {
			continue
		}
		result = append(result, name)
	}
	return result, nil
}

func (s *Server) lookupFunction(name string) (uint64, error) {
	return s.dwarfData.LookupFunction(name)
}

func (s *Server) lookupVariable(name string) (uint64, error) {
	return s.dwarfData.LookupVariable(name)
}

func (s *Server) lookupPC(pc uint64) (string, error) {
	return s.dwarfData.LookupPC(pc)
}

func (s *Server) entryForPC(pc uint64) (entry *dwarf.Entry, lowpc uint64, err error) {
	return s.dwarfData.EntryForPC(pc)
}

// evalLocation parses a DWARF location description encoded in v.  It works for
// cases where the variable is stored at an offset from the Canonical Frame
// Address.  The return value is this offset.
// TODO: a more general location-description-parsing function.
func evalLocation(v []uint8) (int64, error) {
	// Some DWARF constants.
	const (
		opConsts       = 0x11
		opPlus         = 0x22
		opCallFrameCFA = 0x9C
	)
	if len(v) == 0 {
		return 0, errors.New("empty location specifier")
	}
	if v[0] != opCallFrameCFA {
		return 0, errors.New("unsupported location specifier")
	}
	if len(v) == 1 {
		// The location description was just DW_OP_call_frame_cfa, so the location is exactly the CFA.
		return 0, nil
	}
	if v[1] != opConsts {
		return 0, errors.New("unsupported location specifier")
	}
	offset, v, err := sleb128(v[2:])
	if err != nil {
		return 0, err
	}
	if len(v) == 1 && v[0] == opPlus {
		// The location description was DW_OP_call_frame_cfa, DW_OP_consts <offset>, DW_OP_plus.
		// So return the offset.
		return offset, nil
	}
	return 0, errors.New("unsupported location specifier")
}

func uleb128(v []uint8) (u uint64) {
	var shift uint
	for _, x := range v {
		u |= (uint64(x) & 0x7F) << shift
		shift += 7
		if x&0x80 == 0 {
			break
		}
	}
	return u
}

// sleb128 parses a signed integer encoded with sleb128 at the start of v, and
// returns the integer and the remainder of v.
func sleb128(v []uint8) (s int64, rest []uint8, err error) {
	var shift uint
	var sign int64 = -1
	var i int
	var x uint8
	for i, x = range v {
		s |= (int64(x) & 0x7F) << shift
		shift += 7
		sign <<= 7
		if x&0x80 == 0 {
			if x&0x40 != 0 {
				s |= sign
			}
			break
		}
	}
	if i == len(v) {
		return 0, nil, errors.New("truncated sleb128")
	}
	return s, v[i+1:], nil
}
