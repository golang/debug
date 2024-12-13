package main

import (
	"runtime"
)

// Large is an object that (since Go 1.22) is allocated in a span that has a
// non-nil largeType field. Meaning it must be (>maxSmallSize-mallocHeaderSize).
// At the time of writing this is (32768 - 8).
type Large struct {
	ptr *uint8 // Object must contain a pointer to trigger code path.
	arr [32768 - 8]uint8
}

func useLarge(o *Large, ready chan<- struct{}) {
	o.ptr = &o.arr[5]
	o.arr[5] = 0xCA
	ready <- struct{}{}
	<-block
}

type AnyTree struct {
	root any
}

type anyNode struct {
	left, right any // *anyNode, to test direct eface type walking
	entry1      any // myPair, to test indirect eface type walking
	entry2      Xer // myPair, to test indirect iface type walking
}

func makeAnyTree(depth int64) any {
	if depth == 0 {
		return nil
	}
	e1 := myPair{depth, depth}
	e2 := myPair{depth, depth}
	n := &anyNode{
		left:   makeAnyTree(depth - 1),
		right:  makeAnyTree(depth - 1),
		entry1: e1,
		entry2: e2,
	}
	if depth%2 == 0 {
		// Test dealing with direct interfaces of wrapped structures.
		return anyNodeWrap2{{n}}
	}
	return n
}

//go:noinline
func (a *AnyTree) count() int {
	return countAnyNode(a.root)
}

func countAnyNode(a any) int {
	switch v := a.(type) {
	case *anyNode:
		return v.count()
	case anyNodeWrap2:
		return v.unwrap().count()
	}
	return 0
}

// This is load-bearing to make sure anyNodeWrap2 ends up in the binary.
//
//go:noinline
func (w anyNodeWrap2) unwrap() *anyNode {
	return w[0].anyNode
}

func (a *anyNode) count() int {
	return 1 + countAnyNode(a.left) + countAnyNode(a.right)
}

type anyNodeWrap struct{ *anyNode }
type anyNodeWrap2 [1]anyNodeWrap

type TypeSafeTree[K any] struct {
	root *typeSafeNode[K]
}

type typeSafeNode[K any] struct {
	left, right *typeSafeNode[K]
	entry       *K
}

func makeTypeSafeTree(depth int64) *typeSafeNode[myPair] {
	if depth == 0 {
		return nil
	}
	return &typeSafeNode[myPair]{
		left:  makeTypeSafeTree(depth - 1),
		right: makeTypeSafeTree(depth - 1),
		entry: &myPair{depth, depth},
	}
}

//go:noinline
func (t *TypeSafeTree[K]) count() int {
	return t.root.count()
}

func (t *typeSafeNode[K]) count() int {
	if t == nil {
		return 0
	}
	return 1 + t.left.count() + t.right.count()
}

type myPair struct {
	x, y int64
}

func (p myPair) X() int64 {
	return p.x
}

type Xer interface {
	X() int64
}

var globalAnyTree AnyTree
var globalAnyTreeFM func() int
var globalTypeSafeTree TypeSafeTree[myPair]
var globalTypeSafeTreeFM func() int

var block = make(chan struct{})

var a anyNode

func main() {
	globalAnyTree.root = makeAnyTree(5)
	globalTypeSafeTree.root = makeTypeSafeTree(5)

	ready := make(chan struct{})
	go func() {
		var anyTree AnyTree
		var anyTreeFM AnyTree // Captured in a method value.
		var typeSafeTree TypeSafeTree[myPair]
		var typeSafeTreeFM TypeSafeTree[myPair] // Captured in a method value.

		// TODO(mknyszek): AnyTree is described in DWARF in pieces, and we can't handle
		// that yet.
		//
		// anyTree.root = makeAnyTree(5)
		anyTreeFM.root = makeAnyTree(5)
		globalAnyTreeFM = anyTreeFM.count
		typeSafeTree.root = makeTypeSafeTree(5)
		typeSafeTreeFM.root = makeTypeSafeTree(5)
		globalTypeSafeTreeFM = typeSafeTreeFM.count

		ready <- struct{}{}
		<-block

		runtime.KeepAlive(anyTree)
		runtime.KeepAlive(typeSafeTree)
	}()

	// Create a large value and reference
	var o Large
	go useLarge(&o, ready) // Force an escape of o.
	o.arr[14] = 0xDE       // Prevent a future smart compiler from allocating o directly on useLarge's stack.

	// This is load-bearing to make sure anyNodeWrap2 and the count methods end up in the DWARF.
	println("tree counts:", globalAnyTree.count(), globalTypeSafeTree.count())

	// Make sure both goroutines are ready.
	<-ready
	<-ready

	_ = *(*int)(nil)

	runtime.KeepAlive(&o)
}
