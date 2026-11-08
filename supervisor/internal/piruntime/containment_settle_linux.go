//go:build linux

// containment_settle_linux.go: D09 failed-launch cleanup, container cleanup and cell settlement.

package piruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// closeLaunchPidfdLocked records close failures and clears the descriptor only
// after a successful Close. The caller must hold record.ownerMu.
func closeLaunchPidfdLocked(record *d09LaunchRecord) error {
	if record == nil {
		return nil
	}
	if record.pidfd != nil {
		err := record.pidfd.Close()
		record.pidfdCloseErr = errors.Join(record.pidfdCloseErr, err)
		if err == nil {
			record.pidfd = nil
		}
	}
	return record.pidfdCloseErr
}

// finishFailedLaunchRecord releases a failed launch's owners only after each
// close succeeds and the exact record is still registered.
func (l *D09PodmanCrunLauncher) finishFailedLaunchRecord(command *exec.Cmd, record *d09LaunchRecord) error {
	if l == nil || command == nil || record == nil {
		return errors.Join(ErrUnknownSettlement, errors.New("failed launch record is unavailable"))
	}
	record.ownerMu.Lock()
	defer record.ownerMu.Unlock()

	var closeErr error
	if record.stdout != nil && !record.stdoutClosed {
		err := record.stdout.Close()
		record.stdoutCloseErr = errors.Join(record.stdoutCloseErr, err)
		if err == nil {
			record.stdoutClosed = true
		}
	}
	if record.stdoutCloseErr != nil {
		closeErr = errors.Join(closeErr,
			fmt.Errorf("close failed-launch stdout owner: %w", record.stdoutCloseErr))
	}
	if err := closeLaunchPidfdLocked(record); err != nil {
		closeErr = errors.Join(closeErr, fmt.Errorf("close D09 launch pidfd: %w", err))
	}
	if closeErr != nil {
		return errors.Join(ErrUnknownSettlement, closeErr)
	}

	l.proofMu.Lock()
	defer l.proofMu.Unlock()
	if l.proofs[command] != record {
		return errors.Join(ErrUnknownSettlement, errors.New("failed launch record ownership changed"))
	}
	delete(l.proofs, command)
	l.launchInProg = false
	return nil
}

// settledFailedLaunch types a launch that failed after its cell started once the failed-launch
// cleanup proved the cell gone (command waited, held cgroup populated=0, launch record finished).
// The typed complete settlement is what lets the caller release its owners and drain the owner scope;
// an untyped error cannot be told from a launch whose cell may still run. Any cleanup error keeps
// the error untyped, so the caller keeps its owners.
func settledFailedLaunch(launchErr, cleanupErr error) error {
	if launchErr == nil || cleanupErr != nil {
		return launchErr
	}
	return &PiWorkerLaunchError{Cause: launchErr,
		Settlement: Settlement{State: SettlementStopped, ExitObserved: true, DescendantsSettled: true}}
}

func (l *D09PodmanCrunLauncher) cleanupFailedContainer(command *exec.Cmd, podmanPath, name string) error {
	if command == nil || command.Process == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), d09CleanupTimeout)
	defer cancel()
	_ = command.Process.Kill()
	if podmanPath != "" && name != "" {
		l.cleanupContainer(podmanPath, name)
	}
	l.proofMu.Lock()
	record := l.proofs[command]
	l.proofMu.Unlock()
	if record == nil {
		l.storePendingLaunch(command, nil)
		l.proofMu.Lock()
		record = l.proofs[command]
		l.proofMu.Unlock()
	}
	waitErr := l.waitFailedCommand(cleanupCtx, command, record)
	if l.scope == nil {
		return errors.Join(ErrUnknownSettlement, waitErr)
	}
	settled, settleErr := l.scope.Settle(cleanupCtx)
	if !settled || settleErr != nil || waitErr != nil {
		return errors.Join(ErrUnknownSettlement, settleErr, waitErr)
	}
	return l.finishFailedLaunchRecord(command, record)
}

// waitFailedCommand owns the single Wait call for a failed, exact launch.
// Further cleanup attempts observe the same completion instead of racing Wait.
func failedCommandWaitResult(waitErr error) error {
	if waitErr == nil {
		return nil
	}
	if _, ok := waitErr.(*exec.ExitError); ok {
		return nil
	}
	return errors.Join(ErrUnknownSettlement, waitErr)
}

