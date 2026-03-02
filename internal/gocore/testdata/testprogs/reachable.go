// Copyright 2025 The Go Authors.  All rights reserved.
// Use of this srcFile code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

// Tests to make sure Reachable finds all paths.
//
// Note, the goroutine roots are all marked go:noinline. Without it, they look
// like a single frame:
//
//	gocore_test.go:498: [goroutine]
//	gocore_test.go:505: 0x55a67cc8e53b main.main.func1.gowrap3
//	gocore_test.go:533:         unk     (SP+0xb8) 0xc00004b7c8 (unsafe.Pointer) → 0xc000078000 (main.myObj)

package main

import (
	"os"
	"unsafe"

	"golang.org/x/debug/internal/testenv"
)

type myObj struct {
	x []byte
	y complex128
	z func()
	w [12]int
	v string
}

type anyWrapper struct {
	container any
}

type compoundWrapper struct {
	x         int
	container *myObj
}

// multiWrapper contains within it multiple paths to a [myObj].
type multiWrapper struct {
	mx        int
	container *myObj
	my        int
	sub       *compoundWrapper
}

var (
	gPlainMyObj *myObj
	// TODO: uncommenting and assigning this makes [gPlainMyObj] invisible
	// in favour of [gCompoundWrapper]. That shouldn't happen
	// gCompoundWrapper compoundWrapper
)

func main() {
	testenv.RunThenCrash(os.Getenv("GO_DEBUG_TEST_COREDUMP_FILTER"), func() any {
		obj := &myObj{v: "oh my"}
		println("OBJPOINTER", obj) // Give the test driver a pointer value it can search for.

		ready := make(chan struct{})
		done := make(chan struct{})

		// Goroutine roots.
		{
			go retain(obj, ready, done)            // ref
			go multiFrame1Retain(obj, ready, done) // ref

			// Renaming.
			go renameRetain(obj, ready, done)     // otherRef (but keeps ref too, which is a bug)
			go hardRenameRetain(obj, ready, done) // otherRef

			// Aliasing.
			go aliasRetain(obj, ready, done)     // ref, otherRef
			go hardAliasRetain(obj, ready, done) // ref, otherRef

			// Compound objects.
			go complicatedRetain(obj, ready, done) // ref, anyWrapperRef, compoundWrapperRef, multiWrapperRefReuse, multiWrapperRef (should have compoundWrapper too, but that's a bug)
		}

		// Global roots.
		gPlainMyObj = obj
		// gCompoundWrapper.container = obj // TODO: see TODO on global.

		obj = nil // Nil out so we don't see a reference from main.
		<-ready
		<-ready
		<-ready
		<-ready
		<-ready
		<-ready
		<-ready
		return nil
	})
}

//go:noinline
func complicatedRetain(ref *myObj, ready, done chan struct{}) {
	// TODO(aktau): fix the anyWrapper being unnamed (name: "unk").
	anyWrapperRef := &anyWrapper{container: ref}
	// TODO(aktau): fix the compoundWrapper being unnamed (name: "unk").
	compoundWrapperRef := &compoundWrapper{container: ref}
	// TODO(aktau): fix the multiWrapper being unnamed (name: "unk").
	// TODO(aktau): fix only one path being printed for this object.
	multiWrapperRefReuse := &multiWrapper{container: ref, sub: compoundWrapperRef}
	// TODO(aktau): fix the multiWrapper being unnamed (name: "unk").
	// TODO(aktau): fix only one path being printed for this object.
	multiWrapperRef := &multiWrapper{container: ref, sub: &compoundWrapper{container: ref}}
	// TODO(aktau): fix the reference not being found at all if we don't take the
	// address of anyWrapper/compoundWrapper.
	compoundWrapperVal := compoundWrapper{container: ref}

	ready <- struct{}{}
	// PC is here at crash time.
	<-done

	// Print the values so the GC doesn't consider them dead. It also makes it
	// easier to get the association of which value is at which (stack) position
	// by reading the runtime.print(pointer|string|eface) calls in the disasm.
	println("I am retaining", ref.v, "which should be the same as",
		anyWrapperRef,
		anyWrapperRef.container,
		compoundWrapperRef.container.v,
		compoundWrapperRef,
		compoundWrapperVal.container.v,
		multiWrapperRefReuse,
		multiWrapperRefReuse.container,
		multiWrapperRefReuse.container.v,
		multiWrapperRefReuse.sub,
		multiWrapperRefReuse.sub.container.v,
		multiWrapperRef,
		multiWrapperRef.container,
		multiWrapperRef.container.v,
		multiWrapperRef.sub,
		multiWrapperRef.sub.container.v,
	)
}

