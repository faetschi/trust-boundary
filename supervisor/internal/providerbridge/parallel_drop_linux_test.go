//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

const pilotCallZeroID = "call-b3789f85-c0f4-48e3-92cc-4128e115e803"

func pilotParallelReadStream(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../broker/openrouter/testdata/openrouter-f35-c02-parallel-read.sse")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestResearchTurnKeepsCallZeroOfTheF35PilotSettlesItsUsageAndSendsOneCallBack(t *testing.T) {
	doer := &queuedSpendDoer{raw: [][]byte{pilotParallelReadStream(t)}}
	spend := &spendRecorder{}
	journal := &memoryJournal{}
	c := newSpendConversation(t, spend, doer)
	c.journal = journal
	admitSpendPrompt(t, c)
	proposal, decision := driveAllowedReadCall(t, c)
	var args struct {
		Path string `json:"path"`
	}
	if proposal.Tool != "read" || proposal.ToolCallID != pilotCallZeroID || json.Unmarshal(proposal.Arguments, &args) != nil ||
		args.Path != "data/get_day_calendar_events.txt" {
		t.Fatalf("proposal = %+v, want call 0: read data/get_day_calendar_events.txt", proposal)
	}
	if got, want := spend.sequence(), []string{"reserve", "settle:unit-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spend order %v, want %v", got, want)
	}
	var usage struct {
		PromptTokens int `json:"prompt_tokens"`
	}
	if json.Unmarshal(spend.usage, &usage) != nil || usage.PromptTokens != 944 {
		t.Fatalf("settled usage = %s, want the reply's own usage", spend.usage)
	}
	exchanges := 0
	for _, event := range journal.snapshot() {
		if event.Kind == "provider_exchange" {
			exchanges++
			if !strings.Contains(string(event.Data), `"dropped_tool_calls":1`) {
				t.Fatalf("exchange evidence does not count the dropped call: %s", event.Data)
			}
		}
	}
	if exchanges != 1 {
		t.Fatalf("provider_exchange records = %d, want 1", exchanges)
	}
	executor, err := NewExecutor(c, &fixtureExecutor{outputs: []json.RawMessage{
		readOutputFor("data/get_day_calendar_events.txt", "generation-1", "sha256:"+strings.Repeat("0", 64), "events")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
		t.Fatalf("execute call 0: %v", err)
	}
	if _, err := c.NextTurn(context.Background()); err != nil {
		t.Fatalf("next turn: %v", err)
	}
	var next struct {
		Messages []struct {
			ToolCalls  []json.RawMessage `json:"tool_calls"`
			ToolCallID string            `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(doer.inner.body, &next); err != nil {
		t.Fatal(err)
	}
	calls, results := 0, 0
	for _, message := range next.Messages {
		calls += len(message.ToolCalls)
		if message.ToolCallID != "" {
			results++
			if message.ToolCallID != pilotCallZeroID {
				t.Fatalf("the next request answers call %q", message.ToolCallID)
			}
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("next request history holds %d tool calls and %d results, want 1 and 1", calls, results)
	}
}

func TestF35PilotRetryRecordStillVerifiesAsTheResampleClass(t *testing.T) {
	if !ParallelToolCallResample(http.StatusOK, pilotParallelReadStream(t), spendTestRoute(t).WithReasoning(true)) {
		t.Fatal("the F35 pilot's refused stream no longer classifies as the decision 13 resample")
	}
}
