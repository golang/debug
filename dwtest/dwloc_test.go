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
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/debug/internal/testenv"
)

var preserveTemp = flag.Bool("keep", false, "keep tmpdir files for debugging")

// copyFilesForHarness copies various files into the build dir for the
// harness, including the main package, go.mod, and a copy of the
// dwtest package (the latter is why we are doing an explicit copy as
// opposed to just building directly from sources in testdata).
// Return value is the path to a build directory for the harness
// build.
func copyFilesForHarness(t *testing.T, dir string) string {
	mkdir := func(d string) {
		if err := os.Mkdir(d, 0777); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}
	}
	cp := func(from, to string) {
		var payload []byte
		payload, err := os.ReadFile(from)
		if err != nil {
			t.Fatalf("os.ReadFile failed: %v", err)
		}
		if err = os.WriteFile(to, payload, 0644); err != nil {
			t.Fatalf("os.WriteFile failed: %v", err)
		}
	}
	join := filepath.Join
	bd := join(dir, "build")
	bdt := join(bd, "dwtest")
	mkdir(bd)
	mkdir(bdt)
	cp(join("testdata", "dwdumploc.go"), join(bd, "main.go"))
	cp(join("testdata", "go.mod.txt"), join(bd, "go.mod"))
	cp(join("testdata", "go.sum.txt"), join(bd, "go.sum"))
	cp("dwtest.go", join(bdt, "dwtest.go"))
	return bd
}

// buildHarness builds the helper program "dwdumploc.exe"
// and a companion executable "dwdumploc.noopt.exe", built
// with "-gcflags=all=-l -N".
func buildHarness(t *testing.T, dir string) (string, string) {

	// Copy source files into build dir.
	bd := copyFilesForHarness(t, dir)

	// Run builds.
	harnessPath := filepath.Join(dir, "dumpdwloc.exe")
	cmd := exec.Command(testenv.GoToolPath(t), "build", "-trimpath", "-o", harnessPath)
	cmd.Dir = bd
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed (%v): %s", err, b)
	}

	nooptHarnessPath := filepath.Join(dir, "dumpdwloc.exe")
	cmd = exec.Command(testenv.GoToolPath(t), "build", "-trimpath", "-gcflags=all=-l -N", "-o", nooptHarnessPath)
	cmd.Dir = bd
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed (%v): %s", err, b)
	}
	return harnessPath, nooptHarnessPath
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
	return strings.TrimSpace(b.String())
}

// gobuild is a helper to build a Go program from source code,
// so that we can inspect selected bits of DWARF in the resulting binary.
// The first return value is the path to the binary compiled with optimizations,
// the second is the path to the binary compiled without optimizations.
func gobuild(t *testing.T, sourceCode string, pname string, dir string) (string, string) {
	spath := filepath.Join(dir, pname+".go")
	if err := os.WriteFile(spath, []byte(sourceCode), 0644); err != nil {
		t.Fatalf("write to %s failed: %s", spath, err)
	}
	epath := filepath.Join(dir, pname+".exe")
	nooppath := filepath.Join(dir, pname+".noop.exe")

	// A note on this build: Delve currently has problems digesting
	// PIE binaries on Windows; until this can be straightened out,
	// default to "exe" buildmode.
	cmd := exec.Command(testenv.GoToolPath(t), "build", "-trimpath", "-buildmode=exe", "-o", epath, spath)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Logf("%% build output: %s\n", b)
		t.Fatalf("build failed: %s", err)
	}
	cmd = exec.Command(testenv.GoToolPath(t), "build", "-gcflags=-N -l", "-trimpath", "-buildmode=exe", "-o", nooppath, spath)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Logf("%% build output: %s\n", b)
		t.Fatalf("build failed: %s", err)
	}
	return epath, nooppath
}

