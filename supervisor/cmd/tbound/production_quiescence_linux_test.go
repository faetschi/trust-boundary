//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

// This unit boundary supplies a registered managed-root authority, not signed
// host admission. Source preparation, the authenticated offline write, durable
// launch-proof validation, lifecycle checks, lease acquisition/recovery, and
// held-directory measurements use their real implementations.
func newProductionQuiescenceFixture(t *testing.T) (*productionTrust, *sessionrepo.Generation) {
	t.Helper()
	// The shared prepared-source fixture can skip without Git. These direct
	// quiescence tests require it, so report an incomplete fixture as a failure.
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("production quiescence fixture requires installed Git: %v", err)
	}
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{{
		ResponseID: "quiescence-response", CallID: "quiescence-write", Tool: "write",
		Arguments: json.RawMessage(`{"path":"task.txt","content":"after\n"}`),
	}}, true)
	executeGovernedReplayTurn(t, fixture, true)
	baseline, tip := fixture.store.BaselineGeneration(), fixture.durable.Tip()
	if baseline.ID() == tip.ID() {
		t.Fatal("authenticated fixture write did not advance the Store tip")
	}

	root := privateGovernedTestRoot(t)
	livePath := filepath.Join(root, "live")
	if err := os.Mkdir(livePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(livePath, "task.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(livePath, "empty.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	journal, err := audit.Open(filepath.Join(root, "publication.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	const runID, sessionID = "quiescence-run", "quiescence-session"
	lease, err := newFakeManagedLiveRootAuthority().acquire(context.Background(), live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest}, journal,
		runID, governedReplayWorkflow, "quiescence-publication", 1)
	if err != nil {
		t.Fatalf("acquire registered unit-boundary writer lease: %v", err)
	}
	// This test ends at permit issuance, before a publication terminal exists.
	// Close only fixture descriptors; do not simulate a durable lease release.
	t.Cleanup(func() { _ = lease.root.Close(); _ = lease.verified.Close() })
	if err := lease.completeAdmissionRecovery(context.Background()); err != nil {
		t.Fatalf("recover managed writer lease: %v", err)
	}
	if err := lease.revalidate(context.Background(), live); err != nil {
		t.Fatalf("registered held directory is invalid before the negative: %v", err)
	}

	runner := newProductionCommandRunner(nil)
	// The only fixture operation was a write, so no command runner state is
	// retained. Bind the production runner that quiescence must check as drained.
	fixture.authority.runner = runner
	profile, template, proof := productionProofFixture()
	lifecycle := &productionLifecycleEvidence{}
	sourceBinding := fixture.authority.prepared.Provenance().BindingDigest
	if err := journalProductionLaunchProof(fixture.storeJournal, lifecycle, runID, sessionID,
		governedReplayWorkflow, governedReplayConversation, baseline.ID(), sourceBinding,
		profile, template, proof.LauncherProcessPID, proof); err != nil {
		t.Fatalf("journal bound launch proof: %v", err)
	}
	lifecycle.markWorkerLaunchAttempted()
	lifecycle.markWorkerLaunched(proof.LauncherProcessPID)
	lifecycle.markTaskAdmitted()
	lifecycle.markTurnCompleted()
	lifecycle.markChannelsStarted()
	lifecycle.markChannelsJoined(nil)
	lifecycle.markWorkerClosed(piruntime.Settlement{State: piruntime.SettlementStopped,
		ExitObserved: true, DescendantsSettled: true}, nil)
	trust := &productionTrust{
		authority: fixture.authority, storeJournal: fixture.storeJournal, baseline: baseline,
		profile: profile, template: template, sessionID: sessionID, workflowID: governedReplayWorkflow,
		conversationID: governedReplayConversation, sourceGenerationID: baseline.ID(), sourceBindingDigest: sourceBinding,
		lifecycle: lifecycle, runner: runner, runID: runID, liveRoot: live, liveRootLease: lease, ownerEpoch: 1,
	}
	if err := trust.authority.requirePreparedSource(); err != nil {
		t.Fatalf("fixture source is not durably prepared: %v", err)
	}
	if err := verifyProductionLaunchProofJournal(trust.storeJournal, lifecycle.snapshot(), profile, template,
		runID, sessionID, trust.workflowID, trust.conversationID, baseline.ID(), sourceBinding); err != nil {
		t.Fatalf("fixture launch proof did not authenticate: %v", err)
	}
	if err := validateProductionLifecycle(lifecycle.snapshot(), runID, runner); err != nil {
		t.Fatalf("fixture lifecycle is not settled: %v", err)
	}
	return trust, tip
}

func requireProductionQuiescencePermit(t *testing.T, trust *productionTrust, tip *sessionrepo.Generation) {
	t.Helper()
	if err := trust.AttestQuiescent(context.Background(), trust.liveRoot, trust.baseline, tip); err != nil {
		t.Fatalf("valid production quiescence control refused: %v", err)
	}
	permit := trust.liveRootLease.permit
	if permit == nil || permit.state != managedLivePermitIssued || permit.baselineID != trust.baseline.ID() ||
		permit.baselineTree != trust.baseline.TreeDigest() || permit.tipID != tip.ID() || permit.tipTree != tip.TreeDigest() {
		t.Fatalf("quiescence did not issue the exact baseline/tip permit: %+v", permit)
	}
}

func TestProductionTrustAttestQuiescentRegisteredControl(t *testing.T) {
	trust, tip := newProductionQuiescenceFixture(t)
	requireProductionQuiescencePermit(t, trust, tip)
}

func TestProductionTrustAttestQuiescentRejectsMissingWriterLease(t *testing.T) {
	trust, tip := newProductionQuiescenceFixture(t)
	lease := trust.liveRootLease
	trust.liveRootLease = nil
	if err := trust.AttestQuiescent(context.Background(), trust.liveRoot, trust.baseline, tip); !errors.Is(err, ErrManagedLiveRootUnavailable) {
		t.Fatalf("missing registered writer lease = %v, want managed-root refusal", err)
	}
	if lease.permit != nil {
		t.Fatal("missing writer lease issued publication authority")
	}
	trust.liveRootLease = lease
	requireProductionQuiescencePermit(t, trust, tip)
}

func TestProductionTrustAttestQuiescentRejectsHeldRootPermissionChange(t *testing.T) {
	trust, tip := newProductionQuiescenceFixture(t)
	lease := trust.liveRootLease
	if lease.identity.Mode != 0o700 {
		t.Fatalf("registered directory mode = %#o, want 0700", lease.identity.Mode)
	}
	// Change the actual directory through the held descriptor after registration
	// and lease recovery, preserving the exact live handle passed to the trust.
	if err := trust.liveRoot.Chmod(0o750); err != nil {
		t.Fatal(err)
	}
	if err := trust.AttestQuiescent(context.Background(), trust.liveRoot, trust.baseline, tip); !errors.Is(err, ErrManagedLiveRootUnavailable) {
		t.Fatalf("held root permission change = %v, want managed-root refusal", err)
	}
	if lease.permit != nil {
		t.Fatal("changed held-directory permissions issued publication authority")
	}
	if err := trust.liveRoot.Chmod(0o700); err != nil {
		t.Fatal(err)
	}
	requireProductionQuiescencePermit(t, trust, tip)
}
