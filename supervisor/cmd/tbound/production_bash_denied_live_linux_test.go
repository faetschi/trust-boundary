//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

func TestProductionD09DeniedBashCheckpointsAndClosesCleanly(t *testing.T) {
	commandJournal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "command.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commandJournal.Close() })
	profile, template := newBashRunProfile(t)
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runner := newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		cell := &sealedD09Cell{leaseID: leaseID, template: template, templateDigest: templateDigest}
		return newProductionD09BashRunnerWithInvoker(leaseID, cell, commandJournal, "run-denied-live", workflowVerticalSessionID,
			workflowVerticalID, workflowVerticalConversation, profile, template), nil
	})
	// The fixture authorizes only out.txt, so the cell's task.txt write is denied after execution.
	f := newWorkflowVerticalFixture(t, false, 0, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "denied-live-response", CallID: "denied-live-call", Tool: "bash",
			Arguments: json.RawMessage(`{"command":"printf 'bash-stage\\n' >> task.txt"}`)},
		{ResponseID: "denied-live-final", Text: "done"},
	}, runner)
	f.authority.requireEvidencedBashContainment = true
	proposal, decision := ledgerDenyTurn(t, f, "denied-live-call", gate.Allow)
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	output, err := executor.Execute(f.ctx, proposal, decision)
	var denied *protocol.AdmissionDeniedError
	if !errors.As(err, &denied) || denied.ReasonCode != sessionrepo.DenyReasonUncoveredPath || len(output) != 0 {
		t.Fatalf("settled admission denial was not released cleanly: output=%s err=%v", output, err)
	}
	bundle, err := f.store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Operations) != 1 || !deniedAfterEffect(bundle.Operations[0]) ||
		len(bundle.ApprovedDeltaLedger) != 0 || len(bundle.Generations) != 1 || f.durable.Tip().ID() != bundle.Baseline.ID {
		t.Fatalf("denied Bash command promoted or lost its effect: %+v", bundle)
	}
	checkpoint, _ := f.ledger.Snapshot()
	effect := checkpoint.Pins.EffectReservation
	if effect == nil || effect.Disposition != workflowrecovery.EffectReservationTerminal ||
		effect.Binding.OperationID != bundle.Operations[0].EffectID || effect.Binding.ToolCallID != proposal.ToolCallID ||
		effect.OutcomeDigest == "" || checkpoint.Pins.PendingCallDisposition != workflowrecovery.PendingCallTerminal ||
		checkpoint.Pins.ProviderDisposition != workflowrecovery.DispositionAwaitingProviderExchange {
		t.Fatalf("denied Bash command did not commit its terminal effect checkpoint: %+v", checkpoint.Pins)
	}
	if records := settledRecords(t, commandJournal); len(records) != 1 || runner.Pending() != 0 || runner.Settlements() != 1 {
		t.Fatalf("denied command did not settle: events=%d pending=%d settlements=%d", len(records), runner.Pending(), runner.Settlements())
	}
	requireFinalAnswer(t, f, "done", 3, 2048)
	f.conversation.Close()
	if _, err := f.store.Verify(); err != nil {
		t.Fatalf("verify settled Store: %v", err)
	}
	if disposition, err := sessionrepo.Reconstruct(bundle); err != nil || disposition.Quarantined || disposition.Unknowns != 0 {
		t.Fatalf("denied Bash command left an unresolved Store: %+v err=%v", disposition, err)
	}
	lifecycle := settledWorkerLifecycle(g1SettledWorker, true, nil)
	if err := productionSessionCloseIfUnsafe(lifecycle.snapshot(), runner, nil); err != nil {
		t.Fatalf("session close refused the settled denial: %v", err)
	}
	var quarantine productionCleanupQuarantineState
	closers := []func() error{f.store.Close, f.owner.Close}
	if retainProductionCleanupIfUnsafe(&quarantine, lifecycle.snapshot(), runner, closers) || len(quarantine.holds) != 0 {
		t.Fatal("session close quarantined the settled denial")
	}
	for index := len(closers) - 1; index >= 0; index-- {
		if err := closers[index](); err != nil {
			t.Fatalf("close settled session handle %d: %v", index, err)
		}
	}
}
