// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The viewcore tool is a command-line tool for exploring the state of a Go process
// that has dumped core.
// Run "viewcore help" for a list of commands.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"sort"
	"strconv"
	"text/tabwriter"

	"golang.org/x/debug/core"
	"golang.org/x/debug/gocore"
)

func usage() {
	fmt.Println(`
Usage:

        viewcore command corefile

The commands are:

        help: print this message
    overview: print a few overall statistics
    mappings: print virtual memory mappings
  goroutines: list goroutines
   histogram: print histogram of heap memory use by Go type
   breakdown: print memory use by class
     objects: print a list of all live objects
    objgraph: dump object graph to the file tmp.dot
   reachable: find path from root to an object
        html: start an http server on :8080 for browsing core file data
        read: read a chunk of memory

Flags applicable to all commands:\n`)
	flag.PrintDefaults()
}

func main() {
	base := flag.String("base", "", "root directory to find core dump file references")
	prof := flag.String("prof", "", "write cpu profile of viewcore to this file (for viewcore's developers)")
	flag.Parse()

	// Extract command.
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "%s: no command specified\n", os.Args[0])
		usage()
		os.Exit(2)
	}
	cmd := args[0]
	if cmd == "help" {
		usage()
		return
	}

	if *prof != "" {
		f, err := os.Create(*prof)
		if err != nil {
			fmt.Fprintf(os.Stderr, "can't open profile file: %s\n", err)
			os.Exit(2)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	var flags gocore.Flags
	switch cmd {
	default:
		fmt.Fprintf(os.Stderr, "%s: unknown command %s\n", os.Args[0], cmd)
		fmt.Fprintf(os.Stderr, "Run 'viewcore help' for usage.\n")
		os.Exit(2)
	case "overview":
	case "mappings":
	case "goroutines":
	case "histogram":
		flags = gocore.FlagTypes
	case "breakdown":
	case "objgraph":
		flags = gocore.FlagTypes
	case "objects":
		flags = gocore.FlagTypes
	case "reachable":
		flags = gocore.FlagTypes | gocore.FlagReverse
	case "html":
		flags = gocore.FlagTypes | gocore.FlagReverse
	case "read":
	}

	// All commands other than "help" need a core file.
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "%s: no core dump specified for command %s\n", os.Args[0], cmd)
		fmt.Fprintf(os.Stderr, "Run 'viewcore help' for usage.\n")
		os.Exit(2)
	}
	file := args[1]
	p, err := core.Core(file, *base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	for _, w := range p.Warnings() {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	c, err := gocore.Core(p, flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	switch cmd {
	default:
		fmt.Fprintf(os.Stderr, "%s: unknown command %s\n", os.Args[0], cmd)
		fmt.Fprintf(os.Stderr, "Run '%s help' for usage.\n", os.Args[0])
		os.Exit(2)

	case "overview":
		t := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(t, "arch\t%s\n", p.Arch())
		fmt.Fprintf(t, "runtime\t%s\n", c.BuildVersion())
		var total int64
		for _, m := range p.Mappings() {
			total += m.Max().Sub(m.Min())
		}
		fmt.Fprintf(t, "memory\t%.1f MB\n", float64(total)/(1<<20))
		t.Flush()

	case "mappings":
		t := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', tabwriter.AlignRight)
		fmt.Fprintf(t, "min\tmax\tperm\tsource\toriginal\t\n")
		for _, m := range p.Mappings() {
			perm := ""
			if m.Perm()&core.Read != 0 {
				perm += "r"
			} else {
				perm += "-"
			}
			if m.Perm()&core.Write != 0 {
				perm += "w"
			} else {
				perm += "-"
			}
			if m.Perm()&core.Exec != 0 {
				perm += "x"
			} else {
				perm += "-"
			}
			file, off := m.Source()
			fmt.Fprintf(t, "%x\t%x\t%s\t%s@%x\t", m.Min(), m.Max(), perm, file, off)
			if m.CopyOnWrite() {
				file, off = m.OrigSource()
				fmt.Fprintf(t, "%s@%x", file, off)
			}
			fmt.Fprintf(t, "\t\n")
		}
		t.Flush()

	case "goroutines":
		for _, g := range c.Goroutines() {
			fmt.Printf("G stacksize=%x\n", g.Stack())
			for _, f := range g.Frames() {
				pc := f.PC()
				entry := f.Func().Entry()
				var adj string
				switch {
				case pc == entry:
					adj = ""
				case pc < entry:
					adj = fmt.Sprintf("-%d", entry.Sub(pc))
				default:
					adj = fmt.Sprintf("+%d", pc.Sub(entry))
				}
				fmt.Printf("  %016x %016x %s%s\n", f.Min(), f.Max(), f.Func().Name(), adj)
			}
		}

	case "histogram":
		// Produce an object histogram (bytes per type).
		type bucket struct {
			name  string
			size  int64
			count int64
		}
		var buckets []*bucket
		m := map[string]*bucket{}
		c.ForEachObject(func(x gocore.Object) bool {
			name := typeName(c, x)
			b := m[name]
			if b == nil {
				b = &bucket{name: name, size: c.Size(x)}
				buckets = append(buckets, b)
				m[name] = b
			}
			b.count++
			return true
		})
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].size*buckets[i].count > buckets[j].size*buckets[j].count
		})
		t := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', tabwriter.AlignRight)
		fmt.Fprintf(t, "%s\t%s\t%s\t %s\n", "count", "size", "bytes", "type")
		for _, e := range buckets {
			fmt.Fprintf(t, "%d\t%d\t%d\t %s\n", e.count, e.size, e.count*e.size, e.name)
		}
		t.Flush()

	case "breakdown":
		t := tabwriter.NewWriter(os.Stdout, 0, 8, 1, ' ', tabwriter.AlignRight)
		all := c.Stats().Size
		var printStat func(*gocore.Stats, string)
		printStat = func(s *gocore.Stats, indent string) {
			comment := ""
			switch s.Name {
			case "bss":
				comment = "(grab bag, includes OS thread stacks, ...)"
			case "manual spans":
				comment = "(Go stacks)"
			case "retained":
				comment = "(kept for reuse by Go)"
			case "released":
				comment = "(given back to the OS)"
			}
			fmt.Fprintf(t, "%s\t%d\t%6.2f%%\t %s\n", fmt.Sprintf("%-20s", indent+s.Name), s.Size, float64(s.Size)*100/float64(all), comment)
			for _, c := range s.Children {
				printStat(c, indent+"  ")
			}
		}
		printStat(c.Stats(), "")
		t.Flush()

	case "objgraph":
		// Dump object graph to output file.
		w, err := os.Create("tmp.dot")
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(w, "digraph {\n")
		for k, r := range c.Globals() {
			printed := false
			c.ForEachRootPtr(r, func(i int64, y gocore.Object, j int64) bool {
				if !printed {
					fmt.Fprintf(w, "r%d [label=\"%s\n%s\",shape=hexagon]\n", k, r.Name, r.Type)
					printed = true
				}
				fmt.Fprintf(w, "r%d -> o%x [label=\"%s\"", k, c.Addr(y), typeFieldName(r.Type, i))
				if j != 0 {
					fmt.Fprintf(w, " ,headlabel=\"+%d\"", j)
				}
				fmt.Fprintf(w, "]\n")
				return true
			})
		}
		for _, g := range c.Goroutines() {
			last := fmt.Sprintf("o%x", g.Addr())
			for _, f := range g.Frames() {
				frame := fmt.Sprintf("f%x", f.Max())
				fmt.Fprintf(w, "%s [label=\"%s\",shape=rectangle]\n", frame, f.Func().Name())
				fmt.Fprintf(w, "%s -> %s [style=dotted]\n", last, frame)
				last = frame
				for _, r := range f.Roots() {
					c.ForEachRootPtr(r, func(i int64, y gocore.Object, j int64) bool {
						fmt.Fprintf(w, "%s -> o%x [label=\"%s%s\"", frame, c.Addr(y), r.Name, typeFieldName(r.Type, i))
						if j != 0 {
							fmt.Fprintf(w, " ,headlabel=\"+%d\"", j)
						}
						fmt.Fprintf(w, "]\n")
						return true
					})
				}
			}
		}
		c.ForEachObject(func(x gocore.Object) bool {
			addr := c.Addr(x)
			size := c.Size(x)
			fmt.Fprintf(w, "o%x [label=\"%s\\n%d\"]\n", addr, typeName(c, x), size)
			c.ForEachPtr(x, func(i int64, y gocore.Object, j int64) bool {
				fmt.Fprintf(w, "o%x -> o%x [label=\"%s\"", addr, c.Addr(y), fieldName(c, x, i))
				if j != 0 {
					fmt.Fprintf(w, ",headlabel=\"+%d\"", j)
				}
				fmt.Fprintf(w, "]\n")
				return true
			})
			return true
		})
		fmt.Fprintf(w, "}")
		w.Close()

	case "objects":
		c.ForEachObject(func(x gocore.Object) bool {
			fmt.Printf("%16x %s\n", c.Addr(x), typeName(c, x))
			return true
		})

	case "reachable":
		if len(args) < 3 {
			fmt.Fprintf(os.Stderr, "no object address provided\n")
			os.Exit(1)
		}
		n, err := strconv.ParseInt(args[2], 16, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "can't parse %s as an object address\n", args[2])
			os.Exit(1)
		}
		a := core.Address(n)
		obj, _ := c.FindObject(a)
		if obj == 0 {
			fmt.Fprintf(os.Stderr, "can't find object at address %s\n", args[2])
			os.Exit(1)
		}

		// Breadth-first search backwards until we reach a root.
		type hop struct {
			i int64         // offset in "from" object (the key in the path map) where the pointer is
			x gocore.Object // the "to" object
			j int64         // the offset in the "to" object
		}
		depth := map[gocore.Object]int{}
		depth[obj] = 0
		q := []gocore.Object{obj}
		done := false
		for !done {
			if len(q) == 0 {
				panic("can't find a root that can reach the object")
			}
			y := q[0]
			q = q[1:]
			c.ForEachReversePtr(y, func(x gocore.Object, r *gocore.Root, i, j int64) bool {
				if r != nil {
					// found it.
					if r.Frame == nil {
						// Print global
						fmt.Printf("%s", r.Name)
					} else {
						// Print stack up to frame in question.
						var frames []*gocore.Frame
						for f := r.Frame.Parent(); f != nil; f = f.Parent() {
							frames = append(frames, f)
						}
						for k := len(frames) - 1; k >= 0; k-- {
							fmt.Printf("%s\n", frames[k].Func().Name())
						}
						// Print frame + variable in frame.
						fmt.Printf("%s.%s", r.Frame.Func().Name(), r.Name)
					}
					fmt.Printf("%s → \n", typeFieldName(r.Type, i))

					z := y
					for {
						fmt.Printf("%x %s", c.Addr(z), typeName(c, z))
						if z == obj {
							fmt.Println()
							break
						}
						// Find an edge out of z which goes to an object
						// closer to obj.
						c.ForEachPtr(z, func(i int64, w gocore.Object, j int64) bool {
							if d, ok := depth[w]; ok && d < depth[z] {
								fmt.Printf(" %s → %s", objField(c, z, i), objRegion(c, w, j))
								z = w
								return false
							}
							return true
						})
						fmt.Println()
					}
					done = true
					return false
				}
				if _, ok := depth[x]; ok {
					// we already found a shorter path to this object.
					return true
				}
				depth[x] = depth[y] + 1
				q = append(q, x)
				return true
			})
		}
	case "html":
		serveHTML(c)
	case "read":
		if len(args) < 3 {
			fmt.Fprintf(os.Stderr, "no address provided\n")
			os.Exit(1)
		}
		n, err := strconv.ParseInt(args[2], 16, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "can't parse %s as an object address\n", args[2])
			os.Exit(1)
		}
		a := core.Address(n)
		if len(args) < 4 {
			n = 256
		} else {
			n, err = strconv.ParseInt(args[3], 10, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "can't parse %s as a byte count\n", args[3])
				os.Exit(1)
			}
		}
		if !p.ReadableN(a, n) {
			fmt.Fprintf(os.Stderr, "address range [%x,%x] not readable\n", a, a.Add(n))
			os.Exit(1)
		}
		b := make([]byte, n)
		p.ReadAt(b, a)
		for i, x := range b {
			if i%16 == 0 {
				if i > 0 {
					fmt.Println()
				}
				fmt.Printf("%x:", a.Add(int64(i)))
			}
			fmt.Printf(" %02x", x)
		}
		fmt.Println()
	}
}

