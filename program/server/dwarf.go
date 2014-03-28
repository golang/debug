// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

import (
	"fmt"
	"regexp"

	"code.google.com/p/ogle/debug/dwarf"
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
		nameAttr := lookupAttr(entry, dwarf.AttrName)
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

func (s *Server) lookupSym(name string) (uint64, error) {
	r := s.dwarfData.Reader()
	for {
		entry, err := r.Next()
		if err != nil {
			return 0, err
		}
		if entry == nil {
			// TODO: why don't we get an error here.
			break
		}
		if entry.Tag != dwarf.TagSubprogram {
			continue
		}
		nameAttr := lookupAttr(entry, dwarf.AttrName)
		if nameAttr == nil {
			// TODO: this shouldn't be possible.
			continue
		}
		if nameAttr.(string) != name {
			continue
		}
		addrAttr := lookupAttr(entry, dwarf.AttrLowpc)
		if addrAttr == nil {
			return 0, fmt.Errorf("symbol %q has no LowPC attribute", name)
		}
		addr, ok := addrAttr.(uint64)
		if !ok {
			return 0, fmt.Errorf("symbol %q has non-uint64 LowPC attribute", name)
		}
		return addr, nil
	}
	return 0, fmt.Errorf("symbol %q not found", name)
}

func (s *Server) lookupPC(pc uint64) (string, error) {
	r := s.dwarfData.Reader()
	for {
		entry, err := r.Next()
		if err != nil {
			return "", err
		}
		if entry == nil {
			// TODO: why don't we get an error here.
			break
		}
		if entry.Tag != dwarf.TagSubprogram {
			continue
		}
		lowpc, lok := lookupAttr(entry, dwarf.AttrLowpc).(uint64)
		highpc, hok := lookupAttr(entry, dwarf.AttrHighpc).(uint64)
		if !lok || !hok || pc < lowpc || highpc <= pc {
			continue
		}
		nameAttr := lookupAttr(entry, dwarf.AttrName)
		if nameAttr == nil {
			// TODO: this shouldn't be possible.
			continue
		}
		name, ok := nameAttr.(string)
		if !ok {
			return "", fmt.Errorf("name for PC %#x is not a string", pc)
		}
		return name, nil
	}
	return "", fmt.Errorf("PC %#x not found", pc)
}

func lookupAttr(e *dwarf.Entry, a dwarf.Attr) interface{} {
	for _, f := range e.Field {
		if f.Attr == a {
			return f.Val
		}
	}
	return nil
}

func evalLocation(v []uint8) string {
	if len(v) == 0 {
		return "<nil>"
	}
	if v[0] != 0x9C { // DW_OP_call_frame_cfa
		return "UNK0"
	}
	if len(v) == 1 {
		return "0"
	}
	v = v[1:]
	if v[0] != 0x11 { // DW_OP_consts
		return "UNK1"
	}
	return fmt.Sprintf("%x", sleb128(v[1:]))
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