func (l *D09PodmanCrunLauncher) waitFailedCommand(ctx context.Context, command *exec.Cmd, record *d09LaunchRecord) error {
	l.proofMu.Lock()
	if record.waitDone == nil {
		record.waitDone = make(chan struct{})
		done := record.waitDone
		l.proofMu.Unlock()
		go func() {
			waitErr := command.Wait()
			l.proofMu.Lock()
			record.waitErr = waitErr
			close(done)
			l.proofMu.Unlock()
		}()
	} else {
		done := record.waitDone
		l.proofMu.Unlock()
		select {
		case <-done:
			return failedCommandWaitResult(record.waitErr)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-record.waitDone:
		return failedCommandWaitResult(record.waitErr)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *D09PodmanCrunLauncher) cleanupContainer(podmanPath, name string) {
	if strings.TrimSpace(podmanPath) == "" || strings.TrimSpace(name) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d09CleanupTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, podmanPath, "rm", "-f", name)
	command.Env = l.commandEnvironment()
	command.Stdout, command.Stderr = io.Discard, io.Discard
	_ = command.Run()
}

// cleanupCell kills and settles the exact launched worker command after a proof
// validation failure. It does not treat the supervisor's Podman CLI process as
// the cell leader. It succeeds only if the per-command proof record is present
// and the recorded host worker PID's pidfd plus held cgroup reach settlement.
func (l *D09PodmanCrunLauncher) cleanupFailedLaunch(cmd *exec.Cmd) bool {
	if l == nil || cmd == nil {
		return false
	}
	l.proofMu.Lock()
	record := l.proofs[cmd]
	l.proofMu.Unlock()
	if record == nil || record.proof.LeaderPID <= 0 || record.pidfd == nil {
		return false
	}
	if cmd.Process == nil {
		return false
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), d09CleanupTimeout)
	defer cancel()
	if record.proof.ContainerName != "" {
		l.cleanupContainer(l.config.PodmanPath, record.proof.ContainerName)
	}
	_ = cmd.Process.Kill()
	evidence, err := l.settleCellWithPidfd(cleanupCtx, record.proof.LeaderPID, record.pidfd)
	if err != nil || !evidence.PidfdObserved || !evidence.CgroupKilled || !evidence.PopulatedZero {
		return false
	}
	if l.waitFailedCommand(cleanupCtx, cmd, record) != nil {
		return false
	}
	return l.finishFailedLaunchRecord(cmd, record) == nil
}

// D09SettlementEvidence records the measured postconditions of a settled cell.
type D09SettlementEvidence struct {
	LeaderPID     int
	PidfdObserved bool
	CgroupKilled  bool
	PopulatedZero bool
	Detail        string
}

// SettleCell records pidfd evidence for the cell leader and then settles the
// held cgroup scope (cgroup.kill + recursive populated=0 with a deadline). It is
// the D11 evidence API; a profile label alone is never evidence.
func (l *D09PodmanCrunLauncher) SettleCell(ctx context.Context, leaderPID int) (D09SettlementEvidence, error) {
	if l == nil || l.scope == nil || ctx == nil {
		return D09SettlementEvidence{}, ErrProductionLauncherUnavailable
	}
	if leaderPID <= 0 {
		return D09SettlementEvidence{}, fmt.Errorf("%w: entrypoint proof omitted a host leader PID", ErrProductionLauncherUnavailable)
	}
	pidfd, err := openPidfd(leaderPID)
	if err != nil {
		return D09SettlementEvidence{LeaderPID: leaderPID, Detail: "pidfd_open failed: " + err.Error()}, fmt.Errorf("%w: pidfd for D09 cell leader is unavailable: %v", ErrProductionLauncherUnavailable, err)
	}
	pidfdFile := os.NewFile(uintptr(pidfd), "d09-cell-pidfd")
	if pidfdFile == nil {
		_ = syscall.Close(pidfd)
		return D09SettlementEvidence{}, ErrProductionLauncherUnavailable
	}
	defer pidfdFile.Close()
	return l.settleCellWithPidfd(ctx, leaderPID, pidfdFile)
}

func (l *D09PodmanCrunLauncher) settleCellWithPidfd(ctx context.Context, leaderPID int, pidfdFile *os.File) (D09SettlementEvidence, error) {
	if l == nil || l.scope == nil || ctx == nil || leaderPID <= 0 || pidfdFile == nil {
		return D09SettlementEvidence{}, ErrProductionLauncherUnavailable
	}
	pidfd := int(pidfdFile.Fd())
	epollFD, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return D09SettlementEvidence{LeaderPID: leaderPID}, fmt.Errorf("create pidfd epoll: %w", err)
	}
	defer syscall.Close(epollFD)
	event := &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(pidfd)}
	if err := syscall.EpollCtl(epollFD, syscall.EPOLL_CTL_ADD, pidfd, event); err != nil {
		return D09SettlementEvidence{LeaderPID: leaderPID}, fmt.Errorf("register D09 leader pidfd: %w", err)
	}
	evidence := D09SettlementEvidence{LeaderPID: leaderPID}
	settled, err := l.scope.Settle(ctx) // writes cgroup.kill then observes populated=0
	evidence.PopulatedZero = settled
	if err != nil {
		return evidence, err
	}
	if !settled {
		return evidence, fmt.Errorf("%w: cgroup did not reach populated=0", ErrProductionLauncherUnavailable)
	}
	evidence.CgroupKilled = true
	for {
		var events [1]syscall.EpollEvent
		count, pollErr := syscall.EpollWait(epollFD, events[:], 50)
		if pollErr != nil && pollErr != syscall.EINTR {
			return evidence, fmt.Errorf("poll D09 leader pidfd: %w", pollErr)
		}
		if count > 0 {
			evidence.PidfdObserved = true
			break
		}
		if err := ctx.Err(); err != nil {
			return evidence, err
		}
	}
	return evidence, nil
}

// openPidfd opens a pidfd for the leader (linux/amd64 pidfd_open).
func openPidfd(pid int) (int, error) {
	fd, _, errno := syscall.Syscall(434, uintptr(pid), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

// d09NewToken returns a unique per-cell container-name suffix.
func d09NewToken() string {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(token[:])
}
