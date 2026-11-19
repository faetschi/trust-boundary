//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/runend"
)

// The launch chains the rehearsals measured (F23 m3, F28), built the way the launcher builds them: the site's message
// with its sentinel and its syscall cause both wrapped with %w (containment_launch_linux.go d09ExitedBeforeAdmission;
// TestD09ExitedBeforeAdmissionReportsPodmanExitStatus pins that both stay in the real chain), the launcher's phase
// suffix, the joined nil cleanup, and the settled typed launch error that settledFailedLaunch returns.
func preAdmissionLaunchChains() map[string]error {
	settled := piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true}
	launch := func(site error) error {
		withPhases := fmt.Errorf("%w (%s)", site, "phases_ms[podman_started=4399 pidfd_pinned=4543 map_check_start=5210]")
		return productionLaunchFailure(&piruntime.PiWorkerLaunchError{Cause: errors.Join(withPhases, nil, nil), Settlement: settled}, nil)
	}
	gone := &os.PathError{Op: "open", Path: "/proc/663609/uid_map", Err: syscall.ENOENT}
	denied := &os.PathError{Op: "stat", Path: "/proc/663609/ns/user", Err: syscall.EACCES}
	return map[string]error{
		"F23 m3 cell exited, final state read": launch(fmt.Errorf("%w: podman_exit=%s status=%q exit_code=%d oom_killed=%t error=%q: %w",
			piruntime.ErrD09WorkerExitedBeforeAdmission, "running(after 3s)", "exited", 0, false, "", gone)),
		"F23 m3 cell exited, state unavailable": launch(fmt.Errorf("%w: podman_exit=%s (final container state unavailable: %s): %w",
			piruntime.ErrD09WorkerExitedBeforeAdmission, "137", "no such container", gone)),
		"F28 uid/gid map permission denied": launch(fmt.Errorf("verify exact host-visible D09 uid/gid map before launch admission: %w", denied)),
	}
}

// R1 of the #209 verification: a launch that failed before the task was admitted, with its cell settled and its
// evidence recorded (launchNotAdmitted), is an infrastructure end the host may rerun, whatever its leaf. The same
// chain without that proof, or beside any failure the registry does not explain, still ends unclassified.
func TestPreAdmissionLaunchFailureAnswersLaunchNotAdmitted(t *testing.T) {
	for name, chain := range preAdmissionLaunchChains() {
		if answer := unknownAnswerOf(t, launchNotAdmitted{err: chain}); answer.ErrorCode != runend.CodeLaunchNotAdmitted {
			t.Errorf("%s: answer %q, want %q; chain %v", name, answer.ErrorCode, runend.CodeLaunchNotAdmitted, chain)
		}
		for what, runErr := range map[string]error{
			"no proof":                   chain,
			"beside a cleanup failure":   errors.Join(launchNotAdmitted{err: chain}, errors.New("close session before receipt: synthetic")),
			"beside an unsettled close":  errors.Join(launchNotAdmitted{err: chain}, ErrProductionLifecycleUnsettled),
			"beside a ceiling":           errors.Join(launchNotAdmitted{err: chain}, runend.ErrCeiling),
			"inside an unexplained wrap": fmt.Errorf("%w: %w", launchNotAdmitted{err: chain}, errors.New("synthetic")),
		} {
			want := runend.CodeUnclassified
			if what == "beside a ceiling" {
				want = runend.CodeBudgetEnded
			}
			if answer := unknownAnswerOf(t, runErr); answer.ErrorCode != want {
				t.Errorf("%s, %s: answer %q, want %q", name, what, answer.ErrorCode, want)
			}
		}
	}
}
