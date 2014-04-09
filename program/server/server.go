// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package server provides RPC access to a local program being debugged.
// It is the remote end of the client implementation of the Program interface.
package server

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"code.google.com/p/ogle/debug/dwarf"
	"code.google.com/p/ogle/debug/elf"
	"code.google.com/p/ogle/debug/macho"

	"code.google.com/p/ogle/arch"
	"code.google.com/p/ogle/program"
	"code.google.com/p/ogle/program/proxyrpc"
)

type breakpoint struct {
	pc        uint64
	origInstr [arch.MaxBreakpointSize]byte
}

type Server struct {
	arch       arch.Architecture
	executable string // Name of executable.
	dwarfData  *dwarf.Data

	mu sync.Mutex

	fc chan func() error
	ec chan error

	proc        *os.Process
	breakpoints map[uint64]breakpoint
	files       []*file // Index == file descriptor.
}

// New parses the executable and builds local data structures for answering requests.
// It returns a Server ready to serve requests about the executable.
func New(executable string) (*Server, error) {
	fd, err := os.Open(executable)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	dwarfData, err := loadDwarfData(fd)
	if err != nil {
		return nil, err
	}
	srv := &Server{
		arch:        arch.AMD64, // TODO: How do we discover this?
		executable:  executable,
		dwarfData:   dwarfData,
		fc:          make(chan func() error),
		ec:          make(chan error),
		breakpoints: make(map[uint64]breakpoint),
	}
	go ptraceRun(srv.fc, srv.ec)
	return srv, nil
}

func loadDwarfData(f *os.File) (*dwarf.Data, error) {
	if obj, err := elf.NewFile(f); err == nil {
		return obj.DWARF()
	}
	if obj, err := macho.NewFile(f); err == nil {
		return obj.DWARF()
	}
	return nil, fmt.Errorf("unrecognized binary format")
}

type file struct {
	mode  string
	index int
	f     program.File
}

func (s *Server) Open(req *proxyrpc.OpenRequest, resp *proxyrpc.OpenResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// TODO: Better simulation. For now we just open the named OS file.
	var flag int
	switch req.Mode {
	case "r":
		flag = os.O_RDONLY
	case "w":
		flag = os.O_WRONLY
	case "rw":
		flag = os.O_RDWR
	default:
		return fmt.Errorf("Open: bad open mode %q", req.Mode)
	}
	osFile, err := os.OpenFile(req.Name, flag, 0)
	if err != nil {
		return err
	}
	// Find a file descriptor (index) slot.
	index := 0
	for ; index < len(s.files) && s.files[index] != nil; index++ {
	}
	f := &file{
		mode:  req.Mode,
		index: index,
		f:     osFile,
	}
	if index == len(s.files) {
		s.files = append(s.files, f)
	} else {
		s.files[index] = f
	}
	return nil
}

func (s *Server) ReadAt(req *proxyrpc.ReadAtRequest, resp *proxyrpc.ReadAtResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fd := req.FD
	if fd < 0 || len(s.files) <= fd || s.files[fd] == nil {
		return fmt.Errorf("ReadAt: bad file descriptor %d", fd)
	}
	f := s.files[fd]
	buf := make([]byte, req.Len) // TODO: Don't allocate every time
	n, err := f.f.ReadAt(buf, req.Offset)
	resp.Data = buf[:n]
	return err
}

func (s *Server) Close(req *proxyrpc.CloseRequest, resp *proxyrpc.CloseResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fd := req.FD
	if fd < 0 || fd >= len(s.files) || s.files[fd] == nil {
		return fmt.Errorf("Close: bad file descriptor %d", fd)
	}
	err := s.files[fd].f.Close()
	// Remove it regardless
	s.files[fd] = nil
	return err
}

func (s *Server) Run(req *proxyrpc.RunRequest, resp *proxyrpc.RunResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.proc != nil {
		s.proc.Kill()
		s.proc = nil
	}
	p, err := s.startProcess(s.executable, nil, &os.ProcAttr{
		Files: []*os.File{
			nil,       // TODO: be able to feed the target's stdin.
			os.Stderr, // TODO: be able to capture the target's stdout.
			os.Stderr,
		},
		Sys: &syscall.SysProcAttr{
			Ptrace: !req.Start,
		},
	})
	if err != nil {
		return err
	}
	s.proc = p

	if !req.Start {
		// TODO: wait until /proc/{s.proc.Pid}/status says "State:	t (tracing stop)".
	}
	return nil
}

func (s *Server) Resume(req *proxyrpc.ResumeRequest, resp *proxyrpc.ResumeResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	regs := syscall.PtraceRegs{}
	err := s.ptraceGetRegs(s.proc.Pid, &regs)
	if err != nil {
		return err
	}
	if _, ok := s.breakpoints[regs.Rip]; ok {
		err = s.ptraceSingleStep(s.proc.Pid)
		if err != nil {
			return fmt.Errorf("ptraceSingleStep: %v", err)
		}
	}

	err = s.setBreakpoints()
	if err != nil {
		return err
	}

	err = s.ptraceCont(s.proc.Pid, 0)
	if err != nil {
		return err
	}

	err = s.wait()
	if err != nil {
		return err
	}

	err = s.liftBreakpoints()
	if err != nil {
		return err
	}

	err = s.ptraceGetRegs(s.proc.Pid, &regs)
	if err != nil {
		return err
	}

	regs.Rip -= uint64(s.arch.BreakpointSize)
	err = s.ptraceSetRegs(s.proc.Pid, &regs)
	if err != nil {
		return fmt.Errorf("ptraceSetRegs: %v", err)
	}

	resp.Status.PC = regs.Rip
	resp.Status.SP = regs.Rsp
	return nil
}

