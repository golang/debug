// Copyright 2009 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dwarf_test

// Stripped-down, simplified version of ../../gosym/pclntab_test.go

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "golang.org/x/debug/dwarf"
	"golang.org/x/debug/elf"
	"golang.org/x/debug/macho"
)

var (
	pclineTempDir    string
	pclinetestBinary string
)

func dotest(self bool) bool {
	// For now, only works on amd64 platforms.
	if runtime.GOARCH != "amd64" {
		return false
	}
	// Self test reads test binary; only works on Linux or Mac.
	if self {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			return false
		}
	}
	// Command below expects "sh", so Unix.
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		return false
	}
	if pclinetestBinary != "" {
		return true
	}
	var err error
	pclineTempDir, err = ioutil.TempDir("", "pclinetest")
	if err != nil {
		panic(err)
	}
	if strings.Contains(pclineTempDir, " ") {
		panic("unexpected space in tempdir")
	}
	// This command builds pclinetest from ../../gosym/pclinetest.asm;
	// the resulting binary looks like it was built from pclinetest.s,
	// but we have renamed it to keep it away from the go tool.
	pclinetestBinary = filepath.Join(pclineTempDir, "pclinetest")
	command := fmt.Sprintf("go tool asm -o %s.6 ../gosym/pclinetest.asm && go tool link -H %s -E main -o %s %s.6",
		pclinetestBinary, runtime.GOOS, pclinetestBinary, pclinetestBinary)
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic(err)
	}
	return true
}

func endtest() {
	if pclineTempDir != "" {
		os.RemoveAll(pclineTempDir)
		pclineTempDir = ""
		pclinetestBinary = ""
	}
}

func getData(file string) (*Data, error) {
	switch runtime.GOOS {
	case "linux":
		f, err := elf.Open(file)
		if err != nil {
			return nil, err
		}
		dwarf, err := f.DWARF()
		if err != nil {
			return nil, err
		}
		f.Close()
		return dwarf, nil
	case "darwin":
		f, err := macho.Open(file)
		if err != nil {
			return nil, err
		}
		dwarf, err := f.DWARF()
		if err != nil {
			return nil, err
		}
		f.Close()
		return dwarf, nil
	}
	panic("unimplemented DWARF for GOOS=" + runtime.GOOS)
}

func TestPCToLine(t *testing.T) {
	if !dotest(false) {
		return
	}
	defer endtest()

	data, err := getData(pclinetestBinary)
	if err != nil {
		t.Fatal(err)
	}

	// Test PCToLine.
	// TODO: Do much more than this.
	pc, err := data.LookupFunction("linefrompc")
	if err != nil {
		t.Fatal(err)
	}
	file, line, err := data.PCToLine(pc)
	if err != nil {
		t.Fatal(err)
	}
	// We expect <longpath>/pclinetest.asm, line 13.
	if !strings.HasSuffix(file, "/pclinetest.asm") {
		t.Errorf("got %s; want %s", file, ".../pclinetest.asm")
	}
	if line != 13 {
		t.Errorf("got %d; want %d", line, 13)
	}
}
