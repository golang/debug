// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package proxyrpc defines the types used to represent the RPC calls
// used to the ogleproxy.
package proxyrpc

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
