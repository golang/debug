// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package program provides the portable interface to a program being debugged.
package program // import "golang.org/x/debug/ogle/program"

import (
	"io"
)

// Program is the interface to a (possibly remote) program being debugged.
// The process (if any) and text file associated with it may change during
// the session, but many resources are associated with the Program rather
// than process or text file so they persist across debuggging runs.
type Program interface {
	// Open opens a virtual file associated with the process.
	// Names are things like "text", "mem", "fd/2".
	// Mode is one of "r", "w", "rw".
	// Return values are open File and error.
	// When the target binary is re-run, open files are
	// automatically updated to refer to the corresponding
	// file in the new process.
	Open(name string, mode string) (File, error)

	// Run abandons the current running process, if any,
	// and execs a new instance of the target binary file
	// (which may have changed underfoot).
	// Breakpoints and open files are re-established.
	// The call hangs until the program stops executing,
	// at which point it returns the program status.
	// args contains the command-line arguments for the process.
	Run(args ...string) (Status, error)

	// Stop stops execution of the current process but
	// does not kill it.
	Stop() (Status, error)

	// Resume resumes execution of a stopped process.
	// The call hangs until the program stops executing,
	// at which point it returns the program status.
	Resume() (Status, error)

	// TODO: Step(). Where does the granularity happen,
	// on the proxy end or the debugging control end?

	// Kill kills the current process.
	Kill() (Status, error)

	// Breakpoint sets a breakpoint at the specified address.
	Breakpoint(address uint64) (PCs []uint64, err error)

	// BreakpointAtFunction sets a breakpoint at the start of the specified function.
	BreakpointAtFunction(name string) (PCs []uint64, err error)

	// BreakpointAtLine sets a breakpoint at the specified source line.
	BreakpointAtLine(file string, line uint64) (PCs []uint64, err error)

	// DeleteBreakpoints removes the breakpoints at the specified addresses.
	// Addresses where no breakpoint is set are ignored.
	DeleteBreakpoints(pcs []uint64) error

	// Eval evaluates the expression (typically an address) and returns
	// its string representation(s). Multivalued expressions such as
	// matches for regular expressions return multiple values.
	// Syntax:
	//	re:regexp
	//		Returns a list of symbol names that match the expression
	//	addr:symbol
	//		Returns a one-element list holding the hexadecimal
	//		("0x1234") value of the address of the symbol
	//	val:symbol
	//		Returns a one-element list holding the formatted
	//		value of the symbol
	//	0x1234, 01234, 467
	//		Returns a one-element list holding the name of the
	//		symbol ("main.foo") at that address (hex, octal, decimal).
	Eval(expr string) ([]string, error)

	// Frames returns up to count stack frames from where the program
	// is currently stopped.
	Frames(count int) ([]Frame, error)

	// VarByName returns a Var referring to a global variable with the given name.
	// TODO: local variables
	VarByName(name string) (Var, error)

	// Value gets the value of a variable by reading the program's memory.
	Value(v Var) (Value, error)
}

// A reference to a variable in a program.
// TODO: handle variables stored in registers
type Var struct {
	TypeID  uint64 // A type identifier, opaque to the user.
	Address uint64 // The address of the variable.
}

// A value read from a remote program.
type Value interface{}

// Pointer is a Var representing a pointer.
type Pointer Var

// Array is a Var representing an array.
type Array struct {
	ElementTypeID uint64
	Address       uint64
	Length        uint64 // Number of elements in the array
	StrideBits    uint64 // Number of bits between array entries
}

// Len returns the number of elements in the array.
func (a Array) Len() uint64 {
	return a.Length
}

// Element returns a Var referring to the given element of the array.
func (a Array) Element(index uint64) Var {
	return Var{
		TypeID:  a.ElementTypeID,
		Address: a.Address + index*(a.StrideBits/8),
	}
}

// Struct is a Var representing a struct.
type Struct struct {
	Fields []StructField
}

// StructField represents a field in a struct object.
type StructField struct {
	Name string
	Var  Var
}

// The File interface provides access to file-like resources in the program.
// It implements only ReaderAt and WriterAt, not Reader and Writer, because
// random access is a far more common pattern for things like symbol tables,
// and because enormous address space of virtual memory makes routines
// like io.Copy dangerous.
type File interface {
	io.ReaderAt
	io.WriterAt
	io.Closer
}

type Status struct {
	PC, SP uint64
}

type Frame struct {
	// PC is the hardware program counter.
	PC uint64
	// SP is the hardware stack pointer.
	SP uint64
	// File and Line are the source code location of the PC.
	File string
	Line int
	// Function is the name of this frame's function.
	Function string
	// Params contains the function's parameters.
	Params []Param
	// Vars contains the function's local variables.
	Vars []LocalVar
}

// Param is a parameter of a function.
type Param struct {
	Name string
	Var  Var
}

// LocalVar is a local variable of a function.
type LocalVar struct {
	Name string
	Var  Var
}
