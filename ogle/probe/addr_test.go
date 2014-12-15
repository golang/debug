// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package probe

import (
	"reflect"
	"testing"
)

// Defined in assembler.
func base() uintptr
func etext() uintptr
func edata() uintptr
func end() uintptr

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
		base()/2 - 1, // Pull well down below; the Mac only unmaps up to 0x1000.
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
		^uintptr(0),
	}
	for _, a := range addrs {
		if validWrite(a, 1) {
			t.Errorf("%#x is valid; should be invalid", a)
		}
	}
}
