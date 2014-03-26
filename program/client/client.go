// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package client provides remote access to an ogle proxy.
package client

import (
	"errors"
	"fmt"
	"io"
	"net/rpc"
	"os"
	"os/exec"

	"code.google.com/p/ogle/program"
	"code.google.com/p/ogle/program/proxyrpc"
)

var _ program.Program = (*Program)(nil)
var _ program.File = (*File)(nil)

// New connects to the specified host using SSH, starts an ogle proxy
// there, and creates a new program from the specified file with the specified
// arguments, which include the program name the first argument.
// The program is created but stops before executing the first instruction,
// ready for debugging.
func New(host string, textFile string, args ...string) (*Program, error) {
	panic("unimplemented")
}

// Run connects to the specified host using SSH, starts an ogle proxy
// there, and runs a new program from the specified file with the specified
// arguments, which include the program name the first argument.
// It is similar to New except that the program is allowed to run.
func Run(host string, textFile string, args ...string) (*Program, error) {
	// TODO: add args.
	cmd := exec.Command("/usr/bin/ssh", host, "ogleproxy", "-text", textFile)
	stdin, toStdin, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	fromStdout, stdout, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr // Stderr from proxy appears on our stderr.
	err = cmd.Start()
	if err != nil {
		return nil, err
	}
	stdout.Close()
	// Read back one line. It must start "OGLE" and we hope says "OK".
	msg, err := readLine(fromStdout)
	if err != nil {
		return nil, err
	}
	switch msg {
	case "OGLE BAD":
		// Error is on next line.
		msg, err = readLine(fromStdout)
		if err == nil {
			err = errors.New(msg)
		}
		return nil, err
	case "OGLE OK":
	default:
		// Communication error.
		return nil, fmt.Errorf("unrecognized message %q", msg)
	}
	program := &Program{
		client: rpc.NewClient(&rwc{
			ssh: cmd,
			r:   fromStdout,
			w:   toStdin,
		}),
	}
	return program, nil
}

// readLine reads one line of text from the reader. It does no buffering.
// The trailing newline is read but not returned.
func readLine(r io.Reader) (string, error) {
	b := make([]byte, 0, 10)
	var c [1]byte
	for {
		_, err := io.ReadFull(r, c[:])
		if err != nil {
			return "", err
		}
		if c[0] == '\n' {
			break
		}
		b = append(b, c[0])
	}
	return string(b), nil
}

// rwc creates a single io.ReadWriteCloser from a read side and a write side.
// It also holds the command object so we can wait for SSH to complete.
// It allows us to do RPC over an SSH connection.
type rwc struct {
	ssh *exec.Cmd
	r   *os.File
	w   *os.File
}

func (rwc *rwc) Read(p []byte) (int, error) {
	return rwc.r.Read(p)
}

func (rwc *rwc) Write(p []byte) (int, error) {
	return rwc.w.Write(p)
}

func (rwc *rwc) Close() error {
	rerr := rwc.r.Close()
	werr := rwc.w.Close()
	cerr := rwc.ssh.Wait()
	if cerr != nil {
		// Wait exit status is most important.
		return cerr
	}
	if rerr != nil {
		return rerr
	}
	return werr
}

// Program implements the similarly named ogle interface.
// Through that interface it provides access to a program being
// debugged on a possibly remote machine by communicating
// with an ogle proxy adjacent to the target program.
type Program struct {
	client *rpc.Client
}

func (p *Program) Open(name string, mode string) (program.File, error) {
	req := proxyrpc.OpenRequest{
		Name: name,
		Mode: mode,
	}
	var resp proxyrpc.OpenResponse
	err := p.client.Call("Server.Open", &req, &resp)
	if err != nil {
		return nil, err
	}
	f := &File{
		prog: p,
		fd:   resp.FD,
	}
	return f, nil
}

func (p *Program) SetArguments(args ...string) {
	panic("unimplemented")
}

func (p *Program) Run(start bool) (program.Status, error) {
	req := proxyrpc.RunRequest{
		Start: start,
	}
	var resp proxyrpc.RunResponse
	err := p.client.Call("Server.Run", &req, &resp)
	if err != nil {
		return program.Status{}, err
	}
	return resp.Status, nil
}

func (p *Program) Stop() (program.Status, error) {
	panic("unimplemented")
}

func (p *Program) Resume() (program.Status, error) {
	req := proxyrpc.ResumeRequest{}
	var resp proxyrpc.ResumeResponse
	err := p.client.Call("Server.Resume", &req, &resp)
	if err != nil {
		return program.Status{}, err
	}
	return resp.Status, nil
}

func (p *Program) Kill() (program.Status, error) {
	panic("unimplemented")
}

func (p *Program) Breakpoint(address string) error {
	req := proxyrpc.BreakpointRequest{
		Address: address,
	}
	var resp proxyrpc.BreakpointResponse
	return p.client.Call("Server.Breakpoint", &req, &resp)
}

func (p *Program) DeleteBreakpoint(address string) error {
	panic("unimplemented")
}

func (p *Program) Eval(expr string) ([]string, error) {
	req := proxyrpc.EvalRequest{
		Expr: expr,
	}
	var resp proxyrpc.EvalResponse
	err := p.client.Call("Server.Eval", &req, &resp)
	return resp.Result, err
}

// File implements the program.File interface, providing access
// to file-like resources associated with the target program.
type File struct {
	prog *Program // The Program associated with the file.
	fd   int      // File descriptor.
}

func (f *File) ReadAt(p []byte, offset int64) (int, error) {
	req := proxyrpc.ReadAtRequest{
		FD:     f.fd,
		Len:    len(p),
		Offset: offset,
	}
	var resp proxyrpc.ReadAtResponse
	err := f.prog.client.Call("Server.ReadAt", &req, &resp)
	return copy(p, resp.Data), err
}

func (f *File) WriteAt(p []byte, offset int64) (int, error) {
	req := proxyrpc.WriteAtRequest{
		FD:     f.fd,
		Data:   p,
		Offset: offset,
	}
	var resp proxyrpc.WriteAtResponse
	err := f.prog.client.Call("Server.WriteAt", &req, &resp)
	return resp.Len, err
}

func (f *File) Close() error {
	req := proxyrpc.CloseRequest{
		FD: f.fd,
	}
	var resp proxyrpc.CloseResponse
	err := f.prog.client.Call("Server.Close", &req, &resp)
	return err
}
