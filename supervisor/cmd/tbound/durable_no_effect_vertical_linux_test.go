//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

const wantNoEffectRefusalOutput = `{"code":"invalid_arguments","message":"The tool arguments are not valid. Check the tool schema and send a corrected call."}`

// noEffectEvidence is the Store and budget state that a refused call must leave exactly as it found it.
type noEffectEvidence struct {
	storeRecords  int
	tipGeneration string
	tipTree       string
	hostCalls     uint64
	tokens        uint64
}

func snapshotNoEffectEvidence(t *testing.T, f *workflowVerticalFixture) noEffectEvidence {
	t.Helper()
	trace, err := f.storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := f.ledger.Snapshot()
	tip := f.durable.Tip()
	return noEffectEvidence{storeRecords: len(trace.Records), tipGeneration: tip.ID(), tipTree: tip.TreeDigest(),
		hostCalls: checkpoint.Counters.HostCalls, tokens: checkpoint.Counters.Tokens}
}

func requireNoEffectStoreUnchanged(t *testing.T, f *workflowVerticalFixture, before noEffectEvidence) {
	t.Helper()
	if after := snapshotNoEffectEvidence(t, f); after != before {
		t.Fatalf("refusal changed Store or budget state: before=%+v after=%+v", before, after)
	}
	if _, err := f.store.Verify(); err != nil {
		t.Fatalf("Store is unusable after the refusal: %v", err)
	}
}

// requireRetiredNoEffectCall checks the checkpoint a released refusal leaves: the conversation awaits the provider,
// the refused call is retired and uncharged, and no effect can be reserved for it.
func requireRetiredNoEffectCall(t *testing.T, f *workflowVerticalFixture, toolCallID string, hostCalls, tokens uint64) {
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
		t.Fatalf("released refusal was not checkpointed as a retired call awaiting the provider: pins=%+v counters=%+v",
			checkpoint.Pins, checkpoint.Counters)
	}
	captured := reservation.CapturedCall
	retired := workflowrecovery.EffectBinding{OperationID: "retired-effect-" + toolCallID,
		SpecDigest: bareWorkflowDigest(digestBytes([]byte(toolCallID))), WorkflowID: captured.WorkflowID,
		ConversationID: captured.ConversationID, HostCallID: captured.HostCallID, ToolCallID: captured.ToolCallID,
		HostCallOutcomeDigest: captured.OutcomeDigest, RecoveryGeneration: captured.RecoveryGeneration,
		OwnerEpoch: captured.OwnerEpoch}
	if _, err := f.ledger.ReserveEffect(f.ctx, retired, acceptAnyCapturedOperation{}); err == nil {
		t.Fatal("retired refused call was reserved for an effect")
	}
	if after, afterDigest := f.ledger.Snapshot(); afterDigest != digest || after.Counters != checkpoint.Counters {
		t.Fatal("refused effect reservation for a retired call changed the checkpoint")
	}
}

// requireNextRequestCarriesNoEffectRefusal checks that the provider request after the refusal carries the journaled
// refusal as its tool message under the refused call's ID, byte for byte.
func requireNextRequestCarriesNoEffectRefusal(t *testing.T, f *workflowVerticalFixture, toolCallID string) {
	t.Helper()
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var result providerResultEvidence
	var exchanges []providerExchangeEvidence
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "provider_tool_result":
			var evidence providerResultEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.ToolCallID == toolCallID {
				result = evidence
			}
		case "provider_exchange":
			var exchange providerExchangeEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &exchange); err != nil {
				t.Fatal(err)
			}
			exchanges = append(exchanges, exchange)
		}
	}
	if len(exchanges) < 2 {
		t.Fatalf("provider exchanges=%d, the continuation after the refusal was never issued", len(exchanges))
	}
	var request struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(exchanges[1].Request.Body, &request); err != nil || len(request.Messages) == 0 {
		t.Fatalf("continuation request is unreadable: messages=%d err=%v", len(request.Messages), err)
	}
	decoded, err := protocol.DecodeResult(result.Result)
	if err != nil || decoded.Status != protocol.ResultStatusNoEffectRefused || string(decoded.Output) != wantNoEffectRefusalOutput {
		t.Fatalf("journaled refusal is not the fixed no-effect result: %+v err=%v", decoded, err)
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != toolCallID || last.Content != string(result.Result) {
		t.Fatalf("next provider request tool message=%+v, want the exact journaled refusal", last)
	}
}

