// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package local provides access to a local program.
package local // import "golang.org/x/debug/ogle/program/local"

import (
	"golang.org/x/debug/ogle/program"
	"golang.org/x/debug/ogle/program/proxyrpc"
	"golang.org/x/debug/ogle/program/server"
)

var _ program.Program = (*Local)(nil)
var _ program.File = (*File)(nil)

// Local implements the interface program.Program.
// Through that interface it provides access to a program being debugged.
type Local struct {
	s *server.Server
}

// New creates a new program from the specified file.
// The program can then be started by the Run method.
func New(textFile string) (*Local, error) {
	s, err := server.New(textFile)
	return &Local{s: s}, err
}

func (l *Local) Open(name string, mode string) (program.File, error) {
	req := proxyrpc.OpenRequest{
		Name: name,
		Mode: mode,
	}
	var resp proxyrpc.OpenResponse
	err := l.s.Open(&req, &resp)
	if err != nil {
		return nil, err
	}
	f := &File{
		prog: l,
		fd:   resp.FD,
	}
	return f, nil
}

func (l *Local) Run(args ...string) (program.Status, error) {
	req := proxyrpc.RunRequest{args}
	var resp proxyrpc.RunResponse
	err := l.s.Run(&req, &resp)
	if err != nil {
		return program.Status{}, err
	}
	return resp.Status, nil
}

func (l *Local) Stop() (program.Status, error) {
	panic("unimplemented")
}

func (l *Local) Resume() (program.Status, error) {
	req := proxyrpc.ResumeRequest{}
	var resp proxyrpc.ResumeResponse
	err := l.s.Resume(&req, &resp)
	if err != nil {
		return program.Status{}, err
	}
	return resp.Status, nil
}

func (l *Local) Kill() (program.Status, error) {
	panic("unimplemented")
}

func (l *Local) Breakpoint(address uint64) ([]uint64, error) {
	req := proxyrpc.BreakpointRequest{address}
	var resp proxyrpc.BreakpointResponse
	err := l.s.Breakpoint(&req, &resp)
	return resp.PCs, err
}

func (l *Local) BreakpointAtFunction(name string) ([]uint64, error) {
	req := proxyrpc.BreakpointAtFunctionRequest{name}
	var resp proxyrpc.BreakpointResponse
	err := l.s.BreakpointAtFunction(&req, &resp)
	return resp.PCs, err
}

func (l *Local) BreakpointAtLine(file string, line uint64) ([]uint64, error) {
	req := proxyrpc.BreakpointAtLineRequest{file, line}
	var resp proxyrpc.BreakpointResponse
	err := l.s.BreakpointAtLine(&req, &resp)
	return resp.PCs, err
}

func (l *Local) DeleteBreakpoints(pcs []uint64) error {
	req := proxyrpc.DeleteBreakpointsRequest{PCs: pcs}
	var resp proxyrpc.DeleteBreakpointsResponse
	return l.s.DeleteBreakpoints(&req, &resp)
}

func (l *Local) Eval(expr string) ([]string, error) {
	req := proxyrpc.EvalRequest{
		Expr: expr,
	}
	var resp proxyrpc.EvalResponse
	err := l.s.Eval(&req, &resp)
	return resp.Result, err
}

func (l *Local) Evaluate(e string) (program.Value, error) {
	req := proxyrpc.EvaluateRequest{
		Expression: e,
	}
	var resp proxyrpc.EvaluateResponse
	err := l.s.Evaluate(&req, &resp)
	return resp.Result, err
}

func (l *Local) Frames(count int) ([]program.Frame, error) {
	req := proxyrpc.FramesRequest{
		Count: count,
	}
	var resp proxyrpc.FramesResponse
	err := l.s.Frames(&req, &resp)
	return resp.Frames, err
}

func (l *Local) VarByName(name string) (program.Var, error) {
	req := proxyrpc.VarByNameRequest{Name: name}
	var resp proxyrpc.VarByNameResponse
	err := l.s.VarByName(&req, &resp)
	return resp.Var, err
}

func (l *Local) Value(v program.Var) (program.Value, error) {
	req := proxyrpc.ValueRequest{Var: v}
	var resp proxyrpc.ValueResponse
	err := l.s.Value(&req, &resp)
	return resp.Value, err
}

func (l *Local) MapElement(m program.Map, index uint64) (program.Var, program.Var, error) {
	req := proxyrpc.MapElementRequest{Map: m, Index: index}
	var resp proxyrpc.MapElementResponse
	err := l.s.MapElement(&req, &resp)
	return resp.Key, resp.Value, err
}

// File implements the program.File interface, providing access
// to file-like resources associated with the target program.
type File struct {
	prog *Local // The Program associated with the file.
	fd   int    // File descriptor.
}

func (f *File) ReadAt(p []byte, offset int64) (int, error) {
	req := proxyrpc.ReadAtRequest{
		FD:     f.fd,
		Len:    len(p),
		Offset: offset,
	}
	var resp proxyrpc.ReadAtResponse
	err := f.prog.s.ReadAt(&req, &resp)
	return copy(p, resp.Data), err
}

func (f *File) WriteAt(p []byte, offset int64) (int, error) {
	panic("unimplemented")
}

func (f *File) Close() error {
	req := proxyrpc.CloseRequest{
		FD: f.fd,
	}
	var resp proxyrpc.CloseResponse
	err := f.prog.s.Close(&req, &resp)
	return err
}
