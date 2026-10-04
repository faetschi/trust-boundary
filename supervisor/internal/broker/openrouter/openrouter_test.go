package openrouter

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
)

func strptr(s string) *string { return &s }
func event(data []byte) []byte {
	out := append([]byte("data: "), data...)
	out = append(out, 13, 10, 13, 10)
	return out
}
func doneEvent() []byte { return event([]byte("[DONE]")) }
func chunk(id string, delta any, finish any) []byte {
	value := map[string]any{
		"id": id, "model": "vendor/model:free", "provider": "provider-a",
		"object": "chat.completion.chunk", "created": 123,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return event(raw)
}
func toolDelta(args string) map[string]any {
	return map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": args}}}}
}
func toolStream(responseID, callID, firstArgs, secondArgs, finish string) []byte {
	body := chunk(responseID, map[string]any{
		"role": "assistant", "content": "Checking. ",
		"tool_calls": []any{map[string]any{
			"index": 0, "id": callID, "type": "function",
			"function": map[string]any{"name": "read", "arguments": firstArgs},
		}},
	}, nil)
	body = append(body, chunk(responseID, toolDelta(secondArgs), nil)...)
	body = append(body, chunk(responseID, map[string]any{}, finish)...)
	return append(body, doneEvent()...)
}

type fragmentReader struct {
	raw []byte
	max int
}

func (r *fragmentReader) Read(out []byte) (int, error) {
	if len(r.raw) == 0 {
		return 0, io.EOF
	}
	n := len(out)
	if n > r.max {
		n = r.max
	}
	if n > len(r.raw) {
		n = len(r.raw)
	}
	copy(out, r.raw[:n])
	r.raw = r.raw[n:]
	return n, nil
}

func TestBuildRequestFixesEndpointAndToolSchemas(t *testing.T) {
	built, err := BuildRequest("vendor/model:free", []Message{{Role: User, Content: strptr("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	if built.RequestURL() != Endpoint {
		t.Fatalf("endpoint=%q", built.RequestURL())
	}
	var body struct {
		Model string `json:"model"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string         `json:"name"`
				Parameters map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		Stream     bool   `json:"stream"`
		Parallel   *bool  `json:"parallel_tool_calls"`
		ToolChoice string `json:"tool_choice"`
	}
	original := built.Body()
	if err := json.Unmarshal(original, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "vendor/model:free" || !body.Stream || body.Parallel == nil || *body.Parallel || body.ToolChoice != "auto" {
		t.Fatalf("profile=%+v", body)
	}
	names := []string{"read", "write", "edit", "bash"}
	required := [][]string{{"path"}, {"path", "content"}, {"path", "edits"}, {"command"}}
	for i, tool := range body.Tools {
		if tool.Type != "function" || tool.Function.Name != names[i] {
			t.Fatalf("tool[%d]=%+v", i, tool)
		}
		params := tool.Function.Parameters
		if params["type"] != "object" {
			t.Fatalf("%s parameters=%v", tool.Function.Name, params)
		}
		req, ok := params["required"].([]any)
		if !ok {
			t.Fatalf("%s required missing", tool.Function.Name)
		}
		got := make([]string, len(req))
		for j, v := range req {
			got[j] = v.(string)
		}
		if !reflect.DeepEqual(got, required[i]) {
			t.Fatalf("%s required=%v", tool.Function.Name, got)
		}
		if _, invented := params["additionalProperties"]; invented {
			t.Fatalf("%s added an unregistered schema rule", tool.Function.Name)
		}
	}
	copyBody := built.Body()
	copyBody[0] ^= 0xff
	if !bytes.Equal(built.Body(), original) {
		t.Fatal("Body did not return a defensive copy")
	}
}
func TestBuildRequestRejectsUnsupportedMessageAndTool(t *testing.T) {
	if _, err := BuildRequest("vendor/model", []Message{{Role: "function", Content: strptr("x")}}); err == nil {
		t.Fatal("unsupported role accepted")
	}
	if _, err := BuildRequest(" ", []Message{{Role: User, Content: strptr("x")}}); err == nil {
		t.Fatal("invalid model accepted")
	}
	_, err := BuildRequest("vendor/model", []Message{{Role: Assistant, ToolCalls: []PriorToolCall{{ID: "c1", Name: "read", RawArguments: json.RawMessage(`{"path":"ok","extra":true}`)}}}})
	if err == nil {
		t.Fatal("prior call outside the registered schema accepted")
	}
}
func TestCaptureSSEReassemblesFragmentsAndMapsTrustedCapture(t *testing.T) {
	raw := toolStream("resp-1", "call-1", `{"path": `, `"draft.txt"}`, "tool_calls")
	got, err := CaptureSSE(&fragmentReader{raw: raw, max: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.ResponseID() != "resp-1" || got.Model() != "vendor/model:free" || got.Provider() != "provider-a" || got.Created() != 123 || got.AssistantText() != "Checking. " || got.FinishReason() != "tool_calls" {
		t.Fatalf("captured response fields differ")
	}
	if !bytes.Equal(got.RawResponse(), raw) {
		t.Fatal("raw response bytes changed")
	}
	call, ok := got.ToolCall()
	if !ok {
		t.Fatal("missing tool call")
	}
	wantArgs := `{"path": "draft.txt"}`
	if call.ID != "call-1" || call.Name != "read" || string(call.RawArguments) != wantArgs {
		t.Fatalf("call=%+v raw=%q", call, call.RawArguments)
	}
	digest, err := protocol.CanonicalArgumentsDigest("read", json.RawMessage(wantArgs))
	if err != nil {
		t.Fatal(err)
	}
	if call.CanonicalArgumentsDigest != digest {
		t.Fatalf("digest=%s want %s", call.CanonicalArgumentsDigest, digest)
	}
	capture, err := got.TrustedCapture("openrouter-call", "openrouter-response", "generation-0", 1)
	if err != nil {
		t.Fatal(err)
	}
	if capture.ResponseID.Issuer != "openrouter-response" || capture.ResponseID.Opaque != "resp-1" || capture.ToolCallID.Issuer != "openrouter-call" || capture.ToolCallID.Opaque != "call-1" || capture.Sequence != 1 || capture.Generation != "generation-0" || capture.CanonicalArgumentsDigest != digest {
		t.Fatalf("capture=%+v", capture)
	}
	copyRaw := got.RawResponse()
	copyRaw[0] ^= 0xff
	if !bytes.Equal(got.RawResponse(), raw) {
		t.Fatal("RawResponse did not return a defensive copy")
	}
}
func TestCaptureSSEAcceptsTextStop(t *testing.T) {
	raw := append(chunk("resp-text", map[string]any{"role": "assistant", "content": "answer"}, "stop"), doneEvent()...)
	got, err := CaptureSSE(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.FinishReason() != "stop" || got.AssistantText() != "answer" {
		t.Fatal("text completion was not preserved")
	}
	if _, ok := got.ToolCall(); ok {
		t.Fatal("stop response exposed a tool call")
	}
	if _, err := got.TrustedCapture("calls", "responses", "generation-0", 1); err == nil {
		t.Fatal("text response produced trusted tool capture")
	}
}
func TestCaptureSSERejectsAmbiguousStreams(t *testing.T) {
	valid := toolStream("resp-1", "call-1", `{"path":"`, `x.txt"}`, "tool_calls")
	noDone := bytes.TrimSuffix(valid, doneEvent())
	duplicate := event([]byte(`{"id":"r","id":"other","model":"m","created":1,"choices":[]} `))
	badArgs := toolStream("r", "c", `{"path":"x","path":"y"}`, "", "tool_calls")
	stopCall := toolStream("r", "c", `{"path":"x"}`, "", "stop")
	changed := chunk("r", map[string]any{"role": "assistant", "content": "x"}, nil)
	changed = append(changed, chunk("r2", map[string]any{}, "stop")...)
	changed = append(changed, doneEvent()...)
	multipleChoice := event([]byte(`{"id":"r","model":"m","created":1,"choices":[{"index":0,"delta":{},"finish_reason":null},{"index":1,"delta":{},"finish_reason":null}]} `))
	cases := map[string][]byte{"missing DONE": noDone, "duplicate chunk key": append(duplicate, doneEvent()...), "duplicate argument key": badArgs, "tool call with stop finish": stopCall, "response ID changes": changed, "multiple choices": append(multipleChoice, doneEvent()...)}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := CaptureSSE(bytes.NewReader(raw)); err == nil {
				t.Fatal("malformed stream accepted")
			}
		})
	}
}
func TestCaptureSSEBoundsResponse(t *testing.T) {
	over := strings.NewReader(strings.Repeat("x", MaxResponseBytes+1))
	if _, err := CaptureSSE(over); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestBuildRequestPreflightsBeforeMarshaling(t *testing.T) {
	content := strings.Repeat("x", MaxRequestBytes)
	if _, err := BuildRequest("vendor/model", []Message{{Role: User, Content: &content}}); err == nil {
		t.Fatal("oversized request content was accepted")
	}
}

func escapedUnit(hex string) []byte {
	return append([]byte{92, 'u'}, []byte(hex)...)
}

func replaceJSONStringValue(raw []byte, marker string, value []byte) []byte {
	needle := append([]byte{'"'}, []byte(marker)...)
	needle = append(needle, '"')
	replacement := append([]byte{'"'}, value...)
	replacement = append(replacement, '"')
	return bytes.Replace(raw, needle, replacement, 1)
}

func TestStrictJSONSurrogateIDs(t *testing.T) {
	for _, test := range []struct {
		name string
		unit string
	}{
		{name: "high", unit: "d800"},
		{name: "low", unit: "dc00"},
	} {
		t.Run(test.name+" response ID", func(t *testing.T) {
			raw := chunk("response-marker", map[string]any{"role": "assistant", "content": "x"}, "stop")
			raw = replaceJSONStringValue(raw, "response-marker", escapedUnit(test.unit))
			raw = append(raw, doneEvent()...)
			if _, err := CaptureSSE(bytes.NewReader(raw)); err == nil {
				t.Fatal("unpaired escaped response ID was accepted")
			}
		})
		t.Run(test.name+" call ID", func(t *testing.T) {
			raw := toolStream("response", "call-marker", `{"path":"x"}`, "", "tool_calls")
			raw = replaceJSONStringValue(raw, "call-marker", escapedUnit(test.unit))
			if _, err := CaptureSSE(bytes.NewReader(raw)); err == nil {
				t.Fatal("unpaired escaped tool-call ID was accepted")
			}
		})
	}

	pair := append(escapedUnit("d83d"), escapedUnit("de80")...)
	responsePair := append([]byte("resp-"), pair...)
	callPair := append([]byte("call-"), pair...)
	response := chunk("response-marker", map[string]any{"role": "assistant", "content": "x"}, "stop")
	response = replaceJSONStringValue(response, "response-marker", responsePair)
	response = append(response, doneEvent()...)
	gotResponse, err := CaptureSSE(bytes.NewReader(response))
	if err != nil {
		t.Fatalf("valid surrogate pair in response ID was rejected: %v", err)
	}
	rocket := string(rune(0x1f680))
	if gotResponse.ResponseID() != "resp-"+rocket {
		t.Fatalf("response ID=%q", gotResponse.ResponseID())
	}

	call := toolStream("response", "call-marker", `{"path":"x"}`, "", "tool_calls")
	call = replaceJSONStringValue(call, "call-marker", callPair)
	gotCall, err := CaptureSSE(bytes.NewReader(call))
	if err != nil {
		t.Fatalf("valid surrogate pair in tool-call ID was rejected: %v", err)
	}
	captured, ok := gotCall.ToolCall()
	if !ok || captured.ID != "call-"+rocket {
		t.Fatalf("tool-call ID was not decoded: %+v", captured)
	}
}

func TestCaptureSSERejectsMultipleToolCalls(t *testing.T) {
	delta := map[string]any{"tool_calls": []any{
		map[string]any{"index": 0},
		map[string]any{"index": 1},
	}}
	raw := append(chunk("response", delta, "tool_calls"), doneEvent()...)
	if _, err := CaptureSSE(bytes.NewReader(raw)); err == nil {
		t.Fatal("multiple tool calls were accepted")
	}
}
