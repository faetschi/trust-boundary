//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

const (
	ledgerDenyReopenOwnerEnv  = "TBOUND_LEDGER_DENY_REOPEN_OWNER"
	ledgerDenyReopenRootEnv   = "TBOUND_LEDGER_DENY_REOPEN_ROOT"
	ledgerDenyReopenStoreEnv  = "TBOUND_LEDGER_DENY_REOPEN_OWNER_STORE"
	ledgerDenyReopenDigestEnv = "TBOUND_LEDGER_DENY_REOPEN_DIGEST"
)

type acceptAnyCapturedOperation struct{}

func (acceptAnyCapturedOperation) VerifyCapturedOperation(context.Context, workflowrecovery.Checkpoint,
	workflowrecovery.EffectBinding) error {
	return nil
}

// ledgerDenyTurn captures one scripted tool call, gates it under the fixture policy and records the
// decision. It returns the proposal and decision for an ALLOW to execute.
func ledgerDenyTurn(t *testing.T, f *workflowVerticalFixture, toolCallID string, want gate.Verdict) (protocol.Proposal, gate.Decision) {
	t.Helper()
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.ToolCall == nil || turn.ToolCall.ID != toolCallID {
		t.Fatalf("provider turn did not capture %s: turn=%+v err=%v", toolCallID, turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID: turn.ToolCall.ID, Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
	matched, err := f.conversation.Correlate(f.ctx, proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate %s: matched=%+v err=%v", toolCallID, matched, err)
	}
	decision := gate.Evaluate(proposal, matched, f.policy)
	if decision.Verdict != want {
		t.Fatalf("%s verdict = %s, want %s", toolCallID, decision.Verdict, want)
	}
	if err := f.conversation.RecordDecision(f.ctx, proposal, decision); err != nil {
		t.Fatalf("record %s decision for %s: %v", want, toolCallID, err)
	}
	return proposal, decision
}

// requireRetiredDeny checks the checkpoint a released DENY leaves: the conversation still awaits the
// model's answer, and the denied call is retired, uncharged and refused for any effect.
func requireRetiredDeny(t *testing.T, f *workflowVerticalFixture, toolCallID string, hostCalls, tokens uint64) {
	t.Helper()
	checkpoint, digest := f.ledger.Snapshot()
	reservation := checkpoint.Pins.HostCallReservation
	if reservation == nil || reservation.Disposition != workflowrecovery.HostCallTerminal || reservation.CapturedCall == nil ||
		reservation.CapturedCall.ToolCallID != toolCallID ||
		reservation.CapturedCall.Disposition != workflowrecovery.CapturedCallRetired ||
		reservation.CapturedCall.RetirementDigest == "" ||
		checkpoint.Pins.PendingCallDisposition != workflowrecovery.PendingCallRetired ||
		checkpoint.Pins.ProviderDisposition != workflowrecovery.DispositionAwaitingProviderExchange ||
		checkpoint.Counters.HostCalls != hostCalls || checkpoint.Counters.Tokens != tokens {
		t.Fatalf("released DENY was not checkpointed as a retired call awaiting the provider: pins=%+v counters=%+v",
			checkpoint.Pins, checkpoint.Counters)
	}
	captured := reservation.CapturedCall
	retired := workflowrecovery.EffectBinding{OperationID: "retired-effect-" + toolCallID,
		SpecDigest: bareWorkflowDigest(digestBytes([]byte(toolCallID))), WorkflowID: captured.WorkflowID,
		ConversationID: captured.ConversationID, HostCallID: captured.HostCallID, ToolCallID: captured.ToolCallID,
		HostCallOutcomeDigest: captured.OutcomeDigest, RecoveryGeneration: captured.RecoveryGeneration,
		OwnerEpoch: captured.OwnerEpoch}
	if _, err := f.ledger.ReserveEffect(f.ctx, retired, acceptAnyCapturedOperation{}); err == nil {
		t.Fatal("retired DENY call was reserved for an effect")
	}
	if after, afterDigest := f.ledger.Snapshot(); afterDigest != digest || after.Counters != checkpoint.Counters {
		t.Fatal("refused effect reservation for a retired call changed the checkpoint")
	}
}

func requireFinalAnswer(t *testing.T, f *workflowVerticalFixture, text string, hostCalls, tokens uint64) {
	t.Helper()
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.ToolCall != nil || turn.AssistantText != text {
		t.Fatalf("final provider turn = %+v err=%v, want text %q", turn, err, text)
	}
	checkpoint, _ := f.ledger.Snapshot()
	if checkpoint.Pins.ProviderDisposition != workflowrecovery.DispositionAwaitingHostPrompt ||
		checkpoint.Pins.HostCallReservation == nil ||
		checkpoint.Pins.HostCallReservation.Disposition != workflowrecovery.HostCallTerminal ||
		checkpoint.Pins.HostCallReservation.CapturedCall != nil ||
		checkpoint.Counters.HostCalls != hostCalls || checkpoint.Counters.Tokens != tokens {
		t.Fatalf("final answer was not checkpointed at the host prompt: pins=%+v counters=%+v",
			checkpoint.Pins, checkpoint.Counters)
	}
}

// reopenLedgerInFreshProcess closes this process's owner handles and reopens the persisted owner in a
// fresh process, which replays and revalidates every committed transition.
func reopenLedgerInFreshProcess(t *testing.T, f *workflowVerticalFixture) {
	t.Helper()
	_, digest := f.ledger.Snapshot()
	if err := f.owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.protectedRoot.Close(); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestGovernedLedgerDenyRetiresTheCallAndTheConversationContinues$")
	cmd.Env = []string{ledgerDenyReopenOwnerEnv + "=" + f.ownerPath, ledgerDenyReopenRootEnv + "=" + f.rootPath,
		ledgerDenyReopenStoreEnv + "=" + f.ownerStoreDir, ledgerDenyReopenDigestEnv + "=" + digest,
		"TMPDIR=" + os.TempDir()}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh-process reopen of the persisted checkpoint: %v\n%s", err, output)
	}
}

func TestGovernedLedgerDenyRetiresTheCallAndTheConversationContinues(t *testing.T) {
	if os.Getenv(ledgerDenyReopenOwnerEnv) != "" {
		reopenLedgerDenyChild(t)
		return
	}
	f := newWorkflowVerticalFixture(t, false, 0, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "r-write", CallID: "c-write", Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"x\n"}`)},
		{ResponseID: "r-final", Text: "done"},
	})
	ledgerDenyTurn(t, f, "c-write", gate.Deny)
	requireRetiredDeny(t, f, "c-write", 1, 1024)
	requireFinalAnswer(t, f, "done", 2, 2048)
	requireNextRequestCarriesDenyHint(t, f)
	reopenLedgerInFreshProcess(t, f)
}

func TestGovernedLedgerDenyThenARetryableProviderFailureContinues(t *testing.T) {
	f := newWorkflowVerticalFixture(t, false, 1, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "r-write", CallID: "c-write", Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"x\n"}`)},
		{FailStatus: 503, FailBody: `{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`},
		{ResponseID: "r-final", Text: "done"},
	})
	ledgerDenyTurn(t, f, "c-write", gate.Deny)
	requireRetiredDeny(t, f, "c-write", 1, 1024)
	requireFinalAnswer(t, f, "done", 3, 3072)
	reopenLedgerInFreshProcess(t, f)
}

