//go:build linux

package piruntime

import (
	"context"
	"errors"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
)

// F5 G1: the entrypoint refused the command executable before the host acknowledged the start. The
// entrypoint withholds the command until that acknowledgement, so a failure before it, with the cell's
// cgroup observed empty, is typed as a command that never started. Once the acknowledgement may have
// reached the cell, or the cell is not observed gone, the failure stays untyped.
func TestD09PreStartFailureTypedOnlyWhenTheCommandCannotHaveRun(t *testing.T) {
	failed := errors.New("D09 command container did not start with the authorized identity")
	for name, tc := range map[string]struct{ ackSent, settled, typed bool }{
		"entrypoint failed before the acknowledgement":   {false, true, true},
		"acknowledgement sent: the command may have run": {true, true, false},
		"cell not observed gone":                         {false, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := d09PreStartFailure(failed, tc.ackSent, tc.settled)
			if !errors.Is(err, failed) || errors.Is(err, protocol.ErrCommandNotStarted) != tc.typed {
				t.Fatalf("failure = %v, want typed=%t and the cause kept", err, tc.typed)
			}
		})
	}
	if err := d09PreStartFailure(nil, false, true); err != nil {
		t.Fatalf("a successful run was typed as a failure: %v", err)
	}
}

// A refused command cgroup allocation (here: a launcher with no bound scope) happens before any cell
// exists, so it is typed as never started and productionCommandRunner releases the lease.
func TestD09CommandScopeAllocationFailureIsTypedNotStarted(t *testing.T) {
	runner := &D09CommandRunner{launcher: &D09PodmanCrunLauncher{}, leaseID: "lease-1"}
	_, err := runner.allocateCommandScope(context.Background(), "view-1")
	if !errors.Is(err, protocol.ErrCommandNotStarted) || !errors.Is(err, ErrCgroupScopeRefused) {
		t.Fatalf("allocation failure = %v, want ErrCommandNotStarted wrapping the refusal", err)
	}
}
