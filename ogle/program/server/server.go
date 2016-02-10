// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package server provides RPC access to a local program being debugged.
// It is the remote end of the client implementation of the Program interface.
package server // import "golang.org/x/debug/ogle/program/server"

//go:generate sh -c "m4 -P eval.m4 > eval.go"

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/debug/dwarf"
	"golang.org/x/debug/elf"
	"golang.org/x/debug/macho"
	"golang.org/x/debug/ogle/arch"
	"golang.org/x/debug/ogle/program"
	"golang.org/x/debug/ogle/program/proxyrpc"
)

type breakpoint struct {
	pc        uint64
	origInstr [arch.MaxBreakpointSize]byte
}

type call struct {
	req, resp interface{}
	errc      chan error
}

type Server struct {
	arch       arch.Architecture
	executable string // Name of executable.
	dwarfData  *dwarf.Data

	breakpointc chan call
	otherc      chan call

	fc chan func() error
	ec chan error

	proc            *os.Process
	procIsUp        bool
	stoppedPid      int
	stoppedRegs     syscall.PtraceRegs
	topOfStackAddrs []uint64
	breakpoints     map[uint64]breakpoint
	files           []*file // Index == file descriptor.
	printer         *Printer

	// goroutineStack reads the stack of a (non-running) goroutine.
	goroutineStack     func(uint64) ([]program.Frame, error)
	goroutineStackOnce sync.Once
}

// peek implements the Peeker interface required by the printer.
func (s *Server) peek(offset uintptr, buf []byte) error {
	return s.ptracePeek(s.stoppedPid, offset, buf)
}

// New parses the executable and builds local data structures for answering requests.
// It returns a Server ready to serve requests about the executable.
func New(executable string) (*Server, error) {
	fd, err := os.Open(executable)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	architecture, dwarfData, err := loadExecutable(fd)
	if err != nil {
		return nil, err
	}
	srv := &Server{
		arch:        *architecture,
		executable:  executable,
		dwarfData:   dwarfData,
		breakpointc: make(chan call),
		otherc:      make(chan call),
		fc:          make(chan func() error),
		ec:          make(chan error),
		breakpoints: make(map[uint64]breakpoint),
	}
	srv.printer = NewPrinter(architecture, dwarfData, srv)
	go ptraceRun(srv.fc, srv.ec)
	go srv.loop()
	return srv, nil
}

func loadExecutable(f *os.File) (*arch.Architecture, *dwarf.Data, error) {
	// TODO: How do we detect NaCl?
	if obj, err := elf.NewFile(f); err == nil {
		dwarfData, err := obj.DWARF()
		if err != nil {
			return nil, nil, err
		}

		switch obj.Machine {
		case elf.EM_ARM:
			return &arch.ARM, dwarfData, nil
		case elf.EM_386:
			switch obj.Class {
			case elf.ELFCLASS32:
				return &arch.X86, dwarfData, nil
			case elf.ELFCLASS64:
				return &arch.AMD64, dwarfData, nil
			}
		case elf.EM_X86_64:
			return &arch.AMD64, dwarfData, nil
		}
		return nil, nil, fmt.Errorf("unrecognized ELF architecture")
	}
	if obj, err := macho.NewFile(f); err == nil {
		dwarfData, err := obj.DWARF()
		if err != nil {
			return nil, nil, err
		}

		/* TODO
		table, err := parseMachO(obj)
		if err != nil {
			return nil, nil, err
		}
		*/
		switch obj.Cpu {
		case macho.Cpu386:
			return &arch.X86, dwarfData, nil
		case macho.CpuAmd64:
			return &arch.AMD64, dwarfData, nil
		}
		return nil, nil, fmt.Errorf("unrecognized Mach-O architecture")
	}
	return nil, nil, fmt.Errorf("unrecognized binary format")
}

func (s *Server) loop() {
	for {
		var c call
		select {
		case c = <-s.breakpointc:
		case c = <-s.otherc:
		}
		s.dispatch(c)
	}
}

