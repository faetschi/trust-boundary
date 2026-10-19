//go:build linux

package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// e06OutsideReadProbeEnv selects the direct-runtime read probe in the re-executed
// test binary. It is separate from testCommandEnv so the existing probe switch
// stays unchanged.
const e06OutsideReadProbeEnv = "TBOUND_E06_OUTSIDE_READ_PROBE"

func init() {
	if os.Getenv(e06OutsideReadProbeEnv) == "direct" {
		os.Exit(runE06DirectReadProbe())
	}
}

// e06BashCat reads exactly one path with /bin/cat. A relative argument is made
// absolute against the cell working directory so the inside control and the
// outside attempts hand cat the same absolute-path shape.
const e06BashCat = `case "$1" in /*) p="$1" ;; *) p="$PWD/$1" ;; esac; exec /bin/cat -- "$p"`

// TestOutsideWorkspaceReadRecordsNoSuccessfulReadBashView is the E06
// SuccessfulRead evidence for the bash-view route and the outside-workspace-path
// class. A fresh canary lives in a file under the private 0700 work directory,
// outside every Landlock-allowed path (not /tmp, which the cell remounts and
// allows). A bash /bin/cat of it, by absolute path and through a symlink in the
// cell root, must exit nonzero with a permission denial and the canary must
// appear in neither stdout nor stderr. A positive control runs the same command
// on an inside file holding a different canary and requires it to be read, so
// the denial is the cell's and the detector is live.
func TestOutsideWorkspaceReadRecordsNoSuccessfulReadBashView(t *testing.T) {
	requireMechanisms(t)
	work := newWorkDir(t)
	outsideCanary, insideCanary := e06CellCanary(t, "outside"), e06CellCanary(t, "inside")
	outsideFile := e06OutsideCanaryFile(t, work, outsideCanary)

	for _, tc := range []struct{ name, target string }{
		{name: "absolute", target: outsideFile},
		{name: "symlink", target: "escape/secret.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := e06ReadCell(t, work, "bash-"+tc.name, outsideFile, insideCanary)
			result, settlement := e06Launch(t, root, []string{"/bin/bash", "-c", e06BashCat, "bash", tc.target}, nil)
			if !settlement.ExitObserved || !settlement.ProcessScopeEmpty || !settlement.MountDetached {
				t.Fatalf("bash read cell did not settle: %+v", settlement)
			}
			if result.ExitCode == 0 {
				t.Fatalf("bash cat of the outside file succeeded: stdout=%q", result.Stdout)
			}
			if strings.Contains(string(result.Stdout), outsideCanary) || strings.Contains(string(result.Stderr), outsideCanary) {
				t.Fatalf("bash cat released the outside canary: stdout=%q stderr=%q", result.Stdout, result.Stderr)
			}
			if !strings.Contains(string(result.Stderr), "Permission denied") {
				t.Fatalf("bash cat failed without a permission denial: exit=%d stderr=%q", result.ExitCode, result.Stderr)
			}
			t.Logf("bash-view %s: exit=%d stderr=%q", tc.name, result.ExitCode, strings.TrimSpace(string(result.Stderr)))
		})
	}

	t.Run("control: inside file is readable by the same command", func(t *testing.T) {
		root := e06ReadCell(t, work, "bash-control", outsideFile, insideCanary)
		result, _ := e06Launch(t, root, []string{"/bin/bash", "-c", e06BashCat, "bash", "inside.txt"}, nil)
		if result.ExitCode != 0 || !strings.Contains(string(result.Stdout), insideCanary) {
			t.Fatalf("inside control was not readable: exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
		}
		if strings.Contains(string(result.Stdout), outsideCanary) {
			t.Fatalf("inside control released the outside canary: %q", result.Stdout)
		}
	})
}

