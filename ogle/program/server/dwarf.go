// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

import (
	"regexp"

	"golang.org/x/debug/ogle/debug/dwarf"
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
		if entry.Tag != dwarf.TagSubprogram {
			continue
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

// TODO: signedness? Return (x int64, ok bool)??
func evalLocation(v []uint8) int64 {
	if len(v) == 0 {
		return 0
	}
	if v[0] != 0x9C { // DW_OP_call_frame_cfa
		return 0
	}
	if len(v) == 1 {
		return 0
	}
	v = v[1:]
	if v[0] != 0x11 { // DW_OP_consts
		return 0
	}
	return sleb128(v[1:])
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

func sleb128(v []uint8) (s int64) {
	var shift uint
	var sign int64 = -1
	for _, x := range v {
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
	// Sign extend?
	return s
}
