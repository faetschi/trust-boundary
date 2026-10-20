//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDescriptorPathForMountReturnsTheResolvedPathNotAProcFDPath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := descriptorPathForMount(file)
	if err != nil || got != dir || strings.HasPrefix(got, "/proc/") {
		t.Fatalf("descriptorPathForMount = %q, %v; want the resolved path %q", got, err, dir)
	}

	comma := filepath.Join(t.TempDir(), "a,b")
	if err := os.Mkdir(comma, 0o700); err != nil {
		t.Fatal(err)
	}
	commaFile, err := os.Open(comma)
	if err != nil {
		t.Fatal(err)
	}
	defer commaFile.Close()
	if _, err := descriptorPathForMount(commaFile); err == nil {
		t.Fatal("a path containing a comma would split the --mount option but was accepted")
	}

	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	goneFile, err := os.Open(gone)
	if err != nil {
		t.Fatal(err)
	}
	defer goneFile.Close()
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := descriptorPathForMount(goneFile); err == nil {
		t.Fatal("a deleted directory was accepted")
	}
	regular, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if _, err := descriptorPathForMount(regular); err == nil {
		t.Fatal("a regular file was accepted as a mount source directory")
	}
}

func fakePodman(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", script)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return command
}

func TestContextEndsPromptlyWhenTheLaunchedPodmanExitsEarly(t *testing.T) {
	command := fakePodman(t, "exit 125")
	defer command.Wait()
	watchCtx, stop := contextEndingWithProcess(context.Background(), command.Process.Pid)
	defer stop()
	select {
	case <-watchCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the launch context did not end although podman had exited")
	}
	if !errors.Is(context.Cause(watchCtx), errLaunchedProcessExited) {
		t.Fatalf("cause = %v, want errLaunchedProcessExited", context.Cause(watchCtx))
	}
}

func TestContextStaysOpenWhilePodmanRunsAndEndsWhenItIsKilled(t *testing.T) {
	command := fakePodman(t, "exec sleep 30")
	defer command.Wait()
	defer command.Process.Kill()
	watchCtx, stop := contextEndingWithProcess(context.Background(), command.Process.Pid)
	defer stop()
	select {
	case <-watchCtx.Done():
		t.Fatalf("the context ended while podman was still running: %v", context.Cause(watchCtx))
	case <-time.After(400 * time.Millisecond):
	}
	_ = command.Process.Kill()
	select {
	case <-watchCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the context did not end after podman was killed")
	}
}

// The container id is read from a FIFO the service itself holds open for writing, so an
// early podman exit never produces EOF there; only the process watch can end the wait.
func TestContainerIDWaitReturnsPromptlyWhenPodmanExitsBeforeWritingIt(t *testing.T) {
	fifo, err := newD09CIDFifo(filepath.Join(t.TempDir(), "container.id"))
	if err != nil {
		t.Fatal(err)
	}
	defer fifo.Close()
	command := fakePodman(t, "exit 125")
	defer command.Wait()
	watchCtx, stop := contextEndingWithProcess(context.Background(), command.Process.Pid)
	defer stop()
	started := time.Now()
	if _, err := readD09CIDFifo(watchCtx, fifo, d09CommandStartTimeout); err == nil {
		t.Fatal("a container id was read from a podman that exited 125")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the wait took %v; want it to end within seconds of the podman exit", elapsed)
	}
}

func TestFailedLaunchReportsPodmansOwnExitStatus(t *testing.T) {
	command := fakePodman(t, "exit 125")
	launcher := &D09PodmanCrunLauncher{}
	launcher.storePendingLaunch(command, nil)
	record := launcher.launchRecord(command)
	waitErr := command.Wait()
	launcher.proofMu.Lock()
	record.waitErr = waitErr
	launcher.proofMu.Unlock()
	if err := launcher.failedLaunchExitStatus(record); err == nil || !strings.Contains(err.Error(), "exit status 125") {
		t.Fatalf("failed launch status = %v, want podman's exit status 125", err)
	}
}

func TestContainerIDFifoReadHonoursItsTimeoutAndReturnsAWrittenID(t *testing.T) {
	fifo, err := newD09CIDFifo(filepath.Join(t.TempDir(), "container.id"))
	if err != nil {
		t.Fatal(err)
	}
	defer fifo.Close()
	started := time.Now()
	if _, err := readD09CIDFifo(context.Background(), fifo, 200*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an empty cidfile FIFO read returned %v, want the deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the read ignored its %v timeout and took %v", 200*time.Millisecond, elapsed)
	}
	id := strings.Repeat("ab", 32)
	if _, err := fifo.WriteString(id); err != nil {
		t.Fatal(err)
	}
	if got, err := readD09CIDFifo(context.Background(), fifo, 5*time.Second); err != nil || got != id {
		t.Fatalf("read = %q, %v; want %q", got, err, id)
	}
}