// retain retains a reference to the incoming ref.
//
//go:noinline
func retain(ref *myObj, ready, done chan struct{}) {
	ready <- struct{}{}
	<-done
	println("I am retaining", ref.v)
}

// renameRetain is like [retain], but renames the incoming parameter [ref] to
// [otherRef].
//
//go:noinline
func renameRetain(ref *myObj, ready, done chan struct{}) {
	otherRef := ref // After this, there are no more references to ref.

	// TODO: viewcore sees ref as live. It shouldn't. This might be something
	// subtle in DWARF, as the compiler has already "deduplicated" the memory
	// location (essentially renaming).
	//
	//  gocore_test.go:498: [goroutine]
	//  gocore_test.go:505: 0x5625676167a8 main.main.func1.gowrap2
	//  gocore_test.go:505: 0x562567616273 main.renameRetain
	//  gocore_test.go:533:         otherRef        (SP+0x30) 0xc00004afb8 (*main.myObj) → 0xc000078000 (main.myObj)
	//
	//  gocore_test.go:498: [goroutine]
	//  gocore_test.go:505: 0x5625676167a8 main.main.func1.gowrap2
	//  gocore_test.go:505: 0x562567616273 main.renameRetain
	//  gocore_test.go:533:         ref     (SP+0x30) 0xc00004afb8 (*main.myObj) → 0xc000078000 (main.myObj)
	//
	// See the equivalent [hardRenameParamRetain], which is effectively the same,
	// but viewcore no longer sees ref as live, further implying DWARF.
	ref = nil // This shouldn't even be necessary, as Go knows the liveness range. But it's not working for viewcore any way.

	ready <- struct{}{}
	// PC is here at crash time.
	<-done

	println("I am retaining", otherRef.v) // Print the values so the GC doesn't consider them dead
}

// hardRenameRetain is like [renameRetain], but obscures the renaming to the
// compiler.
//
//go:noinline
func hardRenameRetain(ref *myObj, ready, done chan struct{}) {
	otherRef := unalias(ref)

	ready <- struct{}{}
	// PC is here at crash time.
	<-done

	println("I am retaining", otherRef.v) // Print the values so the GC doesn't consider them dead
}

// multiFrame1Retain is just an empty frame to "complexify" the program and make
// stack traces stand out more.
//
//go:noinline
func multiFrame1Retain(ref *myObj, ready, done chan struct{}) { multiFrame2Retain(ref, ready, done) }

//go:noinline
func multiFrame2Retain(ref *myObj, ready, done chan struct{}) { multiFrame3Retain(ref, ready, done) }

//go:noinline
func multiFrame3Retain(ref *myObj, ready, done chan struct{}) { multiFrame4Retain(ref, ready, done) }

// NOTE: no go:noinline marker to test this variant
func multiFrame4Retain(ref *myObj, ready, done chan struct{}) { retain(ref, ready, done) }

// aliasRetain is like [renameRetain], but keeps the original name
// (ref) intact.
//
//go:noinline
func aliasRetain(ref *myObj, ready, done chan struct{}) {
	otherRef := ref

	ready <- struct{}{}
	// PC is here at crash time.
	<-done

	println("I am retaining", ref.v, "which should be the same as", otherRef.v) // Print the values so the GC doesn't consider them dead.
}

// hardAliasRetain is like [aliasRetain], but tries harder to avoid
// the compiler aliasing the roots to the same memory location.
//
//go:noinline
func hardAliasRetain(ref *myObj, ready, done chan struct{}) {
	otherRef := unalias(ref)

	ready <- struct{}{}
	// PC is here at crash time.
	<-done

	println("I am retaining", ref.v, "which should be the same as", otherRef.v) // Print the values so the GC doesn't consider them dead.
}

var off uintptr = 0x0 // Throw the compiler off.

//go:noinline
func unalias(in *myObj) *myObj {
	return (*myObj)(unsafe.Add(unsafe.Pointer(in), off))
}
