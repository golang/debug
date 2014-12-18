// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Demo program that starts another program and calls Ogle library functions
// to debug it.

package main

import (
	"fmt"
	"log"

	"golang.org/x/debug/ogle/program/client"
)

func main() {
	prog, err := client.Run("localhost", "bin/tracee")
	if err != nil {
		log.Fatalf("Run: %v", err)
	}

	_, err = prog.Run()
	if err != nil {
		log.Fatalf("Run: %v", err)
	}

	pcs, err := prog.Breakpoint("re:main.foo")
	if err != nil {
		log.Fatalf("Breakpoint: %v", err)
	}
	fmt.Printf("breakpoints set at %x\n", pcs)

	_, err = prog.Resume()
	if err != nil {
		log.Fatalf("Resume: %v", err)
	}

	frames, err := prog.Frames(100)
	if err != nil {
		log.Printf("prog.Frames error: %v", err)
	}
	fmt.Printf("%#v\n", frames)

	varnames, err := prog.Eval(`re:main\.Z_.*`)
	if err != nil {
		log.Printf("prog.Eval error: %v", err)
	}

	for _, v := range varnames {
		val, err := prog.Eval("val:" + v)
		if err != nil {
			log.Printf("prog.Eval error for %s: %v", v, err)
		} else {
			fmt.Printf("%s = %v\n", v, val)
		}
	}
}
