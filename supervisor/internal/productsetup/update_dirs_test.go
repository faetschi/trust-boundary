//go:build linux

package productsetup

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// looseDirectories are the directories whose write bits would let a local user swap a file that root
// checked and is about to install or run.
var looseDirectories = map[string]struct {
	dir  string
	mode os.FileMode
}{
	"a group-writable binary directory":       {BinaryDir, 0o775},
	"a world-writable ancestor of the binary": {"/usr/local", 0o777},
	"a group-writable ancestor of the binary": {"/usr", 0o775},
	"a group-writable key directory":          {ConfigDir, 0o775},
	"a world-writable ancestor of the key":    {"/etc", 0o777},
}

func TestUpdateRefusesAnUnprotectedBinaryOrKeyDirectoryBeforeStaging(t *testing.T) {
	for name, tc := range looseDirectories {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, dry run %v", name, dryRun), func(t *testing.T) {
				f := newFixture(t)
				f.installed()
				f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
				runs := f.executingSelfTest()
				candidate := f.newBinary("binary v2")
				if err := os.Chmod(f.path(tc.dir), tc.mode); err != nil {
					t.Fatal(err)
				}
				f.env.DryRun = dryRun
				rep, err := Update(f.env, candidate)
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.dir+" is writable by group or others") {
					t.Fatalf("an update through %s was not refused: %v", tc.dir, err)
				}
				if got := systemctlCommands(rep.Commands); len(got) != 0 {
					t.Fatalf("a refused update touched the service: %v", got)
				}
				for _, written := range []string{stagedBinaryPath, LastKnownGoodPath} {
					if _, err := os.Lstat(f.path(written)); err == nil {
						t.Fatalf("a refused update left %s", written)
					}
				}
				if f.read(BinaryPath) != "binary v1" || *runs != 0 {
					t.Fatalf("a refused update installed or executed the candidate: %q, %d self-tests", f.read(BinaryPath), *runs)
				}
			})
		}
	}
}

func TestRollbackRefusesAnUnprotectedBinaryDirectory(t *testing.T) {
	for name, tc := range looseDirectories {
		if tc.dir == ConfigDir || tc.dir == "/etc" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.installed()
			f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
			if _, err := Update(f.env, f.newBinary("binary v2")); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(f.path(tc.dir), tc.mode); err != nil {
				t.Fatal(err)
			}
			rep, err := Rollback(f.env)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.dir+" is writable by group or others") {
				t.Fatalf("a rollback through %s was not refused: %v", tc.dir, err)
			}
			if got := systemctlCommands(rep.Commands); len(got) != 0 || f.read(BinaryPath) != "binary v2" {
				t.Fatalf("a refused rollback stopped the service or swapped the binary: %v %q", got, f.read(BinaryPath))
			}
		})
	}
}

func TestUpdateRefusesADirectoryThatRootDoesNotOwnOnALiveHost(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the test root's directories would be root-owned")
	}
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	candidate := f.newBinary("binary v2")
	f.env.Live = true
	rep, err := Update(f.env, candidate)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "is not owned by root") {
		t.Fatalf("an update below directories another user owns was not refused: %v", err)
	}
	if len(rep.Commands) != 0 || f.read(BinaryPath) != "binary v1" {
		t.Fatalf("a refused update ran %v or changed the binary", rep.Commands)
	}
}

func TestUpdateInstallsOnlyTheStagedFileItAuthenticated(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	runs := f.executingSelfTest()
	sentinel := f.inputs + "/executed"
	// After the staged copy was measured, its name is pointed at other bytes.
	interruptHook = func(step string) {
		if step != "prepared" {
			return
		}
		if err := os.Remove(f.path(stagedBinaryPath)); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(f.path(stagedBinaryPath), []byte(sentinelScript(sentinel, "swapped")), 0o755); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { interruptHook = nil })
	_, err := Update(f.env, f.newBinary("#!/bin/sh\nexit 0\n"))
	if err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("a staged file replaced after authentication was installed: %v", err)
	}
	if f.read(BinaryPath) != "binary v1" || *runs != 0 {
		t.Fatalf("the binary is %q after %d self-tests, want the untouched v1", f.read(BinaryPath), *runs)
	}
	if _, err := os.Lstat(sentinel); err == nil {
		t.Fatal("the replacement was executed")
	}
}