const programSourceCode = `
package main

import (
	"context"
	"strings"
	"fmt"
	"net/http"
)

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

//go:noinline
func Issue72053() {
		u := Address{Addr: "127.0.0.1"}
		fmt.Println(u)
}

type Address struct {
		TLS  bool
		Addr string
}

//go:noinline
func (a Address) String() string {
		sb := new(strings.Builder)
		sb.WriteString(a.Addr)
		return sb.String()
}

//go:noinline
func Issue65405(a int, b string) (int, error) {
	http.Handle("/", http.StripPrefix("/static/", http.FileServer(http.Dir("./output"))))
	return a + len(b), nil
}

//go:noinline
func RegisterLivenessNamedRetParam() (result int) {
	// This function demonstrates the register reuse issue.
	// The return value 'result' should only be valid in its register
	// after it's actually set, not throughout the entire function.

	// Early in the function, do some work that uses registers
	x := 42
	y := 100
	z := x * y  // Register allocator may use RAX here for multiplication

	// Do more work that might reuse the return register
	for i := 0; i < 10; i++ {
		z += i  // More register usage
	}

	// Only NOW do we actually set the return value
	result = z  // Return value is only valid in RAX from here

	// A debugger querying 'result' before this point would get
	// incorrect values if the location list claims it's in RAX
	// for the entire function
	return result
}

//go:noinline
func RegisterLivenessUnnamedRetParam() int {
	x := 42
	y := 100
	z := x * y

	for i := 0; i < 10; i++ {
		z += i  // More register usage
	}

	return z
}

//go:noinline
func multiReturn() (int, int, int, string, float64) {
	return 1, 2, 3, "test", 4.5
}

//go:noinline
func singleReturn() int {
	return 42
}

//go:noinline
func multiReturnStmts(i int) int {
	if i < 10 {
		return 55
	}
	i += 100
	i *= i
	return i
}

//go:noinline
func ManyArgsWithNamedReturns(a, b, c, d, e, f, g, h, i, j, k, l, m, n, o, p int) (sum int, product int) {
	// Use all arguments in computations
	sum = a + b + c + d + e + f + g + h
	product = 1

	// More operations to ensure all args are used
	temp1 := i * j
	temp2 := k * l
	temp3 := m * n
	temp4 := o * p

	sum += temp1 + temp2 + temp3 + temp4

	// Compute product using all arguments
	product = (a + 1) * (b + 1) * (c + 1) * (d + 1)
	product += (e + f) * (g + h) * (i + j) * (k + l)
	product += (m + n) * (o + p)

	// Final adjustments
	sum = sum * 2
	product = product / 2

	return sum, product
}

func main() {
	Issue47354("poo")
	var d DB
	d.Issue46845(context.Background(), nil, func(error) {}, "foo", nil)
	Issue72053()
	_, _ = Issue65405(42, "test")
	a, b, _, _, _ := multiReturn()
	f := singleReturn()
	_ = a + b + f
	_ = RegisterLivenessNamedRetParam()
	_ = RegisterLivenessUnnamedRetParam()
	_, _ = ManyArgsWithNamedReturns(1, 2, 3, 4, 5, 6, a, b, f, 1, 2, 3, 4, 5, 6, 7)
	_ = multiReturnStmts(a+b)
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
7: out-param "res" at RET[0] loc="addr=f98"
7: out-param "res" at RET[1] loc="addr=f98"
8: out-param "err" at RET[0] loc="addr=fa8"
8: out-param "err" at RET[1] loc="addr=fa8"
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
		t.Errorf("failed Issue46845 arch %s:\ngot: %s\nwant: %s",
			runtime.GOARCH, got, want)
	}
}

func testIssue72053(t *testing.T, harnessPath string, ppath string) {
	testenv.NeedsGo1Point(t, 25)
	testenv.NeedsArch(t, "amd64")

	want := "1: in-param \"a\" loc=\"{ [0: S=1 RAX] [1: S=7 addr=0x0] [2: S=8 RBX] [3: S=8 RCX] }\"\n2: out-param \"~r0\" at RET[0] loc=\"addr=fa8\""
	got := runHarness(t, harnessPath, ppath, "main.Address.String")
	if got != want {
		t.Errorf("failed Issue72053 arch %s:\ngot: %q\nwant: %q",
			runtime.GOARCH, got, want)
	}
}

// testRuntimeThrow verifies that we have well-formed DWARF for the
// single input parameter of 'runtime.throw'. This function is
// particularly important to handle correctly, since it is
// special-cased by Delve. The code below checks that things are ok
// both for the regular optimized case and the "-gcflags=all=-l -N"
// case, which Delve users are often selecting.
func testRuntimeThrow(t *testing.T, harnessPath, nooptHarnessPath, ppath string) {
	expected := map[string]string{
		"amd64": "1: in-param \"s\" loc=\"{ [0: S=8 RAX] [1: S=8 RBX] }\"",
		"arm64": "1: in-param \"s\" loc=\"{ [0: S=8 R0] [1: S=8 R1] }\"",
	}
	fname := "runtime.throw"
	harnesses := []string{harnessPath, nooptHarnessPath}
	for _, harness := range harnesses {
		got := runHarness(t, harness, ppath, fname)
		want := expected[runtime.GOARCH]
		if got != want {
			t.Errorf("failed RuntimeThrow arch %s, harness %s:\ngot: %q\nwant: %q", runtime.GOARCH, harness, got, want)
		}
	}
}

func testIssue65405(t *testing.T, harnessPath string, ppath string) {
	// Test that function parameters have location lists
	expected := map[string]string{
		"amd64": `1: in-param "a" loc="{ [0: S=0 RAX] }"
