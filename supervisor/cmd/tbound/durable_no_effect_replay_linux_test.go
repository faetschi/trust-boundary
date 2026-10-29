//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
)

// allowNoEffectReplayTurn captures, correlates and ALLOWs the next provider call, whose arguments the durable decoder
// refuses. With release, the provider executor must release the refusal; without it the ALLOW stays pending.
func allowNoEffectReplayTurn(t *testing.T, fixture *governedReplayFixture, release bool) {
	t.Helper()
	ctx := context.Background()
	turn, err := fixture.conversation.NextTurn(ctx)
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("expected a captured provider tool call, got %+v, err=%v", turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
		Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
	matched, err := fixture.conversation.Correlate(ctx, proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate %s: %+v, err=%v", proposal.ToolCallID, matched, err)
	}
	decision := gate.Evaluate(proposal, matched, fixture.policy)
	if decision.Verdict != gate.Allow {
		t.Fatalf("fixture policy did not allow %s: %+v", proposal.Tool, decision)
	}
	if err := fixture.conversation.RecordDecision(ctx, proposal, decision); err != nil {
		t.Fatalf("record gate decision: %v", err)
	}
	if !release {
		return
	}
	var refused *protocol.NoEffectError
	if _, err := fixture.providerExec.Execute(ctx, proposal, decision); !errors.As(err, &refused) || refused.Tool != proposal.Tool {
		t.Fatalf("refused %s was not released as a no-effect refusal: %v", proposal.ToolCallID, err)
	}
}

// forgeProviderResult copies the provider journal, rewrites the Result released for toolCallID, and rehashes it, so
// the copy is internally consistent and only the Store cross-link and the refusal proof can refute it.
func forgeProviderResult(t *testing.T, fixture *governedReplayFixture, journalName, toolCallID string,
	rewrite func(*protocol.Result)) *audit.Journal {
	t.Helper()
	original, err := fixture.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), journalName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tampered.Close() })
	rewritten := false
	for _, record := range original.Records {
		event := record.Event
		if event.Kind == "provider_tool_result" {
			var evidence providerResultEvidence
			if err := unmarshalGovernedJSON(event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.ToolCallID == toolCallID {
				result, err := protocol.DecodeResult(evidence.Result)
				if err != nil {
					t.Fatal(err)
				}
				rewrite(&result)
				if evidence.Result, err = protocol.MarshalResult(result); err != nil {
					t.Fatal(err)
				}
				evidence.ResultDigest = digestBytes(evidence.Result)
				if event.Data, err = json.Marshal(evidence); err != nil {
					t.Fatal(err)
				}
				rewritten = true
			}
		}
		if _, err := tampered.Append(event); err != nil {
			t.Fatalf("append rehashed event %d: %v", record.Sequence, err)
		}
	}
	if !rewritten {
		t.Fatalf("provider journal has no result for %s", toolCallID)
	}
	return tampered
}

func newNoEffectReplayFixture(t *testing.T) *governedReplayFixture {
	t.Helper()
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "ne-r-read", CallID: "ne-c-read", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt","offset":0}`)},
		{ResponseID: "ne-r-write", CallID: "ne-c-write", Tool: "write", Arguments: json.RawMessage(`{"path":"","content":"x\n"}`)},
		{ResponseID: "ne-r-edit", CallID: "ne-c-edit", Tool: "edit", Arguments: json.RawMessage(`{"path":"task.txt","edits":[]}`)},
		{ResponseID: "ne-r-bash", CallID: "ne-c-bash", Tool: "bash", Arguments: json.RawMessage(`{"command":""}`)},
		{ResponseID: "ok-r-read", CallID: "ok-c-read", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "ok-r-write", CallID: "ok-c-write", Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"after\n"}`)},
	}, true)
	for range 4 {
		allowNoEffectReplayTurn(t, fixture, true)
	}
	executeGovernedReplayTurn(t, fixture, true)
	executeGovernedReplayTurn(t, fixture, true)
	return fixture
}

// Replay re-derives each refusal from the retained arguments and the complete Store evidence, so a run with refusals
// of all four tools and two later legitimate calls verifies, the refusals add no Store operation, and a second replay
// pass reconstructs the same projection.
func TestNoEffectRefusalsReplayWithTheirLegitimateCalls(t *testing.T) {
	fixture := newNoEffectReplayFixture(t)
	projection, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
	if err != nil {
		t.Fatalf("replay of a run with no-effect refusals: %v", err)
	}
	if projection.VerificationScope != "store_crosslinked" || projection.Provider.ExchangeCount != 6 ||
		projection.Provider.CapturedCallCount != 6 || projection.Provider.DecisionCount != 6 ||
		projection.Provider.ResultCount != 6 || projection.StoreOperationCount != 2 ||
		projection.Provider.Disposition != providerbridge.DispositionAwaitingProviderExchange ||
		!VerifyGovernedTranscriptProjectionCommitment(projection) {
		t.Fatalf("replayed projection does not count six released results over two Store operations: %+v", projection)
	}
	again, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
	if err != nil || !reflect.DeepEqual(again, projection) {
		t.Fatalf("second replay pass differs: err=%v\nfirst  %+v\nsecond %+v", err, projection, again)
	}
}

// A self-consistent forged result must not verify: a refusal rewritten as a successful read has no Store operation,
// and a real read rewritten as a refusal has one.
func TestNoEffectReplayRejectsForgedResults(t *testing.T) {
	fixture := newNoEffectReplayFixture(t)
	forgeries := []struct {
		journal, call string
		rewrite       func(*protocol.Result)
	}{
		{"ne-refusal-as-read.audit", "ne-c-read", func(r *protocol.Result) {
			r.Status, r.Output = "", json.RawMessage(`{"tool":"read","content":"forged\n"}`)
		}},
		{"ne-refusal-without-status.audit", "ne-c-write", func(r *protocol.Result) {
			r.Status = ""
		}},
		{"ne-read-as-refusal.audit", "ok-c-read", func(r *protocol.Result) {
			r.Status, r.Output = protocol.ResultStatusNoEffectRefused, json.RawMessage(wantNoEffectRefusalOutput)
		}},
	}
	for _, forgery := range forgeries {
		tampered := forgeProviderResult(t, fixture, forgery.journal, forgery.call, forgery.rewrite)
		if _, err := ReplayGovernedTranscript(tampered, fixture.storeJournal, fixture.store, fixture.pins); err == nil {
			t.Errorf("governed replay accepted a forged result (%s)", forgery.journal)
		}
	}
}

// A crash after the ALLOW of a refused call but before its result is journaled leaves the ALLOW pending: replay
// reconstructs no result for it.
func TestNoEffectPendingAllowReplaysWithoutAResult(t *testing.T) {
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "ne-r-pending", CallID: "ne-c-pending", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt","offset":0}`)},
	}, true)
	allowNoEffectReplayTurn(t, fixture, false)
	projection, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
	if err != nil {
		t.Fatalf("replay of a pending refused ALLOW: %v", err)
	}
	if projection.Provider.Disposition != providerbridge.DispositionPendingProviderResult ||
		projection.Provider.DecisionCount != 1 || projection.Provider.ResultCount != 0 || projection.StoreOperationCount != 0 {
		t.Fatalf("pending refused ALLOW was not replayed as pending without a result: %+v", projection)
	}
}
