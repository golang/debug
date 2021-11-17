// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dwtest_test

// This file contains a set of DWARF variable location generation
// tests that are intended to compliment the existing linker DWARF
// tests. The tests make use of a harness / utility program
// "dwdumploc" that is built during test setup and then
// invoked (fork+exec) in testpoints. We do things this way (as
// opposed to just incorporating all of the source code from
// testdata/dwdumploc.go into this file) so that the dumper code can
// import packages from Delve without needing to vendor everything
// into the Go distribution itself.
//
// Notes on GOARCH/GOOS support: this test is guarded to execute only
// on arch/os combinations supported by Delve (see the testpoint
// below); as Delve evolves we may need to update accordingly.
//
// This test requires network support (the harness build has to
// download packages), so only runs in "long" test mode at the moment,
// and since we don't currently have longtest builders for every
// arch/os pair that Delve supports (ex: no linux/arm64 longtest
// builder, Issue #49649), this is something to keep in mind when
// running trybots etc.
//

import (
	"bytes"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/debug/internal/testenv"
)

// copyFilesForHarness copies various files into the build dir for the
// harness, including the main package, go.mod, and a copy of the
// dwtest package (the latter is why we are doing an explicit copy as
// opposed to just building directly from sources in testdata).
// Return value is the path to a build directory for the harness
// build.
func copyFilesForHarness(t *testing.T) string {
	mkdir := func(d string) {
		if err := os.Mkdir(d, 0777); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}
	}
	cp := func(from, to string) {
		var payload []byte
		payload, err := ioutil.ReadFile(from)
		if err != nil {
			t.Fatalf("ioutil.ReadFile failed: %v", err)
		}
		if err = ioutil.WriteFile(to, payload, 0644); err != nil {
			t.Fatalf("ioutil.WriteFile failed: %v", err)
		}
	}
	join := filepath.Join
	bd := join(t.TempDir(), "build")
	bdt := join(bd, "dwtest")
	mkdir(bd)
	mkdir(bdt)
	cp(join("testdata", "dwdumploc.go"), join(bd, "main.go"))
	cp(join("testdata", "go.mod.txt"), join(bd, "go.mod"))
	cp(join("testdata", "go.sum.txt"), join(bd, "go.sum"))
	cp("dwtest.go", join(bdt, "dwtest.go"))
	return bd
}

// buildHarness builds the helper program "dwdumploc.exe".
func buildHarness(t *testing.T) string {

	// Copy source files into build dir.
	bd := copyFilesForHarness(t)

	// Run build.
	harnessPath := filepath.Join(t.TempDir(), "dumpdwloc.exe")
	cmd := exec.Command(testenv.GoToolPath(t), "build", "-o", harnessPath)
	cmd.Dir = bd
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed (%v): %s", err, b)
	}
	return harnessPath
}

// runHarness runs our previously built harness exec on a Go binary
// 'exePath' for function 'fcn' and returns the results. Stderr from
// the harness is printed to test stderr. Note: to debug the harness,
// try adding "-v=2" to the exec.Command below.
func runHarness(t *testing.T, harnessPath string, exePath string, fcn string) string {
	cmd := exec.Command(harnessPath, "-m", exePath, "-f", fcn)
	var b bytes.Buffer
	cmd.Stderr = os.Stderr
	cmd.Stdout = &b
	if err := cmd.Run(); err != nil {
		t.Fatalf("running 'harness -m %s -f %s': %v", exePath, fcn, err)
	}
	return strings.TrimSpace(string(b.Bytes()))
}

// gobuild is a helper to bulid a Go program from source code,
// so that we can inspect selected bits of DWARF in the resulting binary.
// Return value is binary path.
func gobuild(t *testing.T, sourceCode string, pname string) string {
	spath := filepath.Join(t.TempDir(), pname+".go")
	if err := ioutil.WriteFile(spath, []byte(sourceCode), 0644); err != nil {
		t.Fatalf("write to %s failed: %s", spath, err)
	}
	epath := filepath.Join(t.TempDir(), pname+".exe")

	// A note on this build: Delve currently has problems digesting
	// PIE binaries on Windows; until this can be straightened out,
	// default to "exe" buildmode.
	cmd := exec.Command(testenv.GoToolPath(t), "build", "-buildmode=exe", "-o", epath, spath)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Logf("%% build output: %s\n", b)
		t.Fatalf("build failed: %s", err)
	}
	return epath
}

