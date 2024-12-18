// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gocore

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/debug/internal/core"
	"golang.org/x/debug/internal/testenv"
	"golang.org/x/sys/unix"
)

func loadCore(t *testing.T, corePath, base, exePath string) *Process {
	t.Helper()
	c, err := core.Core(corePath, base, exePath)
	if err != nil {
		t.Fatalf("can't load test core file: %s", err)
	}
	p, err := Core(c)
	if err != nil {
		t.Fatalf("can't parse Go core: %s", err)
	}
	return p
}

// loadExampleGenerated generates a core from a binary built with
// runtime.GOROOT().
func loadExampleGenerated(t *testing.T, buildFlags, env []string) *Process {
	t.Helper()
	testenv.MustHaveGoBuild(t)
	switch runtime.GOOS {
	case "js", "plan9", "windows":
		t.Skipf("skipping: no core files on %s", runtime.GOOS)
	}
	if runtime.GOARCH != "amd64" {
		t.Skipf("skipping: only parsing of amd64 cores is supported")
	}

	cleanup := setupCorePattern(t)
	defer cleanup()

	if err := adjustCoreRlimit(t); err != nil {
		t.Fatalf("unable to adjust core limit, can't test generated core dump: %v", err)
	}

	dir := t.TempDir()
	file, output, err := generateCore(dir, buildFlags, env)
	t.Logf("crasher output: %s", output)
	if err != nil {
		t.Fatalf("generateCore() got err %v want nil", err)
	}
	return loadCore(t, file, "", "")
}

func setupCorePattern(t *testing.T) func() {
	if runtime.GOOS != "linux" {
		t.Skip("skipping: core file pattern check implemented only for Linux")
	}

	const (
		corePatternPath = "/proc/sys/kernel/core_pattern"
		newPattern      = "core"
	)

	b, err := os.ReadFile(corePatternPath)
	if err != nil {
		t.Fatalf("unable to read core pattern: %v", err)
	}
	pattern := string(b)
	t.Logf("original core pattern: %s", pattern)

	// We want a core file in the working directory containing "core" in
	// the name. If the pattern already matches this, there is nothing to
	// do. What we don't want:
	//  - Pipe to another process
	//  - Path components
	if !strings.HasPrefix(pattern, "|") && !strings.Contains(pattern, "/") && strings.Contains(pattern, "core") {
		// Pattern is fine as-is, nothing to do.
		return func() {}
	}

	if os.Getenv("GO_BUILDER_NAME") == "" {
		// Don't change the core pattern on arbitrary machines, as it
		// has global effect.
		t.Skipf("skipping: unable to generate core file due to incompatible core pattern %q; set %s to %q", pattern, corePatternPath, newPattern)
	}

	t.Logf("updating core pattern to %q", newPattern)

	err = os.WriteFile(corePatternPath, []byte(newPattern), 0)
	if err != nil {
		t.Skipf("skipping: unable to write core pattern: %v", err)
	}

	return func() {
		t.Logf("resetting core pattern to %q", pattern)
		err := os.WriteFile(corePatternPath, []byte(pattern), 0)
		if err != nil {
			t.Errorf("unable to write core pattern back to original value: %v", err)
		}
	}
}

func adjustCoreRlimit(t *testing.T) error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &limit); err != nil {
		return fmt.Errorf("getrlimit(RLIMIT_CORE) error: %v", err)
	}

	if limit.Max == 0 {
		return fmt.Errorf("RLIMIT_CORE maximum is 0, core dumping is not possible")
	}

	// Increase the core limit to the maximum (hard limit), if the current soft
	// limit is lower.
	if limit.Cur < limit.Max {
		oldLimit := limit
		limit.Cur = limit.Max
		if err := unix.Setrlimit(unix.RLIMIT_CORE, &limit); err != nil {
			return fmt.Errorf("setrlimit(RLIMIT_CORE, %+v) error: %v", limit, err)
		}
		t.Logf("adjusted RLIMIT_CORE from %+v to %+v", oldLimit, limit)
	}

	return nil
}

// doRunCrasher spawns the supplied cmd, propagating parent state (see
// [exec.Cmd.Run]), and returns an error if the process failed to start or did
// *NOT* crash.
func doRunCrasher(cmd *exec.Cmd) (pid int, output []byte, err error) {
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b

	runtime.LockOSThread() // Propagate parent state, see [exec.Cmd.Run].
	err = cmd.Run()
	runtime.UnlockOSThread()

	// We expect a crash.
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return cmd.Process.Pid, b.Bytes(), fmt.Errorf("crasher did not crash, got err %T %w", err, err)
	}
	return cmd.Process.Pid, b.Bytes(), nil
}