func (s *Server) dispatch(c call) {
	switch req := c.req.(type) {
	case *proxyrpc.BreakpointRequest:
		c.errc <- s.handleBreakpoint(req, c.resp.(*proxyrpc.BreakpointResponse))
	case *proxyrpc.BreakpointAtFunctionRequest:
		c.errc <- s.handleBreakpointAtFunction(req, c.resp.(*proxyrpc.BreakpointResponse))
	case *proxyrpc.BreakpointAtLineRequest:
		c.errc <- s.handleBreakpointAtLine(req, c.resp.(*proxyrpc.BreakpointResponse))
	case *proxyrpc.DeleteBreakpointsRequest:
		c.errc <- s.handleDeleteBreakpoints(req, c.resp.(*proxyrpc.DeleteBreakpointsResponse))
	case *proxyrpc.CloseRequest:
		c.errc <- s.handleClose(req, c.resp.(*proxyrpc.CloseResponse))
	case *proxyrpc.EvalRequest:
		c.errc <- s.handleEval(req, c.resp.(*proxyrpc.EvalResponse))
	case *proxyrpc.EvaluateRequest:
		c.errc <- s.handleEvaluate(req, c.resp.(*proxyrpc.EvaluateResponse))
	case *proxyrpc.FramesRequest:
		c.errc <- s.handleFrames(req, c.resp.(*proxyrpc.FramesResponse))
	case *proxyrpc.OpenRequest:
		c.errc <- s.handleOpen(req, c.resp.(*proxyrpc.OpenResponse))
	case *proxyrpc.ReadAtRequest:
		c.errc <- s.handleReadAt(req, c.resp.(*proxyrpc.ReadAtResponse))
	case *proxyrpc.ResumeRequest:
		c.errc <- s.handleResume(req, c.resp.(*proxyrpc.ResumeResponse))
	case *proxyrpc.RunRequest:
		c.errc <- s.handleRun(req, c.resp.(*proxyrpc.RunResponse))
	case *proxyrpc.VarByNameRequest:
		c.errc <- s.handleVarByName(req, c.resp.(*proxyrpc.VarByNameResponse))
	case *proxyrpc.ValueRequest:
		c.errc <- s.handleValue(req, c.resp.(*proxyrpc.ValueResponse))
	case *proxyrpc.MapElementRequest:
		c.errc <- s.handleMapElement(req, c.resp.(*proxyrpc.MapElementResponse))
	case *proxyrpc.GoroutinesRequest:
		c.errc <- s.handleGoroutines(req, c.resp.(*proxyrpc.GoroutinesResponse))
	default:
		panic(fmt.Sprintf("unexpected call request type %T", c.req))
	}
}

func (s *Server) call(c chan call, req, resp interface{}) error {
	errc := make(chan error)
	c <- call{req, resp, errc}
	return <-errc
}

type file struct {
	mode  string
	index int
	f     program.File
}