func providerExchangeCount(t *testing.T, f *workflowVerticalFixture) int {
	t.Helper()
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range trace.Records {
		if record.Event.Kind == "provider_exchange" {
			count++
		}
	}
	return count
}

// A refusal that the durable executor proves is released as the fixed no_effect_refused result, retires the captured
// call, leaves the Store and budgets as they were, and the provider request after it carries the refusal.
func TestNoEffectRefusedReadIsReleasedRetiredAndContinued(t *testing.T) {
	for _, tc := range []struct {
		name, arguments string
	}{
		{"zero offset", `{"path":"task.txt","offset":0}`},
		{"empty path", `{"path":""}`},
		{"absolute path under no workspace root", `{"path":"/workspace/task.txt"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// One retry allowance raises the fixture's token budget to three provider requests.
			f := newWorkflowVerticalFixture(t, false, 1, []providerbridge.DevelopmentProviderTurn{
				{ResponseID: "r-refused", CallID: "c-refused", Tool: "read", Arguments: json.RawMessage(tc.arguments)},
				{ResponseID: "r-read", CallID: "c-read", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
				{ResponseID: "r-final", Text: "done"},
			})
			executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
			if err != nil {
				t.Fatal(err)
			}
			proposal, decision := ledgerDenyTurn(t, f, "c-refused", gate.Allow)
			before := snapshotNoEffectEvidence(t, f)
			_, err = executor.Execute(f.ctx, proposal, decision)
			var refused *protocol.NoEffectError
			if !errors.As(err, &refused) || refused.Tool != "read" {
				t.Fatalf("refused read was not released as *protocol.NoEffectError: %v", err)
			}
			requireRetiredNoEffectCall(t, f, "c-refused", 1, 1024)
			requireNoEffectStoreUnchanged(t, f, before)
			proposal, decision = ledgerDenyTurn(t, f, "c-read", gate.Allow)
			if _, err := executor.Execute(f.ctx, proposal, decision); err != nil {
				t.Fatalf("later legitimate read failed after the refusal: %v", err)
			}
			// The executed read adds one host-call unit on top of its request, as in the ALLOW checkpoint test.
			requireFinalAnswer(t, f, "done", 4, 3072)
			requireNextRequestCarriesNoEffectRefusal(t, f, "c-refused")
		})
	}
}

// forgingNoEffectExecutor performs the real read, then returns a no-effect marker for that same call. The Store
// retains the read, so the durable absence proof must reject the marker.
type forgingNoEffectExecutor struct {
	*DurableExecutor
}

func (w forgingNoEffectExecutor) Execute(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) (json.RawMessage, error) {
	if _, err := w.DurableExecutor.Execute(ctx, proposal, decision); err != nil {
		return nil, err
	}
	return nil, &protocol.NoEffectError{Tool: proposal.Tool, Err: errors.New("forged refusal after a real read")}
}

func TestNoEffectForgedMarkerAfterARealReadIsWithheldAndNoNextRequestIsIssued(t *testing.T) {
	f := newWorkflowVerticalFixture(t, false, 0, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "r-real", CallID: "c-real", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "r-final", Text: "done"},
	})
	executor, err := providerbridge.NewExecutor(f.conversation, forgingNoEffectExecutor{DurableExecutor: f.durable})
	if err != nil {
		t.Fatal(err)
	}
	proposal, decision := ledgerDenyTurn(t, f, "c-real", gate.Allow)
	_, err = executor.Execute(f.ctx, proposal, decision)
	if err == nil || !errors.Is(err, providerbridge.ErrResultWithheld) {
		t.Fatalf("forged refusal was not withheld as UNKNOWN: %v", err)
	}
	var marker *protocol.NoEffectError
	if errors.As(err, &marker) {
		t.Fatalf("withheld forged refusal still carries the marker: %v", err)
	}
	if !strings.Contains(err.Error(), "no-effect proof failed") {
		t.Fatalf("forged refusal was not rejected by the no-effect proof: %v", err)
	}
	if _, err := f.conversation.NextTurn(f.ctx); !errors.Is(err, providerbridge.ErrClosed) {
		t.Fatalf("conversation issued a next request after a forged refusal: %v", err)
	}
	if count := providerExchangeCount(t, f); count != 1 {
		t.Fatalf("provider exchanges=%d after a forged refusal, want 1", count)
	}
}

// A crash after a released refusal, before the next provider exchange or after the final answer: the persisted owner
// checkpoint, with the refused call retired, must replay in a fresh process.
func TestNoEffectCheckpointReopensInAFreshProcess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		finish bool
	}{{"before the next exchange", false}, {"after the final answer", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkflowVerticalFixture(t, false, 0, []providerbridge.DevelopmentProviderTurn{
				{ResponseID: "r-refused", CallID: "c-refused", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt","offset":0}`)},
				{ResponseID: "r-final", Text: "done"},
			})
			executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
			if err != nil {
				t.Fatal(err)
			}
			proposal, decision := ledgerDenyTurn(t, f, "c-refused", gate.Allow)
			var refused *protocol.NoEffectError
			if _, err := executor.Execute(f.ctx, proposal, decision); !errors.As(err, &refused) {
				t.Fatalf("refused read was not released: %v", err)
			}
			requireRetiredNoEffectCall(t, f, "c-refused", 1, 1024)
			if tc.finish {
				requireFinalAnswer(t, f, "done", 2, 2048)
			}
			reopenLedgerInFreshProcess(t, f)
		})
	}
}

