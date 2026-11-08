//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/workflowrecovery"
)

// A launch that fails before admission (the D09 uid/gid map check) after the launcher proved its cell
// gone must free session cleanup, so the held owner scope is removed and journaled drained. The
// reconciler then settles the workflow from that receipt alone, with no live scope to open. A launch
// whose cleanup proved nothing keeps the scope, and a leftover process still refuses the drain.
func TestPreAdmissionLaunchFailureLeavesDrainedOwnerReceipt(t *testing.T) {
	const workflow = "workflow-launch-failed-before-admission"
	identity := "sha256:" + strings.Repeat("d", 64)
	dead := workflowrecovery.OwnerIncarnation{
		Digest: strings.Repeat("b", 64), BootID: "different-boot", PID: 424242, StartTimeTicks: 1,
		PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64),
	}
	settled := piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true}
	admissionErr := errors.New("verify exact host-visible D09 uid/gid map before launch admission: open /proc/1/gid_map: invalid argument")
	wrap := func(err error) error {
		return fmt.Errorf("launch admitted production Pi worker: %w", fmt.Errorf("start admitted native child: %w", err))
	}
	bind := func(f unboundScopeFixture) {
		f.catalog.ownerScopes[workflowOwnerEpochKey(workflow, 1)] = piServiceWorkflowOwnerScope{
			AdmissionHash: strings.Repeat("a", 64), WorkflowID: workflow, OwnerEpoch: 1, Incarnation: dead,
			ScopeName: workflowOwnerScopeName(workflow, 1), ScopePath: f.path, ScopeIdentityDigest: identity}
	}
	// closeSession runs the session cleanup the binder gates on the lifecycle evidence.
	closeSession := func(t *testing.T, f unboundScopeFixture, launchErr error) bool {
		lifecycle := &productionLifecycleEvidence{}
		lifecycle.markWorkerLaunchAttempted()
		lifecycle.markLaunchFailed(launchErr)
		if !productionCleanupSafe(lifecycle.snapshot(), newProductionCommandRunner(nil)) {
			return false
		}
		scope, err := f.opener.open(nil, workflowOwnerScopeName(workflow, 1), "", "", identity)
		if err != nil {
			t.Fatal(err)
		}
		if err := scope.Remove(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := f.catalog.MarkOwnerScopeDrained(strings.Repeat("a", 64), workflow, 1, dead,
			f.verifier.cgroupRoot, "session_cleanup_complete"); err != nil {
			t.Fatal(err)
		}
		return true
	}
	verify := func(f unboundScopeFixture) error {
		_, err := f.verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), workflow,
			[]workflowrecovery.OwnerEpochIdentity{f.owner})
		return err
	}

	t.Run("settled launch failure drains the scope and abandon settles", func(t *testing.T) {
		f := newUnboundScopeFixture(t, workflow, dead, true, identity)
		bind(f)
		if !closeSession(t, f, wrap(&piruntime.PiWorkerLaunchError{Cause: admissionErr, Settlement: settled})) {
			t.Fatal("a launch failure with a proven stopped cell kept the session cleanup")
		}
		f.opener.openScopeMustNotRun(t, &f.verifier)
		if err := verify(f); err != nil {
			t.Fatalf("drained receipt of the failed launch was refused: %v", err)
		}
		trace, err := f.journal.Trace()
		if err != nil || len(trace.Records) != 1 || trace.Records[0].Event.Kind != "pi_workflow_owner_scope_drained" {
			t.Fatalf("failed launch journaled %d records, want one drained receipt: %v", len(trace.Records), err)
		}
	})

	t.Run("untyped launch failure keeps the scope and its cleanup", func(t *testing.T) {
		f := newUnboundScopeFixture(t, workflow, dead, true, identity)
		bind(f)
		if closeSession(t, f, wrap(admissionErr)) {
			t.Fatal("an untyped launch failure released the session cleanup")
		}
		if _, err := os.Lstat(f.path); err != nil {
			t.Fatalf("retained scope was removed: %v", err)
		}
		if state, _ := f.catalog.ownerScope(workflow, 1); state.Drained {
			t.Fatal("retained scope was recorded drained")
		}
	})

	t.Run("leftover process in the scope still refuses the drain", func(t *testing.T) {
		f := newUnboundScopeFixture(t, workflow, dead, true, identity)
		bind(f)
		// A populated scope cannot be removed: the fake unlinks only an empty directory.
		if err := os.WriteFile(filepath.Join(f.path, "cgroup.procs"), []byte("4242\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verify(f); err == nil {
			t.Fatal("a scope that still holds a process was accepted as drained")
		}
		if state, _ := f.catalog.ownerScope(workflow, 1); state.Drained {
			t.Fatal("a scope that still holds a process was recorded drained")
		}
	})
}

// openScopeMustNotRun fails the test if the verifier opens a live scope: a drained receipt is enough.
func (f *fakeOwnerScopeOpener) openScopeMustNotRun(t *testing.T, v *catalogOwnerDrainVerifier) {
	t.Helper()
	v.openScope = func(*os.File, string, string, string, string) (ownerScopeDrainer, error) {
		t.Fatal("verifier opened a live scope although a drained receipt exists")
		return nil, nil
	}
}
