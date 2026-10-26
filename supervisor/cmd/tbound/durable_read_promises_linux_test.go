//go:build linux

package main

import (
	"context"
	"encoding/json"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
)

// The tool descriptions tell the model that a read returns the whole of an existing UTF-8 text file at a
// workspace-relative path, and that anything else ends the turn. This holds the executor to that text: each
// such read withholds its result and closes the provider conversation, and a plain read does not.
func TestReadToolDescriptionMatchesWhatTheExecutorDoes(t *testing.T) {
	binary, err := json.Marshal(map[string]string{"command": `printf '\377\376ab' > task.txt`})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		prepare bool
		args    string
		ends    bool
	}{
		"existing text file":  {false, `{"path":"task.txt"}`, false},
		"absolute path":       {false, `{"path":"/etc/hostname"}`, true},
		"missing file":        {false, `{"path":"nope.txt"}`, true},
		"offset":              {false, `{"path":"task.txt","offset":1}`, true},
		"limit":               {false, `{"path":"task.txt","limit":1}`, true},
		"file not valid UTF8": {true, `{"path":"task.txt"}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			turns := []providerbridge.DevelopmentProviderTurn{{ResponseID: "r-read", CallID: "c-read", Tool: "read", Arguments: json.RawMessage(c.args)}}
			if c.prepare {
				turns = append([]providerbridge.DevelopmentProviderTurn{{ResponseID: "r-bash", CallID: "c-bash", Tool: "bash", Arguments: binary}}, turns...)
			}
			fixture := newGovernedReplayFixtureWithOutput(t, turns, true, governedReplayAllowAllPolicy(t), 0)
			if c.prepare {
				executeGovernedReplayTurn(t, fixture, true)
			}
			ctx := context.Background()
			turn, err := fixture.conversation.NextTurn(ctx)
			if err != nil || turn.ToolCall == nil {
				t.Fatalf("no captured read call: %+v %v", turn, err)
			}
			proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID, Tool: "read",
				Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
			matched, err := fixture.conversation.Correlate(ctx, proposal)
			if err != nil {
				t.Fatal(err)
			}
			decision := gate.Evaluate(proposal, matched, fixture.policy)
			if err := fixture.conversation.RecordDecision(ctx, proposal, decision); err != nil {
				t.Fatal(err)
			}
			output, err := fixture.providerExec.Execute(ctx, proposal, decision)
			if !c.ends {
				if err != nil || len(output) == 0 {
					t.Fatalf("a read of an existing text file failed: %v", err)
				}
				return
			}
			if err == nil || len(output) != 0 {
				t.Fatalf("the read returned a result instead of ending the turn: %s", output)
			}
			if _, err := fixture.conversation.NextTurn(ctx); err == nil {
				t.Fatal("the provider conversation is still open after the read failed")
			}
		})
	}
}