2: in-param "b" loc="{ [0: S=8 RBX] [1: S=8 RCX] }"
3: out-param "~r0" at RET[0] loc="{ [0: S=0 RAX] }"
4: out-param "~r1" at RET[0] loc="{ [0: S=8 RBX] [1: S=8 RCX] }"`,
		"arm64": `1: in-param "a" loc="{ [0: S=0 R0] }"
2: in-param "b" loc="{ [0: S=8 R1] [1: S=8 R2] }"
3: out-param "~r0" at RET[0] loc="{ [0: S=0 R0] }"
4: out-param "~r1" at RET[0] loc="{ [0: S=8 R1] [1: S=8 R2] }"`,
	}
	fname := "Issue65405"
	got := runHarness(t, harnessPath, ppath, "main."+fname)
	want := expected[runtime.GOARCH]
	if got != want {
		t.Errorf("failed Issue65405 arch %s:\ngot: %q\nwant: %q",
			runtime.GOARCH, got, want)
	}
}

func testReturnValueRegisters(t *testing.T, harnessPath string, ppath string) {
	// Test return value register assignments for multiReturn function
	// Verify that return values follow ABI conventions
	expected := map[string]string{
		"amd64": `1: out-param "~r0" at RET[0] loc="{ [0: S=0 RAX] }"
2: out-param "~r1" at RET[0] loc="{ [0: S=0 RBX] }"
3: out-param "~r2" at RET[0] loc="{ [0: S=0 RCX] }"
4: out-param "~r3" at RET[0] loc="{ [0: S=8 RDI] [1: S=8 RSI] }"
5: out-param "~r4" at RET[0] loc="{ [0: S=0 X0] }"`,
		"arm64": `1: out-param "~r0" at RET[0] loc="{ [0: S=0 R0] }"
2: out-param "~r1" at RET[0] loc="{ [0: S=0 R1] }"
3: out-param "~r2" at RET[0] loc="{ [0: S=0 R2] }"
4: out-param "~r3" at RET[0] loc="{ [0: S=8 R3] [1: S=8 R4] }"
5: out-param "~r4" at RET[0] loc="{ [0: S=0 F0] }"`,
	}
	fname := "multiReturn"
	got := runHarness(t, harnessPath, ppath, "main."+fname)
	want := expected[runtime.GOARCH]
	if got != want {
		t.Errorf("failed ReturnValueRegisters arch %s:\ngot: %q\nwant: %q",
			runtime.GOARCH, got, want)
	}
}

func testRegisterLivenessNamedRetParam(t *testing.T, harnessPath, nooptHarnessPath, ppath, nooppath string) {
	fname := "RegisterLivenessNamedRetParam"
	expected := map[string]string{
		"amd64": `1: out-param "result" at RET[0] loc="{ [0: S=0 RAX] }"`,
		"arm64": ``,
	}

	// Test only optimized builds (non-optimized results are passed on the stack)
	testCases := []struct {
		name    string
		harness string
		binary  string
	}{
		{"optimized", harnessPath, ppath},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := runHarness(t, tc.harness, tc.binary, "main."+fname)
			want := expected[runtime.GOARCH]
			if got != want {
				t.Fatalf("return parameter 'result' has incorrect location in %s build:\ngot:\n%s\nwant:\n%s", tc.name, got, want)
			}
		})
	}
}

func testLivenessForUnnamedRetParams(t *testing.T, harnessPath, nooptHarnessPath, ppath, nooppath string) {
	// This test verifies that return parameters have precise location lists.
	// Return parameters should only be valid in their ABI register after assignment,
	// not throughout the entire function. The location list should start from the
	// assignment point, not from function entry.

	fname := "RegisterLivenessUnnamedRetParam"
	expected := map[string]string{
		"amd64": `1: out-param "~r0" at RET[0] loc="{ [0: S=0 RAX] }"`,
		"arm64": ``,
	}

	// Test only optimized builds (non-optimized results are passed on the stack)
	testCases := []struct {
		name    string
		harness string
		binary  string
	}{
		{"optimized", harnessPath, ppath},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := runHarness(t, tc.harness, tc.binary, "main."+fname)
			want := expected[runtime.GOARCH]
			if got != want {
				t.Fatalf("return parameter 'result' has incorrect location in %s build:\ngot:\n%s\nwant:\n%s", tc.name, got, want)
			}
		})
	}
}

func testManyArgsWithNamedReturns(t *testing.T, harnessPath, nooptHarnessPath, ppath, nooppath string) {
	fname := "ManyArgsWithNamedReturns"
	expected := map[string]string{
		"amd64": `1: in-param "a" loc="{ [0: S=0 RAX] }"
