//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
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
// reserved host call as unknown, the shape abandon reconciles, and the failure is typed for Serve. A tool-call ID
// that an earlier turn of the conversation already captured used to be journaled as a normal exchange and then
// refused by the correlator, with the host call reserved and no disposition, so abandon refused (the F26 wedge).
func TestRefusedCompleteResponseClosesItsHostCallAsUnknown(t *testing.T) {
	read := func(responseID string) fixtureResponse {
		return fixtureResponse{responseID: responseID, callID: "call-reused", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)}
	}
	for name, tc := range map[string]struct {
		responses []fixtureResponse
		toolTurns int
	}{
		"unaccepted response model, refused by the broker": {responses: []fixtureResponse{{failStatus: http.StatusOK, failBody: unacceptedModelStream(t)}}},
		"tool-call ID of an earlier turn":                  {responses: []fixtureResponse{read("response-1"), read("response-2")}, toolTurns: 1},
	} {
		t.Run(name, func(t *testing.T) {
			conversation, journal, _, hooks := hostCallsLeftConversation(t, tc.responses, 0, 11)
			executor, err := NewExecutor(conversation, &fixtureExecutor{outputs: []json.RawMessage{readOutput("g0", recoveryTestTree("tree-0"), "content")}})
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

// researchSpendConversation is a research-route conversation with host-call accounting and a real journal.
func researchSpendConversation(t *testing.T, spend SpendAccountant, doer *queuedSpendDoer) (*Conversation, *ResultingExecutor, *retryHooks, *audit.Journal) {
	t.Helper()
	cfg := Config{
		ConversationID: "conversation-spend", WorkflowID: "workflow-spend", ProfileID: "profile-spend",
		InitialGenerationID: "generation-1", InitialTreeDigest: recoveryTestTree("tree-spend"),
		SystemPrompt: "Spend system prompt.", DeveloperPrompt: "Spend developer profile.",
		Route: spendTestRoute(t), Spend: spend,
	}
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "spend.audit"))
	conversation, err := newWithDoer(cfg, spendTestModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	admitSpendPrompt(t, conversation)
	executor, err := NewExecutor(conversation, &fixtureExecutor{outputs: []json.RawMessage{
		readOutputFor("x", "generation-1", recoveryTestTree("tree-spend"), "content")}})
	if err != nil {
		t.Fatal(err)
	}
	return conversation, executor, hooks, journal
}

// spendToolStream is the research completion of spendDoer with one read call of callID, under responseID.
func spendToolStream(t *testing.T, responseID, callID string) []byte {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, spendTestEndpoint, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&spendDoer{callID: callID}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.ReplaceAll(string(body), `"id":"resp-1"`, `"id":"`+responseID+`"`))
}

// On the research route the correlator's refusal leaves the turn's USD unit for the deferred uncertain mark:
// the first turn's unit is settled to its usage, the refused turn's unit is booked uncertain, never settled.
func TestResearchCorrelatorRefusalBooksTheUnitUncertain(t *testing.T) {
	spend := &spendRecorder{}
	doer := &queuedSpendDoer{raw: [][]byte{spendToolStream(t, "resp-first", "call-reused")}, inner: spendDoer{callID: "call-reused"}}
	conversation, executor, hooks, journal := researchSpendConversation(t, spend, doer)
	driveToolTurn(t, conversation, executor, recoveryTestPolicy(t, true))
	_, err := conversation.NextTurn(context.Background())
	requireUnknownEnd(t, conversation, journal, hooks, err)
	if got := strings.Join(spend.sequence(), ","); got != "reserve,settle:unit-1,reserve,uncertain:unit-1" {
		t.Fatalf("spend = %s, want the refused turn's unit booked uncertain", got)
	}
}

// A research exchange whose spend settlement fails after it is journaled (usage missing, an overage) settles its
// host call first, so nothing reserved is left for abandon, and the turn ends as a typed failure, not released.
func TestResearchSpendSettlementFailureSettlesTheHostCall(t *testing.T) {
	for name, doer := range map[string]*queuedSpendDoer{
		"text reply": {inner: spendDoer{}},
		"tool call":  {inner: spendDoer{callID: "call-spend"}},
	} {
		t.Run(name, func(t *testing.T) {
			spend := &spendRecorder{settleErr: errors.New("research usage missing")}
			conversation, _, hooks, journal := researchSpendConversation(t, spend, doer)
			_, err := conversation.NextTurn(context.Background())
			var journaled journaledFailure
			if err == nil || !errors.As(err, &journaled) || !strings.Contains(err.Error(), "research usage missing") {
				t.Fatalf("turn error = %v, want a typed failure carrying the settlement error", err)
			}
			last := lastRecord(t, journal)
			if last.Event.Kind != "provider_exchange" || len(hooks.reservations) != 1 || len(hooks.completions) != 1 ||
				hooks.completions[0].Binding != hooks.reservations[0] || hooks.completions[0].OutcomeDigest != last.Hash {
				t.Fatalf("last record %s, reservations %d, completions %+v: want the journaled exchange's host call settled",
					last.Event.Kind, len(hooks.reservations), hooks.completions)
			}
			if got := strings.Join(spend.sequence(), ","); got != "reserve,settle:unit-1" {
				t.Fatalf("spend = %s, want one settlement and no second mark", got)
			}
			if !conversation.Closed() {
				t.Fatal("conversation stayed open")
			}
		})
	}
}
