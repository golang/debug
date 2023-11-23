package main

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"golang.org/x/debug/internal/core"
	"golang.org/x/debug/internal/gocore"
)

type ObjRef struct {
	link string
	node *ObjNode
}

type ObjNode struct {
	addr core.Address
	name string
	size int64
	refs []*ObjRef
}

var (
	visitedNodes   = make(map[core.Address]bool)
	allNodes       = make(map[core.Address]*ObjNode)
	rootNodesMap   = make(map[core.Address]*ObjNode)
	rootObjNodes   = []*ObjNode{}
	globalVarNodes = []*ObjNode{}
	goroutineNodes = []*ObjNode{}
)

func genUniqueRefTree(nodes []*ObjNode) {
	// width first visit
	cnodes := []*ObjNode{}

	for _, node := range nodes {
		if _, ok := visitedNodes[node.addr]; !ok {
			panic("add children for not visited node")
			return
		}

		refs := allNodes[node.addr].refs // all refered child nodes
		for _, ref := range refs {
			if _, ok := visitedNodes[ref.node.addr]; ok {
				continue
			}
			newNode := nodeCopy(ref.node)
			newRef := &ObjRef{
				node: newNode,
				link: ref.link,
			}

			node.refs = append(node.refs, newRef)
			visitedNodes[newNode.addr] = true

			cnodes = append(cnodes, newNode)
		}
	}
	if len(cnodes) > 0 {
		genUniqueRefTree(cnodes)
	}
}

func (node *ObjNode) appendChild(cNode *ObjNode, link string) {
	ref := &ObjRef{
		link: link,
		node: cNode,
	}
	node.refs = append(node.refs, ref)
}

func findOrCreateObjNode(name string, addr core.Address, size int64) (*ObjNode, bool) {
	if node, ok := allNodes[addr]; ok {
		if node.size != size {
			fmt.Fprintf(os.Stderr, "same address: %v, old size: %v, new size: %v\n", addr, node.size, size)
		}
		if node.name != name && strings.HasPrefix(node.name, "unk") && !strings.HasPrefix(name, "unk") {
			node.name = name
		}
		return node, true
	}
	node := &ObjNode{
		name: name,
		addr: addr,
		size: size,
	}
	allNodes[addr] = node
	return node, false
}

func calcTreeSize(node *ObjNode) int64 {
	size := int64(0)
	for _, ref := range node.refs {
		size += calcTreeSize(ref.node)
	}
	node.size = size + node.size
	return node.size
}

func nodeCopy(node *ObjNode) *ObjNode {
	newNode := *node
	newNode.refs = []*ObjRef{}
	return &newNode
}

func addGlobalVarNodes(node *ObjNode) {
	n := nodeCopy(node)
	globalVarNodes = append(globalVarNodes, n)
	rootObjNodes = append(rootObjNodes, n)

	// mark visited for root node
	visitedNodes[n.addr] = true
}

func addGoroutines(node *ObjNode) {
	n := nodeCopy(node)
	goroutineNodes = append(goroutineNodes, node)
	rootObjNodes = append(rootObjNodes, n)

	// mark visited for root node
	visitedNodes[n.addr] = true
}

func runObjref(cmd *cobra.Command, args []string) {
	minWidth, err := cmd.Flags().GetFloat64("minwidth")
	if err != nil {
		exitf("%v\n", err)
	}
	printAddr, err := cmd.Flags().GetBool("printaddr")
	if err != nil {
		exitf("%v\n", err)
	}
	_, c, err := readCore()
	if err != nil {
		exitf("%v\n", err)
	}

	sumObjSize := int64(0)
	c.ForEachObject(func(x gocore.Object) bool {
		sumObjSize += c.Size(x)
		xNode, _ := findOrCreateObjNode(typeName(c, x), c.Addr(x), c.Size(x))
		c.ForEachPtr(x, func(i int64, y gocore.Object, j int64) bool {
			yNode, _ := findOrCreateObjNode(typeName(c, y), c.Addr(y), c.Size(y))
			xNode.appendChild(yNode, fieldName(c, x, i))
			return true
		})
		return true
	})
	fmt.Fprintf(os.Stderr, "sum object size %v\n", sumObjSize)

	for _, r := range c.Globals() {
		// size = 0, since global variable is not from heap
		rNode, existing := findOrCreateObjNode(r.Name, r.Addr, 0)
		if !existing {
			// may have duplicated address from globals.
			addGlobalVarNodes(rNode)
		}

		c.ForEachRootPtr(r, func(i int64, y gocore.Object, j int64) bool {
			cNode, _ := findOrCreateObjNode(typeName(c, y), c.Addr(y), c.Size(y))
			rNode.appendChild(cNode, typeFieldName(r.Type, i))
			return true
		})
	}
	for _, g := range c.Goroutines() {
		gName := fmt.Sprintf("go%x", g.Addr())
		gNode, _ := findOrCreateObjNode(gName, g.Addr(), c.Size(gocore.Object(g.Addr())))
		addGoroutines(gNode)
	}

	// first, global variable
	genUniqueRefTree(globalVarNodes)
	// next, goroutines
	genUniqueRefTree(goroutineNodes)

	total := int64(0)
	for _, rNode := range rootObjNodes {
		total += calcTreeSize(rNode)
	}
	fmt.Fprintf(os.Stderr, "total size %v\n", total)

	filename := args[0]
	// Dump object graph to output file.
	w, err := os.Create(filename)
	if err != nil {
		panic(err)
	}
	var path []string
	printedSize := int64(0)
	for _, rNode := range rootObjNodes {
		printedSize += printRefPath(w, path, total, rNode, minWidth, printAddr)
	}
	fmt.Fprintf(os.Stderr, "printed size: %v\n", printedSize)
	w.Close()
	fmt.Fprintf(os.Stderr, "wrote the object reference to %q\n", filename)
}

func genRefPath(slice []string) string {
	// 1. reverse order.
	var reverse = make([]string, len(slice))

	for index, value := range slice {
		// 2. remove unprintable
		newValue := strings.Map(func(r rune) rune {
			if r == '+' || r == '?' {
				return '.'
			}
			if unicode.IsPrint(r) {
				return r
			}
			return -1
		}, value)
		reverse[len(reverse)-1-index] = newValue
	}

	return strings.Join(reverse, "\n")
}

// return the printed size
func printRefPath(w *os.File, path []string, total int64, node *ObjNode, minWidth float64, printAddr bool) int64 {
	if float64(node.size)/float64(total) < minWidth/100 {
		return 0
	}
	printedSize := int64(0)
	if printAddr {
		path = append(path, fmt.Sprintf("%v 0x%x", node.name, node.addr))
	} else {
		path = append(path, node.name)
	}
	for _, ref := range node.refs {
		rPath := path
		if ref.link != "" {
			rPath = append(rPath, ref.link)
		}
		printedSize += printRefPath(w, rPath, total, ref.node, minWidth, printAddr)
	}
	if float64(node.size-printedSize)/float64(total) < minWidth/100 {
		return printedSize
	}
	ref := genRefPath(path)
	fmt.Fprintf(w, "%v\n\t%d\n", ref, node.size-printedSize)

	return node.size
}