func (s *Server) Open(req *proxyrpc.OpenRequest, resp *proxyrpc.OpenResponse) error {
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleOpen(req *proxyrpc.OpenRequest, resp *proxyrpc.OpenResponse) error {
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
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleReadAt(req *proxyrpc.ReadAtRequest, resp *proxyrpc.ReadAtResponse) error {
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
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleClose(req *proxyrpc.CloseRequest, resp *proxyrpc.CloseResponse) error {
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
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleRun(req *proxyrpc.RunRequest, resp *proxyrpc.RunResponse) error {
	if s.proc != nil {
		s.proc.Kill()
		s.proc = nil
		s.procIsUp = false
		s.stoppedPid = 0
		s.stoppedRegs = syscall.PtraceRegs{}
		s.topOfStackAddrs = nil
	}
	argv := append([]string{s.executable}, req.Args...)
	p, err := s.startProcess(s.executable, argv, &os.ProcAttr{
		Files: []*os.File{
			nil,       // TODO: be able to feed the target's stdin.
			os.Stderr, // TODO: be able to capture the target's stdout.
			os.Stderr,
		},
		Sys: &syscall.SysProcAttr{
			Pdeathsig: syscall.SIGKILL,
			Ptrace:    true,
		},
	})
	if err != nil {
		return err
	}
	s.proc = p
	s.stoppedPid = p.Pid
	return nil
}

func (s *Server) Resume(req *proxyrpc.ResumeRequest, resp *proxyrpc.ResumeResponse) error {
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleResume(req *proxyrpc.ResumeRequest, resp *proxyrpc.ResumeResponse) error {
	if s.proc == nil {
		return fmt.Errorf("Resume: Run did not successfully start a process")
	}

	if !s.procIsUp {
		s.procIsUp = true
		if _, err := s.waitForTrap(s.stoppedPid, false); err != nil {
			return err
		}
		if err := s.ptraceSetOptions(s.stoppedPid, syscall.PTRACE_O_TRACECLONE); err != nil {
			return fmt.Errorf("ptraceSetOptions: %v", err)
		}
	} else if _, ok := s.breakpoints[s.stoppedRegs.Rip]; ok {
		if err := s.ptraceSingleStep(s.stoppedPid); err != nil {
			return fmt.Errorf("ptraceSingleStep: %v", err)
		}
		if _, err := s.waitForTrap(s.stoppedPid, false); err != nil {
			return err
		}
	}

	for {
		if err := s.setBreakpoints(); err != nil {
			return err
		}
		if err := s.ptraceCont(s.stoppedPid, 0); err != nil {
			return fmt.Errorf("ptraceCont: %v", err)
		}

		wpid, err := s.waitForTrap(-1, true)
		if err == nil {
			s.stoppedPid = wpid
			break
		}
		bce, ok := err.(*breakpointsChangedError)
		if !ok {
			return err
		}

		if err := syscall.Kill(s.stoppedPid, syscall.SIGSTOP); err != nil {
			return fmt.Errorf("kill(SIGSTOP): %v", err)
		}
		_, status, err := s.wait(s.stoppedPid, false)
		if err != nil {
			return fmt.Errorf("wait (after SIGSTOP): %v", err)
		}
		if !status.Stopped() || status.StopSignal() != syscall.SIGSTOP {
			return fmt.Errorf("wait (after SIGSTOP): unexpected wait status 0x%x", status)
		}

		if err := s.liftBreakpoints(); err != nil {
			return err
		}

	loop:
		for c := bce.call; ; {
			s.dispatch(c)
			select {
			case c = <-s.breakpointc:
			default:
				break loop
			}
		}
	}
	if err := s.liftBreakpoints(); err != nil {
		return err
	}

	if err := s.ptraceGetRegs(s.stoppedPid, &s.stoppedRegs); err != nil {
		return fmt.Errorf("ptraceGetRegs: %v", err)
	}

	s.stoppedRegs.Rip -= uint64(s.arch.BreakpointSize)

	if err := s.ptraceSetRegs(s.stoppedPid, &s.stoppedRegs); err != nil {
		return fmt.Errorf("ptraceSetRegs: %v", err)
	}

	resp.Status.PC = s.stoppedRegs.Rip
	resp.Status.SP = s.stoppedRegs.Rsp
	return nil
}

func (s *Server) waitForTrap(pid int, allowBreakpointsChange bool) (wpid int, err error) {
	for {
		wpid, status, err := s.wait(pid, allowBreakpointsChange)
		if err != nil {
			if _, ok := err.(*breakpointsChangedError); !ok {
				err = fmt.Errorf("wait: %v", err)
			}
			return 0, err
		}
		if status.StopSignal() == syscall.SIGTRAP && status.TrapCause() != syscall.PTRACE_EVENT_CLONE {
			return wpid, nil
		}
		err = s.ptraceCont(wpid, 0) // TODO: non-zero when wait catches other signals?
		if err != nil {
			return 0, fmt.Errorf("ptraceCont: %v", err)
		}
	}
}

func (s *Server) Breakpoint(req *proxyrpc.BreakpointRequest, resp *proxyrpc.BreakpointResponse) error {
	return s.call(s.breakpointc, req, resp)
}

func (s *Server) handleBreakpoint(req *proxyrpc.BreakpointRequest, resp *proxyrpc.BreakpointResponse) error {
	return s.addBreakpoints([]uint64{req.Address}, resp)
}

func (s *Server) BreakpointAtFunction(req *proxyrpc.BreakpointAtFunctionRequest, resp *proxyrpc.BreakpointResponse) error {
	return s.call(s.breakpointc, req, resp)
}

func (s *Server) handleBreakpointAtFunction(req *proxyrpc.BreakpointAtFunctionRequest, resp *proxyrpc.BreakpointResponse) error {
	pc, err := s.lookupFunction(req.Function)
	if err != nil {
		return err
	}
	return s.addBreakpoints([]uint64{pc}, resp)
}

func (s *Server) BreakpointAtLine(req *proxyrpc.BreakpointAtLineRequest, resp *proxyrpc.BreakpointResponse) error {
	return s.call(s.breakpointc, req, resp)
}

func (s *Server) handleBreakpointAtLine(req *proxyrpc.BreakpointAtLineRequest, resp *proxyrpc.BreakpointResponse) error {
	if s.dwarfData == nil {
		return fmt.Errorf("no DWARF data")
	}
	if pcs, err := s.dwarfData.LineToPCs(req.File, req.Line); err != nil {
		return err
	} else {
		return s.addBreakpoints(pcs, resp)
	}
}

// addBreakpoints adds breakpoints at the addresses in pcs, then stores pcs in the response.
func (s *Server) addBreakpoints(pcs []uint64, resp *proxyrpc.BreakpointResponse) error {
	// Get the original code at each address with ptracePeek.
	bps := make([]breakpoint, 0, len(pcs))
	for _, pc := range pcs {
		if _, alreadySet := s.breakpoints[pc]; alreadySet {
			continue
		}
		var bp breakpoint
		if err := s.ptracePeek(s.stoppedPid, uintptr(pc), bp.origInstr[:s.arch.BreakpointSize]); err != nil {
			return fmt.Errorf("ptracePeek: %v", err)
		}
		bp.pc = pc
		bps = append(bps, bp)
	}
	// If all the peeks succeeded, update the list of breakpoints.
	for _, bp := range bps {
		s.breakpoints[bp.pc] = bp
	}
	resp.PCs = pcs
	return nil
}

func (s *Server) DeleteBreakpoints(req *proxyrpc.DeleteBreakpointsRequest, resp *proxyrpc.DeleteBreakpointsResponse) error {
	return s.call(s.breakpointc, req, resp)
}

func (s *Server) handleDeleteBreakpoints(req *proxyrpc.DeleteBreakpointsRequest, resp *proxyrpc.DeleteBreakpointsResponse) error {
	for _, pc := range req.PCs {
		delete(s.breakpoints, pc)
	}
	return nil
}

func (s *Server) setBreakpoints() error {
	for pc := range s.breakpoints {
		err := s.ptracePoke(s.stoppedPid, uintptr(pc), s.arch.BreakpointInstr[:s.arch.BreakpointSize])
		if err != nil {
			return fmt.Errorf("setBreakpoints: %v", err)
		}
	}
	return nil
}

func (s *Server) liftBreakpoints() error {
	for pc, breakpoint := range s.breakpoints {
		err := s.ptracePoke(s.stoppedPid, uintptr(pc), breakpoint.origInstr[:s.arch.BreakpointSize])
		if err != nil {
			return fmt.Errorf("liftBreakpoints: %v", err)
		}
	}
	return nil
}

func (s *Server) Eval(req *proxyrpc.EvalRequest, resp *proxyrpc.EvalResponse) error {
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleEval(req *proxyrpc.EvalRequest, resp *proxyrpc.EvalResponse) (err error) {
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

	case strings.HasPrefix(expr, "addr:"):
		// Symbol lookup. Return address.
		addr, err := s.lookupFunction(expr[5:])
		if err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf("%#x", addr)}, nil

	case strings.HasPrefix(expr, "val:"):
		// Symbol lookup. Return formatted value.
		value, err := s.printer.Sprint(expr[4:])
		if err != nil {
			return nil, err
		}
		return []string{value}, nil

	case strings.HasPrefix(expr, "src:"):
		// Numerical address. Return file.go:123.
		addr, err := strconv.ParseUint(expr[4:], 0, 0)
		if err != nil {
			return nil, err
		}
		file, line, err := s.lookupSource(addr)
		if err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf("%s:%d", file, line)}, nil

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

func (s *Server) Evaluate(req *proxyrpc.EvaluateRequest, resp *proxyrpc.EvaluateResponse) error {
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleEvaluate(req *proxyrpc.EvaluateRequest, resp *proxyrpc.EvaluateResponse) (err error) {
	resp.Result, err = s.evalExpression(req.Expression, s.stoppedRegs.Rip, s.stoppedRegs.Rsp)
	return err
}

func (s *Server) lookupSource(pc uint64) (file string, line uint64, err error) {
	if s.dwarfData == nil {
		return
	}
	// TODO: The gosym equivalent also returns the relevant Func. Do that when
	// DWARF has the same facility.
	return s.dwarfData.PCToLine(pc)
}

// evalAddress takes a simple expression, either a symbol or hex value,
// and evaluates it as an address.
func (s *Server) evalAddress(expr string) (uint64, error) {
	// Might be a symbol.
	addr, err := s.lookupFunction(expr) // TODO: might not be a function
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
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleFrames(req *proxyrpc.FramesRequest, resp *proxyrpc.FramesResponse) error {
	// TODO: verify that we're stopped.
	if s.topOfStackAddrs == nil {
		if err := s.evaluateTopOfStackAddrs(); err != nil {
			return err
		}
	}

	regs := syscall.PtraceRegs{}
	err := s.ptraceGetRegs(s.stoppedPid, &regs)
	if err != nil {
		return err
	}
	resp.Frames, err = s.walkStack(regs.Rip, regs.Rsp, req.Count)
	return err
}

// walkStack returns up to the requested number of stack frames.
func (s *Server) walkStack(pc, sp uint64, count int) ([]program.Frame, error) {
	var frames []program.Frame

	var buf [8]byte
	b := new(bytes.Buffer)
	r := s.dwarfData.Reader()

	// TODO: handle walking over a split stack.
	for i := 0; i < count; i++ {
		b.Reset()
		file, line, err := s.dwarfData.PCToLine(pc)
		if err != nil {
			return frames, err
		}
		fpOffset, err := s.dwarfData.PCToSPOffset(pc)
		if err != nil {
			return frames, err
		}
		fp := sp + uint64(fpOffset)
		entry, funcEntry, err := s.entryForPC(pc)
		if err != nil {
			return frames, err
		}
		frame := program.Frame{
			PC:            pc,
			SP:            sp,
			File:          file,
			Line:          line,
			FunctionStart: funcEntry,
		}
		frame.Function, _ = entry.Val(dwarf.AttrName).(string)
		r.Seek(entry.Offset)
		for {
			entry, err := r.Next()
			if err != nil {
				return frames, err
			}
			if entry.Tag == 0 {
				break
			}
			// TODO: report variables we couldn't parse?
			if entry.Tag == dwarf.TagFormalParameter {
				if v, err := s.parseParameterOrLocal(entry, fp); err == nil {
					frame.Params = append(frame.Params, program.Param(v))
				}
			}
			if entry.Tag == dwarf.TagVariable {
				if v, err := s.parseParameterOrLocal(entry, fp); err == nil {
					frame.Vars = append(frame.Vars, v)
				}
			}
		}
		frames = append(frames, frame)

		// Walk to the caller's PC and SP.
		if s.topOfStack(funcEntry) {
			break
		}
		err = s.ptracePeek(s.stoppedPid, uintptr(fp-uint64(s.arch.PointerSize)), buf[:s.arch.PointerSize])
		if err != nil {
			return frames, fmt.Errorf("ptracePeek: %v", err)
		}
		pc, sp = s.arch.Uintptr(buf[:s.arch.PointerSize]), fp
	}
	return frames, nil
}

// parseParameterOrLocal parses the entry for a function parameter or local
// variable, which are both specified the same way. fp contains the frame
// pointer, which is used to calculate the variable location.
func (s *Server) parseParameterOrLocal(entry *dwarf.Entry, fp uint64) (program.LocalVar, error) {
	var v program.LocalVar
	v.Name, _ = entry.Val(dwarf.AttrName).(string)
	if off, err := s.dwarfData.EntryTypeOffset(entry); err != nil {
		return v, err
	} else {
		v.Var.TypeID = uint64(off)
	}
	if i := entry.Val(dwarf.AttrLocation); i == nil {
		return v, fmt.Errorf("missing location description")
	} else if locationDescription, ok := i.([]uint8); !ok {
		return v, fmt.Errorf("unsupported location description")
	} else if offset, err := evalLocation(locationDescription); err != nil {
		return v, err
	} else {
		v.Var.Address = fp + uint64(offset)
	}
	return v, nil
}

func (s *Server) evaluateTopOfStackAddrs() error {
	var (
		lookup   func(name string) (uint64, error)
		indirect bool
		names    []string
	)
	if _, err := s.lookupVariable("runtime.rt0_goPC"); err != nil {
		// Look for a Go 1.3 binary (or earlier version).
		lookup, indirect, names = s.lookupFunction, false, []string{
			"runtime.goexit",
			"runtime.mstart",
			"runtime.mcall",
			"runtime.morestack",
			"runtime.lessstack",
			"_rt0_go",
		}
	} else {
		// Look for a Go 1.4 binary (or later version).
		lookup, indirect, names = s.lookupVariable, true, []string{
			"runtime.goexitPC",
			"runtime.mstartPC",
			"runtime.mcallPC",
			"runtime.morestackPC",
			"runtime.rt0_goPC",
		}
	}
	// TODO: also look for runtime.externalthreadhandlerp, on Windows.

	addrs := make([]uint64, 0, len(names))
	for _, name := range names {
		addr, err := lookup(name)
		if err != nil {
			return err
		}
		addrs = append(addrs, addr)
	}

	if indirect {
		buf := make([]byte, s.arch.PointerSize)
		for i, addr := range addrs {
			if err := s.ptracePeek(s.stoppedPid, uintptr(addr), buf); err != nil {
				return fmt.Errorf("ptracePeek: %v", err)
			}
			addrs[i] = s.arch.Uintptr(buf)
		}
	}

	s.topOfStackAddrs = addrs
	return nil
}

// topOfStack is the out-of-process equivalent of runtime·topofstack.
func (s *Server) topOfStack(funcEntry uint64) bool {
	for _, addr := range s.topOfStackAddrs {
		if addr == funcEntry {
			return true
		}
	}
	return false
}

func (s *Server) VarByName(req *proxyrpc.VarByNameRequest, resp *proxyrpc.VarByNameResponse) error {
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleVarByName(req *proxyrpc.VarByNameRequest, resp *proxyrpc.VarByNameResponse) error {
	entry, err := s.dwarfData.LookupEntry(req.Name)
	if err != nil {
		return fmt.Errorf("variable %s: %s", req.Name, err)
	}

	loc, err := s.dwarfData.EntryLocation(entry)
	if err != nil {
		return fmt.Errorf("variable %s: %s", req.Name, err)
	}

	off, err := s.dwarfData.EntryTypeOffset(entry)
	if err != nil {
		return fmt.Errorf("variable %s: %s", req.Name, err)
	}

	resp.Var.TypeID = uint64(off)
	resp.Var.Address = loc
	return nil
}

func (s *Server) Value(req *proxyrpc.ValueRequest, resp *proxyrpc.ValueResponse) error {
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleValue(req *proxyrpc.ValueRequest, resp *proxyrpc.ValueResponse) error {
	t, err := s.dwarfData.Type(dwarf.Offset(req.Var.TypeID))
	if err != nil {
		return err
	}
	resp.Value, err = s.value(t, req.Var.Address)
	return err
}

func (s *Server) MapElement(req *proxyrpc.MapElementRequest, resp *proxyrpc.MapElementResponse) error {
	return s.call(s.otherc, req, resp)
}

func (s *Server) handleMapElement(req *proxyrpc.MapElementRequest, resp *proxyrpc.MapElementResponse) error {
	t, err := s.dwarfData.Type(dwarf.Offset(req.Map.TypeID))
	if err != nil {
		return err
	}
	m, ok := t.(*dwarf.MapType)
	if !ok {
		return fmt.Errorf("variable is not a map")
	}
	var count uint64
	// fn will be called for each element of the map.
	// When we reach the requested element, we fill in *resp and stop.
	// TODO: cache locations of elements.
	fn := func(keyAddr, valAddr uint64, keyType, valType dwarf.Type) bool {
		count++
		if count == req.Index+1 {
			resp.Key = program.Var{TypeID: uint64(keyType.Common().Offset), Address: keyAddr}
			resp.Value = program.Var{TypeID: uint64(valType.Common().Offset), Address: valAddr}
			return false
		}
		return true
	}
	if err := s.peekMapValues(m, req.Map.Address, fn); err != nil {
		return err
	}
	if count <= req.Index {
		// There weren't enough elements.
		return fmt.Errorf("map has no element %d", req.Index)
	}
	return nil
}

func (s *Server) Goroutines(req *proxyrpc.GoroutinesRequest, resp *proxyrpc.GoroutinesResponse) error {
	return s.call(s.otherc, req, resp)
}

const invalidStatus program.GoroutineStatus = 99

var (
	gStatus = [...]program.GoroutineStatus{
		0: program.Queued,  // _Gidle
		1: program.Queued,  // _Grunnable
		2: program.Running, // _Grunning
		3: program.Blocked, // _Gsyscall
		4: program.Blocked, // _Gwaiting
		5: invalidStatus,   // _Gmoribund_unused
		6: invalidStatus,   // _Gdead
		7: invalidStatus,   // _Genqueue
		8: program.Running, // _Gcopystack
	}
	gScanStatus = [...]program.GoroutineStatus{
		0: invalidStatus,   // _Gscan + _Gidle
		1: program.Queued,  // _Gscanrunnable
		2: program.Running, // _Gscanrunning
		3: program.Blocked, // _Gscansyscall
		4: program.Blocked, // _Gscanwaiting
		5: invalidStatus,   // _Gscan + _Gmoribund_unused
		6: invalidStatus,   // _Gscan + _Gdead
		7: program.Queued,  // _Gscanenqueue
	}
	gStatusString = [...]string{
		0: "idle",
		1: "runnable",
		2: "running",
		3: "syscall",
		4: "waiting",
		8: "copystack",
	}
	gScanStatusString = [...]string{
		1: "scanrunnable",
		2: "scanrunning",
		3: "scansyscall",
		4: "scanwaiting",
		7: "scanenqueue",
	}
)

func (s *Server) handleGoroutines(req *proxyrpc.GoroutinesRequest, resp *proxyrpc.GoroutinesResponse) error {
	// Get DWARF type information for runtime.g.
	ge, err := s.dwarfData.LookupEntry("runtime.g")
	if err != nil {
		return err
	}
	t, err := s.dwarfData.Type(ge.Offset)
	if err != nil {
		return err
	}
	gType, ok := followTypedefs(t).(*dwarf.StructType)
	if !ok {
		return errors.New("runtime.g is not a struct")
	}

	// Read runtime.allg.
	allgEntry, err := s.dwarfData.LookupEntry("runtime.allg")
	if err != nil {
		return err
	}
	allgAddr, err := s.dwarfData.EntryLocation(allgEntry)
	if err != nil {
		return err
	}
	allg, err := s.peekPtr(allgAddr)
	if err != nil {
		return fmt.Errorf("reading allg: %v", err)
	}

	// Read runtime.allglen.
	allglenEntry, err := s.dwarfData.LookupEntry("runtime.allglen")
	if err != nil {
		return err
	}
	off, err := s.dwarfData.EntryTypeOffset(allglenEntry)
	if err != nil {
		return err
	}
	allglenType, err := s.dwarfData.Type(off)
	if err != nil {
		return err
	}
	allglenAddr, err := s.dwarfData.EntryLocation(allglenEntry)
	if err != nil {
		return err
	}
	var allglen uint64
	switch followTypedefs(allglenType).(type) {
	case *dwarf.UintType, *dwarf.IntType:
		allglen, err = s.peekUint(allglenAddr, allglenType.Common().ByteSize)
		if err != nil {
			return fmt.Errorf("reading allglen: %v", err)
		}
	default:
		// Some runtimes don't specify the type for allglen.  Assume it's uint32.
		allglen, err = s.peekUint(allglenAddr, 4)
		if err != nil {
			return fmt.Errorf("reading allglen: %v", err)
		}
		if allglen != 0 {
			break
		}
		// Zero?  Let's try uint64.
		allglen, err = s.peekUint(allglenAddr, 8)
		if err != nil {
			return fmt.Errorf("reading allglen: %v", err)
		}
	}

	// Initialize s.goroutineStack.
	s.goroutineStackOnce.Do(func() { s.goroutineStackInit(gType) })

	for i := uint64(0); i < allglen; i++ {
		// allg is an array of pointers to g structs.  Read allg[i].
		g, err := s.peekPtr(allg + i*uint64(s.arch.PointerSize))
		if err != nil {
			return err
		}
		gr := program.Goroutine{}

		// Read status from the field named "atomicstatus" or "status".
		status, err := s.peekUintStructField(gType, g, "atomicstatus")
		if err != nil {
			status, err = s.peekUintOrIntStructField(gType, g, "status")
		}
		if err != nil {
			return err
		}
		if status == 6 {
			// _Gdead.
			continue
		}
		gr.Status = invalidStatus
		if status < uint64(len(gStatus)) {
			gr.Status = gStatus[status]
			gr.StatusString = gStatusString[status]
		} else if status^0x1000 < uint64(len(gScanStatus)) {
			gr.Status = gScanStatus[status^0x1000]
			gr.StatusString = gScanStatusString[status^0x1000]
		}
		if gr.Status == invalidStatus {
			return fmt.Errorf("unexpected goroutine status 0x%x", status)
		}
		if status == 4 || status == 0x1004 {
			// _Gwaiting or _Gscanwaiting.
			// Try reading waitreason to get a better value for StatusString.
			// Depending on the runtime, waitreason may be a Go string or a C string.
			if waitreason, err := s.peekStringStructField(gType, g, "waitreason", 80); err == nil {
				if waitreason != "" {
					gr.StatusString = waitreason
				}
			} else if ptr, err := s.peekPtrStructField(gType, g, "waitreason"); err == nil {
				waitreason := s.peekCString(ptr, 80)
				if waitreason != "" {
					gr.StatusString = waitreason
				}
			}
		}

		gr.ID, err = s.peekIntStructField(gType, g, "goid")
		if err != nil {
			return err
		}

		// Best-effort attempt to get the names of the goroutine function and the
		// function that created the goroutine.  They aren't always available.
		functionName := func(pc uint64) string {
			entry, _, err := s.dwarfData.EntryForPC(pc)
			if err != nil {
				return ""
			}
			name, _ := entry.Val(dwarf.AttrName).(string)
			return name
		}
		if startpc, err := s.peekUintStructField(gType, g, "startpc"); err == nil {
			gr.Function = functionName(startpc)
		}
		if gopc, err := s.peekUintStructField(gType, g, "gopc"); err == nil {
			gr.Caller = functionName(gopc)
		}
		if gr.Status != program.Running {
			// TODO: running goroutines too.
			gr.StackFrames, _ = s.goroutineStack(g)
		}

		resp.Goroutines = append(resp.Goroutines, &gr)
	}

	return nil
}

// TODO: let users specify how many frames they want.  10 will be enough to
// determine the reason a goroutine is blocked.
const goroutineStackFrameCount = 10

// goroutineStackInit initializes s.goroutineStack.
func (s *Server) goroutineStackInit(gType *dwarf.StructType) {
	// If we fail to read the DWARF data needed for s.goroutineStack, calling it
	// will always return the error that occurred during initialization.
	var err error // err is captured by the func below.
	s.goroutineStack = func(gAddr uint64) ([]program.Frame, error) {
		return nil, err
	}

	// Get g field "sched", which contains fields pc and sp.
	schedField, err := getField(gType, "sched")
	if err != nil {
		return
	}
	schedOffset := uint64(schedField.ByteOffset)
	schedType, ok := followTypedefs(schedField.Type).(*dwarf.StructType)
	if !ok {
		err = errors.New(`g field "sched" has the wrong type`)
		return
	}

	// Get the size of the pc and sp fields and their offsets inside the g struct,
	// so we can quickly peek those values for each goroutine later.
	var (
		schedPCOffset, schedSPOffset     uint64
		schedPCByteSize, schedSPByteSize int64
	)
	for _, x := range []struct {
		field    string
		offset   *uint64
		bytesize *int64
	}{
		{"pc", &schedPCOffset, &schedPCByteSize},
		{"sp", &schedSPOffset, &schedSPByteSize},
	} {
		var f *dwarf.StructField
		f, err = getField(schedType, x.field)
		if err != nil {
			return
		}
		*x.offset = schedOffset + uint64(f.ByteOffset)
		switch t := followTypedefs(f.Type).(type) {
		case *dwarf.UintType, *dwarf.IntType:
			*x.bytesize = t.Common().ByteSize
		default:
			err = fmt.Errorf("gobuf field %q has the wrong type", x.field)
			return
		}
	}

	s.goroutineStack = func(gAddr uint64) ([]program.Frame, error) {
		schedPC, err := s.peekUint(gAddr+schedPCOffset, schedPCByteSize)
		if err != nil {
			return nil, err
		}
		schedSP, err := s.peekUint(gAddr+schedSPOffset, schedSPByteSize)
		if err != nil {
			return nil, err
		}
		return s.walkStack(schedPC, schedSP, goroutineStackFrameCount)
	}
}
