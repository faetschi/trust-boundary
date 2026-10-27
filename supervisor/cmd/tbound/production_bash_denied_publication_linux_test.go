//go:build linux

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
)

// A refused Bash command promoted nothing, so productionTrust.CheckPublicationPrerequisites skips the approved
// transition and provider-call origin for it and requires only its D09 settlement. The trust is built over the
// real denied run with no managed live-root lease: a command that passes the per-operation checks is stopped
// only by the lease check after them, which is the only place ErrManagedLiveRootUnavailable comes from.
func TestCheckPublicationPrerequisitesRequiresASettledDeniedD09BashCommand(t *testing.T) {
	run := deniedBashRun(t)
	baseline := run.store.BaselineGeneration()
	sourceBinding := "sha256:" + strings.Repeat("d", 64)
	proofJournal, err := audit.Open(filepath.Join(run.root, "launch-proof.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proofJournal.Close() })
	_, _, proof := productionProofFixture()
	proof.TemplateDigest, proof.LauncherProcessPID = run.profile.ContainmentProfileDigest, 4242
	lifecycle := settledWorkerLifecycle(g1SettledWorker, true, nil)
	lifecycle.markTurnCompleted()
	if err := journalProductionLaunchProof(proofJournal, lifecycle, bashRunRunID, bashRunSessionID, bashRunWorkflowID,
		bashRunConversationID, baseline.ID(), sourceBinding, run.profile, run.template, 4242, proof); err != nil {
		t.Fatal(err)
	}
	trust := &productionTrust{
		authority: &SessionOperationAuthority{storeJournal: run.journals[0], store: run.store, baseline: baseline,
			prepared: run.prepared, baselineBound: true, runner: run.runner},
		storeJournal: proofJournal, baseline: baseline, profile: run.profile, template: run.template,
		sessionID: bashRunSessionID, workflowID: bashRunWorkflowID, conversationID: bashRunConversationID,
		sourceGenerationID: baseline.ID(), sourceBindingDigest: sourceBinding,
		policyDigest: bashRunStorePolicy, metadataPolicyDigest: bashRunMetadataPolicy,
		lifecycle: lifecycle, runner: run.runner, runID: bashRunRunID,
	}
	check := func(bundle sessionrepo.EvidenceBundle) error {
		return trust.CheckPublicationPrerequisites(context.Background(), workflow.EvidenceRequest{Bundle: bundle})
	}

	if err := check(run.bundle); !errors.Is(err, ErrManagedLiveRootUnavailable) {
		t.Fatalf("settled denied Bash command = %v, want it past the per-operation checks and stopped only at the live-root lease", err)
	}

	// The promoted shape claimed for the refused operation is not the denied branch, and no approved transition backs it.
	promoted := run.bundle
	promoted.Operations = append([]sessionrepo.OperationEvidence(nil), run.bundle.Operations...)
	promoted.Operations[0].Outcome = "success"
	if err := check(promoted); err == nil || !strings.Contains(err.Error(), "matched 0 approved transitions") {
		t.Errorf("promoted shape over a denied operation = %v, want the missing approved transition", err)
	}

	// The refused command's settlement event is gone from the command journal.
	signed, ok := run.runner.evidenceRunners[run.operation.ViewID].(*productionD09BashRunner)
	if !ok {
		t.Fatalf("no signed runner retained for view %q", run.operation.ViewID)
	}
	trace, err := signed.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	signed.journal = derivedJournal(t, trace, func(_ int, event audit.Event) (audit.Event, bool) {
		return event, event.Kind != productionD09SettledKind
	})
	if err := check(run.bundle); !errors.Is(err, ErrProductionCommandEvidence) || !strings.Contains(err.Error(), "event-journal-record") {
		t.Errorf("denied command without its settlement = %v, want the event-journal-record refusal", err)
	}
}
