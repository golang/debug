// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// debugproxy connects to the target binary, and serves an RPC interface using
// the types in server/protocol to access and control it.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/rpc"
	"os"

	"golang.org/x/debug/server"
)

var (
	textFlag = flag.String("text", "", "file name of binary being debugged")
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("debugproxy: ")
	flag.Parse()
	if *textFlag == "" {
		flag.Usage()
		os.Exit(2)
	}
	s, err := server.New(*textFlag)
	if err != nil {
		fmt.Printf("server.New: %v\n", err)
		os.Exit(2)
	}
	err = rpc.Register(s)
	if err != nil {
		fmt.Printf("rpc.Register: %v\n", err)
		os.Exit(2)
	}
	fmt.Println("OK")
	log.Print("starting server")
	rpc.ServeConn(&rwc{
		os.Stdin,
		os.Stdout,
	})
	log.Print("server finished")
}

// rwc creates a single io.ReadWriteCloser from a read side and a write side.
// It allows us to do RPC using standard in and standard out.
type rwc struct {
	r *os.File
	w *os.File
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
	if rerr != nil {
		return rerr
	}
	return werr
}
