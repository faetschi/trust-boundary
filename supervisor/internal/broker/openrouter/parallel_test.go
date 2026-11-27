package openrouter

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const (
	pilotCallZeroStart = `{"index":0,"id":"call-b3789f85-c0f4-48e3-92cc-4128e115e803","type":"function","function":{"name":"read","arguments":""}}`
	pilotCallZero      = `{"index":0,"id":"call-b3789f85-c0f4-48e3-92cc-4128e115e803","type":"function","function":{"name":"read","arguments":"{\"path\": \"data/get_day_calendar_events.txt\"}"}}`
	pilotCallOne       = `{"index":1,"id":"call-b1feebc9-c81c-485f-90bf-0e0a8e864250","type":"function","function":{"name":"read","arguments":"{\"path\": \"data/search_calendar_events.txt\"}"}}`
)

func pilotParallelReadEvents(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/openrouter-f35-c02-parallel-read.sse")
	if err != nil {
		t.Fatal(err)
	}
	events := strings.SplitAfter(string(raw), "\n\n")
	if len(events) != 9 || !strings.Contains(events[1], pilotCallZeroStart) || !strings.Contains(events[3], `"index":1`) {
		t.Fatalf("the F35 pilot stream changed: %d events", len(events))
	}
	return events
}

func requirePilotCallZeroKept(t *testing.T, raw string) {
	t.Helper()
	captured, err := CaptureSSEForRoute(strings.NewReader(raw), researchTestRoute(t).WithReasoning(true))
	if err != nil {
		t.Fatalf("the research capture refused a reply with two tool calls: %v", err)
	}
	call, ok := captured.ToolCall()
	var args struct {
		Path string `json:"path"`
	}
	if !ok || call.ID != "call-b3789f85-c0f4-48e3-92cc-4128e115e803" || call.Name != "read" ||
		json.Unmarshal(call.RawArguments, &args) != nil || args.Path != "data/get_day_calendar_events.txt" {
		t.Fatalf("captured call = %+v (%v), want call 0: read data/get_day_calendar_events.txt", call, ok)
	}
	if captured.FinishReason() != "tool_calls" || len(captured.Usage()) == 0 || captured.AssistantText() != "I'll start by reading both data files.\n\n" {
		t.Fatalf("finish = %q, usage = %s, text = %q", captured.FinishReason(), captured.Usage(), captured.AssistantText())
	}
}

func TestResearchCaptureKeepsTheFirstOfTheF35PilotsTwoReadCalls(t *testing.T) {
	requirePilotCallZeroKept(t, strings.Join(pilotParallelReadEvents(t), ""))
}

func TestResearchCaptureKeepsTheFirstCallWhenBothArriveInOneDelta(t *testing.T) {
	e := pilotParallelReadEvents(t)
	packed := strings.Replace(e[1], pilotCallZeroStart, pilotCallZero+","+pilotCallOne, 1)
	requirePilotCallZeroKept(t, e[0]+packed+strings.Join(e[5:], ""))
}

func TestResearchCaptureKeepsCallZeroBesideAMalformedCallOne(t *testing.T) {
	e := pilotParallelReadEvents(t)
	malformed := strings.Replace(e[3], `"name":"read","arguments":""`, `"name":"ls","arguments":"{bad"`, 1)
	requirePilotCallZeroKept(t, e[0]+e[1]+e[2]+malformed+strings.Join(e[5:], ""))
}

func TestResearchCaptureAssemblesCallZeroAcrossInterleavedDeltas(t *testing.T) {
	e := pilotParallelReadEvents(t)
	requirePilotCallZeroKept(t, e[0]+e[1]+e[3]+e[2]+e[4]+strings.Join(e[5:], ""))
}
