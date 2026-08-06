// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !aix && !plan9 && !wasm

package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandPath(t *testing.T) {
	root := &cobra.Command{Use: "root <arg>"}
	child := &cobra.Command{Use: "child"}
	grandchild := &cobra.Command{Use: "grandchild"}
	root.AddCommand(child)
	child.AddCommand(grandchild)

	tests := []struct {
		name string
		cmd  *cobra.Command
		want string
	}{
		{name: "root", cmd: root, want: "root <arg>"},
		{name: "child", cmd: child, want: "root <arg> child"},
		{name: "grandchild", cmd: grandchild, want: "root <arg> child grandchild"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := commandPath(test.cmd); got != test.want {
				t.Errorf("commandPath() = %q, want %q", got, test.want)
			}
		})
	}
}