func (s *Server) Breakpoint(req *proxyrpc.BreakpointRequest, resp *proxyrpc.BreakpointResponse) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	addrs, err := s.eval(req.Address)
	if err != nil {
		return err
	}
	var bp breakpoint
	for _, addr := range addrs {
		pc, err := s.evalAddress(addr)
		if err != nil {
			return err
		}
		if _, alreadySet := s.breakpoints[pc]; alreadySet {
			return fmt.Errorf("breakpoint already set at %#x (TODO)", pc)
		}

		err = s.ptracePeek(s.proc.Pid, uintptr(pc), bp.origInstr[:s.arch.BreakpointSize])
		if err != nil {
			return fmt.Errorf("ptracePeek: %v", err)
		}
		bp.pc = pc
		s.breakpoints[pc] = bp
	}

	return nil
}

func (s *Server) setBreakpoints() error {
	for pc := range s.breakpoints {
		err := s.ptracePoke(s.proc.Pid, uintptr(pc), s.arch.BreakpointInstr[:s.arch.BreakpointSize])
		if err != nil {
			return fmt.Errorf("setBreakpoints: %v", err)
		}
	}
	return nil
}

func (s *Server) liftBreakpoints() error {
	for pc, breakpoint := range s.breakpoints {
		err := s.ptracePoke(s.proc.Pid, uintptr(pc), breakpoint.origInstr[:s.arch.BreakpointSize])
		if err != nil {
			return fmt.Errorf("liftBreakpoints: %v", err)
		}
	}
	return nil
}

func (s *Server) Eval(req *proxyrpc.EvalRequest, resp *proxyrpc.EvalResponse) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resp.Result, err = s.eval(req.Expr)
	return err
}

// eval evaluates an expression.
// TODO: very weak.
func (s *Server) eval(expr string) ([]string, error) {
	switch {
	case strings.HasPrefix(expr, "re:"):
		// Regular expression. Return list of symbols.
		re, err := regexp.Compile(expr[3:])
		if err != nil {
			return nil, err
		}
		return s.lookupRE(re)

	case strings.HasPrefix(expr, "sym:"):
		// Symbol lookup. Return address.
		addr, err := s.lookupSym(expr[4:])
		if err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf("%#x", addr)}, nil

	case len(expr) > 0 && '0' <= expr[0] && expr[0] <= '9':
		// Numerical address. Return symbol.
		addr, err := strconv.ParseUint(expr, 0, 0)
		if err != nil {
			return nil, err
		}
		funcName, err := s.lookupPC(addr)
		if err != nil {
			return nil, err
		}
		return []string{funcName}, nil
	}

	return nil, fmt.Errorf("bad expression syntax: %q", expr)
}

// evalAddress takes a simple expression, either a symbol or hex value,
// and evaluates it as an address.
func (s *Server) evalAddress(expr string) (uint64, error) {
	// Might be a symbol.
	addr, err := s.lookupSym(expr)
	if err == nil {
		return addr, nil
	}

	// Must be a number.
	addr, err = strconv.ParseUint(expr, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("eval: %q is neither symbol nor number", expr)
	}

	return addr, nil
}

func (s *Server) Frames(req *proxyrpc.FramesRequest, resp *proxyrpc.FramesResponse) error {
	// TODO: verify that we're stopped.
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.Count != 1 {
		// TODO: implement.
		return fmt.Errorf("Frames.Count != 1 is not implemented")
	}

	// TODO: we're assuming we're at a function's entry point (LowPC).

	regs := syscall.PtraceRegs{}
	err := s.ptraceGetRegs(s.proc.Pid, &regs)
	if err != nil {
		return err
	}
	fp := regs.Rsp + uint64(s.arch.PointerSize)

	entry, err := s.entryForPC(regs.Rip)
	if err != nil {
		return err
	}

	var buf [8]byte
	frame := program.Frame{}
	r := s.dwarfData.Reader()
	r.Seek(entry.Offset)
	for {
		entry, err := r.Next()
		if err != nil {
			return err
		}
		if entry.Tag == 0 {
			break
		}
		if entry.Tag != dwarf.TagFormalParameter {
			continue
		}
		if entry.Children {
			// TODO: handle this??
			return fmt.Errorf("FormalParameter has children, expected none")
		}
		// TODO: the returned frame should be structured instead of a hacked up string.
		location := uintptr(0)
		for _, f := range entry.Field {
			switch f.Attr {
			case dwarf.AttrLocation:
				offset := evalLocation(f.Val.([]uint8))
				location = uintptr(int64(fp) + offset)
				frame.S += fmt.Sprintf("(%d(FP))", offset)
			case dwarf.AttrName:
				frame.S += " " + f.Val.(string)
			case dwarf.AttrType:
				t, err := s.dwarfData.Type(f.Val.(dwarf.Offset))
				if err == nil {
					frame.S += fmt.Sprintf("[%v]", t)
				}
				if t.String() != "int" || t.Size() != int64(s.arch.IntSize) {
					break
				}
				if location == 0 {
					return fmt.Errorf("no location for FormalParameter")
				}
				err = s.ptracePeek(s.proc.Pid, location, buf[:s.arch.IntSize])
				if err != nil {
					return err
				}
				frame.S += fmt.Sprintf("==%#x", s.arch.Int(buf[:s.arch.IntSize]))
			}
		}
	}
	resp.Frames = append(resp.Frames, frame)
	return nil
}