2: in-param "b" loc="{ [0: S=0 RBX] }"
3: in-param "c" loc="{ [0: S=0 RCX] }"
4: in-param "d" loc="{ [0: S=0 RDI] }"
5: in-param "e" loc="{ [0: S=0 RSI] }"
6: in-param "f" loc="{ [0: S=0 R8] }"
7: in-param "g" loc="{ [0: S=0 R9] }"
8: in-param "h" loc="{ [0: S=0 R10] }"
9: in-param "i" loc="{ [0: S=0 R11] }"
10: in-param "j" loc="addr=1000"
11: in-param "k" loc="addr=1008"
12: in-param "l" loc="addr=1010"
13: in-param "m" loc="addr=1018"
14: in-param "n" loc="addr=1020"
15: in-param "o" loc="addr=1028"
16: in-param "p" loc="addr=1030"
17: out-param "sum" at RET[0] loc="{ [0: S=0 RAX] }"
18: out-param "product" at RET[0] loc="{ [0: S=0 RBX] }"`,
		"arm64": ``,
	}

	testCases := []struct {
		name    string
		harness string
		binary  string
	}{
		{"optimized", harnessPath, ppath},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := runHarness(t, tc.harness, tc.binary, "main."+fname)
			want := expected[runtime.GOARCH]
			if got != want {
				t.Fatalf("return parameter 'result' has incorrect location in %s build:\ngot:\n%s\nwant:\n%s", tc.name, got, want)
			}
		})
	}
}

func testMultipleReturnStmts(t *testing.T, harnessPath, nooptHarnessPath, ppath, nooppath string) {
	fname := "multiReturnStmts"
	expected := map[string]string{
		"amd64": `1: in-param "i" loc="{ [0: S=0 RAX] }"
2: out-param "~r0" at RET[0] loc="{ [0: S=0 RAX] }"
2: out-param "~r0" at RET[1] loc="{ [0: S=0 RAX] }"`,
		"arm64": ``,
	}

	// Test only optimized builds (non-optimized results are passed on the stack)
	testCases := []struct {
		name    string
		harness string
		binary  string
	}{
		{"optimized", harnessPath, ppath},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := runHarness(t, tc.harness, tc.binary, "main."+fname)
			want := expected[runtime.GOARCH]
			if got != want {
				t.Fatalf("return parameter 'result' has incorrect location in %s build:\ngot:\n%s\nwant:\n%s", tc.name, got, want)
			}
		})
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

	tdir := t.TempDir()
	if *preserveTemp {
		if td, err := os.MkdirTemp("", "dwloctest"); err != nil {
			t.Fatal(err)
		} else {
			tdir = td
			fmt.Fprintf(os.Stderr, "** preserving tmpdir %s\n", td)
		}
	}

	// Build test harness.
	harnessPath, nooptHarnessPath := buildHarness(t, tdir)

	// Build program to inspect. NB: we're building at default (with
	// optimization); it might also be worth doing a "-l -N" build
	// to verify the location expressions in that case.
	ppath, nooppath := gobuild(t, programSourceCode, "prog", tdir)

	// Sub-tests for each function we want to inspect.
	t.Run("Issue47354", func(t *testing.T) {
		t.Parallel()
		testIssue47354(t, harnessPath, ppath)
	})
	t.Run("Issue46845", func(t *testing.T) {
		t.Parallel()
		testIssue46845(t, harnessPath, ppath)
	})
	t.Run("Issue72053", func(t *testing.T) {
		t.Parallel()
		testIssue72053(t, harnessPath, nooppath)
	})
	t.Run("RuntimeThrow", func(t *testing.T) {
		t.Parallel()
		testRuntimeThrow(t, harnessPath, nooptHarnessPath, ppath)
	})
	t.Run("Issue65405", func(t *testing.T) {
		t.Parallel()
		testIssue65405(t, harnessPath, ppath)
	})
	t.Run("ReturnValueRegisters", func(t *testing.T) {
		t.Parallel()
		testReturnValueRegisters(t, harnessPath, ppath)
	})
	t.Run("RegisterLivenessNamedRetParam", func(t *testing.T) {
		t.Parallel()
		testRegisterLivenessNamedRetParam(t, harnessPath, nooptHarnessPath, ppath, nooppath)
	})
	t.Run("RegisterLivenessUnnamedRetParam", func(t *testing.T) {
		t.Parallel()
		testLivenessForUnnamedRetParams(t, harnessPath, nooptHarnessPath, ppath, nooppath)
	})
	t.Run("ManyArgsWithNamedReturns", func(t *testing.T) {
		t.Parallel()
		testManyArgsWithNamedReturns(t, harnessPath, nooptHarnessPath, ppath, nooppath)
	})
	t.Run("multiReturnStmts", func(t *testing.T) {
		t.Parallel()
		testMultipleReturnStmts(t, harnessPath, nooptHarnessPath, ppath, nooppath)
	})
}
