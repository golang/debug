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

	"golang.org/x/debug/ogle/program"
	"golang.org/x/debug/ogle/program/client"
	"golang.org/x/debug/ogle/program/local"
)

var expectedVarValues = map[string]interface{}{
	`main.Z_bool_false`: false,
	`main.Z_bool_true`:  true,
	`main.Z_complex128`: complex128(1.987654321 - 2.987654321i),
	`main.Z_complex64`:  complex64(1.54321 + 2.54321i),
	`main.Z_float32`:    float32(1.54321),
	`main.Z_float64`:    float64(1.987654321),
	`main.Z_int16`:      int16(-32321),
	`main.Z_int32`:      int32(-1987654321),
	`main.Z_int64`:      int64(-9012345678987654321),
	`main.Z_int8`:       int8(-121),
	`main.Z_uint16`:     uint16(54321),
	`main.Z_uint32`:     uint32(3217654321),
	`main.Z_uint64`:     uint64(12345678900987654321),
	`main.Z_uint8`:      uint8(231),
}

var expectedVars = map[string]string{
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
	`main.Z_map`:                 `map[-21:3.54321]`,
	`main.Z_map_2`:               `map[1024:1]`,
	`main.Z_map_3`:               `map[1024:1 512:-1]`,
	`main.Z_map_empty`:           `map[]`,
	`main.Z_map_nil`:             `map[]`,
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

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

const (
	proxySrc     = "golang.org/x/debug/ogle/cmd/ogleproxy"
	proxyBinary  = "./ogleproxy"
	traceeSrc    = "golang.org/x/debug/ogle/demo/tracee"
	traceeBinary = "./tracee"
)

func TestMain(m *testing.M) {
	os.Exit(buildAndRunTests(m))
}

func buildAndRunTests(m *testing.M) int {
	if err := run("go", "build", "-o", proxyBinary, proxySrc); err != nil {
		fmt.Println(err)
		return 1
	}
	client.OgleproxyCmd = proxyBinary
	defer os.Remove(proxyBinary)
	if err := run("go", "build", "-o", traceeBinary, traceeSrc); err != nil {
		fmt.Println(err)
		return 1
	}
	defer os.Remove(traceeBinary)
	return m.Run()
}

func TestLocalProgram(t *testing.T) {
	prog, err := local.New(traceeBinary)
	if err != nil {
		t.Fatal("local.New:", err)
	}
	testProgram(t, prog)
}

func TestRemoteProgram(t *testing.T) {
	prog, err := client.New("localhost", traceeBinary)
	if err != nil {
		t.Fatal("client.New:", err)
	}
	testProgram(t, prog)
}

func testProgram(t *testing.T, prog program.Program) {
	_, err := prog.Run("some", "arguments")
	if err != nil {
		log.Fatalf("Run: %v", err)
	}

	pcs, err := prog.BreakpointAtFunction("main.foo")
	if err != nil {
		log.Fatalf("BreakpointAtFunction: %v", err)
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
	if len(frames) == 0 {
		t.Fatalf("no stack frames returned")
	}
	if frames[0].Function != "main.foo" {
		t.Errorf("function name: got %s expected main.foo", frames[0].Function)
	}
	if len(frames[0].Params) != 2 {
		t.Errorf("got %d parameters, expected 2", len(frames[0].Params))
	} else {
		x := frames[0].Params[0]
		y := frames[0].Params[1]
		if x.Name != "x" {
			x, y = y, x
		}
		if x.Name != "x" {
			t.Errorf("parameter name: got %s expected x", x.Name)
		}
		if y.Name != "y" {
			t.Errorf("parameter name: got %s expected y", y.Name)
		}
		if val, err := prog.Value(x.Var); err != nil {
			t.Errorf("value of x: %s", err)
		} else if val != int16(42) {
			t.Errorf("value of x: got %T(%v) expected int16(42)", val, val)
		}
		if val, err := prog.Value(y.Var); err != nil {
			t.Errorf("value of y: %s", err)
		} else if val != float32(1.5) {
			t.Errorf("value of y: got %T(%v) expected float32(1.5)", val, val)
		}
	}

	varnames, err := prog.Eval(`re:main\.Z_.*`)
	if err != nil {
		log.Fatalf("prog.Eval error: %v", err)
	}

	// Evaluate each of the variables found above, and check they match
	// expectedVars.
	seen := make(map[string]bool)
	for _, v := range varnames {
		val, err := prog.Eval("val:" + v)
		if err != nil {
			log.Fatalf("prog.Eval error for %s: %v", v, err)
		} else {
			fmt.Printf("%s = %v\n", v, val)
			if seen[v] {
				log.Fatalf("repeated variable %s\n", v)
			}
			seen[v] = true
			if len(val) != 1 {
				log.Fatalf("should be one value for %s\n", v)
			}
			expected, ok := expectedVars[v]
			if !ok {
				log.Fatalf("unexpected variable %s\n", v)
			} else {
				if !matches(expected, val[0]) {
					log.Fatalf("expected %s = %s\n", v, expected)
				}
			}
		}
	}
	for v, e := range expectedVars {
		if !seen[v] {
			log.Fatalf("didn't get %s = %s\n", v, e)
		}
	}

	// Remove the breakpoint at main.foo.
	err = prog.DeleteBreakpoints(pcs)
	if err != nil {
		log.Fatalf("DeleteBreakpoints: %v", err)
	}

	// Set a breakpoint at line 80, resume, and check we stopped there.
	pcsLine80, err := prog.BreakpointAtLine("tracee/main.go", 80)
	if err != nil {
		t.Fatal("BreakpointAtLine:", err)
	}
	status, err := prog.Resume()
	if err != nil {
		log.Fatalf("Resume: %v", err)
	}
	stoppedAt := func(pcs []uint64) bool {
		for _, pc := range pcs {
			if status.PC == pc {
				return true
			}
		}
		return false
	}
	if !stoppedAt(pcsLine80) {
		t.Errorf("stopped at %X; expected one of %X.", status.PC, pcsLine80)
	}

	// Remove the breakpoint at line 80, set a breakpoint at main.f1 and main.f2,
	// then delete the breakpoint at main.f1.  Resume, then check we stopped at
	// main.f2.
	err = prog.DeleteBreakpoints(pcsLine80)
	if err != nil {
		log.Fatalf("DeleteBreakpoints: %v", err)
	}
	pcs1, err := prog.BreakpointAtFunction("main.f1")
	if err != nil {
		log.Fatalf("BreakpointAtFunction: %v", err)
	}
	pcs2, err := prog.BreakpointAtFunction("main.f2")
	if err != nil {
		log.Fatalf("BreakpointAtFunction: %v", err)
	}
	err = prog.DeleteBreakpoints(pcs1)
	if err != nil {
		log.Fatalf("DeleteBreakpoints: %v", err)
	}
	status, err = prog.Resume()
	if err != nil {
		log.Fatalf("Resume: %v", err)
	}
	if !stoppedAt(pcs2) {
		t.Errorf("stopped at %X; expected one of %X.", status.PC, pcs2)
	}

	// Check we get the expected results calling VarByName then Value
	// for the variables in expectedVarValues.
	for name, exp := range expectedVarValues {
		if v, err := prog.VarByName(name); err != nil {
			t.Errorf("VarByName(%s): %s", name, err)
		} else if val, err := prog.Value(v); err != nil {
			t.Errorf("value of %s: %s", name, err)
		} else if val != exp {
			t.Errorf("value of %s: got %T(%v) want %T(%v)", name, val, val, exp, exp)
		}
	}

	// Check some error cases for VarByName and Value.
	if _, err = prog.VarByName("not a real name"); err == nil {
		t.Error("VarByName for invalid name: expected error")
	}
	if _, err = prog.Value(program.Var{}); err == nil {
		t.Error("value of invalid var: expected error")
	}
	if v, err := prog.VarByName("main.Z_int16"); err != nil {
		t.Error("VarByName(main.Z_int16) error:", err)
	} else {
		v.Address = 0
		// v now has a valid type but a bad address.
		_, err = prog.Value(v)
		if err == nil {
			t.Error("value of invalid location: expected error")
		}
	}

	// checkValue tests that we can get a Var for a variable with the given name,
	// that we can then get the value of that Var, and that calling fn for that
	// value succeeds.
	checkValue := func(name string, fn func(val program.Value) error) {
		if v, err := prog.VarByName(name); err != nil {
			t.Errorf("VarByName(%s): %s", name, err)
		} else if val, err := prog.Value(v); err != nil {
			t.Errorf("value of %s: %s", name, err)
		} else if err := fn(val); err != nil {
			t.Errorf("value of %s: %s", name, err)
		}
	}

	checkValue("main.Z_uintptr", func(val program.Value) error {
		if val != uint32(21) && val != uint64(21) {
			// Z_uintptr should be an unsigned integer with size equal to the debugged
			// program's address size.
			return fmt.Errorf("got %T(%v) want 21", val, val)
		}
		return nil
	})

	checkValue("main.Z_int", func(val program.Value) error {
		if val != int32(-21) && val != int64(-21) {
			return fmt.Errorf("got %T(%v) want -21", val, val)
		}
		return nil
	})

	checkValue("main.Z_uint", func(val program.Value) error {
		if val != uint32(21) && val != uint64(21) {
			return fmt.Errorf("got %T(%v) want 21", val, val)
		}
		return nil
	})

	checkValue("main.Z_pointer", func(val program.Value) error {
		if _, ok := val.(program.Pointer); !ok {
			return fmt.Errorf("got %T(%v) expected Pointer", val, val)
		}
		return nil
	})

	checkValue("main.Z_pointer_nil", func(val program.Value) error {
		if p, ok := val.(program.Pointer); !ok {
			return fmt.Errorf("got %T(%v) expected Pointer", val, val)
		} else if p.Address != 0 {
			return fmt.Errorf("got %T(%v) expected nil pointer", val, val)
		}
		return nil
	})

	checkValue("main.Z_array", func(val program.Value) error {
		a, ok := val.(program.Array)
		if !ok {
			return fmt.Errorf("got %T(%v) expected Array", val, val)
		}
		if a.Len() != 5 {
			return fmt.Errorf("got array length %d expected 5", a.Len())
		}
		expected := [5]int8{-121, 121, 3, 2, 1}
		for i := uint64(0); i < 5; i++ {
			if v, err := prog.Value(a.Element(i)); err != nil {
				return fmt.Errorf("reading element %d: %s", i, err)
			} else if v != expected[i] {
				return fmt.Errorf("element %d: got %T(%v) want %T(%d)", i, v, v, expected[i], expected[i])
			}
		}
		return nil
	})

	checkValue("main.Z_slice", func(val program.Value) error {
		s, ok := val.(program.Slice)
		if !ok {
			return fmt.Errorf("got %T(%v) expected Slice", val, val)
		}
		if s.Len() != 5 {
			return fmt.Errorf("got slice length %d expected 5", s.Len())
		}
		expected := []uint8{115, 108, 105, 99, 101}
		for i := uint64(0); i < 5; i++ {
			if v, err := prog.Value(s.Element(i)); err != nil {
				return fmt.Errorf("reading element %d: %s", i, err)
			} else if v != expected[i] {
				return fmt.Errorf("element %d: got %T(%v) want %T(%d)", i, v, v, expected[i], expected[i])
			}
		}
		return nil
	})

	checkValue("main.Z_map_empty", func(val program.Value) error {
		m, ok := val.(program.Map)
		if !ok {
			return fmt.Errorf("got %T(%v) expected Map", val, val)
		}
		if m.Length != 0 {
			return fmt.Errorf("got map length %d expected 0", m.Length)
		}
		return nil
	})

	checkValue("main.Z_map_nil", func(val program.Value) error {
		m, ok := val.(program.Map)
		if !ok {
			return fmt.Errorf("got %T(%v) expected Map", val, val)
		}
		if m.Length != 0 {
			return fmt.Errorf("got map length %d expected 0", m.Length)
		}
		return nil
	})

	checkValue("main.Z_map_3", func(val program.Value) error {
		m, ok := val.(program.Map)
		if !ok {
			return fmt.Errorf("got %T(%v) expected Map", val, val)
		}
		if m.Length != 2 {
			return fmt.Errorf("got map length %d expected 2", m.Length)
		}
		keyVar0, valVar0, err := prog.MapElement(m, 0)
		if err != nil {
			return err
		}
		keyVar1, valVar1, err := prog.MapElement(m, 1)
		if err != nil {
			return err
		}
		key0, err := prog.Value(keyVar0)
		if err != nil {
			return err
		}
		key1, err := prog.Value(keyVar1)
		if err != nil {
			return err
		}
		val0, err := prog.Value(valVar0)
		if err != nil {
			return err
		}
		val1, err := prog.Value(valVar1)
		if err != nil {
			return err
		}
		// The map should contain 1024,1 and 512,-1 in some order.
		ok1 := key0 == int16(1024) && val0 == int8(1) && key1 == int16(512) && val1 == int8(-1)
		ok2 := key1 == int16(1024) && val1 == int8(1) && key0 == int16(512) && val0 == int8(-1)
		if !ok1 && !ok2 {
			return fmt.Errorf("got values (%d,%d) and (%d,%d), expected (1024,1) and (512,-1) in some order", key0, val0, key1, val1)
		}
		_, _, err = prog.MapElement(m, 2)
		if err == nil {
			return fmt.Errorf("MapElement: reading at a bad index succeeded, expected error")
		}
		return nil
	})
}