func TestGovernedLedgerAllowDenyAllowCheckpointsEveryCall(t *testing.T) {
	f := newWorkflowVerticalFixture(t, false, 2, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "r-read-1", CallID: "c-read-1", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "r-write", CallID: "c-write", Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"x\n"}`)},
		{ResponseID: "r-read-2", CallID: "c-read-2", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "r-final", Text: "done"},
	})
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	allow := func(toolCallID string, hostCalls, tokens uint64) {
		t.Helper()
		proposal, decision := ledgerDenyTurn(t, f, toolCallID, gate.Allow)
		if _, err := activeDeniedCaptureDigest(f.providerJournal, toolCallID); err == nil {
			t.Fatal("journaled ALLOW was accepted as DENY retirement evidence")
		}
		if _, err := executor.Execute(f.ctx, proposal, decision); err != nil {
			t.Fatalf("execute allowed %s: %v", toolCallID, err)
		}
		if _, err := activeDeniedCaptureDigest(f.providerJournal, toolCallID); err == nil {
			t.Fatal("executed ALLOW result was accepted as DENY retirement evidence")
		}
		checkpoint, _ := f.ledger.Snapshot()
		effect := checkpoint.Pins.EffectReservation
		if effect == nil || effect.Disposition != workflowrecovery.EffectReservationTerminal || effect.Binding.ToolCallID != toolCallID ||
			checkpoint.Pins.PendingCallDisposition != workflowrecovery.PendingCallTerminal ||
			checkpoint.Pins.ProviderDisposition != workflowrecovery.DispositionAwaitingProviderExchange ||
			checkpoint.Counters.HostCalls != hostCalls || checkpoint.Counters.Tokens != tokens {
			t.Fatalf("allowed %s was not checkpointed terminal: pins=%+v counters=%+v", toolCallID, checkpoint.Pins, checkpoint.Counters)
		}
	}
	allow("c-read-1", 2, 1024)
	ledgerDenyTurn(t, f, "c-write", gate.Deny)
	requireRetiredDeny(t, f, "c-write", 3, 2048)
	allow("c-read-2", 5, 3072)
	requireFinalAnswer(t, f, "done", 6, 4096)
	reopenLedgerInFreshProcess(t, f)
}

// reopenLedgerDenyChild runs in the fresh process started by reopenLedgerInFreshProcess.
func reopenLedgerDenyChild(t *testing.T) {
	ownerPath := os.Getenv(ledgerDenyReopenOwnerEnv)
	ctx := context.Background()
	boundary, err := workflowrecovery.NewLinuxHostBoundary(os.Getenv(ledgerDenyReopenStoreEnv), uint32(os.Geteuid()),
		verticalNoPriorOwnerDrain{}, &verticalFreshWorkflowPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := workflowrecovery.OpenExistingProtectedRoot(os.Getenv(ledgerDenyReopenRootEnv), os.Getenv(ledgerDenyReopenStoreEnv), boundary)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	owner, err := workflowrecovery.OpenExistingSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("reopen and replay the owner history: %v", err)
	}
	defer owner.Close()
	checkpoint, digest, exists, err := owner.LoadCheckpoint(ctx, workflowVerticalID)
	if err != nil || !exists || digest != os.Getenv(ledgerDenyReopenDigestEnv) {
		t.Fatalf("persisted head differs: exists=%v err=%v digest=%s", exists, err, digest)
	}
	if _, err := workflowrecovery.Open(ctx, owner, workflowrecovery.OpenOptions{WorkflowID: workflowVerticalID,
		OwnerEpoch: checkpoint.Pins.OwnerEpoch, RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits}); err != nil {
		t.Fatalf("open the persisted checkpoint: %v", err)
	}
}

// Inspect the actual second request, including the exact Go-owned tool Result and static hint.
func requireNextRequestCarriesDenyHint(t *testing.T, f *workflowVerticalFixture) {
	t.Helper()
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var result providerResultEvidence
	var next providerExchangeEvidence
	exchanges := 0
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "provider_tool_result":
			if err := unmarshalGovernedJSON(record.Event.Data, &result); err != nil {
				t.Fatal(err)
			}
		case "provider_exchange":
			exchanges++
			if exchanges == 2 {
				if err := unmarshalGovernedJSON(record.Event.Data, &next); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	decoded, err := protocol.DecodeResult(result.Result)
	if err != nil || decoded.Verdict != string(gate.Deny) || decoded.Tool != "write" || decoded.ReasonCode != "policy_default_deny" ||
		result.EffectID != "" || result.TransitionID != "" {
		t.Fatalf("missing effect-free write denial: %+v err=%v", result, err)
	}
	var request struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(next.Request.Body, &request); err != nil {
		t.Fatal(err)
	}
	want := string(result.Result) + "\nhint: The policy does not allow this tool or action. Use another available tool on the task's files."
	if exchanges != 2 || len(request.Messages) == 0 {
		t.Fatalf("provider exchanges=%d, missing continuation request", exchanges)
	}
	got := request.Messages[len(request.Messages)-1]
	if got.Role != "tool" || got.ToolCallID != "c-write" || got.Content != want {
		t.Fatalf("next provider request tool message=%+v, want exact denial and hint %q", got, want)
	}
}
