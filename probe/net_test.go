// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package probe

import (
	"bufio"
	"io"
	"net"
	"os"
	"testing"
	"unsafe"

	"code.google.com/p/ogle/socket"
)

// traceThisFunction turns on tracing and returns a function to turn it off.
// It is intended for use as "defer traceThisFunction()()".
func traceThisFunction() func() {
	// TODO: This should be done atomically to guarantee the probe can see the update.
	tracing = true
	return func() {
		tracing = false
	}
}

type Conn struct {
	conn   net.Conn
	input  *bufio.Reader
	output *bufio.Writer
}

func (c *Conn) close() {
	c.output.Flush()
	c.conn.Close()
}

// newConn makes a connection.
func newConn(t *testing.T) *Conn {
	// defer traceThisFunction()()
	<-listening
	conn, err := socket.Dial(os.Getuid(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return &Conn{
		conn:   conn,
		input:  bufio.NewReader(conn),
		output: bufio.NewWriter(conn),
	}
}

// bytesToUint64 returns the uint64 stored in the 8 bytes of buf.
func bytesToUint64(buf []byte) uint64 {
	// We're using same machine here, so byte order is the same.
	// We can just fetch it, but on some architectures it
	// must be aligned so copy first.
	var tmp [8]byte
	copy(tmp[:], buf)
	return *(*uint64)(unsafe.Pointer(&tmp[0]))
}

// Test that we get an error back for a request to read an illegal address.
func TestReadBadAddress(t *testing.T) {
	//defer traceThisFunction()()

	conn := newConn(t)
	defer conn.close()

	// Read the elements in pseudo-random order.
	var tmp [100]byte
	// Request a read of a bad address.
	conn.output.WriteByte('r')
	// Address.
	n := putUvarint(tmp[:], uint64(base()-8))
	conn.output.Write(tmp[:n])
	// Length. Any length will do.
	n = putUvarint(tmp[:], 8)
	conn.output.Write(tmp[:n])
	// Send it.
	err := conn.output.Flush()
	if err != nil {
		t.Fatal(err)
	}
	// Read result.
	// We expect an initial non-zero value, the number of bytes of the error message.
	u, err := readUvarint(conn.input)
	if err != nil {
		t.Fatal(err)
	}
	if u == 0 {
		t.Fatalf("expected error return; got none")
	}
	// We expect a particular error.
	const expect = "invalid read address"
	if u != uint64(len(expect)) {
		t.Fatalf("got %d bytes; expected %d", u, len(expect))
	}
	_, err = io.ReadFull(conn.input, tmp[:u])
	if err != nil {
		t.Fatal(err)
	}
	msg := string(tmp[:u])
	if msg != expect {
		t.Fatalf("got %q; expected %q", msg, expect)
	}
}

// Test that we can read some data from the address space on the other side of the connection.
func TestReadUint64(t *testing.T) {
	//defer traceThisFunction()()

	conn := newConn(t)
	defer conn.close()

	// Some data to send over the wire.
	data := make([]uint64, 10)
	for i := range data {
		data[i] = 0x1234567887654321 + 12345*uint64(i)
	}
	// TODO: To be righteous we should put a memory barrier here.

	// Read the elements in pseudo-random order.
	var tmp [100]byte
	which := 0
	for i := 0; i < 100; i++ {
		which = (which + 7) % len(data)
		// Request a read of data[which].
		conn.output.WriteByte('r')
		// Address.
		n := putUvarint(tmp[:], uint64(addr(&data[which])))
		conn.output.Write(tmp[:n])
		// Length
		n = putUvarint(tmp[:], 8)
		conn.output.Write(tmp[:n])
		// Send it.
		err := conn.output.Flush()
		if err != nil {
			t.Fatal(err)
		}
		// Read result.
		// We expect 10 bytes: the initial zero, followed by 8 (the count), followed by 8 bytes of data.
		u, err := readUvarint(conn.input)
		if err != nil {
			t.Fatal(err)
		}
		if u != 0 {
			t.Fatalf("expected leading zero byte; got %#x\n", u)
		}
		// N bytes of data.
		u, err = readUvarint(conn.input)
		if err != nil {
			t.Fatal(err)
		}
		if u != 8 {
			t.Fatalf("got %d bytes of data; expected 8", u)
		}
		_, err = io.ReadFull(conn.input, tmp[:u])
		if err != nil {
			t.Fatal(err)
		}
		u = bytesToUint64(tmp[:u])
		if u != data[which] {
			t.Fatalf("got %#x; expected %#x", u, data[which])
		}
	}
}

// Test that we can read an array bigger than the pipe's buffer size.
func TestBigRead(t *testing.T) {
	// defer traceThisFunction()()

	conn := newConn(t)
	defer conn.close()

	// A big array.
	data := make([]byte, 3*len(pipe{}.buf))
	noise := 17
	for i := range data {
		data[i] = byte(noise)
		noise += 23
	}
	// TODO: To be righteous we should put a memory barrier here.

	// Read the elements in pseudo-random order.
	tmp := make([]byte, len(data))
	conn.output.WriteByte('r')
	// Address.
	n := putUvarint(tmp[:], uint64(addr(&data[0])))
	conn.output.Write(tmp[:n])
	// Length
	n = putUvarint(tmp[:], uint64(len(data)))
	conn.output.Write(tmp[:n])
	// Send it.
	err := conn.output.Flush()
	if err != nil {
		t.Fatal(err)
	}
	// Read result.
	// We expect the full data back.
	u, err := readUvarint(conn.input)
	if err != nil {
		t.Fatal(err)
	}
	if u != 0 {
		t.Fatalf("expected leading zero byte; got %#x\n", u)
	}
	// N bytes of data.
	u, err = readUvarint(conn.input)
	if err != nil {
		t.Fatal(err)
	}
	if u != uint64(len(data)) {
		t.Fatalf("got %d bytes of data; expected 8", u)
	}
	_, err = io.ReadFull(conn.input, tmp)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range data {
		if tmp[i] != c {
			t.Fatalf("at offset %d expected %#x; got %#x", i, c, tmp[i])
		}
	}
}

// TestCollectGarbage doesn't actually test anything, but it does collect any
// garbage sockets that are no longer used. It is a courtesy for computers that
// run this test suite often.
func TestCollectGarbage(t *testing.T) {
	socket.CollectGarbage()
}
