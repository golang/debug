// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Demo program that starts another program and calls Ogle library functions
// to debug it.

package ogler

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/debug/ogle/program/client"
)

var expected_vars = map[string]string{
	`main.Z_array`:               `[5]int8{-121, 121, 3, 2, 1}`,
	`main.Z_array_empty`:         `[0]int8{}`,
	`main.Z_bool_false`:          `false`,
	`main.Z_bool_true`:           `true`,
	`main.Z_channel`:             `(chan int 0xX)`,
	`main.Z_channel_buffered`:    `(chan int 0xX [0/10])`,
	`main.Z_channel_nil`:         `(chan int <nil>)`,
	`main.Z_array_of_empties`:    `[2]struct struct {}{struct struct {} {}, (struct struct {} 0xX)}`,
	`main.Z_complex128`:          `(1.987654321-2.987654321i)`,
	`main.Z_complex64`:           `(1.54321+2.54321i)`,
	`main.Z_float32`:             `1.54321`,
	`main.Z_float64`:             `1.987654321`,
	`main.Z_func_int8_r_int8`:    `func(int8, *int8) void @0xX `,
	`main.Z_func_int8_r_pint8`:   `func(int8, **int8) void @0xX `,
	`main.Z_func_bar`:            `func(*main.FooStruct) void @0xX `,
	`main.Z_int`:                 `-21`,
	`main.Z_int16`:               `-32321`,
	`main.Z_int32`:               `-1987654321`,
	`main.Z_int64`:               `-9012345678987654321`,
	`main.Z_int8`:                `-121`,
	`main.Z_interface`:           `("*main.FooStruct", 0xX)`,
	`main.Z_interface_nil`:       `(<nil>, <nil>)`,
	`main.Z_interface_typed_nil`: `("*main.FooStruct", <nil>)`,
	`main.Z_map`:                 `{-21:3.54321}`,
	`main.Z_map_2`:               `{1024:1}`,
	`main.Z_map_empty`:           `{}`,
	`main.Z_map_nil`:             `<nil>`,
	`main.Z_pointer`:             `0xX`,
	`main.Z_pointer_nil`:         `0x0`,
	`main.Z_slice`:               `[]uint8{115, 108, 105, 99, 101}`,
	`main.Z_slice_2`:             `[]int8{-121, 121}`,
	`main.Z_slice_nil`:           `[]uint8{}`,
	`main.Z_string`:              `"I'm a string"`,
	`main.Z_struct`:              `struct main.FooStruct {21, "hi"}`,
	`main.Z_uint`:                `21`,
	`main.Z_uint16`:              `54321`,
	`main.Z_uint32`:              `3217654321`,
	`main.Z_uint64`:              `12345678900987654321`,
	`main.Z_uint8`:               `231`,
	`main.Z_uintptr`:             `21`,
	`main.Z_unsafe_pointer`:      `0xX`,
	`main.Z_unsafe_pointer_nil`:  `0x0`,
}

func isHex(r uint8) bool {
	switch {
	case '0' <= r && r <= '9':
		return true
	case 'a' <= r && r <= 'f':
		return true
	case 'A' <= r && r <= 'F':
		return true
	default:
		return false
	}
}

// Check s matches the pattern in p.
// An 'X' in p greedily matches one or more hex characters in s.
func matches(p, s string) bool {
	j := 0
	for i := 0; i < len(p); i++ {
		if j == len(s) {
			return false
		}
		c := p[i]
		if c == 'X' {
			if !isHex(s[j]) {
				return false
			}
			for j < len(s) && isHex(s[j]) {
				j++
			}
			continue
		}
		if c != s[j] {
			return false
		}
		j++
	}
	return j == len(s)
}

func run(t *testing.T, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		t.Fatal(err)
	}
}

const (
	proxySrc     = "golang.org/x/debug/ogle/cmd/ogleproxy"
	proxyBinary  = "./ogleproxy"
	traceeSrc    = "golang.org/x/debug/ogle/demo/tracee"
	traceeBinary = "./tracee"
)

func TestBreakAndEval(t *testing.T) {
	run(t, "go", "build", "-o", proxyBinary, proxySrc)
	defer os.Remove(proxyBinary)

	run(t, "go", "build", "-o", traceeBinary, traceeSrc)
	defer os.Remove(traceeBinary)

	client.OgleproxyCmd = proxyBinary
	prog, err := client.Run("localhost", traceeBinary)
	if err != nil {
		log.Fatalf("Run: %v", err)
	}

	_, err = prog.Run()
	if err != nil {
		log.Fatalf("Run: %v", err)
	}

	pcs, err := prog.Breakpoint("re:main.foo")
	if err != nil {
		log.Fatalf("Breakpoint: %v", err)
	}
	fmt.Printf("breakpoints set at %x\n", pcs)

	_, err = prog.Resume()
	if err != nil {
		log.Fatalf("Resume: %v", err)
	}

	frames, err := prog.Frames(100)
	if err != nil {
		log.Fatalf("prog.Frames error: %v", err)
	}
	fmt.Printf("%#v\n", frames)

	varnames, err := prog.Eval(`re:main\.Z_.*`)
	if err != nil {
		log.Fatalf("prog.Eval error: %v", err)
	}

	// Evaluate each of the variables found above, and check they match
	// expected_vars.
	seen := make(map[string]bool)
	for _, v := range varnames {
		val, err := prog.Eval("val:" + v)
		if err != nil {
			log.Fatalf("prog.Eval error for %s: %v", v, err)
		} else {
			fmt.Printf("%s = %v\n", v, val)
			if seen[v] {
				log.Fatalf("Repeated variable %s\n", v)
			}
			seen[v] = true
			if len(val) != 1 {
				log.Fatalf("Should be one value for %s\n", v)
			}
			expected, ok := expected_vars[v]
			if !ok {
				log.Fatalf("Unexpected variable %s\n", v)
			} else {
				if !matches(expected, val[0]) {
					log.Fatalf("Expected %s = %s\n", v, expected)
				}
			}
		}
	}
	for v, e := range expected_vars {
		if !seen[v] {
			log.Fatalf("Didn't get %s = %s\n", v, e)
		}
	}

	// Remove the breakpoint at main.foo, set a breakpoint at main.f1 and main.f2,
	// then delete the breakpoint at main.f1.  Resume, then check we stopped at
	// main.f2.
	err = prog.DeleteBreakpoints(pcs)
	if err != nil {
		log.Fatalf("DeleteBreakpoints: %v", err)
	}
	pcs1, err := prog.Breakpoint("re:main.f1")
	if err != nil {
		log.Fatalf("Breakpoint: %v", err)
	}
	pcs2, err := prog.Breakpoint("re:main.f2")
	if err != nil {
		log.Fatalf("Breakpoint: %v", err)
	}
	err = prog.DeleteBreakpoints(pcs1)
	if err != nil {
		log.Fatalf("DeleteBreakpoints: %v", err)
	}
	status, err := prog.Resume()
	if err != nil {
		log.Fatalf("Resume: %v", err)
	}
	ok := false
	for _, pc := range pcs2 {
		if status.PC == pc {
			ok = true
			break
		}
	}
	if !ok {
		t.Errorf("Stopped at %X expected one of %X.", status.PC, pcs2)
	}
}
