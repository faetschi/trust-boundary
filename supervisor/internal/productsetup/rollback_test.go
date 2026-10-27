//go:build linux

package productsetup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// rollbackChild is the fresh process of TestRollbackIsRecoveredByAFreshProcessAfterAnInterruptionAtEveryStep:
// it runs one Rollback on the test root and, when TB_ROLLBACK_CRASH_AT names a step, ends at that step
// without running any cleanup.
func rollbackChild(root string) {
	crashAt := os.Getenv("TB_ROLLBACK_CRASH_AT")
	if crashAt != "" {
		interruptHook = func(step string) {
			if step == crashAt {
				os.Exit(77)
			}
		}
	}
	selfTests := 0
	env := Env{Root: root, Turns: func() ([]OpenTurn, error) { return nil, nil }, SelfTestHook: func() error {
		selfTests++
		if os.Getenv("TB_ROLLBACK_SELFTEST") == "fail" {
			return errors.New("the synthetic turn failed")
		}
		return nil
	}}
	rep, err := Rollback(env)
	fmt.Printf("RESULT refused=%v selftest_failed=%v selftests=%d starts=%d\n", errors.Is(err, ErrRefused), errors.Is(err, ErrSelfTest), selfTests,
		strings.Count(strings.Join(rep.Commands, "\n"), "systemctl start "+UnitName))
}

// runRollbackChild runs rollbackChild in a new process and returns its output and exit code.
func runRollbackChild(t *testing.T, root, crashAt, selfTest string) (string, int) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestRollbackIsRecoveredByAFreshProcessAfterAnInterruptionAtEveryStep$")
	child.Env = append(os.Environ(), "TB_ROLLBACK_ROOT="+root, "TB_ROLLBACK_CRASH_AT="+crashAt, "TB_ROLLBACK_SELFTEST="+selfTest)
	out, err := child.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatalf("run the child: %v", err)
	return "", -1
}

func TestRollbackIsRecoveredByAFreshProcessAfterAnInterruptionAtEveryStep(t *testing.T) {
	if root := os.Getenv("TB_ROLLBACK_ROOT"); root != "" {
		rollbackChild(root)
		return
	}
	for _, step := range []string{"stopped", "backed-up", "prepared", "swapped", "started", "tested"} {
		t.Run(step, func(t *testing.T) {
			f := newFixture(t)
			f.installed()
			f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
			if _, err := Update(f.env, f.newBinary("binary v2")); err != nil {
				t.Fatal(err)
			}
			if f.read(BinaryPath) != "binary v2" || f.read(LastKnownGoodPath) != "binary v1" {
				t.Fatal("the precondition is installed v2 over last-known-good v1")
			}

			if out, code := runRollbackChild(t, f.root, step, "pass"); code != 77 {
				t.Fatalf("the child did not end at %q (exit %d):\n%s", step, code, out)
			}
			have := []string{f.read(BinaryPath), f.read(LastKnownGoodPath)}
			slices.Sort(have)
			if !slices.Equal(have, []string{"binary v1", "binary v2"}) {
				t.Fatalf("an interruption at %q lost a binary: installed %q, last-known-good %q", step, f.read(BinaryPath), f.read(LastKnownGoodPath))
			}

			out, code := runRollbackChild(t, f.root, "", "pass")
			if code != 0 || !strings.Contains(out, "refused=false selftest_failed=false selftests=1 starts=1") {
				t.Fatalf("the retry in a fresh process (exit %d):\n%s", code, out)
			}
			if f.read(BinaryPath) != "binary v1" || f.read(LastKnownGoodPath) != "binary v2" {
				t.Fatalf("after the retry the installed binary is %q and last-known-good %q, want v1 over v2", f.read(BinaryPath), f.read(LastKnownGoodPath))
			}
			for _, leftover := range []string{stagedBinaryPath, rollbackIntentPath} {
				if _, err := os.Lstat(f.path(leftover)); err == nil {
					t.Errorf("%s survived the recovered rollback", leftover)
				}
			}
		})
	}
}

func TestRollbackWhoseSelfTestFailsRestoresAndAnInterruptedRestoreIsNotUndone(t *testing.T) {
	if os.Getenv("TB_ROLLBACK_ROOT") != "" {
		t.Skip("only the parent runs this test")
	}
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	if _, err := Update(f.env, f.newBinary("binary v2")); err != nil {
		t.Fatal(err)
	}
	if out, code := runRollbackChild(t, f.root, "restored", "fail"); code != 77 {
		t.Fatalf("the child did not end after the restore (exit %d):\n%s", code, out)
	}
	if f.read(BinaryPath) != "binary v2" || f.read(LastKnownGoodPath) != "binary v2" {
		t.Fatalf("the restore left installed %q, last-known-good %q, want the binary rolled back from", f.read(BinaryPath), f.read(LastKnownGoodPath))
	}
	out, code := runRollbackChild(t, f.root, "", "pass")
	if code != 0 || !strings.Contains(out, "refused=true") || f.read(BinaryPath) != "binary v2" {
		t.Fatalf("a retry after an interrupted restore must refuse and change nothing (exit %d, installed %q):\n%s", code, f.read(BinaryPath), out)
	}
}

func TestRollbackRefusesWhenThereIsNothingToRollBackToOrTheRecordIsBad(t *testing.T) {
	if os.Getenv("TB_ROLLBACK_ROOT") != "" {
		t.Skip("only the parent runs this test")
	}
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	if err := os.WriteFile(f.path(LastKnownGoodPath), []byte("binary v1"), 0o755); err != nil || os.Chmod(f.path(LastKnownGoodPath), 0o755) != nil {
		t.Fatal("write the last-known-good", err)
	}
	if _, err := Rollback(f.env); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "nothing to roll back to") {
		t.Fatalf("a last-known-good equal to the installed binary must refuse: %v", err)
	}
	if _, err := Update(f.env, f.newBinary("binary v2")); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"not json":       "garbage",
		"unknown field":  `{"target":"sha256:` + strings.Repeat("a", 64) + `","previous":"sha256:` + strings.Repeat("b", 64) + `","extra":1}`,
		"short digest":   `{"target":"sha256:aa","previous":"sha256:bb"}`,
		"digest no hash": `{"target":"` + strings.Repeat("a", 64) + `","previous":"` + strings.Repeat("b", 64) + `"}`,
	} {
		if err := os.WriteFile(f.path(rollbackIntentPath), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Rollback(f.env); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "rollback record") {
			t.Fatalf("%s: a bad rollback record was not refused: %v", name, err)
		}
		if f.read(BinaryPath) != "binary v2" || f.read(LastKnownGoodPath) != "binary v1" {
			t.Fatalf("%s: a refused rollback changed the binaries", name)
		}
	}
	if err := os.WriteFile(f.path(rollbackIntentPath), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(f.env); !errors.Is(err, ErrRefused) {
		t.Fatalf("a rollback record with a wrong mode must be refused: %v", err)
	}
	if err := os.Remove(f.path(rollbackIntentPath)); err != nil {
		t.Fatal(err)
	}
	f.env.DryRun = true
	rep, err := Rollback(f.env)
	if err != nil || !strings.Contains(rep.String(), "exchanged with") || f.read(BinaryPath) != "binary v2" {
		t.Fatalf("dry run: %v\n%s", err, rep)
	}
	if _, err := os.Lstat(f.path(rollbackIntentPath)); err == nil {
		t.Fatal("a dry run wrote the rollback record")
	}
}