// A refusal only the Store can decide (here a missing read target) is not a no-effect class: it stays an UNKNOWN
// execution, the result is withheld without the marker, and no next provider request is issued.
func TestNoEffectStoreSideRefusalStaysUnknown(t *testing.T) {
	f := newWorkflowVerticalFixture(t, false, 0, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "r-path", CallID: "c-path", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)},
		{ResponseID: "r-final", Text: "done"},
	})
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	proposal, decision := ledgerDenyTurn(t, f, "c-path", gate.Allow)
	_, err = executor.Execute(f.ctx, proposal, decision)
	var marker *protocol.NoEffectError
	if !errors.Is(err, providerbridge.ErrResultWithheld) || errors.As(err, &marker) {
		t.Fatalf("Store-side refusal was not withheld as UNKNOWN without the marker: %v", err)
	}
	if _, err := f.conversation.NextTurn(f.ctx); !errors.Is(err, providerbridge.ErrClosed) {
		t.Fatalf("conversation issued a next request after a Store-side refusal: %v", err)
	}
	if count := providerExchangeCount(t, f); count != 1 {
		t.Fatalf("provider exchanges=%d after a Store-side refusal, want 1", count)
	}
}

// Serve releases a no-effect marker only from an executor that journals and proves the refusal. The publication
// runtime's admission wrapper runs the raw durable executor, so a marker it returns must stay withheld.
func TestNoEffectReleaseNeedsTheProvingConversationExecutor(t *testing.T) {
	var admitted piruntime.Executor = &admittedExecutor{}
	if _, ok := admitted.(piruntime.VerifiedNoEffectReleaser); ok {
		t.Fatal("the publication runtime's admission wrapper may release unproven no-effect markers")
	}
	var resulting piruntime.Executor = &providerbridge.ResultingExecutor{}
	if _, ok := resulting.(piruntime.VerifiedNoEffectReleaser); !ok {
		t.Fatal("the proving conversation executor cannot release no-effect refusals")
	}
}
