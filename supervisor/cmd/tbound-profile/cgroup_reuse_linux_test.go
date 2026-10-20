//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCgroupReuseCommandHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"cgroup-reuse-check", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(stdout.String()+stderr.String(), "confirm-disposable-cgroup") {
		t.Fatal("help does not explain explicit confirmation")
	}
}

func TestCgroupReuseRequiresExplicitConfirmation(t *testing.T) {
	for _, args := range [][]string{nil, {"--confirm-disposable-cgroup=false"}, {"--confirm-disposable-cgroup", "unexpected"}, {"--unknown"}, {"--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			err := runCgroupReuseCheckWithProbe(args, &stdout, &stderr, func() (cgroupReuseReport, error) {
				called = true
				return cgroupReuseReport{}, nil
			})
			if called || stdout.Len() != 0 {
				t.Fatalf("unconfirmed/invalid invocation ran probe=%v or emitted report=%q", called, stdout.String())
			}
			if len(args) != 1 || args[0] != "--help" {
				if err == nil {
					t.Fatal("invalid or unconfirmed invocation succeeded")
				}
			}
		})
	}
}

func TestCgroupReuseReportPreservesFailureAndIsAdvisory(t *testing.T) {
	probeErr := errors.New("probe failed")
	for _, tc := range []struct {
		name, result, want string
		err                error
	}{
		{"observed-pass", "pass", "pass", nil},
		{"setup-unavailable", "", "unverified", probeErr},
		{"cleanup-failed-after-pass", "pass", "unverified", probeErr},
		{"reuse-killed", "incompatible", "incompatible", probeErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runCgroupReuseCheckWithProbe([]string{"--confirm-disposable-cgroup"}, &stdout, &stderr, func() (cgroupReuseReport, error) {
				return cgroupReuseReport{Result: tc.result}, tc.err
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("returned %v, want %v", err, tc.err)
			}
			var report cgroupReuseReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("invalid report: %v", err)
			}
			if report.Result != tc.want || !report.Advisory || (tc.err != nil && report.Error != tc.err.Error()) {
				t.Fatalf("unexpected report: %+v", report)
			}
		})
	}
}

func TestCgroupReuseOutcomeRequiresBothLiveControlsAndMembership(t *testing.T) {
	live := cgroupReuseObservation{}
	killed := cgroupReuseObservation{Exited: true, Signal: syscall.SIGKILL.String()}
	otherExit := cgroupReuseObservation{Exited: true, ExitStatus: 1}
	for _, tc := range []struct {
		name                   string
		reused, fresh, outside cgroupReuseObservation
		populated              bool
		want                   string
	}{
		{"all-alive", live, live, live, true, ""},
		{"reproduced-kill", killed, live, live, false, "incompatible"},
		{"fresh-control-killed", killed, killed, live, false, "unverified"},
		{"outside-control-killed", killed, live, killed, false, "unverified"},
		{"other-reuse-exit", otherExit, live, live, false, "unverified"},
		{"killed-but-populated", killed, live, live, true, "unverified"},
		{"missing-membership", live, live, live, false, "unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cgroupReuseOutcome(tc.reused, tc.fresh, tc.outside, tc.populated)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCgroupReuseControlsRejectInvalidEventsAndSymlinkWrites(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, tc := range []struct {
		value string
		live  bool
		valid bool
	}{{"populated 0\nfrozen 0\n", false, true}, {"populated 1\n", true, true}, {"populated 2\n", false, false}, {"frozen 0\n", false, false}} {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.events"), []byte(tc.value), 0600); err != nil {
			t.Fatal(err)
		}
		live, err := cgroupReusePopulated(f)
		if (err == nil) != tc.valid || live != tc.live {
			t.Fatalf("events=%q: populated=%v err=%v", tc.value, live, err)
		}
	}
	target := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "cgroup.kill")); err != nil {
		t.Fatal(err)
	}
	if _, err := cgroupReuseControl(f, "cgroup.kill", "1", true); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("symlink control write: %v", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "unchanged" {
		t.Fatalf("symlink target changed: %q, %v", b, err)
	}
}

func startCgroupReuseTestChild(t *testing.T, name string) *cgroupReuseChild {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &cgroupReuseChild{name: name, cmd: cmd, done: make(chan error, 1)}
	go func() { c.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if !c.poll() {
			_ = c.cmd.Process.Kill()
		}
		if err := c.waitForExit(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func TestCgroupReuseCleanupKillsAndReapsOnlyOwnedChildren(t *testing.T) {
	owned := startCgroupReuseTestChild(t, "owned")
	unrelated := startCgroupReuseTestChild(t, "unrelated")
	dir := filepath.Join(t.TempDir(), "disposable")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupCgroupReuse([]*cgroupReuseChild{owned}, []*os.File{f}, []string{dir}); err != nil {
		t.Fatal(err)
	}
	if o := owned.observation("cleanup"); !o.Exited || o.Signal != syscall.SIGKILL.String() {
		t.Fatalf("owned child was not killed and reaped: %+v", o)
	}
	if unrelated.poll() {
		t.Fatal("cleanup killed unrelated process")
	}
	if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("cgroup descriptor remains open: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disposable cgroup remains: %v", err)
	}
}

func TestCgroupReuseCleanupReportsFailureAndStillReapsChild(t *testing.T) {
	owned := startCgroupReuseTestChild(t, "owned")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "not-empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupCgroupReuse([]*cgroupReuseChild{owned}, nil, []string{dir}); err == nil {
		t.Fatal("cleanup hid directory removal failure")
	}
	if !owned.poll() {
		t.Fatal("cleanup failure left owned child running")
	}
}
