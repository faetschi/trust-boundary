//go:build linux

package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/gate"
)

type reasoningRun struct {
	cfg     Config
	policy  gate.Policy
	doer    *rawDoer
	journal *audit.Journal
}

func (r reasoningRun) replay(route openrouter.Route) error {
	cfg := r.cfg
	cfg.Route = route
	_, err := ReplayTranscript(r.journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: r.policy}, &replayVerifierFixture{})
	return err
}

// runReasoningConversation journals a two-turn tool conversation: turn 1 streams reasoning and a read call, turn 2
// (after the result) answers in text.
func runReasoningConversation(t *testing.T, route openrouter.Route) reasoningRun {
	t.Helper()
	cfg := recoveryTestConfig()
	cfg.Route = route
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "reasoning-provider.audit"))
	doer := &rawDoer{bodies: [][]byte{
		sseChunks("resp-1", openrouter.ApprovedModel, "tool_calls", readCallDeltas(openrouter.ApprovedModel)[0]...),
		sseChunks("resp-2", openrouter.ApprovedModel, "stop", map[string]any{"role": "assistant", "content": "done"}),
	}}
	conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-d50", "Read it."); err != nil {
		t.Fatal(err)
	}
	policy := recoveryTestPolicy(t, true)
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("turn 1 = %+v, %v", turn, err)
	}
	proposal := proposalFor(turn)
	matched, err := conversation.Correlate(context.Background(), proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate: %+v %v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, policy)
	if err := conversation.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(conversation, &fixtureExecutor{outputs: []json.RawMessage{
		readOutputFor("README.md", "g0", recoveryTestTree("tree-0"), "contents"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	return reasoningRun{cfg: cfg, policy: policy, doer: doer, journal: journal}
}

func TestReplayRebuildsReasoningPassbackByteForByte(t *testing.T) {
	route := openrouter.EngineeringRoute().WithReasoning(true)
	run := runReasoningConversation(t, route)
	if len(run.doer.requests) != 2 || !bytes.Contains(run.doer.requests[1], []byte(`"reasoning_content":"think-1a think-1b"`)) {
		t.Fatalf("turn 2 request lacks turn 1 reasoning: %s", run.doer.requests[1])
	}
	if err := run.replay(route); err != nil {
		t.Fatalf("replay of a reasoning journal: %v", err)
	}
	// The mark is a pin: the same journal does not replay under the default route's bytes.
	if err := run.replay(openrouter.Route{}); err == nil {
		t.Fatal("a reasoning journal replayed under the unmarked route")
	}
}

func TestReplayOfAnUnmarkedJournalKeepsTheDefaultBytes(t *testing.T) {
	run := runReasoningConversation(t, openrouter.Route{})
	if bytes.Contains(run.doer.requests[1], []byte("reasoning")) || !bytes.Contains(run.doer.requests[1], []byte(`"thinking":{"type":"disabled"}`)) {
		t.Fatalf("default request: %s", run.doer.requests[1])
	}
	if err := run.replay(openrouter.Route{}); err != nil {
		t.Fatalf("replay of a default journal: %v", err)
	}
	if err := run.replay(openrouter.EngineeringRoute().WithReasoning(true)); err == nil {
		t.Fatal("a default journal replayed under the reasoning route")
	}
}
