// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The viewcore tool is a command-line tool for exploring the state of a Go process
// that has dumped core.
// Run "viewcore help" for a list of commands.
package main

import (
	"fmt"
	"os"
	"runtime/pprof"
	"sort"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/debug/internal/core"
	"golang.org/x/debug/internal/gocore"
)

var cmdRoot = &cobra.Command{
	Use:               "viewcore <command>",
	Short:             "viewcore is a tool for analyzing core dumped from Go process",
	PersistentPreRun:  func(cmd *cobra.Command, args []string) { startProfile() },
	PersistentPostRun: func(cmd *cobra.Command, args []string) { endProfile() },
}

var cfg struct {
	// flags
	base    string
	cpuprof string
}

var (
	cmdOverview = &cobra.Command{
		Use:   "overview <corefile>",
		Short: "print a few overall statistics",
		Args:  cobra.ExactArgs(1),
		Run:   runOverview,
	}

	cmdMappings = &cobra.Command{
		Use:   "mappings <corefile>",
		Short: "print virtual memory mappings",
		Args:  cobra.ExactArgs(1),
		Run:   runMappings,
	}

	cmdGoroutines = &cobra.Command{
		Use:   "goroutines <corefile>",
		Short: "list goroutines",
		Args:  cobra.ExactArgs(1),
		Run:   runGoroutines,
	}

	cmdHistogram = &cobra.Command{
		Use:   "histogram <corefile>",
		Short: "print histogram of heap memory use by Go type",
		Args:  cobra.ExactArgs(1),
		Run:   runHistogram,
	}

	cmdBreakdown = &cobra.Command{
		Use:   "breakdown <corefile>",
		Short: "print memory use by class",
		Args:  cobra.ExactArgs(1),
		Run:   runBreakdown,
	}

	cmdObjects = &cobra.Command{
		Use:   "objects <corefile>",
		Short: "print a list of all live objects",
		Args:  cobra.ExactArgs(1),
		Run:   runObjects,
	}

	cmdObjgraph = &cobra.Command{
		Use:   "objgraph <corefile>",
		Short: "dump object graph to the file tmp.dot",
		Args:  cobra.ExactArgs(1),
		Run:   runObjgraph,

		// TODO: output file name flag
	}

	cmdReachable = &cobra.Command{
		Use:   "reachable <corefile> <address>",
		Short: "find path from root to an object",
		Args:  cobra.ExactArgs(2),
		Run:   runReachable,
	}

	cmdHTML = &cobra.Command{
		Use:   "html <corefile>",
		Short: "start an http server on :8080 for browsing core file data",
		Args:  cobra.ExactArgs(1),
		Run:   runHTML,

		// TODO: port flag
	}

	cmdRead = &cobra.Command{
		Use:   "read <corefile> <address> [<size>]",
		Short: "read a chunk of memory", // oh very helpful!
		Args:  cobra.RangeArgs(2, 3),
		Run:   runRead,
	}
)

func init() {
	cmdRoot.PersistentFlags().StringVar(&cfg.base, "base", "", "root directory to find core dump file references")
	cmdRoot.PersistentFlags().StringVar(&cfg.cpuprof, "prof", "", "write cpu profile of viewcore to this file for viewcore's developers")

	cmdRoot.AddCommand(
		cmdOverview,
		cmdMappings,
		cmdGoroutines,
		cmdHistogram,
		cmdBreakdown,
		cmdObjects,
		cmdObjgraph,
		cmdReachable,
		cmdHTML,
		cmdRead)
}

func main() {
	cmdRoot.Execute()
}

// readCore reads corefile and returns core and gocore process states.
func readCore(corefile, base string, flags gocore.Flags) (*core.Process, *gocore.Process, error) {
	p, err := core.Core(corefile, base)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range p.Warnings() {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	c, err := gocore.Core(p, flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return nil, nil, err
	}
	return p, c, nil
}

func runOverview(cmd *cobra.Command, args []string) {
	p, c, err := readCore(args[0], cfg.base, 0)
	if err != nil {
		exitf("%v\n", err)
	}

	t := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(t, "arch\t%s\n", p.Arch())
	fmt.Fprintf(t, "runtime\t%s\n", c.BuildVersion())
	var total int64
	for _, m := range p.Mappings() {
		total += m.Max().Sub(m.Min())
	}
	fmt.Fprintf(t, "memory\t%.1f MB\n", float64(total)/(1<<20))
	t.Flush()
}

func runMappings(cmd *cobra.Command, args []string) {
	p, _, err := readCore(args[0], cfg.base, 0)
	if err != nil {
		exitf("%v\n", err)
	}
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
}

func runGoroutines(cmd *cobra.Command, args []string) {
	_, c, err := readCore(args[0], cfg.base, 0)
	if err != nil {
		exitf("%v\n", err)
	}
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
}

func runHistogram(cmd *cobra.Command, args []string) {
	_, c, err := readCore(args[0], cfg.base, gocore.FlagTypes)
	if err != nil {
		exitf("%v\n", err)
	}
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

}

func runBreakdown(cmd *cobra.Command, args []string) {
	_, c, err := readCore(args[0], cfg.base, 0)
	if err != nil {
		exitf("%v\n", err)
	}
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

}

func runObjgraph(cmd *cobra.Command, args []string) {
	_, c, err := readCore(args[0], cfg.base, gocore.FlagTypes)
	if err != nil {
		exitf("%v\n", err)
	}
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

}

func runObjects(cmd *cobra.Command, args []string) {
	_, c, err := readCore(args[0], cfg.base, gocore.FlagTypes)
	if err != nil {
		exitf("%v\n", err)
	}
	c.ForEachObject(func(x gocore.Object) bool {
		fmt.Printf("%16x %s\n", c.Addr(x), typeName(c, x))
		return true
	})

}

func runReachable(cmd *cobra.Command, args []string) {
	_, c, err := readCore(args[0], cfg.base, gocore.FlagTypes|gocore.FlagReverse)
	if err != nil {
		exitf("%v\n", err)
	}
	n, err := strconv.ParseInt(args[1], 16, 64)
	if err != nil {
		exitf("can't parse %q as an object address\n", args[1])
	}
	a := core.Address(n)
	obj, _ := c.FindObject(a)
	if obj == 0 {
		exitf("can't find object at address %s\n", args[1])
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
}

func runHTML(cmd *cobra.Command, args []string) {
	_, c, err := readCore(args[0], cfg.base, gocore.FlagTypes|gocore.FlagReverse)
	if err != nil {
		exitf("%v\n", err)
	}
	serveHTML(c)
}

func runRead(cmd *cobra.Command, args []string) {
	p, _, err := readCore(args[0], cfg.base, 0)
	if err != nil {
		exitf("%v\n", err)
	}
	n, err := strconv.ParseInt(args[1], 16, 64)
	if err != nil {
		exitf("can't parse %q as an object address\n", args[1])
	}
	a := core.Address(n)
	if len(args) < 3 {
		n = 256
	} else {
		n, err = strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			exitf("can't parse %q as a byte count\n", args[2])
		}
	}
	if !p.ReadableN(a, n) {
		exitf("address range [%x,%x] not readable\n", a, a.Add(n))
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

func startProfile() {
	if cfg.cpuprof != "" {
		f, err := os.Create(cfg.cpuprof)
		if err != nil {
			fmt.Fprintf(os.Stderr, "can't open profile file: %s\n", err)
			os.Exit(2)
		}
		pprof.StartCPUProfile(f)

	}
}

func endProfile() {
	if cfg.cpuprof != "" {
		pprof.StopCPUProfile()
	}
}

func exitf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(1)
}
