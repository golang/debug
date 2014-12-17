// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package socket provides a way for multiple processes from the same user to
// communicate over a Unix domain socket.
package socket // import "golang.org/x/debug/ogle/socket"

// TODO: euid instead of uid?
// TODO: Windows support.

import (
	"net"
	"os"
	"syscall"
)

// atoi is like strconv.Atoi but we aim to minimize this package's dependencies.
func atoi(s string) (i int, ok bool) {
	for _, c := range s {
		if c < '0' || '9' < c {
			return 0, false
		}
		i = 10*i + int(c-'0')
	}
	return i, true
}

// itoa is like strconv.Itoa but we aim to minimize this package's dependencies.
func itoa(i int) string {
	var buf [30]byte
	n := len(buf)
	neg := false
	if i < 0 {
		i = -i
		neg = true
	}
	ui := uint(i)
	for ui > 0 || n == len(buf) {
		n--
		buf[n] = byte('0' + ui%10)
		ui /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}

func names(uid, pid int) (dirName, socketName string) {
	dirName = "/tmp/ogle-socket-uid" + itoa(uid)
	socketName = dirName + "/pid" + itoa(pid)
	return
}

// Listen creates a PID-specific socket under a UID-specific sub-directory of
// /tmp. That sub-directory is created with 0700 permission bits (before
// umasking), so that only processes with the same UID can dial that socket.
func Listen() (net.Listener, error) {
	dirName, socketName := names(os.Getuid(), os.Getpid())
	if err := os.MkdirAll(dirName, 0700); err != nil {
		return nil, err
	}
	if err := os.Remove(socketName); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return net.Listen("unix", socketName)
}

// Dial dials the Unix domain socket created by the process with the given UID
// and PID.
func Dial(uid, pid int) (net.Conn, error) {
	_, socketName := names(uid, pid)
	return net.Dial("unix", socketName)
}

// CollectGarbage deletes any no-longer-used sockets in the UID-specific sub-
// directory of /tmp.
func CollectGarbage() {
	dirName, _ := names(os.Getuid(), os.Getpid())
	dir, err := os.Open(dirName)
	if err != nil {
		return
	}
	defer dir.Close()
	fileNames, err := dir.Readdirnames(-1)
	if err != nil {
		return
	}
	for _, fileName := range fileNames {
		if len(fileName) < 3 || fileName[:3] != "pid" {
			continue
		}
		pid, ok := atoi(fileName[3:])
		if !ok {
			continue
		}
		// See if there is a process with the given PID. The os.FindProcess function
		// looks relevant, but on Unix that always succeeds even if there is no such
		// process. Instead, we send signal 0 and look for ESRCH.
		if syscall.Kill(pid, 0) != syscall.ESRCH {
			continue
		}
		os.Remove(dirName + "/" + fileName)
	}
}
