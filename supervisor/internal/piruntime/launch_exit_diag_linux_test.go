//go:build linux

package piruntime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// fakeInspectPodman writes a podman stand-in that prints the given inspect output (or fails).
func fakeInspectPodman(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "podman")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// exitedCell starts a fake cell, opens its pidfd, then lets it exit and reaps it, so its /proc entry is gone.
func exitedCell(t *testing.T) (pid, pidfd int) {
	t.Helper()
	cell := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cell.Start(); err != nil {
		t.Fatal(err)
	}
	pid = cell.Process.Pid
	pidfd, err := openPidfd(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(pidfd) })
	_ = cell.Wait()
	return pid, pidfd
}

const d09FinalStateJSON = `[{"Name":"/c","State":{"Status":"exited","ExitCode":137,"OOMKilled":true,"Error":"crun: boom\nsecret=\u00e9x"}}]`

func TestD09LeaderGoneAfterInspectReportsExitedBeforeAdmission(t *testing.T) {
	pid, pidfd := exitedCell(t)
	_, statErr := os.Stat("/proc/" + strconv.Itoa(pid) + "/ns/user")
	if statErr == nil {
		t.Skip("pid still visible")
	}
	podman := fakeInspectPodman(t, "cat <<'EOT'\n"+d09FinalStateJSON+"\nEOT")
	err := d09ExitedBeforeAdmission(podman, "cid", nil, pidfd, statErr)
	if !errors.Is(err, ErrD09WorkerExitedBeforeAdmission) {
		t.Fatalf("err = %v, want ErrD09WorkerExitedBeforeAdmission", err)
	}
	for _, want := range []string{"exited", "137", "true", "crun: boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	if strings.ContainsAny(err.Error(), "\n\x00\u00e9") {
		t.Fatalf("error carries non-printable bytes: %q", err)
	}
}

func TestD09PidfdExitAloneIsExitedBeforeAdmission(t *testing.T) {
	_, pidfd := exitedCell(t)
	podman := fakeInspectPodman(t, "cat <<'EOT'\n"+d09FinalStateJSON+"\nEOT")
	if err := d09ExitedBeforeAdmission(podman, "cid", nil, pidfd, errors.New("unrelated map mismatch")); !errors.Is(err, ErrD09WorkerExitedBeforeAdmission) {
		t.Fatalf("err = %v", err)
	}
}

func TestD09LiveLeaderMapErrorIsNotRelabelled(t *testing.T) {
	live := exec.Command("/bin/sh", "-c", "exec sleep 30")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Process.Kill(); _ = live.Wait() }()
	pidfd, err := openPidfd(live.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(pidfd)
	if got := d09ExitedBeforeAdmission("/nonexistent", "cid", nil, pidfd, errors.New("map mismatch")); got != nil {
		t.Fatalf("a live leader with a map mismatch was relabelled: %v", got)
	}
}

func TestD09FinalInspectFailureStillFailsClosedAndSaysSo(t *testing.T) {
	_, pidfd := exitedCell(t)
	podman := fakeInspectPodman(t, "exit 1")
	err := d09ExitedBeforeAdmission(podman, "cid", nil, pidfd, os.ErrNotExist)
	if !errors.Is(err, ErrD09WorkerExitedBeforeAdmission) || !strings.Contains(err.Error(), "final container state unavailable") {
		t.Fatalf("err = %v", err)
	}
}
