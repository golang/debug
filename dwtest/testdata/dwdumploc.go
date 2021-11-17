// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

// This file is the source code for a test helper binary 'dwdumploc',
// which is built and then run by the tests in cmd/link/internal/dwtest.
// It is set up to import packages "op" and "proc" from Delve (so
// as to use Delve's DWARF location expression parser), and the
// "dwtest" package from cmd/link/internal/dwtest (for general
// DWARF examination/inspection).
//
// Given an input Go binary and a function name, this program
// visits the DWARF for the function and dumps out the location expressions
// for the function's input parameters on entry to the func. Example:
//
// ./dumpdwloc.exe -m ./dumpdwloc.exe -f main.locateFuncDetails
// 1: in-param "executable" loc="{ [0: S=8 RAX] [1: S=8 RBX] }"
// 2: in-param "fcn" loc="{ [0: S=8 RCX] [1: S=8 RDI] }"
// 3: out-param "~r0" loc="<not available>"
// 4: out-param "~r1" loc="<not available>"
//
// Since location expressions can refer to machine registers, dump
// output for location expressions are architecture-dependent.  The
// dumper tool currently supports two archs: amd64 and arm64.
//

import (
	"debug/dwarf"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"dwdumploc/dwtest"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"

	"github.com/go-delve/delve/pkg/dwarf/op"
	"github.com/go-delve/delve/pkg/proc"
)

var verbflag = flag.Int("v", 0, "Verbose trace output level")
var fcnflag = flag.String("f", "", "name of function to display")
var moduleflag = flag.String("m", "", "load module to read")

func verb(vlevel int, s string, a ...interface{}) {
	if *verbflag >= vlevel {
		fmt.Fprintf(os.Stderr, s, a...)
		fmt.Fprintf(os.Stderr, "\n")
	}
}

func warn(s string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, s, a...)
	fmt.Fprintf(os.Stderr, "\n")
}

func usage(msg string) {
	if len(msg) > 0 {
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	}
	fmt.Fprintf(os.Stderr, "usage: dwdumploc [flags] -m <binary> -f <fcn>\n")
	flag.PrintDefaults()
	os.Exit(2)
}

type finfo struct {
	name     string
	dwOffset dwarf.Offset
	dwLoPC   uint64
	dwHiPC   uint64
	valid    bool
	dwx      *dwtest.Examiner
}

// opener defines an interface for opening DWARF info in a Go binary.
type opener interface {
	// Open examines binary pointed to by 'path', returning a pointer
	// to debug/dwarf.Data for it. If something goes wrong opening the
	// binary (file missing, or maybe not a supported binary) a
	// suitable error will be returned in the first error return. If
	// something goes wrong reading the DWARF, a suitable error will
	// be returned in the second error return.
	Open(path string) (*dwarf.Data, error, error)
	Which() string
}

// Remark: the boilerplate for three scenarios below (Elf, Macho, and
// PE) seems like it should be an ideal scenario where you could use
// generics, but my attempts at this were not especially successful.
// The sticking point is that each of elf.Open/macho.Open etc return a
// different thing, and I couldn't quite figure out how to get this to
// work with generics. TODO: make another attempt.

// elfOpener implements the opener interface for ELF
// (https://en.wikipedia.org/wiki/Executable_and_Linkable_Format) binaries
//
type elfOpener struct {
}

func (eo *elfOpener) Open(path string) (*dwarf.Data, error, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err, nil
	}
	d, err := f.DWARF()
	if err != nil {
		return nil, nil, err
	}
	return d, nil, nil
}

func (eo *elfOpener) Which() string {
	return "elf"
}

// machoOpener implements the opener for macos/darwin Macho-O
// (https://en.wikipedia.org/wiki/Mach-O) binaries.
type machoOpener struct {
}

func (mo *machoOpener) Open(path string) (*dwarf.Data, error, error) {
	f, err := macho.Open(path)
	if err != nil {
		return nil, err, nil
	}
	d, err := f.DWARF()
	if err != nil {
		return nil, nil, err
	}
	return d, nil, nil
}

func (mo *machoOpener) Which() string {
	return "macho"
}

// peOpener implements the opener interface for Windows PE
// (https://en.wikipedia.org/wiki/Portable_Executable) binaries.
type peOpener struct {
}

func (po *peOpener) Open(path string) (*dwarf.Data, error, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, err, nil
	}
	d, err := f.DWARF()
	if err != nil {
		return nil, nil, err
	}
	return d, nil, nil
}

func (po *peOpener) Which() string {
	return "pe"
}

