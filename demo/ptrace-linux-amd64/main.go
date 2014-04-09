// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This program is a standalone demo that demonstrates a Go tracer program
// (this program) forking, executing and inserting breakpoints into a (multi-
// threaded) Go tracee program. It logs all syscall.Wait4 results, and reading
// those logs, as well as ../../doc/ptrace-nptl.txt, should help understand how
// the (more complicated) code.google.com/p/ogle/program/server package works.
//
// Only tested on linux/amd64.
package main

// TODO: other threads are not stopped when one tracee thread hits a breakpoint.
// Thus, other threads could racily miss a breakpoint when they concurrently
// execute code where the trap should be, in between lifting and re-setting the
// trap.
//
// One option is to simply accept this behavior as is. Another option, "all-
// stop", is to stop all other threads when one traps. A third option, "non-
// stop", is to not lift the trap and not stop other threads, but to simulate
// the original instruction when single-stepping: the "Non-stop Multi-Threaded
// Debugging in GDB" paper calls this "displaced stepping". "Non-stop" is less
// intrusive than "all-stop" (e.g. network I/O can still happen while at a
// breakpoint), but it involves complicated, architecture-specific code.

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"syscall"
	"time"

	"code.google.com/p/ogle/debug/dwarf"
	"code.google.com/p/ogle/debug/elf"
	"code.google.com/p/ogle/debug/macho"
)

const (
	exeFilename = "tracee/tracee"

	// TODO: don't be amd64-specific.
	breakpointInstr    = 0xcc
	breakpointInstrLen = 1
)

type breakpoint struct {
	pc        uint64
	origInstr byte // TODO: don't be amd64-specific.
}

func main() {
	go run()
	time.Sleep(2 * time.Second)
}

func run() {
	// If the debugger itself is multi-threaded, ptrace calls must come from
	// the same thread that originally attached to the remote thread.
	runtime.LockOSThread()

	f, err := os.Open(exeFilename)
	if err != nil {
		log.Printf(`%q not found. Did you run "go build ." in that directory?`, exeFilename)
		log.Fatalf("Open: %v", err)
	}
	defer f.Close()
	dwarfData, err := loadDwarfData(f)
	if err != nil {
		log.Fatalf("loadDwarfData: %v", err)
	}

	proc, err := os.StartProcess(exeFilename, []string{exeFilename}, &os.ProcAttr{
		Files: []*os.File{
			os.Stdin,
			os.Stdout,
			os.Stderr,
		},
		Sys: &syscall.SysProcAttr{
			Ptrace:    true,
			Pdeathsig: syscall.SIGKILL,
		},
	})
	if err != nil {
		log.Fatalf("StartProcess: %v", err)
	}

	fmt.Printf("\tproc.Pid=%d\n", proc.Pid)

	_, status, err := wait(proc.Pid)
	if err != nil {
		log.Fatalf("wait: %v", err)
	}
	if status != 0x00057f { // 0x05=SIGTRAP, 0x7f=stopped.
		log.Fatalf("status: got %#x, want %#x", status, 0x57f)
	}
	err = syscall.PtraceSetOptions(proc.Pid, syscall.PTRACE_O_TRACECLONE|syscall.PTRACE_O_TRACEEXIT)
	if err != nil {
		log.Fatalf("PtraceSetOptions: %v", err)
	}

	addr, err := lookupSym(dwarfData, "fmt.Printf")
	if err != nil {
		log.Fatalf("lookupSym: %v", err)
	}
	fmt.Printf("\tfmt.Printf=%#x\n", addr)

	var buf [1]byte
	if err := peek(proc.Pid, addr, buf[:1]); err != nil {
		log.Fatalf("peek: %v", err)
	}
	breakpoints := map[uint64]breakpoint{
		addr: {pc: addr, origInstr: buf[0]},
	}
	buf[0] = breakpointInstr
	if err := poke(proc.Pid, addr, buf[:1]); err != nil {
		log.Fatalf("poke: %v", err)
	}

	err = syscall.PtraceCont(proc.Pid, 0)
	if err != nil {
		log.Fatalf("PtraceCont: %v", err)
	}

	for {
		pid, status, err := wait(-1)
		if err != nil {
			log.Fatalf("wait: %v", err)
		}

		switch status {
		case 0x00057f: // 0x05=SIGTRAP, 0x7f=stopped.
			regs := syscall.PtraceRegs{}
			if err := syscall.PtraceGetRegs(pid, &regs); err != nil {
				log.Fatalf("PtraceGetRegs: %v", err)
			}
			regs.Rip -= breakpointInstrLen
			if err := syscall.PtraceSetRegs(pid, &regs); err != nil {
				log.Fatalf("PtraceSetRegs: %v", err)
			}
			bp, ok := breakpoints[regs.Rip]
			if !ok {
				log.Fatalf("no breakpoint for address %#x\n", regs.Rip)
			}
			buf[0] = bp.origInstr
			if err := poke(pid, addr, buf[:1]); err != nil {
				log.Fatalf("poke: %v", err)
			}
			fmt.Printf("\thit breakpoint at %#x, pid=%5d\n", regs.Rip, pid)
			if err := syscall.PtraceSingleStep(pid); err != nil {
				log.Fatalf("PtraceSingleStep: %v", err)
			}
			_, status, err := wait(pid)
			if err != nil {
				log.Fatalf("wait: %v", err)
			}
			if status != 0x00057f {
				log.Fatalf("PtraceSingleStep: unexpected status %#x\n", status)
			}
			buf[0] = breakpointInstr
			if err := poke(pid, addr, buf[:1]); err != nil {
				log.Fatalf("poke: %v", err)
			}

		case 0x00137f: // 0x13=SIGSTOP, 0x7f=stopped.
			// No-op.

		case 0x03057f: // 0x05=SIGTRAP, 0x7f=stopped, 0x03=PTRACE_EVENT_CLONE.
			msg, err := syscall.PtraceGetEventMsg(pid)
			if err != nil {
				log.Fatalf("PtraceGetEventMsg: %v", err)
			}
			fmt.Printf("\tclone: new pid=%d\n", msg)

		default:
			log.Fatalf("unexpected status %#x\n", status)
		}

		err = syscall.PtraceCont(pid, 0)
		if err != nil {
			log.Fatalf("PtraceCont: %v", err)
		}
	}
}

