// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// TODO #include "../../cmd/ld/textflag.h"

#define NOSPLIT 4

// Functions that return details about our address space.
// They use the C-defined symbols like edata and also know
// a little about the heap and memory layout.

// From the linker. Well-known but might change.
// TODO: Is there a better way to know this?
#define	INITTEXT 0x2000

// base of the address space.
TEXT ·base(SB), NOSPLIT, $0
	MOVQ	$INITTEXT, ret+0(FP)
	RET

// end of the text segment.
TEXT ·etext(SB), NOSPLIT, $0
	LEAQ	etext+0(SB), BX
	MOVQ	BX, ret+0(FP)
	RET

// end of the data segment.
TEXT ·edata(SB), NOSPLIT, $0
	LEAQ	edata+0(SB), BX
	MOVQ	BX, ret+0(FP)
	RET

// end of the pre-defined address space.
TEXT ·end(SB), NOSPLIT, $0
	LEAQ	end+0(SB), BX
	MOVQ	BX, ret+0(FP)
	RET

// These are offsets of critical fields of runtime.mheap.
// TODO: Very susceptible to change! They (or something equivalent) need to be published by runtime.
#define arena_start_offset 14504
#define arena_used_offset 14512
#define arena_end_offset 14520

// start of heap.
TEXT ·heapStart(SB), NOSPLIT, $0
	MOVQ	runtime·mheap+arena_start_offset(SB), BX
	MOVQ	BX, ret+0(FP)
	RET

// end of active region of heap.
TEXT ·heapUsed(SB), NOSPLIT, $0
	MOVQ	runtime·mheap+arena_used_offset(SB), BX
	MOVQ	BX, ret+0(FP)
	RET

// end of all of heap.
TEXT ·heapEnd(SB), NOSPLIT, $0
	MOVQ	runtime·mheap+arena_end_offset(SB), BX
	MOVQ	BX, ret+0(FP)
	RET
