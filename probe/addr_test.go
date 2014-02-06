// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"reflect"
	"testing"
)

var dataItem = 3
var bssItem [100]int

// addr turns an arbitrary address into a uintptr.
func addr(p interface{}) uintptr {
	v := reflect.ValueOf(p)
	switch v.Kind() {
	case reflect.Ptr:
		return v.Elem().UnsafeAddr()
	default:
		// TODO: how do we find the address of a text symbol?
		panic("unknown type " + v.Type().String())
	}
}

func TestGoodReadAddresses(t *testing.T) {
	addrs := []uintptr{
		base(),
		addr(&t),        // On the stack.
		addr(&dataItem), // In data.
		addr(&bssItem),  // In bss.
		heapUsed() - 1,
	}
	for _, a := range addrs {
		if !validRead(a, 1) {
			t.Errorf("%#x is invalid; should be valid", a)
		}
	}
}

func TestBadReadAddresses(t *testing.T) {
	addrs := []uintptr{
		0,
		base() - 1,
		heapUsed() + 1,
		^uintptr(0),
	}
	for _, a := range addrs {
		if validRead(a, 1) {
			t.Errorf("%#x is valid; should be invalid", a)
		}
	}
}

func TestGoodWriteAddresses(t *testing.T) {
	addrs := []uintptr{
		addr(&t),        // On the stack.
		addr(&dataItem), // In data.
		addr(&bssItem),  // In bss.
		heapUsed() - 1,
	}
	for _, a := range addrs {
		if !validWrite(a, 1) {
			t.Errorf("%#x is invalid; should be valid", a)
		}
	}
}

func TestBadWriteAddresses(t *testing.T) {
	addrs := []uintptr{
		0,
		base(), // In the text segment.
		base() - 1,
		heapUsed(),
		^uintptr(0),
	}
	for _, a := range addrs {
		if validWrite(a, 1) {
			t.Errorf("%#x is valid; should be invalid", a)
		}
	}
}

type span struct {
	p    uintptr
	size int
	ok   bool
}

func TestReadAddressSpan(t *testing.T) {
	spans := []span{
		{base(), 1, true},
		{base(), 4096, true},
		{base(), int(heapStart() - base()), false},
		{base(), 1e9, false},
		{heapStart(), 1, true},
		{heapStart(), 4096, true},
		{heapStart(), 1e9, false},
	}
	for _, s := range spans {
		if validRead(s.p, s.size) != s.ok {
			t.Errorf("(%#x,%d) should be %t; is %t", s.p, s.size, s.ok, !s.ok)
		}
	}
}

func TestWriteAddressSpan(t *testing.T) {
	spans := []span{
		{etext(), 1, true},
		{etext(), 4096, true},
		{etext(), int(heapStart() - base()), false},
		{etext(), 1e9, false},
		{heapStart(), 1, true},
		{heapStart(), 4096, true},
		{heapStart(), 1e9, false},
	}
	for _, s := range spans {
		if validWrite(s.p, s.size) != s.ok {
			t.Errorf("(%#x,%d) should be %t; is %t", s.p, s.size, s.ok, !s.ok)
		}
	}
}