func wait(pid int) (wpid int, status syscall.WaitStatus, err error) {
	wpid, err = syscall.Wait4(pid, &status, syscall.WALL, nil)
	if err != nil {
		return 0, 0, err
	}
	fmt.Printf("\t\twait: wpid=%5d, status=0x%06x\n", wpid, status)
	return wpid, status, nil
}

func peek(pid int, addr uint64, data []byte) error {
	n, err := syscall.PtracePeekText(pid, uintptr(addr), data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("peek: got %d bytes, want %d", len(data))
	}
	return nil
}

func poke(pid int, addr uint64, data []byte) error {
	n, err := syscall.PtracePokeText(pid, uintptr(addr), data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("poke: got %d bytes, want %d", len(data))
	}
	return nil
}

func lookupSym(dwarfData *dwarf.Data, name string) (uint64, error) {
	r := dwarfData.Reader()
	for {
		entry, err := r.Next()
		if err != nil {
			return 0, err
		}
		if entry == nil {
			// TODO: why don't we get an error here.
			break
		}
		if entry.Tag != dwarf.TagSubprogram {
			continue
		}
		nameAttr := lookupAttr(entry, dwarf.AttrName)
		if nameAttr == nil {
			// TODO: this shouldn't be possible.
			continue
		}
		if nameAttr.(string) != name {
			continue
		}
		addrAttr := lookupAttr(entry, dwarf.AttrLowpc)
		if addrAttr == nil {
			return 0, fmt.Errorf("symbol %q has no LowPC attribute", name)
		}
		addr, ok := addrAttr.(uint64)
		if !ok {
			return 0, fmt.Errorf("symbol %q has non-uint64 LowPC attribute", name)
		}
		return addr, nil
	}
	return 0, fmt.Errorf("symbol %q not found", name)
}

func lookupAttr(e *dwarf.Entry, a dwarf.Attr) interface{} {
	for _, f := range e.Field {
		if f.Attr == a {
			return f.Val
		}
	}
	return nil
}

func loadDwarfData(f *os.File) (*dwarf.Data, error) {
	if obj, err := elf.NewFile(f); err == nil {
		return obj.DWARF()
	}
	if obj, err := macho.NewFile(f); err == nil {
		return obj.DWARF()
	}
	return nil, fmt.Errorf("unrecognized binary format")
}