// typeName returns a string representing the type of this object.
func typeName(c *gocore.Process, x gocore.Object) string {
	size := c.Size(x)
	typ, repeat := c.Type(x)
	if typ == nil {
		return fmt.Sprintf("unk%d", size)
	}
	name := typ.String()
	n := size / typ.Size
	if n > 1 {
		if repeat < n {
			name = fmt.Sprintf("[%d+%d?]%s", repeat, n-repeat, name)
		} else {
			name = fmt.Sprintf("[%d]%s", repeat, name)
		}
	}
	return name
}

// fieldName returns the name of the field at offset off in x.
func fieldName(c *gocore.Process, x gocore.Object, off int64) string {
	size := c.Size(x)
	typ, repeat := c.Type(x)
	if typ == nil {
		return fmt.Sprintf("f%d", off)
	}
	n := size / typ.Size
	i := off / typ.Size
	if i == 0 && repeat == 1 {
		// Probably a singleton object, no need for array notation.
		return typeFieldName(typ, off)
	}
	if i >= n {
		// Partial space at the end of the object - the type can't be complete.
		return fmt.Sprintf("f%d", off)
	}
	q := ""
	if i >= repeat {
		// Past the known repeat section, add a ? because we're not sure about the type.
		q = "?"
	}
	return fmt.Sprintf("[%d]%s%s", i, typeFieldName(typ, off-i*typ.Size), q)
}

// typeFieldName returns the name of the field at offset off in t.
func typeFieldName(t *gocore.Type, off int64) string {
	switch t.Kind {
	case gocore.KindBool, gocore.KindInt, gocore.KindUint, gocore.KindFloat:
		return ""
	case gocore.KindComplex:
		if off == 0 {
			return ".real"
		}
		return ".imag"
	case gocore.KindIface, gocore.KindEface:
		if off == 0 {
			return ".type"
		}
		return ".data"
	case gocore.KindPtr, gocore.KindFunc:
		return ""
	case gocore.KindString:
		if off == 0 {
			return ".ptr"
		}
		return ".len"
	case gocore.KindSlice:
		if off == 0 {
			return ".ptr"
		}
		if off <= t.Size/2 {
			return ".len"
		}
		return ".cap"
	case gocore.KindArray:
		s := t.Elem.Size
		i := off / s
		return fmt.Sprintf("[%d]%s", i, typeFieldName(t.Elem, off-i*s))
	case gocore.KindStruct:
		for _, f := range t.Fields {
			if f.Off <= off && off < f.Off+f.Type.Size {
				return "." + f.Name + typeFieldName(f.Type, off-f.Off)
			}
		}
	}
	return ".???"
}
