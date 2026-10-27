//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
)

const verifyRetainedMainEnv = "TBOUND_TEST_VERIFY_RETAINED_MAIN"

// The verb verifies every persisted case in a fresh process, through the same entry point the
// operator runs: a verified run prints exactly one JSON line and returns nil, and each refusal returns
// its cause and prints nothing on stdout.
func TestVerifyRetainedVerbInFreshProcess(t *testing.T) {
	if inputs, child := d09RecoveryChildInputs(t); child {
		for _, test := range inputs.Cases {
			t.Run(test.Name, func(t *testing.T) {
				restore := openRetainedRun
				t.Cleanup(func() { openRetainedRun = restore })
				var openedRunID string
				openRetainedRun = func(runID string) (retainedRun, error) {
					openedRunID = runID
					return fixtureRetainedRun(inputs.Profile, inputs.Template, test), nil
				}
				var stdout, stderr bytes.Buffer
				err := run([]string{"verify-retained", "--run-id", bashRunRunID}, &stdout, &stderr)
				if openedRunID != bashRunRunID {
					t.Fatalf("the command resolved run %q, want %q", openedRunID, bashRunRunID)
				}
				if test.Refusal != "" {
					if err == nil || !strings.Contains(err.Error(), test.Refusal) || stdout.Len() != 0 {
						t.Fatalf("want a refusal containing %q and no stdout, got err=%v stdout=%q", test.Refusal, err, stdout.String())
					}
					return
				}
				if err != nil {
					t.Fatalf("untouched %s run refused: %v", test.Outcome, err)
				}
				line := strings.TrimSuffix(stdout.String(), "\n")
				if line == "" || strings.Contains(line, "\n") || !strings.HasSuffix(stdout.String(), "\n") {
					t.Fatalf("want exactly one JSON line on stdout, got %q", stdout.String())
				}
				var fields map[string]any
				if err := json.Unmarshal([]byte(line), &fields); err != nil {
					t.Fatal(err)
				}
				promoted, denied := 1.0, 0.0
				if test.Outcome != "success" {
					promoted, denied = 0, 1
				}
				if fields["run_id"] != bashRunRunID || fields["verified"] != true || fields["operations"] != 1.0 ||
					fields["promoted_bash"] != promoted || fields["denied_bash"] != denied ||
					fields["store_audit_head"] == "" || fields["store_audit_records"].(float64) < 1 {
					t.Fatalf("report = %s, want run %s verified with %v promoted and %v denied Bash", line, bashRunRunID, promoted, denied)
				}
			})
		}
		return
	}
	inputs := buildD09RecoveryProcessInputs(t)
	roots := map[string]struct{}{}
	for _, test := range inputs.Cases {
		roots[test.Root] = struct{}{}
	}
	before := make(map[string]map[string]string, len(roots))
	for root := range roots {
		before[root] = hashRetainedTree(t, root)
	}
	runD09RecoveryChild(t, inputs, "TestVerifyRetainedVerbInFreshProcess")
	for root := range roots {
		after := hashRetainedTree(t, root)
		if !reflect.DeepEqual(before[root], after) {
			for path, state := range after {
				if before[root][path] != state {
					t.Errorf("verification changed %s: %q -> %q", filepath.Join(root, path), before[root][path], state)
				}
			}
			for path := range before[root] {
				if _, kept := after[path]; !kept {
					t.Errorf("verification removed %s", filepath.Join(root, path))
				}
			}
		}
	}
}

// hashRetainedTree records, for every entry under root, its type, mode, size, mtime and content digest.
func hashRetainedTree(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		description := fmt.Sprintf("%v size=%d mtime=%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			description += " sha256=" + digestBytes(data)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			description += " -> " + target
		}
		state[relative] = description
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state) < 3 {
		t.Fatalf("tree %s has %d entries; the fixture is missing", root, len(state))
	}
	return state
}

// The journals are flock-held while the service runs, so the verb is refused then, and it changes
// nothing. Once the holder is gone the same run verifies.
func TestVerifyRetainedRefusesWhileAJournalIsHeld(t *testing.T) {
	run := promotedBashRun(t)
	run.release()
	profile, template := run.profile, run.template
	test := d09RecoveryProcessCase{Root: run.root, CommandPath: run.commandJournalPath}
	for name, held := range map[string]string{"store journal": run.storeJournalPath, "command journal": run.commandJournalPath} {
		t.Run(name, func(t *testing.T) {
			holder, err := audit.Open(held)
			if err != nil {
				t.Fatal(err)
			}
			before := hashRetainedTree(t, run.root)
			_, err = verifyRetainedRun(fixtureRetainedRun(profile, template, test))
			if err == nil || !strings.Contains(err.Error(), "is tbound-pi.service stopped?") ||
				!strings.Contains(err.Error(), "lock audit journal") {
				t.Fatalf("want a refusal naming the held journal lock, got %v", err)
			}
			if err := holder.Close(); err != nil {
				t.Fatal(err)
			}
			if after := hashRetainedTree(t, run.root); !reflect.DeepEqual(before, after) {
				t.Fatalf("a refused verification changed the retained tree")
			}
		})
	}
	if report, err := verifyRetainedRun(fixtureRetainedRun(profile, template, test)); err != nil || !report.Verified {
		t.Fatalf("the run does not verify once the journals are free: %+v %v", report, err)
	}
}

// Without root the verb refuses before it touches any host source, and with a missing or extra
// argument it refuses before it resolves the run. A refusal exits the process with status 2 and writes
// only to stderr (main's convention), which also shows the verb is registered in main.
func TestVerifyRetainedRefusalsExitTwo(t *testing.T) {
	if mode := os.Getenv(verifyRetainedMainEnv); mode != "" {
		operatorEffectiveUID = func() int { return 1000 }
		args := []string{"tbound", "verify-retained"}
		if mode == "non-root" {
			args = append(args, "--run-id", "production-run-0123456789ab")
		}
		os.Args = args
		main()
		fmt.Println("main returned")
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for mode, want := range map[string]string{"non-root": "verify-retained is root-only", "no-run-id": "needs --run-id"} {
		t.Run(mode, func(t *testing.T) {
			child := exec.Command(executable, "-test.run=^TestVerifyRetainedRefusalsExitTwo$")
			child.Env = []string{verifyRetainedMainEnv + "=" + mode, "TMPDIR=" + os.TempDir()}
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			err := child.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), want) {
				t.Fatalf("want exit 2, empty stdout and stderr containing %q, got err=%v stdout=%q stderr=%q", want, err, stdout.String(), stderr.String())
			}
		})
	}
}
