// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package socket

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"
)

func TestSocket(t *testing.T) {
	const msg = "Zoich!"

	l, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	wc := make(chan string, 1)
	go func(c chan string) {
		w, err := Dial(os.Getuid(), os.Getpid())
		if err != nil {
			c <- fmt.Sprintf("dial: %v", err)
			return
		}
		defer w.Close()
		_, err = w.Write([]byte(msg))
		if err != nil {
			c <- fmt.Sprintf("write: %v", err)
			return
		}
		c <- ""
	}(wc)

	rc := make(chan string, 1)
	go func(c chan string) {
		r, err := l.Accept()
		if err != nil {
			c <- fmt.Sprintf("accept: %v", err)
			return
		}
		defer r.Close()
		s, err := ioutil.ReadAll(r)
		if err != nil {
			c <- fmt.Sprintf("readAll: %v", err)
			return
		}
		c <- string(s)
	}(rc)

	for wc != nil || rc != nil {
		select {
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timed out")
		case errStr := <-wc:
			if errStr != "" {
				t.Fatal(errStr)
			}
			wc = nil
		case got := <-rc:
			if got != msg {
				t.Fatalf("got %q, want %q", got, msg)
			}
			rc = nil
		}
	}
}

// TestCollectGarbage doesn't actually test anything, but it does collect any
// garbage sockets that are no longer used. It is a courtesy for computers that
// run this test suite often.
func TestCollectGarbage(t *testing.T) {
	CollectGarbage()
}
