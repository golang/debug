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
	// The flag specifies whether to run the program (true)
	// or stop it before it executes any instructions (false).
	// The call hangs until the program stops executing,
	// at which point it returns the program status.
	Run(start bool) (Status, error)

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
	// Address syntax:
	//	"main.main"  Start of function
	//	"main.go:23" Line number
	//	(more to follow; may want an expression grammar)
	// It is OK if two breakpoints evaluate to the same PC. (TODO: verify.)
	Breakpoint(address string) error

	// DeleteBreakpoint removes the breakpoint at the specified
	// address.
	DeleteBreakpoint(address string) error

	// Eval evaluates the expression (typically an address) and returns
	// its string representation.
	Eval(expr string) (string, error)
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