// openDwarf tries to open the Go binary 'path' as an ELF file,
// a Macho file, or a PE file in turn. If an open succeeds, it
// returns a dwarf.Data for the binary; if they all fail, it returns
// an error.
func openDwarf(path string) (*dwarf.Data, error) {
	eo := elfOpener{}
	mo := machoOpener{}
	po := peOpener{}
	var eoo opener = &eo
	var moo opener = &mo
	var poo opener = &po
	openers := []opener{eoo, moo, poo}
	postmortem := ""
	for k, o := range openers {
		d, errf, errd := o.Open(path)
		if d != nil {
			return d, nil
		}
		if errd != nil {
			return nil, errd
		}
		postmortem += fmt.Sprintf("\tattempt %d (%s) failed: %v\n", k, o.Which(), errf)
	}
	return nil, fmt.Errorf("init failed:\n%s", postmortem)
}

// locateFuncDetails walks the DWARF for Go binary 'executable'
// looking for a subprogram DIE for function 'fcn'. If it finds a
// function of the right name, it fills in info from the DWARF into
// a "finfo" struct and returns it, or returns an empty struct
// and an error if something went wrong.
func locateFuncDetails(executable string, fcn string) (finfo, error) {
	rrv := finfo{}

	if inf, err := os.Open(executable); err != nil {
		return rrv, fmt.Errorf("unable to open input executable %s: %v", executable, err)
	} else {
		inf.Close()
	}

	verb(1, "loading DWARF for %s", executable)
	d, err := openDwarf(executable)
	if err != nil {
		return finfo{}, err
	}
	verb(1, "DWARF loaded for %s", executable)

	// Construct dwtest.Examiner helper object, to make
	// inspection of the DWARF easier.
	rdr := d.Reader()
	dwx := dwtest.Examiner{}
	if err := dwx.Populate(rdr); err != nil {
		return finfo{}, fmt.Errorf("error reading DWARF: %v", err)
	}

	// Walk DIEs looking for subprogram DIEs.
	dies := dwx.DIEs()
	for idx := 0; idx < len(dies); idx++ {
		die := dies[idx]
		off := die.Offset
		if die.Tag == dwarf.TagCompileUnit {
			if name, ok := die.Val(dwarf.AttrName).(string); ok {
				verb(2, "compilation unit: %s", name)
			}
			continue
		}
		if die.Tag != dwarf.TagSubprogram {
			// TODO: skip children
			continue
		}
		// Name has to match the function we're looking for.
		name, ok := die.Val(dwarf.AttrName).(string)
		if !ok {
			continue
		}
		if name != fcn {
			// TODO: skip children
			continue
		}

		verb(1, "found function %s at offset %x", fcn, off)
		rrv.dwOffset = off
		if lopc, ok := die.Val(dwarf.AttrLowpc).(uint64); ok {
			rrv.dwLoPC = lopc
		} else {
			return finfo{}, fmt.Errorf("target function seems to be missing LowPC attribute")
		}
		if hipc, ok := die.Val(dwarf.AttrHighpc).(uint64); ok {
			rrv.dwHiPC = hipc
		} else {
			return finfo{}, fmt.Errorf("target function seems to be missing HighPC attribute")
		}
		rrv.dwx = &dwx
		rrv.name = fcn
		rrv.valid = true
		return rrv, nil
	}
	return finfo{}, fmt.Errorf("could not locate target function in DWARF")
}

var AMD64DWARFRegisters = map[int]string{
	0:  "RAX",
	1:  "RDX",
	2:  "RCX",
	3:  "RBX",
	4:  "RSI",
	5:  "RDI",
	6:  "RBP",
	7:  "RSP",
	8:  "R8",
	9:  "R9",
	10: "R10",
	11: "R11",
	12: "R12",
	13: "R13",
	14: "R14",
	15: "R15",
	17: "X0",
	18: "X1",
	19: "X2",
	20: "X3",
	21: "X4",
	22: "X5",
	23: "X6",
	24: "X7",
	25: "X8",
	26: "X9",
	27: "X10",
	28: "X11",
	29: "X12",
	30: "X13",
	31: "X14",
	32: "X15",
}

