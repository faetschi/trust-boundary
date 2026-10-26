//go:build linux

package productsetup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewParentSymlinksAndThesisRoots(t *testing.T) {
	for _, parent := range []string{"/etc", "/usr/local/lib", "/var/lib", "/run"} {
		t.Run(parent, func(t *testing.T) {
			f := newFixture(t)
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(f.path(parent)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, f.path(parent)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.setup(); !errors.Is(err, ErrRefused) {
				t.Fatalf("setup accepted parent symlink: %v", err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Fatalf("escaped root: %v %v", entries, err)
			}
			if _, err := Uninstall(f.env, false); !errors.Is(err, ErrRefused) {
				t.Fatalf("uninstall accepted parent symlink: %v", err)
			}
		})
	}
	for _, root := range ThesisRoots {
		f := newFixture(t)
		f.env.Root = root + "/review-test"
		f.env.DryRun = true
		if _, err := f.setup(); !errors.Is(err, ErrRefused) {
			t.Fatalf("accepted thesis root %s: %v", root, err)
		}
		f.env.DryRun = false
		if rep := Doctor(f.env, true); !rep.Failed() || strings.Contains(rep.String(), "PASSED") {
			t.Fatalf("doctor established readiness for a refused root: %s", rep)
		}
	}
	t.Run("parent replaced after preflight", func(t *testing.T) {
		f := newFixture(t)
		f.installed()
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(outside, "systemd/system"), 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(outside, "systemd/system", UnitName)
		if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
		f.env.Confirm = func(string) (bool, error) {
			if err := os.Rename(f.path("/etc"), f.path("/etc.saved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, f.path("/etc")); err != nil {
				t.Fatal(err)
			}
			return true, nil
		}
		if _, err := Uninstall(f.env, true); !errors.Is(err, ErrRefused) {
			t.Fatalf("replaced parent accepted: %v", err)
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
			t.Fatalf("removed outside root: %s %v", data, err)
		}
	})
}

func TestReviewUninstallFailsClosed(t *testing.T) {
	for _, failure := range []string{"stop", "disable", "active", "unknown", "catalog"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.installed()
			f.env.Live = true
			f.env.Confirm = func(string) (bool, error) { return true, nil }
			f.env.Exec = func(_ string, args ...string) ([]byte, error) {
				if args[0] == failure {
					return nil, errors.New("systemctl failed")
				}
				if args[0] == "show" {
					if failure == "unknown" {
						return nil, errors.New("no bus")
					}
					if failure == "active" {
						return []byte("LoadState=loaded\nActiveState=active\n"), nil
					}
					return []byte("LoadState=loaded\nActiveState=inactive\n"), nil
				}
				return nil, nil
			}
			f.env.Turns = func() ([]OpenTurn, error) {
				if failure == "catalog" {
					return nil, errors.New("catalog locked")
				}
				return nil, nil
			}
			if _, err := Uninstall(f.env, true); err == nil {
				t.Fatal("uninstall succeeded without proving it was safe")
			}
			for _, kept := range []string{UnitPath, BinaryPath, RegistrationPath, StateDir} {
				if _, err := os.Stat(f.path(kept)); err != nil {
					t.Fatalf("removed %s: %v", kept, err)
				}
			}
		})
	}
	t.Run("absent unit", func(t *testing.T) {
		f := newFixture(t)
		f.env.Live = true
		f.env.Exec = func(_ string, args ...string) ([]byte, error) {
			if args[0] == "show" {
				return []byte("LoadState=not-found\nActiveState=inactive\n"), nil
			}
			return nil, errors.New("unit not found")
		}
		if _, err := Uninstall(f.env, false); err != nil {
			t.Fatalf("absent unit: %v", err)
		}
	})
}

func TestReviewUninstallReportsDeletionFailure(t *testing.T) {
	f := newFixture(t)
	f.installed()
	if err := os.Remove(f.path(UnitPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.path(UnitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path(UnitPath+"/keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Uninstall(f.env, false)
	if err == nil {
		t.Fatal("deletion failure was ignored")
	}
	for _, step := range rep.Steps {
		if step.Path == UnitPath && step.Status == "removed" {
			t.Fatal("failed deletion reported removed")
		}
	}
	if f.read(BinaryPath) != "binary v1" {
		t.Fatal("deleting continued after failure")
	}
}

func TestReviewUpdateRetryStartsAndTestsWithoutReplacingLastKnownGood(t *testing.T) {
	if root := os.Getenv("TB_PRODUCT_RETRY_ROOT"); root != "" {
		calls := 0
		fail := os.Getenv("TB_PRODUCT_RETRY_FAIL") == "true"
		env := Env{Root: root, SelfTestHook: func() error {
			calls++
			if fail {
				return errors.New("bad turn")
			}
			return nil
		}}
		rep, err := Update(env, os.Getenv("TB_PRODUCT_RETRY_BINARY"))
		if calls != 1 || !strings.Contains(strings.Join(rep.Commands, "\n"), "systemctl start "+UnitName) {
			t.Fatalf("retry did not recover service: %v %v", calls, rep.Commands)
		}
		if fail != errors.Is(err, ErrSelfTest) {
			t.Fatalf("retry self-test result: %v", err)
		}
		return
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "pass", true: "fail"}[fail], func(t *testing.T) {
			f := newFixture(t)
			f.installed()
			f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
			next := f.newBinary("binary v2")
			if _, err := Update(f.env, next); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestReviewUpdateRetryStartsAndTestsWithoutReplacingLastKnownGood$", "-test.v")
			child.Env = append(os.Environ(), "TB_PRODUCT_RETRY_ROOT="+f.root, "TB_PRODUCT_RETRY_BINARY="+next, "TB_PRODUCT_RETRY_FAIL="+map[bool]string{false: "false", true: "true"}[fail])
			if out, err := child.CombinedOutput(); err != nil {
				t.Fatalf("fresh-process retry: %v\n%s", err, out)
			}
			if f.read(LastKnownGoodPath) != "binary v1" {
				t.Fatal("retry replaced last-known-good")
			}
			want := "binary v2"
			if fail {
				want = "binary v1"
			}
			if f.read(BinaryPath) != want {
				t.Fatalf("binary after retry: %q", f.read(BinaryPath))
			}
		})
	}
}

func TestReviewDoctorDryRunPlansSelfTest(t *testing.T) {
	f := newFixture(t)
	f.cfg.SelfTest = &SelfTest{ProjectID: "alpha", ProviderProfile: "opencode-go-engineering"}
	f.installed()
	f.env.DryRun = true
	f.env.RunSelfTest = func(SelfTest) (string, error) { t.Fatal("dry run invoked self-test"); return "", nil }
	rep := Doctor(f.env, true)
	if text := rep.String(); !strings.Contains(text, "would run") || strings.Contains(text, "PASSED") || !strings.Contains(text, "governed readiness: NOT established") {
		t.Fatalf("dry-run result: %s", text)
	}
}