const programSourceCode = `
package main

import "context"

var G int

//go:noinline
func another(x int) {
	println(G)
}

//go:noinline
func docall(f func()) {
	f()
}

//go:noinline
func Issue47354(s string) {
	docall(func() {
		println("s is", s)
	})
	G++
	another(int(s[0]))
}

type DB int
type driverConn int
type Result interface {
	Foo()
}

//go:noinline
func (db *DB) Issue46845(ctx context.Context, dc *driverConn, release func(error), query string, args []interface{}) (res Result, err error) {
	defer func() {
		release(err)
        println(len(args))
	}()
	return nil, nil
}

func main() {
	Issue47354("poo")
	var d DB
	d.Issue46845(context.Background(), nil, func(error) {}, "foo", nil)
}

`

func testIssue47354(t *testing.T, harnessPath string, ppath string) {
	expected := map[string]string{
		"amd64": "1: in-param \"s\" loc=\"{ [0: S=8 RAX] [1: S=8 RBX] }\"",
		"arm64": "1: in-param \"s\" loc=\"{ [0: S=8 R0] [1: S=8 R1] }\"",
	}
	fname := "Issue47354"
	got := runHarness(t, harnessPath, ppath, "main."+fname)
	want := expected[runtime.GOARCH]
	if got != want {
		t.Errorf("failed Issue47354 arch %s:\ngot: %q\nwant: %q",
			runtime.GOARCH, got, want)
	}
}

func testIssue46845(t *testing.T, harnessPath string, ppath string) {

	// NB: note the "addr=0x1000" for the stack-based parameter "args"
	// below. This is not an accurate stack location, it's just an
	// artifact of the way we call into Delve.
	expected := map[string]string{
		"amd64": `
1: in-param "db" loc="{ [0: S=0 RAX] }"
2: in-param "ctx" loc="{ [0: S=8 RBX] [1: S=8 RCX] }"
3: in-param "dc" loc="{ [0: S=0 RDI] }"
4: in-param "release" loc="{ [0: S=0 RSI] }"
5: in-param "query" loc="{ [0: S=8 R8] [1: S=8 R9] }"
6: in-param "args" loc="{ [0: S=8 addr=0x1000] [1: S=8 addr=0x1008] [2: S=8 addr=0x1010] }"
7: out-param "res" loc="<not available>"
8: out-param "err" loc="<not available>"
`,
		"arm64": `
1: in-param "db" loc="{ [0: S=0 R0] }"
2: in-param "ctx" loc="{ [0: S=8 R1] [1: S=8 R2] }"
3: in-param "dc" loc="{ [0: S=0 R3] }"
4: in-param "release" loc="{ [0: S=0 R4] }"
5: in-param "query" loc="{ [0: S=8 R5] [1: S=8 R6] }"
6: in-param "args" loc="{ [0: S=8 R7] [1: S=8 R8] [2: S=8 R9] }"
7: out-param "res" loc="<not available>"
8: out-param "err" loc="<not available>"
`,
	}
	fname := "(*DB).Issue46845"
	got := runHarness(t, harnessPath, ppath, "main."+fname)
	want := strings.TrimSpace(expected[runtime.GOARCH])
	if got != want {
		t.Errorf("failed Issue47354 arch %s:\ngot: %s\nwant: %s",
			runtime.GOARCH, got, want)
	}
}

func TestDwarfVariableLocations(t *testing.T) {
	testenv.NeedsGo1Point(t, 18)
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExternalNetwork(t)

	// A note on the guard below:
	// - Delve doesn't officially support darwin/arm64, but I've run
	//   this test by hand on darwin/arm64 and it seems to work, so
	//   it is included for the moment
	// - the harness code currently only supports amd64 + arm64. If more
	//   archs are added (ex: 386) the harness will need to be updated.
	pair := runtime.GOOS + "/" + runtime.GOARCH
	switch pair {
	case "linux/amd64", "linux/arm64", "windows/amd64",
		"darwin/amd64", "darwin/arm64":
	default:
		t.Skipf("unsupported OS/ARCH pair %s (this tests supports only OS values supported by Delve", pair)
	}

	// Build test harness.
	harnessPath := buildHarness(t)

	// Build program to inspect. NB: we're building at default (with
	// optimization); it might also be worth doing a "-l -N" build
	// to verify the location expressions in that case.
	ppath := gobuild(t, programSourceCode, "prog")

	// Sub-tests for each function we want to inspect.
	t.Run("Issue47354", func(t *testing.T) {
		t.Parallel()
		testIssue47354(t, harnessPath, ppath)
	})
	t.Run("Issue46845", func(t *testing.T) {
		t.Parallel()
		testIssue46845(t, harnessPath, ppath)
	})
}
