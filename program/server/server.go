// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package server provides RPC access to a local program being debugged.
// It is the remote end of the client implementation of the Program interface.
package server

import (
	"fmt"
	"os"

	"debug/elf"
	"debug/macho"
	"debug/pe"

	"code.google.com/p/ogle/gosym"
	"code.google.com/p/ogle/program"
	"code.google.com/p/ogle/program/proxyrpc"
)

type Server struct {
	executable string // Name of executable.
	lines      *gosym.LineTable
	symbols    *gosym.Table
	// TODO: Needs mutex.
	files []*file // Index == file descriptor.
}

// New parses the executable and builds local data structures for answering requests.
// It returns a Server ready to serve requests about the executable.
func New(executable string) (*Server, error) {
	fd, err := os.Open(executable)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	textStart, symtab, pclntab, err := loadTables(fd)
	if err != nil {
		return nil, err
	}
	lines := gosym.NewLineTable(pclntab, textStart)
	symbols, err := gosym.NewTable(symtab, lines)
	if err != nil {
		return nil, err
	}
	srv := &Server{
		executable: executable,
		lines:      lines,
		symbols:    symbols,
	}
	return srv, nil
}

// This function is copied from $GOROOT/src/cmd/addr2line/main.go.
// TODO: Make this architecture-defined? Push into gosym?
// TODO: Why is the .gosymtab always empty?
func loadTables(f *os.File) (textStart uint64, symtab, pclntab []byte, err error) {
	if obj, err := elf.NewFile(f); err == nil {
		if sect := obj.Section(".text"); sect != nil {
			textStart = sect.Addr
		}
		if sect := obj.Section(".gosymtab"); sect != nil {
			if symtab, err = sect.Data(); err != nil {
				return 0, nil, nil, err
			}
		}
		if sect := obj.Section(".gopclntab"); sect != nil {
			if pclntab, err = sect.Data(); err != nil {
				return 0, nil, nil, err
			}
		}
		return textStart, symtab, pclntab, nil
	}

	if obj, err := macho.NewFile(f); err == nil {
		if sect := obj.Section("__text"); sect != nil {
			textStart = sect.Addr
		}
		if sect := obj.Section("__gosymtab"); sect != nil {
			if symtab, err = sect.Data(); err != nil {
				return 0, nil, nil, err
			}
		}
		if sect := obj.Section("__gopclntab"); sect != nil {
			if pclntab, err = sect.Data(); err != nil {
				return 0, nil, nil, err
			}
		}
		return textStart, symtab, pclntab, nil
	}

	if obj, err := pe.NewFile(f); err == nil {
		if sect := obj.Section(".text"); sect != nil {
			textStart = uint64(sect.VirtualAddress)
		}
		if sect := obj.Section(".gosymtab"); sect != nil {
			if symtab, err = sect.Data(); err != nil {
				return 0, nil, nil, err
			}
		}
		if sect := obj.Section(".gopclntab"); sect != nil {
			if pclntab, err = sect.Data(); err != nil {
				return 0, nil, nil, err
			}
		}
		return textStart, symtab, pclntab, nil
	}

	return 0, nil, nil, fmt.Errorf("unrecognized binary format")
}

type file struct {
	mode  string
	index int
	f     program.File
}

func (s *Server) Open(req *proxyrpc.OpenRequest, resp *proxyrpc.OpenResponse) error {
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
	fd := req.FD
	if fd < 0 || fd >= len(s.files) || s.files[fd] == nil {
		return fmt.Errorf("Close: bad file descriptor %d", fd)
	}
	err := s.files[fd].f.Close()
	// Remove it regardless
	s.files[fd] = nil
	return err
}

func (s *Server) Eval(req *proxyrpc.EvalRequest, resp *proxyrpc.EvalResponse) error {
	expr := req.Expr
	// Simple version: Turn symbol into address. Must be function.
	// TODO: Why is Table.Syms always empty?
	sym := s.symbols.LookupFunc(expr)
	if sym == nil {
		return fmt.Errorf("symbol %q not found")
	}
	resp.Result = fmt.Sprintf("%#x", sym.Value)
	return nil
}
