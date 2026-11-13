package openrouter

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const researchReasoningMembers = `{"provider":{"order":["example-provider"]},"reasoning":{"effort":"high"}}`

func reasoningResearchRoute(t *testing.T) Route {
	t.Helper()
	route, err := NewResearchRoute(ResearchSpec{Model: researchTestModel, Endpoint: researchTestEndpoint,
		ProfileDigest: researchTestDigest, Members: researchReasoningMembers, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return route
}

func reasoningHistory() []Message {
	return []Message{
		{Role: User, Content: strptr("go")},
		{Role: Assistant, Content: strptr(""), Reasoning: "I should read it.",
			ToolCalls: []PriorToolCall{{ID: "call-1", Name: "read", RawArguments: []byte(`{"path":"a.txt"}`)}}},
		{Role: Tool, ToolCallID: "call-1", Content: strptr("contents")},
	}
}

func TestDefaultEngineeringRequestKeepsThinkingDisabledAndDropsReasoning(t *testing.T) {
	built, err := BuildRequestForRoute(EngineeringRoute(), reasoningHistory(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	body := string(built.Body())
	if !strings.Contains(body, `"thinking":{"type":"disabled"},"max_completion_tokens":1024}`) ||
		strings.Contains(body, "reasoning") {
		t.Fatalf("default route changed: %s", body)
	}
}

func TestReasoningEngineeringRouteEnablesThinkingAndPassesReasoningContentBack(t *testing.T) {
	route := EngineeringRoute().WithReasoning(true)
	if err := route.Validate(); err != nil {
		t.Fatal(err)
	}
	built, err := BuildRequestForRoute(route, reasoningHistory(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	body := string(built.Body())
	if !strings.Contains(body, `"thinking":{"type":"enabled"},"reasoning_effort":"high","max_completion_tokens":4096}`) {
		t.Fatalf("thinking not enabled with pinned effort: %s", body)
	}
	if !strings.Contains(body, `"tool_calls":[{"id":"call-1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}}],"reasoning_content":"I should read it."}`) {
		t.Fatalf("reasoning_content not carried on the assistant turn: %s", body)
	}
	if strings.Count(body, "reasoning_content") != 1 {
		t.Fatalf("reasoning leaked onto other messages: %s", body)
	}
}

func TestReasoningResearchRoutePassesReasoningBackAsReasoning(t *testing.T) {
	route := reasoningResearchRoute(t).WithReasoning(true)
	if err := route.Validate(); err != nil {
		t.Fatal(err)
	}
	built, err := BuildRequestForRoute(route, reasoningHistory(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	body := string(built.Body())
	if !strings.Contains(body, `}}],"reasoning":"I should read it."}`) || strings.Contains(body, "reasoning_content") ||
		strings.Contains(body, `"thinking"`) || !strings.HasSuffix(body, `,"reasoning":{"effort":"high"}}`) {
		t.Fatalf("research reasoning wire form: %s", body)
	}
	plain, err := BuildRequestForRoute(reasoningResearchRoute(t), reasoningHistory(), 4096)
	if err != nil || strings.Contains(string(plain.Body()), `"reasoning":"`) {
		t.Fatalf("unmarked research route must not send reasoning back: %v", err)
	}
}

func TestReasoningOnlyOnAssistantMessages(t *testing.T) {
	route := EngineeringRoute().WithReasoning(true)
	_, err := BuildRequestForRoute(route, []Message{{Role: User, Content: strptr("x"), Reasoning: "r"}}, 0)
	if err == nil {
		t.Fatal("reasoning on a user message was accepted")
	}
}

func reasoningStream(deltas int, finish string, content string) []byte {
	var body []byte
	for i := 0; i < deltas; i++ {
		body = append(body, chunk("resp-r", map[string]any{"reasoning_content": "t"}, nil)...)
	}
	if content != "" {
		body = append(body, chunk("resp-r", map[string]any{"content": content}, nil)...)
	}
	body = append(body, chunk("resp-r", map[string]any{}, finish)...)
	return append(body, doneEvent()...)
}

func TestCaptureSSEForRouteAccumulatesReasoningOnlyWhenTheRouteAsksForIt(t *testing.T) {
	body := chunk("resp-r", map[string]any{"role": "assistant", "reasoning_content": "ab"}, nil)
	body = append(body, chunk("resp-r", map[string]any{"reasoning": "cd", "content": "ok"}, nil)...)
	body = append(body, chunk("resp-r", map[string]any{}, "stop")...)
	body = append(body, doneEvent()...)
	got, err := CaptureSSEForRoute(bytes.NewReader(body), EngineeringRoute().WithReasoning(true))
	if err != nil || got.Reasoning() != "abcd" || got.AssistantText() != "ok" {
		t.Fatalf("reasoning = %q text=%q err=%v", got.Reasoning(), got.AssistantText(), err)
	}
	plain, err := CaptureSSEForRoute(bytes.NewReader(body), EngineeringRoute())
	if err != nil || plain.Reasoning() != "" {
		t.Fatalf("unmarked route kept reasoning %q (%v)", plain.Reasoning(), err)
	}
}

func TestReasoningStreamPastTheOldEventLimitParsesOnlyOnAReasoningRoute(t *testing.T) {
	body := reasoningStream(MaxSSEEvents+100, "stop", "done")
	got, err := CaptureSSEForRoute(bytes.NewReader(body), EngineeringRoute().WithReasoning(true))
	if err != nil || len(got.Reasoning()) != MaxSSEEvents+100 {
		t.Fatalf("reasoning stream: %v", err)
	}
	if _, err := CaptureSSE(bytes.NewReader(body)); err == nil {
		t.Fatal("the default parser event limit grew")
	}
	if _, err := CaptureSSEForRoute(bytes.NewReader(reasoningStream(MaxReasoningSSEEvents, "stop", "")),
		EngineeringRoute().WithReasoning(true)); err == nil {
		t.Fatal("reasoning route accepted more events than its bound")
	}
}

func TestOversizeReasoningFailsClosed(t *testing.T) {
	big := strings.Repeat("x", MaxSSEEventBytes/2)
	var body []byte
	for i := 0; i < 3; i++ {
		body = append(body, chunk("resp-r", map[string]any{"reasoning_content": big}, nil)...)
	}
	body = append(body, chunk("resp-r", map[string]any{"content": "x"}, "stop")...)
	body = append(body, doneEvent()...)
	if _, err := CaptureSSEForRoute(bytes.NewReader(body), EngineeringRoute().WithReasoning(true)); err == nil {
		t.Fatal("oversize reasoning was accepted or truncated")
	}
}

func TestLengthFinishWithReasoningIsACaptureError(t *testing.T) {
	_, err := CaptureSSEForRoute(bytes.NewReader(reasoningStream(5, "length", "")), EngineeringRoute().WithReasoning(true))
	if err == nil || !strings.Contains(err.Error(), "finish reason") {
		t.Fatalf("length finish: %v", err)
	}
}

func TestMessageCountBoundDoesNotGrowWithTheEventLimit(t *testing.T) {
	messages := make([]Message, 4097)
	for i := range messages {
		messages[i] = Message{Role: User, Content: strptr("x")}
	}
	if _, err := BuildRequestForRoute(EngineeringRoute().WithReasoning(true), messages, 0); err == nil {
		t.Fatal("message bound grew with the reasoning event limit")
	}
}

func TestReasoningTakesTheFirstNonEmptyOfBothFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delta map[string]any
		want  string
	}{
		{"empty reasoning_content beside reasoning", map[string]any{"reasoning_content": "", "reasoning": "R"}, "R"},
		{"null reasoning_content beside reasoning", map[string]any{"reasoning_content": nil, "reasoning": "R"}, "R"},
		{"both set", map[string]any{"reasoning_content": "C", "reasoning": "R"}, "C"},
		{"empty reasoning beside reasoning_content", map[string]any{"reasoning_content": "C", "reasoning": ""}, "C"},
		{"both empty", map[string]any{"reasoning_content": "", "reasoning": ""}, ""},
	} {
		body := chunk("resp-p", tc.delta, nil)
		body = append(body, chunk("resp-p", map[string]any{"content": "x"}, "stop")...)
		body = append(body, doneEvent()...)
		got, err := CaptureSSEForRoute(bytes.NewReader(body), EngineeringRoute().WithReasoning(true))
		if err != nil || got.Reasoning() != tc.want {
			t.Errorf("%s: reasoning %q (%v), want %q", tc.name, got.Reasoning(), err, tc.want)
		}
	}
}
