// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

// TODO: syscall.PTRACE_O_TRACECLONE shenanigans to trace multi-threaded
// programs.

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// ptraceRun runs all the closures from fc on a dedicated OS thread. Errors
// are returned on ec. Both channels must be unbuffered, to ensure that the
// resultant error is sent back to the same goroutine that sent the closure.
func ptraceRun(fc chan func() error, ec chan error) {
	if cap(fc) != 0 || cap(ec) != 0 {
		panic("ptraceRun was given unbuffered channels")
	}
	runtime.LockOSThread()
	for f := range fc {
		ec <- f()
	}
}

func (s *Server) startProcess(name string, argv []string, attr *os.ProcAttr) (proc *os.Process, err error) {
	s.fc <- func() error {
		var err1 error
		proc, err1 = os.StartProcess(name, argv, attr)
		return err1
	}
	return proc, <-s.ec
}

func (s *Server) ptraceCont(pid int, signal int) (err error) {
	s.fc <- func() error {
		return syscall.PtraceCont(pid, signal)
	}
	return <-s.ec
}

func (s *Server) ptraceGetRegs(pid int, regsout *syscall.PtraceRegs) (err error) {
	s.fc <- func() error {
		return syscall.PtraceGetRegs(pid, regsout)
	}
	return <-s.ec
}

func (s *Server) ptracePeek(pid int, addr uintptr, out []byte) (err error) {
	s.fc <- func() error {
		n, err := syscall.PtracePeekText(pid, addr, out)
		if err != nil {
			return err
		}
		if n != len(out) {
			return fmt.Errorf("ptracePeek: peeked %d bytes, want %d", n, len(out))
		}
		return nil
	}
	return <-s.ec
}

func (s *Server) ptracePoke(pid int, addr uintptr, data []byte) (err error) {
	s.fc <- func() error {
		n, err := syscall.PtracePokeText(pid, addr, data)
		if err != nil {
			return err
		}
		if n != len(data) {
			return fmt.Errorf("ptracePoke: poked %d bytes, want %d", n, len(data))
		}
		return nil
	}
	return <-s.ec
}

func (s *Server) ptraceSingleStep(pid int) (err error) {
	s.fc <- func() error {
		return syscall.PtraceSingleStep(pid)
	}
	return <-s.ec
}

func (s *Server) wait() (err error) {
	var status syscall.WaitStatus
	s.fc <- func() error {
		_, err1 := syscall.Wait4(-1, &status, 0, nil)
		return err1
	}
	// TODO: do something with status.
	return <-s.ec
}
