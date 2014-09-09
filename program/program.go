// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package program provides the portable interface to a program being debugged.
package program

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

	// SetArguments sets the command-line arguments for
	// the next running of the target binary, excluding the
	// target's binary name. That is, while debugging the
	// echo command, to prepare a run of "echo hi" call
	//	SetArguments("hi")
	SetArguments(args ...string)

	// Run abandons the current running process, if any,
	// and execs a new instance of the target binary file
	// (which may have changed underfoot).
	// Breakpoints and open files are re-established.
	// The call hangs until the program stops executing,
	// at which point it returns the program status.
	Run() (Status, error)

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
	// When the target binary is re-run, breakpoints are
	// automatically re-established in the new process by
	// re-evaluating the address.
	// The address is the same mini-language accepted by Eval,
	// which permits setting multiple breakpoints using a regular
	// expression to match a set of symbols.
	Breakpoint(address string) (PCs []uint64, err error)

	// DeleteBreakpoint removes the breakpoint at the specified
	// address. TODO: Probably the wrong interface.
	DeleteBreakpoint(address string) error

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
	S string
}