func generateCore(dir string, buildFlags, env []string) (string, []byte, error) {
	goTool, err := testenv.GoTool()
	if err != nil {
		return "", nil, fmt.Errorf("cannot find go tool: %w", err)
	}

	const source = "./testdata/coretest/test.go"
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("erroring getting cwd: %w", err)
	}

	srcPath := filepath.Join(cwd, source)
	argv := []string{"build"}
	argv = append(argv, buildFlags...)
	argv = append(argv, "-o", "test.exe", srcPath)
	cmd := exec.Command(goTool, argv...)
	cmd.Dir = dir

	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("error building crasher: %w\n%s", err, string(b))
	}

	cmd = exec.Command("./test.exe")
	cmd.Env = append(os.Environ(), "GOMAXPROCS=2", "GOTRACEBACK=crash")
	cmd.Env = append(cmd.Env, env...)
	cmd.Dir = dir
	_, b, err = doRunCrasher(cmd)
	if err != nil {
		return "", b, err
	}

	// Look for any file with "core" in the name.
	dd, err := os.ReadDir(dir)
	if err != nil {
		return "", b, fmt.Errorf("error reading output directory: %w", err)
	}

	for _, d := range dd {
		if strings.Contains(d.Name(), "core") {
			return filepath.Join(dir, d.Name()), b, nil
		}
	}

	names := make([]string, 0, len(dd))
	for _, d := range dd {
		names = append(names, d.Name())
	}
	return "", b, fmt.Errorf("did not find core file in %+v", names)
}

func checkProcess(t *testing.T, p *Process) {
	t.Helper()
	if gs := p.Goroutines(); len(gs) == 0 {
		t.Error("len(p.Goroutines()) == 0, want >0")
	}

	const heapName = "heap"
	heapStat := p.Stats().Sub(heapName)
	if heapStat == nil || heapStat.Value == 0 {
		t.Errorf("stat[%q].Size == 0, want >0", heapName)
	}

	// TODO(aktau): Adding package os to the test binary causes the dominator test
	// to panic. We suspect the dominator code may not be working right even if it
	// doesn't crash. This needs a fixup and dedicated tests at a later time.
	if false {
		lt := runLT(p)
		if !checkDominator(t, lt) {
			t.Errorf("sanityCheckDominator(...) = false, want true")
		}
	}
}

type parameters struct {
	buildFlags []string
	env        []string
}

func (p parameters) String() string {
	var parts []string
	if len(p.buildFlags) != 0 {
		parts = append(parts, "gcflags="+strings.Join(p.buildFlags, ","))
	}
	if len(p.env) != 0 {
		parts = append(parts, "env="+strings.Join(p.env, ","))
	}
	return cmp.Or(strings.Join(parts, "%"), "DEFAULT")
}

// Variations in build and execution environments common to different tests.
var variations = [...]parameters{
	{}, // Default.
	{buildFlags: []string{"-buildmode=pie"}},
	{buildFlags: []string{"-buildmode=pie"}, env: []string{"GO_DEBUG_TEST_COREDUMP_FILTER=0x3f"}},
}

func TestVersions(t *testing.T) {
	t.Run("goroot", func(t *testing.T) {
		for _, test := range variations {
			t.Run(test.String(), func(t *testing.T) {
				p := loadExampleGenerated(t, test.buildFlags, test.env)
				checkProcess(t, p)
			})
		}
	})
}

func TestObjects(t *testing.T) {
	t.Run("goroot", func(t *testing.T) {
		for _, test := range variations {
			t.Run(test.String(), func(t *testing.T) {
				const largeObjectThreshold = 32768

				p := loadExampleGenerated(t, test.buildFlags, test.env)

				// Statistics to check.
				n := 0
				largeObjects := 0 // Number of objects larger than (or equal to largeObjectThreshold)
				myPairObjects := 0
				anyNodeObjects := 0
				typeSafeNodeObjects := 0

				p.ForEachObject(func(x Object) bool {
					siz := p.Size(x)
					typ := typeName(p, x)
					t.Logf("%s size=%d", typ, p.Size(x))
					if siz >= largeObjectThreshold {
						largeObjects++
					}
					switch typ {
					case "main.myPair":
						myPairObjects++
					case "main.anyNode":
						anyNodeObjects++
					case "main.typeSafeNode[main.myPair]":
						typeSafeNodeObjects++
					}
					n++
					return true
				})
				if n < 10 {
					t.Errorf("#objects = %d, want >10", n)
				}
				if largeObjects != 1 {
					t.Errorf("expected exactly one object larger than %d, found %d", largeObjectThreshold, largeObjects)
				}

				// Check object counts.
				const depth = 5
				const tsTrees = 3
				const anTrees = 2
				const nodes = 1<<depth - 1
				if want := tsTrees*nodes + anTrees*nodes*2; myPairObjects != want {
					t.Errorf("expected exactly %d main.myPair objects, found %d", want, myPairObjects)
				}
				if want := anTrees * nodes; anyNodeObjects != want {
					t.Errorf("expected exactly %d main.anyNode objects, found %d", want, anyNodeObjects)
				}
				if want := tsTrees * nodes; typeSafeNodeObjects != want {
					t.Errorf("expected exactly %d main.typeSafeNode[main.myPair] objects, found %d", want, typeSafeNodeObjects)
				}
			})
		}
	})
}

// typeName returns a string representing the type of this object.
func typeName(c *Process, x Object) string {
	size := c.Size(x)
	typ, repeat := c.Type(x)
	if typ == nil {
		return fmt.Sprintf("unk%d", size)
	}
	name := typ.String()
	n := size / typ.Size
	if n > 1 {
		if repeat < n {
			name = fmt.Sprintf("[%d+%d?]%s", repeat, n-repeat, name)
		} else {
			name = fmt.Sprintf("[%d]%s", repeat, name)
		}
	}
	return name
}
