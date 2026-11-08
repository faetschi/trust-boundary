package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
)

// rawDoer answers each request with the next prepared SSE body and keeps the request bodies.
type rawDoer struct {
	bodies   [][]byte
	requests [][]byte
}

func (d *rawDoer) Do(request *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(request.Body)
	d.requests = append(d.requests, body)
	raw := d.bodies[len(d.requests)-1]
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), Request: request}, nil
}

func sseChunks(id, model string, finish any, deltas ...map[string]any) []byte {
	var out []byte
	emit := func(delta map[string]any, fin any) {
		raw, _ := json.Marshal(map[string]any{"id": id, "model": model, "created": 1, "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}}})
		out = append(out, []byte("data: "+string(raw)+"\n\n")...)
	}
	for _, delta := range deltas {
		emit(delta, nil)
	}
	emit(map[string]any{}, finish)
	return append(out, []byte("data: [DONE]\n\n")...)
}

func reasoningConversation(t *testing.T, route openrouter.Route, spend SpendAccountant, doer *rawDoer) (*Conversation, *memoryJournal) {
	t.Helper()
	journal := &memoryJournal{}
	model := route.Resolved().Model
	c, err := newWithDoer(Config{
		ConversationID: "conversation-d50", WorkflowID: "workflow-d50", ProfileID: "profile-d50",
		InitialGenerationID: "g0", InitialTreeDigest: "tree-g0",
		SystemPrompt: "D50 system prompt.", DeveloperPrompt: "D50 developer profile.",
		Route: route, Spend: spend,
	}, model, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AdmitPrompt("task-d50", "Read it."); err != nil {
		t.Fatal(err)
	}
	return c, journal
}

func readCallDeltas(model string) [][]map[string]any {
	call := map[string]any{"index": 0, "id": "call-d50", "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"README.md"}`}}
	return [][]map[string]any{{
		{"role": "assistant", "reasoning_content": "think-1a "}, {"reasoning_content": "think-1b"}, {"tool_calls": []any{call}},
	}}
}

func TestReasoningRoutePassesReasoningBackAndRebuildsItFromTheJournaledSSE(t *testing.T) {
	route := openrouter.EngineeringRoute().WithReasoning(true)
	deltas := readCallDeltas(openrouter.ApprovedModel)[0]
	doer := &rawDoer{bodies: [][]byte{
		sseChunks("resp-1", openrouter.ApprovedModel, "tool_calls", deltas...),
		sseChunks("resp-2", openrouter.ApprovedModel, "stop", map[string]any{"role": "assistant", "content": "done"}),
	}}
	c, journal := reasoningConversation(t, route, nil, doer)
	proposal, decision := driveAllowedReadCall(t, c)
	executor, err := NewExecutor(c, &fixtureExecutor{outputs: []json.RawMessage{readOutput("g0", "tree-g0", "x")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(doer.requests[0], []byte("reasoning_content")) ||
		!bytes.Contains(doer.requests[0], []byte(`"thinking":{"type":"enabled"},"reasoning_effort":"high"`)) {
		t.Fatalf("turn 1 request: %s", doer.requests[0])
	}
	if got := strings.Count(string(doer.requests[1]), `"reasoning_content":"think-1a think-1b"`); got != 1 {
		t.Fatalf("turn 2 request carries turn 1 reasoning %d times: %s", got, doer.requests[1])
	}
	// The history is rebuilt from the journaled raw response alone and must give turn 2's exact bytes.
	var exchange exchangeEvidence
	for _, event := range journal.snapshot() {
		if event.Kind == "provider_exchange" {
			if err := json.Unmarshal(event.Data, &exchange); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	captured, err := openrouter.CaptureSSEForRoute(bytes.NewReader(exchange.Response), route)
	if err != nil {
		t.Fatal(err)
	}
	call, _ := captured.ToolCall()
	history := cloneMessages(c.history[:3]) // system, developer, user
	history = append(history, assistantMessage(route, captured, captured.AssistantText(),
		[]openrouter.PriorToolCall{{ID: call.ID, Name: call.Name, RawArguments: call.RawArguments}}), c.history[4])
	rebuilt, err := openrouter.BuildRequestForRoute(route, history, 0)
	if err != nil || !bytes.Equal(rebuilt.Body(), doer.requests[1]) {
		t.Fatalf("rebuilt request differs (%v):\n%s\n%s", err, rebuilt.Body(), doer.requests[1])
	}
}

func TestDefaultRouteDropsReasoningFromHistoryAndRequests(t *testing.T) {
	deltas := readCallDeltas(openrouter.ApprovedModel)[0]
	doer := &rawDoer{bodies: [][]byte{
		sseChunks("resp-1", openrouter.ApprovedModel, "tool_calls", deltas...),
		sseChunks("resp-2", openrouter.ApprovedModel, "stop", map[string]any{"role": "assistant", "content": "done"}),
	}}
	c, _ := reasoningConversation(t, openrouter.Route{}, nil, doer)
	proposal, decision := driveAllowedReadCall(t, c)
	executor, _ := NewExecutor(c, &fixtureExecutor{outputs: []json.RawMessage{readOutput("g0", "tree-g0", "x")}})
	if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, request := range doer.requests {
		if bytes.Contains(request, []byte("reasoning")) || !bytes.Contains(request, []byte(`"thinking":{"type":"disabled"}`)) {
			t.Fatalf("default route request changed: %s", request)
		}
	}
	for _, message := range c.history {
		if message.Reasoning != "" {
			t.Fatal("default route kept reasoning in history")
		}
	}
}

func memoryJournalKinds(journal *memoryJournal) []string {
	var kinds []string
	for _, event := range journal.snapshot() {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func TestReasoningOnlyStopIsJournaledSettledAndUnknown(t *testing.T) {
	raw := sseChunks("resp-1", spendTestModel, "stop", map[string]any{"role": "assistant", "reasoning": "only thinking"})
	rd := &rawDoer{bodies: [][]byte{raw}}
	spend := &spendRecorder{}
	route := spendTestRoute(t).WithReasoning(true)
	c, journal := reasoningConversation(t, route, spend, rd)
	_, err := c.NextTurn(context.Background())
	if err == nil {
		t.Fatal("a reasoning-only reply was accepted")
	}
	kinds := memoryJournalKinds(journal)
	if kinds[len(kinds)-1] != "provider_exchange_unknown" {
		t.Fatalf("exchange was not journaled as unknown: %v", kinds)
	}
	var unknown struct {
		Response []byte `json:"response"`
	}
	if json.Unmarshal(journal.snapshot()[len(kinds)-1].Data, &unknown) != nil || !bytes.Equal(unknown.Response, raw) {
		t.Fatal("unknown record does not carry the raw response")
	}
	if got, want := spend.sequence(), []string{"reserve", "settle:unit-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spend %v, want %v", got, want)
	}
	if !errorsIsClosed(c) {
		t.Fatal("conversation stayed open")
	}
}

func errorsIsClosed(c *Conversation) bool {
	_, err := c.NextTurn(context.Background())
	return err == ErrClosed || (err != nil && strings.Contains(err.Error(), ErrClosed.Error()))
}

func TestLengthTruncatedReasoningIsJournaledUnknownNotAMalformedToolCall(t *testing.T) {
	raw := sseChunks("resp-1", openrouter.ApprovedModel, "length", map[string]any{"role": "assistant", "reasoning_content": "still thinking"})
	rd := &rawDoer{bodies: [][]byte{raw}}
	c, journal := reasoningConversation(t, openrouter.EngineeringRoute().WithReasoning(true), nil, rd)
	_, err := c.NextTurn(context.Background())
	if err == nil || strings.Contains(err.Error(), "malformed tool call") {
		t.Fatalf("length truncation: %v", err)
	}
	kinds := memoryJournalKinds(journal)
	if kinds[len(kinds)-1] != "provider_exchange_unknown" {
		t.Fatalf("journal kinds %v", kinds)
	}
	var unknown struct {
		Failure  string `json:"failure"`
		Response []byte `json:"response"`
	}
	if json.Unmarshal(journal.snapshot()[len(kinds)-1].Data, &unknown) != nil || !bytes.Equal(unknown.Response, raw) ||
		!strings.Contains(unknown.Failure, "finish reason") {
		t.Fatalf("unknown evidence: %+v", unknown)
	}
}

func TestOversizeReasoningStreamIsJournaledUnknown(t *testing.T) {
	big := strings.Repeat("x", openrouter.MaxSSEEventBytes/2)
	rd := &rawDoer{bodies: [][]byte{sseChunks("resp-1", openrouter.ApprovedModel, "stop",
		map[string]any{"reasoning_content": big}, map[string]any{"reasoning_content": big}, map[string]any{"reasoning_content": big},
		map[string]any{"content": "x"})}}
	c, journal := reasoningConversation(t, openrouter.EngineeringRoute().WithReasoning(true), nil, rd)
	if _, err := c.NextTurn(context.Background()); err == nil {
		t.Fatal("oversize reasoning accepted")
	}
	if kinds := memoryJournalKinds(journal); kinds[len(kinds)-1] != "provider_exchange_unknown" {
		t.Fatalf("journal kinds %v", kinds)
	}
}
