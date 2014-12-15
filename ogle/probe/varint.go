// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package probe

// This file contains an implementation of "varint" encoding and decoding.
// Code is adapted from encoding/binary/varint.go, copied here to avoid dependencies,
// simplified somewhat, and made local to the package.
// It handles unsigned integers only.

import (
	"errors"
	"io"
)

// maxVarintLenN is the maximum length of a varint-encoded N-bit integer.
const (
	maxVarintLen16 = 3
	maxVarintLen32 = 5
	maxVarintLen64 = 10
)

// putUvarint encodes a uint64 into buf and returns the number of bytes written.
// If the buffer is too small, putUvarint will panic.
func putUvarint(buf []byte, x uint64) int {
	i := 0
	for x >= 0x80 {
		buf[i] = byte(x) | 0x80
		x >>= 7
		i++
	}
	buf[i] = byte(x)
	return i + 1
}

// getUvarint decodes a uint64 from buf and returns that value and the
// number of bytes read (> 0). If an error occurred, the value is 0
// and the number of bytes n is <= 0 meaning:
//
//	n == 0: buf too small
//	n  < 0: value larger than 64 bits (overflow)
//              and -n is the number of bytes read
//
// TODO: Unused. Delete if it doesn't get used.
func getUvarint(buf []byte) (uint64, int) {
	var x uint64
	var s uint
	for i, b := range buf {
		if b < 0x80 {
			if i > 9 || i == 9 && b > 1 {
				return 0, -(i + 1) // overflow
			}
			return x | uint64(b)<<s, i + 1
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	return 0, 0
}

var overflow = errors.New("ogle probe: varint overflows a 64-bit integer")

// readUvarint reads an encoded unsigned integer from r and returns it as a uint64.
func readUvarint(r io.ByteReader) (uint64, error) {
	var x uint64
	var s uint
	for i := 0; ; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return x, err
		}
		if b < 0x80 {
			if i > 9 || i == 9 && b > 1 {
				return x, overflow
			}
			return x | uint64(b)<<s, nil
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
}
