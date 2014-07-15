// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dwarf

// This file provides simple methods to access the symbol table by name and address.

import "fmt"

// LookupFunction returns the address of the named symbol, a function.
func (data *Data) LookupFunction(name string) (uint64, error) {
	r := data.Reader()
	for {
		entry, err := r.Next()
		if err != nil {
			return 0, err
		}
		if entry == nil {
			// TODO: why don't we get an error here?
			break
		}
		if entry.Tag != TagSubprogram {
			continue
		}
		nameAttr := entry.Val(AttrName)
		if nameAttr == nil {
			// TODO: this shouldn't be possible.
			continue
		}
		if nameAttr.(string) != name {
			continue
		}
		addrAttr := entry.Val(AttrLowpc)
		if addrAttr == nil {
			return 0, fmt.Errorf("symbol %q has no LowPC attribute", name)
		}
		addr, ok := addrAttr.(uint64)
		if !ok {
			return 0, fmt.Errorf("symbol %q has non-uint64 LowPC attribute", name)
		}
		return addr, nil
	}
	return 0, fmt.Errorf("function %q not found", name)
}

// LookupEntry returns the Entry for the named symbol.
func (data *Data) LookupEntry(name string) (*Entry, error) {
	r := data.Reader()
	for {
		entry, err := r.Next()
		if err != nil {
			return nil, err
		}
		if entry == nil {
			// TODO: why don't we get an error here?
			break
		}
		nameAttr := entry.Val(AttrName)
		if nameAttr == nil {
			continue
		}
		if nameAttr.(string) == name {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("entry for %q not found", name)
}

// LookupPC returns the name of a symbol at the specified PC.
func (data *Data) LookupPC(pc uint64) (string, error) {
	entry, _, err := data.EntryForPC(pc)
	if err != nil {
		return "", err
	}
	nameAttr := entry.Val(AttrName)
	if nameAttr == nil {
		// TODO: this shouldn't be possible.
		return "", fmt.Errorf("LookupPC: TODO")
	}
	name, ok := nameAttr.(string)
	if !ok {
		return "", fmt.Errorf("name for PC %#x is not a string", pc)
	}
	return name, nil
}

// EntryForPC returns the entry and address for a symbol at the specified PC.
func (data *Data) EntryForPC(pc uint64) (entry *Entry, lowpc uint64, err error) {
	// TODO: do something better than a linear scan?
	r := data.Reader()
	for {
		entry, err := r.Next()
		if err != nil {
			return nil, 0, err
		}
		if entry == nil {
			// TODO: why don't we get an error here.
			break
		}
		if entry.Tag != TagSubprogram {
			continue
		}
		lowpc, lok := entry.Val(AttrLowpc).(uint64)
		highpc, hok := entry.Val(AttrHighpc).(uint64)
		if !lok || !hok || pc < lowpc || highpc <= pc {
			continue
		}
		return entry, lowpc, nil
	}
	return nil, 0, fmt.Errorf("PC %#x not found", pc)
}
