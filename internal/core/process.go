// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The core library is used to process ELF core dump files.  You can
// open a core dump file and read from addresses in the process that
// dumped core, called the "inferior". Some ancillary information
// about the inferior is also provided, like architecture and OS
// thread state.
//
// There's nothing Go-specific about this library, it could
// just as easily be used to read a C++ core dump. See ../gocore
// for the next layer up, a Go-specific core dump reader.
//
// The Read* operations all panic with an error (the builtin Go type)
// if the inferior is not readable at the address requested.
package core

import (
	"bytes"
	"debug/dwarf"
	"debug/elf" // TODO: use golang.org/x/debug/elf instead?
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// A Process represents the state of the process that core dumped.
type Process struct {
	base string   // base directory from which files in the core can be found
	exe  *os.File // user-supplied main executable path

	files        map[string]*file // files found from the note section
	mainExecName string           // open main executable name

	entryPoint Address
	memory     splicedMemory // virtual address mappings
	threads    []*Thread     // os threads (TODO: map from pid?)

	arch         string             // amd64, ...
	ptrSize      int64              // 4 or 8
	logPtrSize   uint               // 2 or 3
	byteOrder    binary.ByteOrder   //
	littleEndian bool               // redundant with byteOrder
	syms         map[string]Address // symbols (could be empty if executable is stripped)
	symErr       error              // an error encountered while reading symbols
	dwarf        *dwarf.Data        // debugging info (could be nil)
	dwarfErr     error              // an error encountered while reading DWARF
	pageTable    pageTable4         // for fast address->mapping lookups
	args         string             // first part of args retrieved from NT_PRPSINFO

	warnings []string // warnings generated during loading
}

type file struct {
	f   *os.File
	err error
}

// Mappings returns a list of virtual memory mappings for p.
func (p *Process) Mappings() []*Mapping {
	return p.memory.mappings
}

// Readable reports whether the address a is readable.
func (p *Process) Readable(a Address) bool {
	return p.findMapping(a) != nil
}

// ReadableN reports whether the n bytes starting at address a are readable.
func (p *Process) ReadableN(a Address, n int64) bool {
	for {
		m := p.findMapping(a)
		if m == nil || m.perm&Read == 0 {
			return false
		}
		c := m.max.Sub(a)
		if n <= c {
			return true
		}
		n -= c
		a = a.Add(c)
	}
}

// Writeable reports whether the address a was writeable (by the inferior at the time of the core dump).
func (p *Process) Writeable(a Address) bool {
	m := p.findMapping(a)
	if m == nil {
		return false
	}
	return m.perm&Write != 0
}

// Threads returns information about each OS thread in the inferior.
func (p *Process) Threads() []*Thread {
	return p.threads
}

func (p *Process) Arch() string {
	return p.arch
}

// PtrSize returns the size in bytes of a pointer in the inferior.
func (p *Process) PtrSize() int64 {
	return p.ptrSize
}
func (p *Process) LogPtrSize() uint {
	return p.logPtrSize
}

func (p *Process) ByteOrder() binary.ByteOrder {
	return p.byteOrder
}

func (p *Process) DWARF() (*dwarf.Data, error) {
	return p.dwarf, p.dwarfErr
}

// Symbols returns a mapping from name to inferior address, along with
// any error encountered during reading the symbol information.
// (There may be both an error and some returned symbols.)
// Symbols might not be available with core files from stripped binaries.
func (p *Process) Symbols() (map[string]Address, error) {
	return p.syms, p.symErr
}

var mapFile = func(fd int, offset int64, length int) (data []byte, err error) {
	return nil, fmt.Errorf("file mapping is not implemented yet")
}

// Core takes the name of a core file and returns a Process that
// represents the state of the inferior that generated the core file.
func Core(coreFile, base, exePath string) (*Process, error) {
	core, err := os.Open(coreFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open core file: %v", err)
	}

	p := &Process{base: base, files: make(map[string]*file)}
	if exePath != "" {
		bin, err := os.Open(exePath)
		if err != nil {
			return nil, fmt.Errorf("failed to open executable file: %v", err)
		}
		p.exe = bin
	}

	if err := p.readExec(p.exe); err != nil {
		return nil, err
	}

	if err := p.readCore(core); err != nil {
		return nil, err
	}

	if err := p.readDebugInfo(); err != nil {
		return nil, err
	}

	// Sort then merge mappings, just to clean up a bit.
	mappings := p.memory.mappings
	sort.Slice(mappings, func(i, j int) bool {
		return mappings[i].min < mappings[j].min
	})
	ms := mappings[1:]
	mappings = mappings[:1]
	for _, m := range ms {
		k := mappings[len(mappings)-1]
		if m.min == k.max &&
			m.perm == k.perm &&
			m.f == k.f &&
			m.off == k.off+k.Size() {
			k.max = m.max
			// TODO: also check origF?
		} else {
			mappings = append(mappings, m)
		}
	}
	p.memory.mappings = mappings

	// Memory map all the mappings.
	hostPageSize := int64(syscall.Getpagesize())
	for _, m := range p.memory.mappings {
		size := m.max.Sub(m.min)
		if m.f == nil {
			// We don't have any source for this data.
			// Could be a mapped file that we couldn't find.
			// Could be a mapping madvised as MADV_DONTDUMP.
			// Pretend this is read-as-zero.
			// The other option is to just throw away
			// the mapping (and thus make Read*s of this
			// mapping fail).
			p.warnings = append(p.warnings,
				fmt.Sprintf("Missing data at addresses [%x %x]. Assuming all zero.", m.min, m.max))
			// TODO: this allocation could be large.
			// Use mmap to avoid real backing store for all those zeros, or
			// perhaps split the mapping up into chunks and share the zero contents among them.
			m.contents = make([]byte, size)
			continue
		}
		if m.perm&Write != 0 && m.f != core {
			p.warnings = append(p.warnings,
				fmt.Sprintf("Writeable data at [%x %x] missing from core. Using possibly stale backup source %s.", m.min, m.max, m.f.Name()))
		}
		// Data in core file might not be aligned enough for the host.
		// Expand memory range so we can map full pages.
		minOff := m.off
		maxOff := m.off + size
		minOff -= minOff % hostPageSize
		if maxOff%hostPageSize != 0 {
			maxOff += hostPageSize - maxOff%hostPageSize
		}

		// Read data from file.
		data, err := mapFile(int(m.f.Fd()), minOff, int(maxOff-minOff))
		if err != nil {
			return nil, fmt.Errorf("can't memory map %s at %x: %s\n", m.f.Name(), minOff, err)
		}

		// Trim any data we mapped but don't need.
		data = data[m.off-minOff:]
		data = data[:size]

		m.contents = data
	}

	// Build page table for mapping lookup.
	for _, m := range p.memory.mappings {
		err := p.addMapping(m)
		if err != nil {
			return nil, err
		}
	}

	return p, nil
}

func (p *Process) readExec(exe *os.File) error {
	if exe == nil {
		return nil
	}
	e, err := elf.NewFile(exe)
	if err != nil {
		return err
	}
	// Load virtual memory mappings.
	for _, prog := range e.Progs {
		if prog.Type == elf.PT_LOAD {
			if err := p.readLoad(exe, e, prog); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Process) readCore(core *os.File) error {
	e, err := elf.NewFile(core)
	if err != nil {
		return err
	}
	if e.Type != elf.ET_CORE {
		return fmt.Errorf("%s is not a core file", core.Name())
	}
	switch e.Class {
	case elf.ELFCLASS32:
		p.ptrSize = 4
		p.logPtrSize = 2
	case elf.ELFCLASS64:
		p.ptrSize = 8
		p.logPtrSize = 3
	default:
		return fmt.Errorf("unknown elf class %s\n", e.Class)
	}
	switch e.Machine {
	case elf.EM_386:
		p.arch = "386"
	case elf.EM_X86_64:
		p.arch = "amd64"
		// TODO: detect amd64p32?
	case elf.EM_ARM:
		p.arch = "arm"
	case elf.EM_AARCH64:
		p.arch = "arm64"
	case elf.EM_MIPS:
		p.arch = "mips"
	case elf.EM_MIPS_RS3_LE:
		p.arch = "mipsle"
		// TODO: value for mips64?
	case elf.EM_PPC64:
		if e.ByteOrder.String() == "LittleEndian" {
			p.arch = "ppc64le"
		} else {
			p.arch = "ppc64"
		}
	case elf.EM_S390:
		p.arch = "s390x"
	default:
		return fmt.Errorf("unknown arch %s\n", e.Machine)
	}
	p.byteOrder = e.ByteOrder
	// We also compute explicitly what byte order the inferior is.
	// Just using p.byteOrder to decode fields makes any arguments passed to it
	// escape to the heap.  We use explicit binary.{Little,Big}Endian.UintXX
	// calls when we want to avoid heap-allocating the buffer.
	p.littleEndian = e.ByteOrder.String() == "LittleEndian"

	// Load virtual memory mappings.
	for _, prog := range e.Progs {
		if prog.Type == elf.PT_LOAD {
			if err := p.readLoad(core, e, prog); err != nil {
				return err
			}
		}
	}
	// Load notes (includes file mapping information).
	for _, prog := range e.Progs {
		if prog.Type == elf.PT_NOTE {
			if err := p.readNote(core, e, prog.Off, prog.Filesz); err != nil {
				return err
			}
		}
	}

	return nil
}

func (p *Process) readLoad(f *os.File, e *elf.File, prog *elf.Prog) error {
	min := Address(prog.Vaddr)
	max := min.Add(int64(prog.Memsz))
	var perm Perm
	if prog.Flags&elf.PF_R != 0 {
		perm |= Read
	}
	if prog.Flags&elf.PF_W != 0 {
		perm |= Write
	}
	if prog.Flags&elf.PF_X != 0 {
		perm |= Exec
	}
	if perm == 0 {
		// TODO: keep these nothing-mapped mappings?
		return nil
	}
	if prog.Filesz > 0 {
		// Data backing this mapping is in the core file.
		p.memory.Add(min, max, perm, f, int64(prog.Off))
	} else {
		p.memory.Add(min, max, perm, nil, 0)
	}
	if prog.Filesz < prog.Memsz {
		// We only have partial data for this mapping in the core file.
		// Trim the mapping and allocate an anonymous mapping for the remainder.
		p.memory.Add(min.Add(int64(prog.Filesz)), max, perm, nil, 0)
	}
	return nil
}

func (p *Process) readNote(f *os.File, e *elf.File, off, size uint64) error {
	// TODO: add this to debug/elf?
	const NT_FILE elf.NType = 0x46494c45
	const NT_AUXV elf.NType = 0x6 // auxv

	b := make([]byte, size)
	_, err := f.ReadAt(b, int64(off))
	if err != nil {
		return err
	}
	for len(b) > 0 {
		namesz := e.ByteOrder.Uint32(b)
		b = b[4:]
		descsz := e.ByteOrder.Uint32(b)
		b = b[4:]
		typ := elf.NType(e.ByteOrder.Uint32(b))
		b = b[4:]
		name := string(b[:namesz-1])
		b = b[(namesz+3)/4*4:]
		desc := b[:descsz]
		b = b[(descsz+3)/4*4:]

		if name != "CORE" { // what does this mean?
			continue
		}
		switch typ {
		case NT_FILE:
			if err := p.readNTFile(f, e, desc); err != nil {
				return fmt.Errorf("reading NT_FILE: %v", err)
			}
		case elf.NT_PRSTATUS:
			// An OS thread (an M)
			if err := p.readPRStatus(f, e, desc); err != nil {
				return fmt.Errorf("reading NT_PRSTATUS: %v", err)
			}
		case elf.NT_PRPSINFO:
			if err := p.readPRPSInfo(desc); err != nil {
				return fmt.Errorf("reading NT_PRPSINFO: %v", err)
			}
		case NT_AUXV:
			if entry, ok := findEntryPoint(desc, e.ByteOrder); ok {
				p.entryPoint = entry
			}
		}
		// TODO: NT_FPREGSET for floating-point registers
	}
	return nil
}

func findEntryPoint(auxvDesc []byte, order binary.ByteOrder) (Address, bool) {
	// amd64 only?
	const _AT_ENTRY_AMD64 = 9

	buf := bytes.NewBuffer(auxvDesc)
	for {
		var tag, val uint64
		if err := binary.Read(buf, order, &tag); err != nil {
			panic(err)
		}
		if err := binary.Read(buf, order, &val); err != nil {
			panic(err)
		}
		if tag == _AT_ENTRY_AMD64 {
			return Address(val), true
		}
	}
	return 0, false
}

func (p *Process) readNTFile(f *os.File, e *elf.File, desc []byte) error {
	// TODO: 4 instead of 8 for 32-bit machines?
	count := e.ByteOrder.Uint64(desc)
	desc = desc[8:]
	pagesize := e.ByteOrder.Uint64(desc)
	desc = desc[8:]
	filenames := string(desc[3*8*count:])
	desc = desc[:3*8*count]

	for i := uint64(0); i < count; i++ {
		min := Address(e.ByteOrder.Uint64(desc))
		desc = desc[8:]
		max := Address(e.ByteOrder.Uint64(desc))
		desc = desc[8:]
		off := int64(e.ByteOrder.Uint64(desc) * pagesize)
		desc = desc[8:]

		var name string
		j := strings.IndexByte(filenames, 0)
		if j >= 0 {
			name = filenames[:j]
			filenames = filenames[j+1:]
		} else {
			name = filenames
			filenames = ""
		}

		// TODO: this is O(n^2). Shouldn't be a big problem in practice.
		p.splitMappingsAt(min)
		p.splitMappingsAt(max)
		for _, m := range p.memory.mappings {
			if m.max <= min || m.min >= max {
				continue
			}
			// m should now be entirely in [min,max]
			if !(m.min >= min && m.max <= max) {
				panic("mapping overlapping end of file region")
			}

			f, err := p.openMappedFile(name, m)
			if err != nil {
				// Can't find mapped file.
				// We don't want to make this a hard error because there are
				// lots of possible missing files that probably aren't critical,
				// like a random shared library.
				p.warnings = append(p.warnings, fmt.Sprintf("Missing data for addresses [%x %x] because of failure to %s. Assuming all zero.", m.min, m.max, err))
			}

			if m.f == nil {
				m.f = f
				m.off = int64(off) + m.min.Sub(min)
			} else {
				// Data is both in the core file and in a mapped file.
				// The mapped file may be stale (even if it is readonly now,
				// it may have been writeable at some point).
				// Keep the file+offset just for printing.
				m.origF = f
				m.origOff = int64(off) + m.min.Sub(min)
			}
		}
	}
	return nil
}

func (p *Process) openMappedFile(fname string, m *Mapping) (*os.File, error) {
	if fname == "" {
		return nil, nil
	}

	if backing := p.files[fname]; backing != nil {
		return backing.f, backing.err
	}

	backing := &file{}

	isMainExe := m.perm&Exec != 0 && p.mainExecName == "" // first executable region
	if p.entryPoint != 0 && m.Min() <= p.entryPoint && p.entryPoint < m.Max() {
		// Or if we have the entry point info and it falls into this mappint, this is the region
		// the main executable is mapped.
		isMainExe = true
	}

	if !isMainExe {
		backing.f, backing.err = os.Open(filepath.Join(p.base, fname))
	} else { // keep main executable in p.mainExecName
		p.mainExecName = fname
		if p.exe != nil {
			backing.f, backing.err = p.exe, nil
		} else {
			backing.f, backing.err = os.Open(filepath.Join(p.base, fname))
		}
	}

	p.files[fname] = backing

	return backing.f, backing.err
}

// splitMappingsAt ensures that a is not in the middle of any mapping.
// Splits mappings as necessary.
func (p *Process) splitMappingsAt(a Address) {
	for _, m := range p.memory.mappings {
		if a < m.min || a > m.max {
			continue
		}
		if a == m.min || a == m.max {
			return
		}
		// Split this mapping at a.
		m2 := new(Mapping)
		*m2 = *m
		m.max = a
		m2.min = a
		if m2.f != nil {
			m2.off += m.Size()
		}
		if m2.origF != nil {
			m2.origOff += m.Size()
		}
		p.memory.mappings = append(p.memory.mappings, m2)
		return
	}
}

func (p *Process) readPRPSInfo(desc []byte) error {
	r := bytes.NewReader(desc)
	switch p.arch {
	default:
		// TODO: return error?
	case "amd64":
		prpsinfo := &linuxPrPsInfo{}
		if err := binary.Read(r, binary.LittleEndian, prpsinfo); err != nil {
			return err
		}
		p.args = strings.Trim(string(prpsinfo.Args[:]), "\x00 ")
	}
	return nil
}

func (p *Process) readPRStatus(f *os.File, e *elf.File, desc []byte) error {
	t := &Thread{}
	p.threads = append(p.threads, t)
	// Linux
	//   sys/procfs.h:
	//     struct elf_prstatus {
	//       ...
	//       pid_t	pr_pid;
	//       ...
	//       elf_gregset_t pr_reg;	/* GP registers */
	//       ...
	//     };
	//   typedef struct elf_prstatus prstatus_t;
	// Register numberings are listed in sys/user.h.
	// prstatus layout will probably be different for each arch/os combo.
	switch p.arch {
	default:
		// TODO: return error here?
	case "amd64":
		// 32 = offsetof(prstatus_t, pr_pid), 4 = sizeof(pid_t)
		t.pid = uint64(p.byteOrder.Uint32(desc[32 : 32+4]))
		// 112 = offsetof(prstatus_t, pr_reg), 216 = sizeof(elf_gregset_t)
		reg := desc[112 : 112+216]
		for i := 0; i < len(reg); i += 8 {
			t.regs = append(t.regs, p.byteOrder.Uint64(reg[i:]))
		}
		// Registers are:
		//  0: r15
		//  1: r14
		//  2: r13
		//  3: r12
		//  4: rbp
		//  5: rbx
		//  6: r11
		//  7: r10
		//  8: r9
		//  9: r8
		// 10: rax
		// 11: rcx
		// 12: rdx
		// 13: rsi
		// 14: rdi
		// 15: orig_rax
		// 16: rip
		// 17: cs
		// 18: eflags
		// 19: rsp
		// 20: ss
		// 21: fs_base
		// 22: gs_base
		// 23: ds
		// 24: es
		// 25: fs
		// 26: gs
		t.pc = Address(t.regs[16])
		t.sp = Address(t.regs[19])
	}
	return nil
}

func (p *Process) readDebugInfo() error {
	p.syms = map[string]Address{}
	// Read symbols from all available files.
	for _, f := range p.files {
		if f.f == nil {
			continue
		}
		e, err := elf.NewFile(f.f)
		if err != nil {
			return err
		}

		syms, err := e.Symbols()
		if err != nil {
			p.symErr = fmt.Errorf("can't read symbols from %s", f.f.Name())
			continue
		}
		for _, s := range syms {
			p.syms[s.Name] = Address(s.Value)
		}
	}

	// Prepare DWARF from the main exe.
	// An error while reading DWARF info is not an immediate error,
	// but any error will be returned if the caller asks for DWARF.
	exe := p.exe
	if exe == nil {
		f := p.files[p.mainExecName]
		if f.err != nil {
			p.dwarfErr = f.err
			return nil
		}
		exe = f.f
	}

	if exe == nil {
		p.dwarfErr = fmt.Errorf("can't find mappings for the main executable")
		return nil
	}

	e, err := elf.NewFile(exe)
	if err != nil {
		p.dwarfErr = fmt.Errorf("can't read DWARF info from %s: %s", exe.Name(), err)
		return nil
	}

	dwarf, err := e.DWARF()
	if err != nil {
		p.dwarfErr = fmt.Errorf("can't read DWARF info from %s: %s", exe.Name(), err)
		return nil
	}
	p.dwarf = dwarf
	return nil
}

func (p *Process) Warnings() []string {
	return p.warnings
}

// Args returns the initial part of the program arguments.
func (p *Process) Args() string {
	return p.args
}

// ELF/Linux types

// linuxPrPsInfo is the info embedded in NT_PRPSINFO.
type linuxPrPsInfo struct {
	State                uint8
	Sname                int8
	Zomb                 uint8
	Nice                 int8
	_                    [4]uint8
	Flag                 uint64
	Uid, Gid             uint32
	Pid, Ppid, Pgrp, Sid int32
	Fname                [16]uint8 // filename of executables
	Args                 [80]uint8 // first part of program args
}
