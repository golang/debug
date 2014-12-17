// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package proxyrpc defines the types used to represent the RPC calls
// used to the ogleproxy.
package proxyrpc // import "golang.org/x/debug/ogle/program/proxyrpc"

import "golang.org/x/debug/ogle/program"

// For regularity, each method has a unique Request and a Response type even
// when not strictly necessary.

// File I/O, at the top because they're simple.

type ReadAtRequest struct {
	FD     int
	Len    int
	Offset int64
}

type ReadAtResponse struct {
	Data []byte
}

type WriteAtRequest struct {
	FD     int
	Data   []byte
	Offset int64
}

type WriteAtResponse struct {
	Len int
}

type CloseRequest struct {
	FD int
}

type CloseResponse struct {
}

// Program methods.

type OpenRequest struct {
	Name string
	Mode string
}

type OpenResponse struct {
	FD int
}

type RunRequest struct {
}

type RunResponse struct {
	Status program.Status
}

type ResumeRequest struct {
}

type ResumeResponse struct {
	Status program.Status
}

type BreakpointRequest struct {
	Address string
}

type BreakpointResponse struct {
	PCs []uint64
}

type EvalRequest struct {
	Expr string
}

type EvalResponse struct {
	Result []string
}

type FramesRequest struct {
	Count int
}

type FramesResponse struct {
	Frames []program.Frame
}
