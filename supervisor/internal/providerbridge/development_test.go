//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
)

// TestDevelopmentOfflineConversationCapturesScriptedToolTurn proves the
// exported development seam drives the real Conversation capture path with a
// scripted offline turn and no network or credentials.
func TestDevelopmentOfflineConversationCapturesScriptedToolTurn(t *testing.T) {
	privateRoot := os.Getenv("TMPDIR")
	directory, err := os.MkdirTemp(privateRoot, "providerbridge-dev-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	journal, err := audit.Open(filepath.Join(directory, "dev-provider.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	conversation, err := NewDevelopmentOfflineConversation(Config{
		ConversationID: "dev-conversation", WorkflowID: "dev-workflow", ProfileID: "dev-profile",
		InitialGenerationID: "g0", InitialTreeDigest: "tree-g0",
		SystemPrompt: "dev system prompt", DeveloperPrompt: "dev developer prompt",
	}, journal, []DevelopmentProviderTurn{
		{ResponseID: "dev-response-1", CallID: "dev-call-1", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("dev-task", "dev prompt"); err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.NextTurn(context.Background())
	if err != nil {
		t.Fatalf("scripted offline provider turn: %v", err)
	}
	if turn.ToolCall == nil || turn.ToolCall.ID != "dev-call-1" || turn.ToolCall.Name != "read" {
		t.Fatalf("scripted offline tool call projection = %+v", turn.ToolCall)
	}
	decision, err := conversation.Correlate(context.Background(), protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
		Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...),
	})
	if err != nil || !decision.Accepted {
		t.Fatalf("scripted offline capture did not correlate: decision=%+v err=%v", decision, err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	kinds := make(map[string]int)
	for _, record := range trace.Records {
		kinds[record.Event.Kind]++
	}
	if kinds["provider_user_admitted"] != 1 || kinds["provider_exchange"] != 1 {
		t.Fatalf("scripted offline audit kinds = %v", kinds)
	}
}
