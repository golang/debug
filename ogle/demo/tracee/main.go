// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Program for the Ogle demo to debug.

package main

import (
	"fmt"
	"time"
	"unsafe"
)

type FooInterface interface {
	Bar()
}

type FooStruct struct {
	a int
	b string
}

func (f *FooStruct) Bar() {}

var (
	Z_bool_false          bool        = false
	Z_bool_true           bool        = true
	Z_int                 int         = -21
	Z_int8                int8        = -121
	Z_int16               int16       = -32321
	Z_int32               int32       = -1987654321
	Z_int64               int64       = -9012345678987654321
	Z_uint                uint        = 21
	Z_uint8               uint8       = 231
	Z_uint16              uint16      = 54321
	Z_uint32              uint32      = 3217654321
	Z_uint64              uint64      = 12345678900987654321
	Z_uintptr             uintptr     = 21
	Z_float32             float32     = 1.54321
	Z_float64             float64     = 1.987654321
	Z_complex64           complex64   = 1.54321 + 2.54321i
	Z_complex128          complex128  = 1.987654321 - 2.987654321i
	Z_array               [5]int8     = [5]int8{-121, 121, 3, 2, 1}
	Z_array_empty         [0]int8     = [0]int8{}
	Z_array_of_empties    [2]struct{} = [2]struct{}{struct{}{}, struct{}{}}
	Z_channel             chan int    = make(chan int)
	Z_channel_buffered    chan int    = make(chan int, 10)
	Z_channel_nil         chan int
	Z_func_bar                         = (*FooStruct).Bar
	Z_func_int8_r_int8                 = func(x int8) int8 { return x + 1 }
	Z_func_int8_r_pint8                = func(x int8) *int8 { y := x + 1; return &y }
	Z_interface           FooInterface = &Z_struct
	Z_interface_typed_nil FooInterface = Z_pointer_nil
	Z_interface_nil       FooInterface
	Z_map                 map[int8]float32 = map[int8]float32{-21: 3.54321}
	Z_map_2               map[int16]int8   = map[int16]int8{1024: 1}
	Z_map_empty           map[int8]float32 = map[int8]float32{}
	Z_map_nil             map[int8]float32
	Z_pointer             *FooStruct = &Z_struct
	Z_pointer_nil         *FooStruct
	Z_slice               []byte = []byte{'s', 'l', 'i', 'c', 'e'}
	Z_slice_2             []int8 = Z_array[0:2]
	Z_slice_nil           []byte
	Z_string              string         = "I'm a string"
	Z_struct              FooStruct      = FooStruct{a: 21, b: "hi"}
	Z_unsafe_pointer      unsafe.Pointer = unsafe.Pointer(&Z_uint)
	Z_unsafe_pointer_nil  unsafe.Pointer
)

func foo() {
	fmt.Println(Z_bool_false, Z_bool_true)
	fmt.Println(Z_int, Z_int8, Z_int16, Z_int32, Z_int64)
	fmt.Println(Z_uint, Z_uint8, Z_uint16, Z_uint32, Z_uint64, Z_uintptr)
	fmt.Println(Z_float32, Z_float64, Z_complex64, Z_complex128)
	fmt.Println(Z_array, Z_array_empty, Z_array_of_empties)
	fmt.Println(Z_channel, Z_channel_buffered, Z_channel_nil)
	fmt.Println(Z_func_bar, Z_func_int8_r_int8, Z_func_int8_r_pint8)
	fmt.Println(Z_interface, Z_interface_nil, Z_interface_typed_nil)
	fmt.Println(Z_map, Z_map_2, Z_map_empty, Z_map_nil)
	fmt.Println(Z_pointer, Z_pointer_nil)
	fmt.Println(Z_slice, Z_slice_2, Z_slice_nil)
	fmt.Println(Z_string, Z_struct)
	fmt.Println(Z_unsafe_pointer, Z_unsafe_pointer_nil)
	f1()
	f2()
}

func f1() {
	fmt.Println()
}

func f2() {
	fmt.Println()
}

func bar() {
	foo()
	fmt.Print()
}

func main() {
	for ; ; time.Sleep(2 * time.Second) {
		bar()
	}
	select {}
}
