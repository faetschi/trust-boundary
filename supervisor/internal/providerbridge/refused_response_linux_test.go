//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// unacceptedModelStream is the real G1 first-turn stream answered by a model the route does not accept.
func unacceptedModelStream(t *testing.T) string {
	t.Helper()
	stream := singleCallStream(t)
	renamed := strings.ReplaceAll(stream, `"model":"deepseek-v4.1-flash"`, `"model":"unregistered-model"`)
	if renamed == stream {
		t.Fatal("the G1 fixture no longer names its model")
	}
	return renamed
}

// SB-05: a complete provider response that the bridge refuses before it journals a normal exchange closes its
// reserved host call as unknown, the shape abandon reconciles, and the failure is typed for Serve. A response
// model the route does not accept, and a tool-call ID that an earlier turn of the conversation already captured,
// used to close the conversation with the host call reserved and no disposition, so abandon refused (the F26
// wedge).
func TestRefusedCompleteResponseClosesItsHostCallAsUnknown(t *testing.T) {
	read := func(responseID string) fixtureResponse {
		return fixtureResponse{responseID: responseID, callID: "call-reused", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)}
	}
	for name, tc := range map[string]struct {
		responses []fixtureResponse
		toolTurns int
	}{
		"unaccepted response model":       {responses: []fixtureResponse{{failStatus: http.StatusOK, failBody: unacceptedModelStream(t)}}},
		"tool-call ID of an earlier turn": {responses: []fixtureResponse{read("response-1"), read("response-2")}, toolTurns: 1},
	} {
		t.Run(name, func(t *testing.T) {
			conversation, journal, _, hooks := hostCallsLeftConversation(t, tc.responses, 0, 11)
			executor, err := NewExecutor(conversation, &fixtureExecutor{outputs: []json.RawMessage{readOutput("g0", "tree-0", "content")}})
			if err != nil {
				t.Fatal(err)
			}
			for range tc.toolTurns {
				driveToolTurn(t, conversation, executor, recoveryTestPolicy(t, true))
			}
			_, err = conversation.NextTurn(context.Background())
			requireUnknownEnd(t, conversation, journal, hooks, err)
		})
	}
}
