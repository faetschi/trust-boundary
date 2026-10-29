//go:build linux

package main

import (
	"context"
	"encoding/json"
	"testing"

	"tbound/supervisor/internal/providerbridge"
)

// unleasedCallRetirementFixture stands in for the host's proof that a captured call holds no Store authority. That
// proof is not under test here; the replay passes around it are.
type unleasedCallRetirementFixture struct{}

func (unleasedCallRetirementFixture) VerifyUnleasedProviderCallRetirement(providerbridge.ProviderCallRetirementBinding) error {
	return nil
}

// TestReplayRetiresAnUnleasedCallAfterAPromotedWrite runs the research recovery's sequence: a promoted write, then a
// captured read that never reached a gate decision. The retirement replays the history again with the same verifier,
// so that pass must restart from the pinned tip rather than the tip the write promoted.
func TestReplayRetiresAnUnleasedCallAfterAPromotedWrite(t *testing.T) {
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "retire-response-write", CallID: "retire-call-write", Tool: "write",
			Arguments: json.RawMessage(`{"path":"task.txt","content":"promoted\n"}`)},
		{ResponseID: "retire-response-read", CallID: "retire-call-read", Tool: "read",
			Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	}, true)
	executeGovernedReplayTurn(t, fixture, true)
	if turn, err := fixture.conversation.NextTurn(context.Background()); err != nil || turn.ToolCall == nil {
		t.Fatalf("expected the captured read, got %+v, err=%v", turn, err)
	}
	pins := fixture.pins
	pins.Provider.PreviousOwnerEpoch, pins.Provider.RetirementOwnerEpoch = 1, 2
	pins.Provider.RetirementVerifier = unleasedCallRetirementFixture{}
	_, verifier, err := replayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, pins, 0, true)
	if err != nil {
		t.Fatalf("replay the promoted write and the captured read: %v", err)
	}
	retired, err := providerbridge.AppendUnleasedProviderCallRetirement(fixture.providerJournal, pins.Provider, verifier)
	if err != nil {
		t.Fatalf("retire the captured read after the promoted write: %v", err)
	}
	if retired.Disposition != providerbridge.DispositionRetiredUnexecuted || retired.RetiredCall == nil ||
		retired.RetiredCall.ToolCallID != "retire-call-read" || retired.ProviderGenerationID != "g1" {
		t.Fatalf("retirement projection = %+v; want the captured read retired on the promoted tip g1", retired)
	}
	strict, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, pins)
	if err != nil {
		t.Fatalf("strict replay of the retired history: %v", err)
	}
	if strict.Provider.Disposition != providerbridge.DispositionRetiredUnexecuted || strict.StoreOperationCount != 1 || strict.StoreHeadGeneration != "g1" {
		t.Fatalf("strict replay = %+v; want the retired read over one promoted Store operation", strict)
	}
}