// TestOutsideWorkspaceReadRecordsNoSuccessfulReadDirectRuntime is the E06
// SuccessfulRead evidence for the direct-runtime route and the
// outside-workspace-path class. The re-executed test binary reads the outside
// canary file directly with open(2) by absolute path, by ".." traversal from the
// cell working directory, and through a symlink; every attempt must fail with
// EACCES or EPERM and return no bytes, and the canary must appear in neither
// stdout nor stderr. The probe prints any bytes it did read, so a released
// canary would be visible. A positive control reads an inside file with a
// different canary in the same probe run.
func TestOutsideWorkspaceReadRecordsNoSuccessfulReadDirectRuntime(t *testing.T) {
	requireMechanisms(t)
	work := newWorkDir(t)
	outsideCanary, insideCanary := e06CellCanary(t, "outside"), e06CellCanary(t, "inside")
	outsideFile := e06OutsideCanaryFile(t, work, outsideCanary)
	root := e06ReadCell(t, work, "direct", outsideFile, insideCanary)

	result, settlement := e06Launch(t, root, []string{"/proc/self/exe"}, []string{
		e06OutsideReadProbeEnv + "=direct", "TBOUND_E06_OUTSIDE=" + outsideFile,
	})
	if !settlement.ExitObserved || !settlement.ProcessScopeEmpty || !settlement.MountDetached {
		t.Fatalf("direct read cell did not settle: %+v", settlement)
	}
	stdout, stderr := string(result.Stdout), string(result.Stderr)
	t.Logf("direct-runtime probe (exit %d):\n%s", result.ExitCode, strings.TrimSpace(stdout))
	if strings.Contains(stdout, outsideCanary) || strings.Contains(stderr, outsideCanary) {
		t.Fatalf("direct runtime released the outside canary: stdout=%q stderr=%q", stdout, stderr)
	}
	for _, name := range []string{"absolute", "dotdot", "symlink"} {
		if !strings.Contains(stdout, name+" DENIED ") {
			t.Fatalf("direct runtime %s read was not denied with EACCES/EPERM: %s", name, stdout)
		}
	}
	if !strings.Contains(stdout, "inside READ "+fmt.Sprintf("%q", insideCanary+"\n")) {
		t.Fatalf("inside control was not readable by the same probe: %s", stdout)
	}
	if result.ExitCode != 0 {
		t.Fatalf("direct read probe reported a failure (%d): %s", result.ExitCode, stdout)
	}
}

func e06CellCanary(t *testing.T, label string) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return "tbound-e06-" + label + "-canary-" + hex.EncodeToString(raw[:])
}

// e06OutsideCanaryFile writes the canary under the private work directory and
// confirms the host can read it, so a later denial is the cell's.
func e06OutsideCanaryFile(t *testing.T, work, canary string) string {
	t.Helper()
	if strings.HasPrefix(work, "/tmp/") {
		t.Fatalf("work directory %s is under /tmp, which the cell remounts; set TMPDIR to a private directory", work)
	}
	outsideDir := filepath.Join(work, "outside")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte(canary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if host, err := os.ReadFile(outsideFile); err != nil || !strings.Contains(string(host), canary) {
		t.Fatalf("host control could not read the outside canary: %v", err)
	}
	return outsideFile
}

// e06ReadCell creates a fresh cell root holding inside.txt and an escape
// symlink to the outside canary's directory.
func e06ReadCell(t *testing.T, work, name, outsideFile, insideCanary string) *os.File {
	t.Helper()
	rootPath := filepath.Join(work, "cell-"+name)
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "inside.txt"), []byte(insideCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outsideFile), filepath.Join(rootPath, "escape")); err != nil {
		t.Fatal(err)
	}
	return openDir(t, rootPath)
}

func e06Launch(t *testing.T, root *os.File, argv, env []string) (Result, Settlement) {
	t.Helper()
	result, settlement, err := Launch(context.Background(), Request{
		Root: root,
		Argv: argv,
		Env:  append([]string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp"}, env...),
	})
	if err != nil {
		t.Fatalf("Launch %v: %v\n%s", argv, err, result.Stderr)
	}
	return result, settlement
}

// runE06DirectReadProbe reads each target and prints one line per target:
// "<name> READ <bytes>" on success, "<name> DENIED <err>" for EACCES/EPERM, and
// "<name> FAILED <err> <bytes>" otherwise. Outside reads that succeed or fail for
// another reason make the probe exit 1.
func runE06DirectReadProbe() int {
	outside := os.Getenv("TBOUND_E06_OUTSIDE")
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("getcwd FAILED %v\n", err)
		return 1
	}
	relative, err := filepath.Rel(cwd, outside)
	if err != nil || !strings.HasPrefix(relative, "..") {
		fmt.Printf("dotdot FAILED relative=%q %v\n", relative, err)
		return 1
	}
	code := 0
	for _, target := range []struct{ name, path string }{
		{"absolute", outside}, {"dotdot", relative}, {"symlink", "escape/secret.txt"}, {"inside", "inside.txt"},
	} {
		data, err := os.ReadFile(target.path)
		switch {
		case err == nil:
			fmt.Printf("%s READ %q\n", target.name, data)
			if target.name != "inside" {
				code = 1
			}
		case len(data) == 0 && (errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)):
			fmt.Printf("%s DENIED %v\n", target.name, err)
			if target.name == "inside" {
				code = 1
			}
		default:
			fmt.Printf("%s FAILED %v %q\n", target.name, err, data)
			code = 1
		}
	}
	return code
}
