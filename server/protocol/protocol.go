// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package protocol defines the types used to represent calls to the debug server.
package protocol // import "golang.org/x/debug/server/protocol"

import (
	"encoding/gob"

	"golang.org/x/debug"
)

func init() {
	// Register implementations of debug.Value with gob.
	gob.Register(debug.Pointer{})
	gob.Register(debug.Array{})
	gob.Register(debug.Struct{})
	gob.Register(debug.Slice{})
	gob.Register(debug.Map{})
	gob.Register(debug.String{})
	gob.Register(debug.Channel{})
	gob.Register(debug.Func{})
	gob.Register(debug.Interface{})
}

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
	Args []string
}

type RunResponse struct {
	Status debug.Status
}

type ResumeRequest struct {
}

type ResumeResponse struct {
	Status debug.Status
}

type BreakpointRequest struct {
	Address uint64
}

type BreakpointAtFunctionRequest struct {
	Function string
}

type BreakpointAtLineRequest struct {
	File string
	Line uint64
}

type BreakpointResponse struct {
	PCs []uint64
}

type DeleteBreakpointsRequest struct {
	PCs []uint64
}

type DeleteBreakpointsResponse struct {
}

type EvalRequest struct {
	Expr string
}

type EvalResponse struct {
	Result []string
}

type EvaluateRequest struct {
	Expression string
}

type EvaluateResponse struct {
	Result debug.Value
}

type FramesRequest struct {
	Count int
}

type FramesResponse struct {
	Frames []debug.Frame
}

type VarByNameRequest struct {
	Name string
}

type VarByNameResponse struct {
	Var debug.Var
}

type ValueRequest struct {
	Var debug.Var
}

type ValueResponse struct {
	Value debug.Value
}

type MapElementRequest struct {
	Map   debug.Map
	Index uint64
}

type MapElementResponse struct {
	Key   debug.Var
	Value debug.Var
}

type GoroutinesRequest struct {
}

type GoroutinesResponse struct {
	Goroutines []*debug.Goroutine
}