var ARM64DWARFRegisters = map[int]string{
	// int
	0:  "R0",
	1:  "R1",
	2:  "R2",
	3:  "R3",
	4:  "R4",
	5:  "R5",
	6:  "R6",
	7:  "R7",
	8:  "R8",
	9:  "R9",
	10: "R10",
	11: "R11",
	12: "R12",
	13: "R13",
	14: "R14",
	15: "R15",
	16: "R16",
	17: "R17",
	18: "R18",
	19: "R19",
	20: "R20",
	21: "R21",
	22: "R22",
	23: "R23",
	24: "R24",
	25: "R25",
	26: "R26",
	27: "R27",
	28: "R28",
	29: "R29",
	30: "R30",

	// float
	64: "F0",
	65: "F1",
	66: "F2",
	67: "F3",
	68: "F4",
	69: "F5",
	70: "F6",
	71: "F7",
	72: "F8",
	73: "F9",
	74: "F10",
	75: "F11",
	76: "F12",
	77: "F13",
	78: "F14",
	79: "F15",
	80: "F16",
	81: "F17",
	82: "F18",
	83: "F19",
	84: "F20",
	85: "F21",
	86: "F22",
	87: "F23",
	88: "F24",
	89: "F25",
	90: "F26",
	91: "F27",
	92: "F28",
	93: "F29",
	94: "F30",
	95: "F31",
}

func regString(dwreg int) string {
	var v string
	switch runtime.GOARCH {
	case "amd64":
		v = AMD64DWARFRegisters[dwreg]
	case "arm64":
		v = ARM64DWARFRegisters[dwreg]
	default:
		panic(fmt.Sprintf("no support for arch %s", runtime.GOARCH))
	}
	if v != "" {
		return v
	}
	return fmt.Sprintf("reg=%d", dwreg)
}

func pstring(addr int64, pcs []op.Piece, err error) (string, error) {
	if err != nil {
		serr := fmt.Sprintf("%s", err)
		if strings.HasPrefix(serr, "could not find loclist entry") {
			return "<not available>", nil
		}
		return "", err
	}
	if pcs == nil {
		return fmt.Sprintf("addr=%x", addr), nil
	}
	r := "{"
	for k, p := range pcs {
		r += fmt.Sprintf(" [%d: S=%d", k, p.Size)
		if p.Kind == op.RegPiece {
			r += fmt.Sprintf(" %s]", regString(int(p.Val)))
		} else {
			r += fmt.Sprintf(" addr=0x%x]", p.Val)
		}
	}
	r += " }"
	return r, nil
}

// processParams initializes a 'proc.BinaryInfo' object for the binary
// in question and then walks the formal parameters of the selected
// function, invoking the Location method on each param to read its
// location expression. Results are dumped to stdout, and an error is
// returned if something goes wrong.
func processParams(executable string, fi *finfo) error {
	const _cfa = 0x1000
	bi := proc.NewBinaryInfo(runtime.GOOS, runtime.GOARCH)
	if err := bi.LoadBinaryInfo(executable, 0, []string{}); err != nil {
		return err
	}

	// Walk subprogram DIE's children.
	pidx := fi.dwx.IdxFromOffset(fi.dwOffset)
	childDies := fi.dwx.Children(pidx)
	idx := 0
	for _, e := range childDies {
		if e.Tag != dwarf.TagFormalParameter {
			continue
		}
		if e.Val(dwarf.AttrName) == nil {
			continue
		}
		idx++
		name := e.Val(dwarf.AttrName).(string)
		var isrvar bool
		if e.Tag == dwarf.TagFormalParameter {
			isrvar = e.Val(dwarf.AttrVarParam).(bool)
		}
		addr, pieces, _, err := bi.Location(e, dwarf.AttrLocation, fi.dwLoPC, op.DwarfRegisters{CFA: _cfa, FrameBase: _cfa}, nil)
		pdump, err := pstring(addr, pieces, err)
		if err != nil {
			if fmt.Sprintf("%s", err) == "empty OP stack" {
				pdump = "<not available>"
			} else {
				return fmt.Errorf("bad return from bi.Location at pc 0x%x: %q\n", fi.dwLoPC, err)
			}
		}
		wh := "in"
		if isrvar {
			wh = "out"
		}
		fmt.Printf("%d: %s-param %q loc=%q\n", idx, wh, name, pdump)

	}
	return nil

}

// examineFile kicks off the search for DWARF info for 'fcn' within
// Go binary 'executable', printing results to stdout if possible.
func examineFile(executable string, fcn string) {
	verb(1, "examineFile(%s,%s)", executable, fcn)
	fi, err := locateFuncDetails(executable, fcn)
	if err != nil {
		log.Fatalf("error: %v\n", err)
	}
	if !fi.valid {
		log.Fatalf("could not locate target function %s in executable %s", fcn, executable)

	}
	if err := processParams(executable, &fi); err != nil {
		log.Fatalf("error: %v\n", err)
	}
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("dwdumploc: ")
	flag.Parse()
	verb(1, "in main")
	if *fcnflag == "" || *moduleflag == "" {
		usage("please supply -f and -m options")
	}
	if flag.NArg() != 0 {
		usage("unexpected additional arguments")
	}
	examineFile(*moduleflag, *fcnflag)
	verb(1, "leaving main")
}
