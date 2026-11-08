//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
	launcher := exitedPodman(t, 137)
	err := d09ExitedBeforeAdmission(context.Background(), podman, "cid", nil, pidfd, 0, launcher, statErr)
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
	launcher := exitedPodman(t, 137)
	if err := d09ExitedBeforeAdmission(context.Background(), podman, "cid", nil, pidfd, 0, launcher, errors.New("unrelated map mismatch")); !errors.Is(err, ErrD09WorkerExitedBeforeAdmission) {
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
	if got := d09ExitedBeforeAdmission(context.Background(), "/nonexistent", "cid", nil, pidfd, 0, 0, errors.New("map mismatch")); got != nil {
		t.Fatalf("a live leader with a map mismatch was relabelled: %v", got)
	}
}

func TestD09FinalInspectFailureStillFailsClosedAndSaysSo(t *testing.T) {
	_, pidfd := exitedCell(t)
	podman := fakeInspectPodman(t, "exit 1")
	launcher := exitedPodman(t, 137)
	err := d09ExitedBeforeAdmission(context.Background(), podman, "cid", nil, pidfd, 0, launcher, os.ErrNotExist)
	if !errors.Is(err, ErrD09WorkerExitedBeforeAdmission) || !strings.Contains(err.Error(), "final container state unavailable") {
		t.Fatalf("err = %v", err)
	}
}

// exitedPodman starts a stand-in for the 'podman run' process that exits with code and is left unreaped
// (as in production, where cleanupFailedContainer owns Wait); it returns its pid.
func exitedPodman(t *testing.T, code int) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit "+strconv.Itoa(code))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	return cmd.Process.Pid
}

func TestD09ExitedBeforeAdmissionReportsPodmanExitStatus(t *testing.T) {
	_, pidfd := exitedCell(t)
	podman := fakeInspectPodman(t, "cat <<'EOT'\n"+d09FinalStateJSON+"\nEOT")
	err := d09ExitedBeforeAdmission(context.Background(), podman, "cid", nil, pidfd, 0, exitedPodman(t, 137), os.ErrNotExist)
	if !errors.Is(err, ErrD09WorkerExitedBeforeAdmission) || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "podman_exit=137") {
		t.Fatalf("err = %v", err)
	}
}

// With --rm the container is already removed when the diagnostic runs, so inspect says "no such container".
func TestD09ExitedBeforeAdmissionKeepsPodmanExitWhenContainerIsRemoved(t *testing.T) {
	_, pidfd := exitedCell(t)
	podman := fakeInspectPodman(t, "echo 'Error: no such container' >&2; exit 125")
	err := d09ExitedBeforeAdmission(context.Background(), podman, "cid", nil, pidfd, 0, exitedPodman(t, 137), os.ErrNotExist)
	if !errors.Is(err, ErrD09WorkerExitedBeforeAdmission) ||
		!strings.Contains(err.Error(), "podman_exit=137") || !strings.Contains(err.Error(), "final container state unavailable") {
		t.Fatalf("err = %v", err)
	}
}

func TestD09PodmanStillRunningIsBoundedNotBlocking(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exec sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if got := d09PodmanExitStatus(cmd.Process.Pid, 100*time.Millisecond); !strings.HasPrefix(got, "running") {
		t.Fatalf("got %q", got)
	}
}

func TestD09PidfdReadableAfterChecksIsRefused(t *testing.T) {
	_, pidfd := exitedCell(t)
	if !pidfdReadable(pidfd) {
		t.Fatal("an exited leader's pidfd is not readable")
	}
	live := exec.Command("/bin/sh", "-c", "exec sleep 30")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Process.Kill(); _ = live.Wait() }()
	liveFD, err := openPidfd(live.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(liveFD)
	if pidfdReadable(liveFD) {
		t.Fatal("a live leader's pidfd is readable")
	}
}

// zombieCell starts a fake cell and leaves it exited but unreaped: /proc/<pid> still exists with State Z.
func zombieCell(t *testing.T) int {
	t.Helper()
	cell := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cell.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cell.Wait() })
	for i := 0; i < 200; i++ {
		if d09LeaderStateGone(cell.Process.Pid) {
			return cell.Process.Pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cell never became a zombie")
	return 0
}

// proc_id_map_open fails with EINVAL (not ENOENT) once the pid has no task, so EINVAL must reach the exit check.
func TestD09EINVALMapErrorOnExitedLeaderIsExitedBeforeAdmission(t *testing.T) {
	pid := zombieCell(t)
	podman := fakeInspectPodman(t, "exit 1")
	cause := &os.PathError{Op: "open", Path: "/proc/" + strconv.Itoa(pid) + "/gid_map", Err: syscall.EINVAL}
	err := d09ExitedBeforeAdmission(context.Background(), podman, "cid", nil, -1, pid, exitedPodman(t, 137), cause)
	if !errors.Is(err, ErrD09WorkerExitedBeforeAdmission) || !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("err = %v", err)
	}
}

func TestD09EINVALMapErrorOnLiveLeaderIsNotRelabelled(t *testing.T) {
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
	cause := &os.PathError{Op: "open", Path: "/proc/x/gid_map", Err: syscall.EINVAL}
	if got := d09ExitedBeforeAdmission(context.Background(), "/nonexistent", "cid", nil, pidfd, live.Process.Pid, 0, cause); got != nil {
		t.Fatalf("a live leader was relabelled: %v", got)
	}
}

func TestD09AdmissionPhasesAreAttributable(t *testing.T) {
	phases := newD09AdmissionPhases()
	phases.mark("podman_started")
	phases.mark("cid_read")
	got := phases.String()
	for _, want := range []string{"phases_ms[", "podman_started=", "cid_read=", "]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
	if strings.Index(got, "podman_started") > strings.Index(got, "cid_read") {
		t.Fatalf("phases out of order: %q", got)
	}
}
