//go:build linux

package main

import (
	"encoding/json"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
)

const readWindowFileContent = "l1\nl2\nl3\n"

func readWindowReplayFixture(t *testing.T, readArguments string) *governedReplayFixture {
	t.Helper()
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "window-response-write", CallID: "window-call-write", Tool: "write",
			Arguments: json.RawMessage(`{"path":"task.txt","content":"l1\nl2\nl3\n"}`)},
		{ResponseID: "window-response-read", CallID: "window-call-read", Tool: "read", Arguments: json.RawMessage(readArguments)},
	}, true)
	executeGovernedReplayTurn(t, fixture, true)
	executeGovernedReplayTurn(t, fixture, true)
	return fixture
}

// A windowed read releases only the requested lines, and replay reconstructs those exact bytes from the
// Store's whole-file result and the recorded request arguments.
func TestReplayGovernedTranscriptReplaysAWindowedRead(t *testing.T) {
	for _, testCase := range []struct{ name, arguments, want string }{
		{"offset and limit", `{"path":"task.txt","offset":2,"limit":1}`, "l2"},
		{"offset only", `{"path":"task.txt","offset":3}`, "l3\n"},
		{"offset beyond end of file", `{"path":"task.txt","offset":40}`, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := readWindowReplayFixture(t, testCase.arguments)
			projection, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
			if err != nil {
				t.Fatalf("replay of a windowed read: %v", err)
			}
			if projection.VerificationScope != "store_crosslinked" || projection.StoreOperationCount != 2 ||
				projection.Provider.ResultCount != 2 || !VerifyGovernedTranscriptProjectionCommitment(projection) {
				t.Fatalf("windowed read did not replay as a Store-crosslinked transcript: %+v", projection)
			}
			released := finalProviderReadSummary(t, fixture)
			if released.Content != testCase.want || released.Size != len(testCase.want) ||
				released.ContentDigest != durableContentDigest([]byte(testCase.want)) {
				t.Fatalf("provider result released content=%q size=%d; want %q", released.Content, released.Size, testCase.want)
			}
		})
	}
}

// Releasing the whole file for a windowed request is self-consistent provider evidence (its size and digest
// match its content) but is not the bytes the request selects, so the Store cross-link refuses it. The
// opposite forgery, a window the request did not ask for, is refused the same way.
func TestReplayGovernedTranscriptRejectsAReadReleasedOutsideItsWindow(t *testing.T) {
	for _, testCase := range []struct{ name, forged string }{
		{"full file for a windowed request", readWindowFileContent},
		{"a different window", "l3\n"},
		{"empty for a non-empty window", ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := readWindowReplayFixture(t, `{"path":"task.txt","offset":2,"limit":1}`)
			if _, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins); err != nil {
				t.Fatalf("honest windowed transcript did not replay: %v", err)
			}
			tampered := forgeFinalProviderReadOutput(t, fixture, "window-tamper.audit", func(summary *durableReadSummaryPayload) {
				summary.Content = testCase.forged
			})
			if _, err := providerbridge.ReplayTranscript(tampered, fixture.pins.Provider, acceptingProviderReplayVerifier{}); err != nil {
				t.Fatalf("forged output is not internally self-consistent provider evidence: %v", err)
			}
			if _, err := ReplayGovernedTranscript(tampered, fixture.storeJournal, fixture.store, fixture.pins); err == nil {
				t.Fatalf("governed replay accepted %q released for a windowed request", testCase.forged)
			}
		})
	}
}

func finalProviderReadSummary(t *testing.T, fixture *governedReplayFixture) durableReadSummaryPayload {
	t.Helper()
	trace, err := fixture.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var summary durableReadSummaryPayload
	found := false
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_tool_result" {
			continue
		}
		var evidence providerResultEvidence
		if err := unmarshalGovernedJSON(record.Event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.ToolCallID != "window-call-read" {
			continue
		}
		result, err := protocol.DecodeResult(evidence.Result)
		if err != nil {
			t.Fatal(err)
		}
		if err := decodeGovernedResult(result.Output, &summary); err != nil {
			t.Fatal(err)
		}
		found = true
	}
	if !found {
		t.Fatal("no provider result for the windowed read")
	}
	return summary
}

// A model that fills the advertised optional offset/limit keeps its trial: the read releases the window,
// a read past the end of the file is an empty success rather than an unknown execution, and the
// conversation reaches its final answer.
func TestWorkflowVerticalWindowedReadsCompleteTheConversation(t *testing.T) {
	f := newWorkflowVerticalFixture(t, false, 0, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "window-r1", CallID: "window-c1", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt","offset":1,"limit":1}`)},
		{ResponseID: "window-r2", CallID: "window-c2", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt","offset":50,"limit":10}`)},
		{ResponseID: "window-r3", Text: "done"},
	})
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct{ callID, content string }{{"window-c1", "vertical fixture"}, {"window-c2", ""}} {
		proposal, decision := ledgerDenyTurn(t, f, expected.callID, gate.Allow)
		output, err := executor.Execute(f.ctx, proposal, decision)
		if err != nil {
			t.Fatalf("windowed read %s was not released: %v", expected.callID, err)
		}
		var summary durableReadSummaryPayload
		if err := json.Unmarshal(output, &summary); err != nil {
			t.Fatalf("decode %s output %q: %v", expected.callID, output, err)
		}
		if summary.Content != expected.content || summary.Size != len(expected.content) ||
			summary.ContentDigest != durableContentDigest([]byte(expected.content)) || summary.Outcome != "success" {
			t.Fatalf("windowed read %s released %+v; want content %q", expected.callID, summary, expected.content)
		}
	}
	requireFinalAnswer(t, f, "done", 5, 3072)
	f.conversation.Close()
	if _, err := f.store.Verify(); err != nil {
		t.Fatalf("verify Store after windowed reads: %v", err)
	}
}
