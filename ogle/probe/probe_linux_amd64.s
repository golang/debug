// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// TODO #include "../../cmd/ld/textflag.h"

#define	NOSPLIT 4

// Functions that return details about our address space.
// They use the C-defined symbols like edata and also know
// a little about the heap and memory layout.

// From the linker. Well-known but might change.
// TODO: Is there a better way to know this?
#define	ELFRESERVE	3072
#define	INITTEXT ((1<<22)+ELFRESERVE)

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
