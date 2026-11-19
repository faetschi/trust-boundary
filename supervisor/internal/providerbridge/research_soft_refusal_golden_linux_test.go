//go:build linux

package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

type researchGoldenDoer struct{}

func (researchGoldenDoer) Do(request *http.Request) (*http.Response, error) {
	raw := toolResponse(fixtureResponse{responseID: "research-response", callID: "research-call", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)})
	raw = bytes.ReplaceAll(raw, []byte(ApprovedModel), []byte(spendTestModel))
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), Request: request}, nil
}

func TestResearchSoftRefusalJournalAndResultGolden(t *testing.T) {
	cfg := recoveryTestConfig()
	cfg.Route = spendTestRoute(t)
	journal := &memoryJournal{}
	c, err := newWithDoer(cfg, spendTestModel, researchGoldenDoer{}, journal)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.AdmitPrompt("research-golden", "Read README.md."); err != nil {
		t.Fatal(err)
	}
	turn, err := c.NextTurn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	matched, err := c.Correlate(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Evaluate(proposal, matched, recoveryTestPolicy(t, true))
	if err := c.RecordDecision(ctx, proposal, decision); err != nil {
		t.Fatal(err)
	}
	output := readOutputFor("README.md", cfg.InitialGenerationID, cfg.InitialTreeDigest, "research bytes")
	executor, err := NewExecutor(c, &fixtureExecutor{outputs: []json.RawMessage{output}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(ctx, proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, output) {
		t.Fatalf("research result changed: %s", result)
	}
	events, err := json.Marshal(journal.events)
	if err != nil {
		t.Fatal(err)
	}
	// Confirmed against providerbridge at pre-feature commit 7ceeff6 using this research fixture; re-pinned
	// 2026-11-19 when the research request stopped sending parallel_tool_calls (no other change).
	const want = "sha256:a85cf5767bc59dba9362fac24ca3f429a9da54780908468b300910e36a8a7685"
	if got := digestBytes(events); got != want {
		t.Fatalf("research journal golden: %s", got)
	}
	for _, event := range journal.events {
		if strings.Contains(event.Kind, "unexecuted") {
			t.Fatal("research emitted product evidence")
		}
	}
	if err := c.RecordSoftRefusal(ctx, protocol.Proposal{}, "", "", "", nil); err == nil {
		t.Fatal("research accepted a soft refusal")
	}
}
