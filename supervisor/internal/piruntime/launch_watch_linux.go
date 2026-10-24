//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

var errLaunchedProcessExited = errors.New("the launched podman process exited before the cell proved itself")

// contextEndingWithProcess returns a context that ends when ctx ends or when the
// child process pid has exited (a zombie counts), with errLaunchedProcessExited as
// its cause. It watches a pidfd, never reaps, and so does not interfere with the
// single Wait the launcher owns. If the process cannot be watched the context is
// cancelled at once with that error: a launch that cannot notice podman dying
// must not wait for a deadline.
func contextEndingWithProcess(ctx context.Context, pid int) (context.Context, context.CancelFunc) {
	derived, cancel := context.WithCancelCause(ctx)
	fd, err := openPidfd(pid)
	if err != nil {
		cancel(errors.Join(errLaunchedProcessExited, err))
		return derived, func() { cancel(context.Canceled) }
	}
	go func() {
		defer syscall.Close(fd)
		for derived.Err() == nil {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if n, _ := unix.Poll(fds, 100); n > 0 {
				cancel(errLaunchedProcessExited)
				return
			}
		}
	}()
	return derived, func() { cancel(context.Canceled) }
}

// launchRecord returns the record that owns a launched command, which cleanup
// may later remove from the launcher.
func (l *D09PodmanCrunLauncher) launchRecord(command *exec.Cmd) *d09LaunchRecord {
	if l == nil || command == nil {
		return nil
	}
	l.proofMu.Lock()
	defer l.proofMu.Unlock()
	return l.proofs[command]
}

// failedLaunchExitStatus describes how a reaped failed launch command ended, so an
// early podman failure reports podman's own status.
func (l *D09PodmanCrunLauncher) failedLaunchExitStatus(record *d09LaunchRecord) error {
	if l == nil || record == nil {
		return nil
	}
	l.proofMu.Lock()
	defer l.proofMu.Unlock()
	var exitErr *exec.ExitError
	if errors.As(record.waitErr, &exitErr) {
		return errors.New("podman " + exitErr.ProcessState.String())
	}
	return nil
}
