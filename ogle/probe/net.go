// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// TODO: Document the protocol once it settles.

package probe

import (
	"errors"
	"io" // Used only for the definitions of the various interfaces and errors.
	"net"

	"golang.org/x/debug/ogle/socket"
)

var (
	tracing   = false
	listening = make(chan struct{})
)

// init starts a listener and leaves it in the background waiting for connections.
func init() {
	go demon()
}

// demon answers consecutive connection requests and starts a server to manage each one.
// The server runs in the same goroutine as the demon, so a new connection cannot be
// established until the previous one is completed.
func demon() {
	listener, err := socket.Listen()
	close(listening)
	if err != nil {
		trace("listen:", err)
		return
	}
	trace("listening")
	for {
		conn, err := listener.Accept()
		if err != nil {
			trace("accept", err)
			continue
		}
		trace("accepted a connection")
		serve(conn)
		conn.Close()
	}
}

// stringer is the same as fmt.Stringer. We redefine it here to avoid pulling in fmt.
type stringer interface {
	String() string
}

func printHex(b byte) {
	const hex = "0123456789ABCDEF"
	b1, b0 := b>>4&0xF, b&0xF
	print(hex[b1:b1+1], hex[b0:b0+1])
}

// trace is a simple version of println that is enabled by the tracing boolean.
func trace(args ...interface{}) {
	if !tracing {
		return
	}
	print("ogle demon: ")
	for i, arg := range args {
		if i > 0 {
			print(" ")
		}
		// A little help. Built-in print isn't very capable.
		switch arg := arg.(type) {
		case stringer:
			print(arg.String())
		case error:
			print(arg.Error())
		case []byte:
			print("[")
			for i := range arg {
				if i > 0 {
					print(" ")
				}
				printHex(arg[i])
			}
			print("]")
		case int:
			print(arg)
		case string:
			print(arg)
		case uintptr:
			print("0x")
			for i := ptrSize - 1; i >= 0; i-- {
				printHex(byte(arg >> uint(8*i)))
			}
		default:
			print(arg)
		}
	}
	print("\n")
}

func serve(conn net.Conn) {
	const (
		bufSize = 1 << 16
	)
	var buf [bufSize]byte
	network := &pipe{
		rw: conn,
	}
	for {
		// One message per loop.
		n, err := network.Read(buf[:1])
		if n != 1 || err != nil {
			return
		}
		switch buf[0] {
		case 'r':
			// Read: ['r', address, size] => [0, size, size bytes]
			u, err := network.readUintptr()
			if err != nil {
				return
			}
			n, err := network.readInt()
			if err != nil {
				return
			}
			if !validRead(u, n) {
				trace("read", err)
				network.error("invalid read address")
				continue
			}
			network.sendReadResponse(u, n)
		default:
			// TODO: shut down connection?
			trace("unknown message type:", buf[0])
		}
	}
}

// pipe is a buffered network connection (actually just a reader/writer) that
// implements Read and ReadByte as well as readFull.
// It also has support routines to make it easier to read and write
// network messages.
type pipe struct {
	rw      io.ReadWriter
	pos     int
	end     int
	oneByte [1]byte
	buf     [4096]byte
}

// readFull fills the argument slice with data from the wire. If it cannot fill the
// slice, it returns an error.
// TODO: unused for now; write will need it.
func (p *pipe) readFull(buf []byte) error {
	for len(buf) > 0 {
		n, err := p.rw.Read(buf)
		if n == len(buf) {
			return nil
		}
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
		if n == 0 {
			return io.EOF
		}
		buf = buf[n:]
	}
	return nil
}

// Read satisfies io.Reader.
func (p *pipe) Read(buf []byte) (int, error) {
	n := len(buf)
	if p.end == p.pos {
		p.pos = 0
		// Read from network
		var err error
		p.end, err = p.rw.Read(p.buf[:])
		if err != nil {
			trace("read:", err)
			return p.end, err
		}
		if p.end == 0 {
			trace("read: eof")
			return p.end, io.EOF
		}
	}
	if n > p.end-p.pos {
		n = p.end - p.pos
	}
	copy(buf, p.buf[p.pos:p.pos+n])
	p.pos += n
	return n, nil
}

// ReadByte satisfies io.ByteReader.
func (p *pipe) ReadByte() (byte, error) {
	_, err := p.Read(p.oneByte[:])
	return p.oneByte[0], err
}

// readUintptr reads a varint-encoded uinptr value from the connection.
func (p *pipe) readUintptr() (uintptr, error) {
	u, err := readUvarint(p)
	if err != nil {
		trace("read uintptr:", err)
		return 0, err
	}
	if u > uint64(^uintptr(0)) {
		trace("read uintptr: overflow")
		return 0, err
	}
	return uintptr(u), nil
}

var intOverflow = errors.New("ogle probe: varint overflows int")

// readInt reads an varint-encoded int value from the connection.
// The transported value is always a uint64; this routine
// verifies that it fits in an int.
func (p *pipe) readInt() (int, error) {
	u, err := readUvarint(p)
	if err != nil {
		trace("read int:", err)
		return 0, err
	}
	// Does it fit in an int?
	if u > maxInt {
		trace("int overflow")
		return 0, intOverflow
	}
	return int(u), nil
}

// error writes an error message to the connection.
// The format is [size, size bytes].
func (p *pipe) error(msg string) {
	// A zero-length message is problematic. It should never arise, but be safe.
	if len(msg) == 0 {
		msg = "undefined error"
	}
	// Truncate if necessary. Extremely unlikely.
	if len(msg) > len(p.buf)-maxVarintLen64 {
		msg = msg[:len(p.buf)-maxVarintLen64]
	}
	n := putUvarint(p.buf[:], uint64(len(msg)))
	n += copy(p.buf[n:], msg)
	_, err := p.rw.Write(p.buf[:n])
	if err != nil {
		trace("write:", err)
		// TODO: shut down connection?
	}
}

// sendReadResponse sends a read response to the connection.
// The format is [0, size, size bytes].
func (p *pipe) sendReadResponse(addr uintptr, size int) {
	trace("sendRead:", addr, size)
	m := 0
	m += putUvarint(p.buf[m:], 0)            // No error.
	m += putUvarint(p.buf[m:], uint64(size)) // Number of bytes to follow.
	for m > 0 || size > 0 {
		n := len(p.buf) - m
		if n > size {
			n = size
		}
		if !read(addr, p.buf[m:m+n]) {
			trace("copy error")
			// TODO: shut down connection?
			// TODO: for now, continue delivering data. We said we would.
		}
		_, err := p.rw.Write(p.buf[:m+n])
		if err != nil {
			trace("write:", err)
			// TODO: shut down connection?
		}
		addr += uintptr(n)
		size -= n
		// Next time we can use the whole buffer.
		m = 0
	}
}
