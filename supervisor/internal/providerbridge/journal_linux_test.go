//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
)

func TestRealAuditJournalOrdersPromptExchangeDecisionAndToolResult(t *testing.T) {
	dir, err := os.MkdirTemp("", ".tbound-providerbridge-audit-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	journal, err := audit.Open(filepath.Join(dir, "provider.jsonl"))
	if err != nil {
		t.Skipf("secure owner-only audit journal unavailable in this environment: %v", err)
	}
	defer journal.Close()

	doer := &fixtureDoer{responses: []fixtureResponse{{
		responseID: "response-real-journal", callID: "call-real-journal", tool: "read",
		arguments: json.RawMessage(`{"path":"README.md"}`),
	}}}
	c := newFixtureConversation(t, journal, doer, "g0", "tree-g0")
	if err := c.AdmitPrompt("task-real-journal", "synthetic audit-order test"); err != nil {
		t.Fatal(err)
	}
	turn, err := c.NextTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	matched, err := c.Correlate(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "read", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Evaluate(proposal, matched, policy)
	if err := c.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	output := readOutput("g0", "tree-g0", "synthetic content\n")
	executor, err := NewExecutor(c, &fixtureExecutor{outputs: []json.RawMessage{output}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"provider_user_admitted", "provider_exchange", "provider_gate_decision", "provider_tool_result"}
	if len(trace.Records) != len(want) {
		t.Fatalf("journal record count=%d want=%d", len(trace.Records), len(want))
	}
	for i, kind := range want {
		if trace.Records[i].Event.Kind != kind || trace.Records[i].Sequence != uint64(i+1) || trace.Records[i].Hash == "" {
			t.Fatalf("record[%d]=%+v want kind=%s", i, trace.Records[i], kind)
		}
	}
}
