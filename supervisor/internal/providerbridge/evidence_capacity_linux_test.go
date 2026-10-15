//go:build linux

package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
)

type capacityResponseDoer struct {
	response []byte
	requests int
	request  []byte
	header   http.Header
}

func (d *capacityResponseDoer) Do(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || request.URL.String() != openrouter.Endpoint ||
		request.Header.Get("User-Agent") != openrouter.UserAgent ||
		request.Header.Get(openrouter.SessionHeader) == "" {
		return nil, errors.New("unexpected capacity fixture request")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	d.requests++
	d.request = append([]byte(nil), body...)
	d.header = request.Header.Clone()
	return &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(bytes.NewReader(d.response)), Request: request,
	}, nil
}

func TestNearBoundExchangeAndMaximumCanonicalProjectionAppend(t *testing.T) {
	journal := openCapacityJournal(t)
	workflowID := strings.Repeat("w", MaxIdentityBytes)
	conversationID := strings.Repeat("c", MaxIdentityBytes)
	profileID := strings.Repeat("p", 128)
	generation := strings.Repeat("g", 128)
	treeDigest := strings.Repeat("d", MaxIdentityBytes)
	responseID := strings.Repeat("r", MaxIdentityBytes)
	callID := strings.Repeat("i", MaxIdentityBytes)
	reservationID := strings.Repeat("h", MaxIdentityBytes)
	systemPrompt := strings.Repeat("s", openrouter.MaxAssistantTextBytes)
	developerPrompt := strings.Repeat("d", openrouter.MaxAssistantTextBytes)
	userPrompt := strings.Repeat("u", MaxPromptBytes)
	completionLimit := uint64(math.MaxInt32)
	messages := []openrouter.Message{
		{Role: openrouter.System, Content: &systemPrompt},
		{Role: openrouter.Developer, Content: &developerPrompt},
		{Role: openrouter.User, Content: &userPrompt},
	}
	built, err := openrouter.BuildRequestWithCompletionLimit(ApprovedModel, messages, completionLimit)
	if err != nil {
		t.Fatalf("build maximum bounded request: %v", err)
	}
	if len(built.Body()) >= broker.MaxDurableExchangeBytes {
		t.Fatalf("request length=%d leaves no response budget", len(built.Body()))
	}
	responseBudget := broker.MaxDurableExchangeBytes - len(built.Body())
	assistantText, arguments, projectionBytes, err := maximumCanonicalProjection(
		responseID, callID, generation, MaxTurnProjectionBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(projectionBytes) != MaxTurnProjectionBytes {
		t.Fatalf("canonical projection length=%d, want exact cap %d", len(projectionBytes), MaxTurnProjectionBytes)
	}
	response, err := padSSEToLength(projectedToolResponse(responseID, callID, assistantText, arguments), responseBudget)
	if err != nil {
		t.Fatalf("pad valid response to reserved budget: %v", err)
	}
	if len(response)+len(built.Body()) != broker.MaxDurableExchangeBytes {
		t.Fatalf("raw exchange length=%d, want exact cap %d", len(response)+len(built.Body()), broker.MaxDurableExchangeBytes)
	}
	captured, err := openrouter.CaptureSSE(bytes.NewReader(response))
	if err != nil {
		t.Fatalf("parse maximum bounded SSE capture: %v", err)
	}
	call, ok := captured.ToolCall()
	if !ok || call.ID != callID || call.Name != "write" || !bytes.Equal(call.RawArguments, arguments) || captured.AssistantText() != assistantText {
		t.Fatalf("maximum capture lost decoded projection fields: call=%t arguments=%d assistant=%d", ok, len(call.RawArguments), len(captured.AssistantText()))
	}

	var settled HostCallCompletion
	doer := &capacityResponseDoer{response: response}
	c, err := newWithDoer(Config{
		ConversationID: conversationID, WorkflowID: workflowID, ProfileID: profileID,
		InitialGenerationID: generation, InitialTreeDigest: treeDigest,
		SystemPrompt: systemPrompt, DeveloperPrompt: developerPrompt,
		SelectCompletionTokenLimit: func(context.Context) (uint64, error) { return completionLimit, nil },
		ReserveHostCall: func(_ context.Context, request broker.RequestRecord, tokenLimit uint64) (HostCallBinding, error) {
			return HostCallBinding{
				CallID: reservationID, RequestDigest: broker.DigestRequestRecord(request), TokenUpperBound: tokenLimit,
			}, nil
		},
		CompleteHostCall: func(_ context.Context, completion HostCallCompletion) error {
			settled = completion
			return nil
		},
	}, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatalf("construct maximum bounded conversation: %v", err)
	}
	if err := c.AdmitPrompt(strings.Repeat("t", MaxTaskIDBytes), userPrompt); err != nil {
		t.Fatalf("admit maximum bounded prompt: %v", err)
	}
	turn, err := c.NextTurn(context.Background())
	if err != nil {
		t.Fatalf("append maximum bounded exchange: %v", err)
	}
	if doer.requests != 1 || !bytes.Equal(doer.request, built.Body()) || doer.header.Get(openrouter.SessionHeader) != conversationID {
		t.Fatalf("provider request count/body/session mismatch: calls=%d body=%d session=%q", doer.requests, len(doer.request), doer.header.Get(openrouter.SessionHeader))
	}
	if turn.ToolCall == nil || turn.ToolCall.ID != callID || turn.AssistantText != assistantText || len(canonicalTurnProjection(turn)) != MaxTurnProjectionBytes {
		t.Fatalf("returned turn does not match the exact maximum projection: call=%v text=%d canonical=%d", turn.ToolCall != nil, len(turn.AssistantText), len(canonicalTurnProjection(turn)))
	}
	if settled.Binding.CallID != reservationID || settled.Binding.TokenUpperBound != completionLimit || len(settled.OutcomeDigest) != 64 {
		t.Fatalf("host-call settlement did not bind the appended exchange: %+v", settled)
	}

	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace actual audit appends: %v", err)
	}
	var found bool
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_exchange" {
			continue
		}
		var evidence exchangeEvidence
		if err := json.Unmarshal(record.Event.Data, &evidence); err != nil {
			t.Fatalf("decode appended exchange evidence: %v", err)
		}
		if evidence.WorkflowID != workflowID || evidence.Request.ProfileID != profileID ||
			evidence.Request.Headers[openrouter.SessionHeader][0] != conversationID || evidence.HostCall == nil ||
			evidence.HostCall.CallID != reservationID || len(evidence.HostCall.RequestDigest) != 64 ||
			evidence.HostCall.TokenUpperBound != completionLimit ||
			len(evidence.Request.Body)+len(evidence.Response) != broker.MaxDurableExchangeBytes {
			t.Fatalf("actual exchange lost bounded maximum metadata or raw cap: %+v", evidence)
		}
		encodedRecord, err := json.Marshal(record)
		if err != nil || len(encodedRecord)+1 > audit.MaxRecordBytes {
			t.Fatalf("actual Append record exceeds frame cap: bytes=%d err=%v", len(encodedRecord)+1, err)
		}
		found = true
	}
	if !found {
		t.Fatal("actual audit journal lacks the provider_exchange record")
	}
}

func TestResponseOverflowPersistsOnlyBoundedUnknownEvidence(t *testing.T) {
	journal := openCapacityJournal(t)
	responseID, callID := "response-overflow", "call-overflow"
	validPrefix := projectedToolResponse(responseID, callID, "", []byte(`{"path":"fixture"}`))
	doer := &capacityResponseDoer{}
	c := newCapacityConversation(t, journal, doer)
	if err := c.AdmitPrompt("task-overflow", "bounded response overflow"); err != nil {
		t.Fatal(err)
	}
	request, err := openrouter.BuildRequest(ApprovedModel, c.history)
	if err != nil {
		t.Fatal(err)
	}
	responseLimit := broker.MaxDurableExchangeBytes - len(request.Body())
	if responseLimit <= len(validPrefix) {
		t.Fatalf("fixture response limit=%d is too small", responseLimit)
	}
	response := append(append([]byte(nil), validPrefix...), bytes.Repeat([]byte("x"), responseLimit+2-len(validPrefix))...)
	doer.response = response
	if _, err := c.NextTurn(context.Background()); err == nil {
		t.Fatal("response beyond its pre-dispatch reservation was accepted")
	}
	if doer.requests != 1 {
		t.Fatalf("provider calls=%d, want one bounded request", doer.requests)
	}
	assertUnknownExchangeOnly(t, journal, response[:responseLimit+1], "response overflow")
	decision, err := c.Correlate(context.Background(), protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: callID,
		Tool: "write", Arguments: json.RawMessage(`{"path":"fixture"}`),
	})
	if err != nil || decision.Accepted || !decision.StreamClosed {
		t.Fatalf("overflowed tool capture escaped a closed conversation: decision=%+v err=%v", decision, err)
	}
}

func TestOversizedCanonicalProjectionPersistsRawUnknownWithoutProjection(t *testing.T) {
	journal := openCapacityJournal(t)
	responseID, callID := "response-projection-overflow", "call-projection-overflow"
	assistantText, arguments, _, err := maximumCanonicalProjection(
		responseID, callID, "generation-capacity", MaxTurnProjectionBytes+1,
	)
	if err != nil {
		t.Fatal(err)
	}
	response := projectedToolResponse(responseID, callID, assistantText, arguments)
	captured, err := openrouter.CaptureSSE(bytes.NewReader(response))
	if err != nil {
		t.Fatalf("parse projection overflow fixture: %v", err)
	}
	call, ok := captured.ToolCall()
	if !ok {
		t.Fatal("oversize projection fixture lacks tool call")
	}
	turn := Turn{
		SchemaVersion: bridgeSchemaVersion, ResponseID: captured.ResponseID(), Model: captured.Model(),
		Created: captured.Created(), Generation: "generation-capacity", Sequence: 1,
		AssistantText: captured.AssistantText(), FinishReason: captured.FinishReason(), Usage: captured.Usage(),
		ToolCall: &ToolCallProjection{ID: call.ID, Name: call.Name, Arguments: call.RawArguments},
	}
	if len(canonicalTurnProjection(turn)) != MaxTurnProjectionBytes+1 {
		t.Fatalf("negative canonical projection length=%d", len(canonicalTurnProjection(turn)))
	}
	doer := &capacityResponseDoer{response: response}
	c := newCapacityConversation(t, journal, doer)
	if err := c.AdmitPrompt("task-projection", "bounded projection overflow"); err != nil {
		t.Fatal(err)
	}
	if turn, err := c.NextTurn(context.Background()); err == nil || turn.ToolCall != nil || turn.AssistantText != "" {
		t.Fatalf("oversized projection was released: turn=%+v err=%v", turn, err)
	}
	if doer.requests != 1 {
		t.Fatalf("provider calls=%d, want one", doer.requests)
	}
	assertUnknownExchangeOnly(t, journal, response, "projection overflow")
	decision, err := c.Correlate(context.Background(), protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: callID,
		Tool: "write", Arguments: append(json.RawMessage(nil), arguments...),
	})
	if err != nil || decision.Accepted || !decision.StreamClosed {
		t.Fatalf("oversized tool capture escaped a closed conversation: decision=%+v err=%v", decision, err)
	}
}

func newCapacityConversation(t *testing.T, journal eventJournal, doer broker.HTTPDoer) *Conversation {
	t.Helper()
	if doer == nil {
		doer = &capacityResponseDoer{}
	}
	c, err := newWithDoer(Config{
		ConversationID: "conversation-capacity", WorkflowID: "workflow-capacity", ProfileID: "profile-capacity",
		InitialGenerationID: "generation-capacity", InitialTreeDigest: "tree-capacity",
		SystemPrompt: "bounded capacity fixture", DeveloperPrompt: "fixed profile",
	}, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func openCapacityJournal(t *testing.T) *audit.Journal {
	t.Helper()
	directory, err := os.MkdirTemp(os.Getenv("TMPDIR"), "provider-capacity-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(directory, "provider.jsonl"))
	if err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close private capacity journal: %v", err)
		}
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove private capacity journal: %v", err)
		}
	})
	return journal
}

func maximumCanonicalProjection(responseID, callID, generation string, target int) (string, []byte, []byte, error) {
	assistant := strings.Repeat("<>&", 27_000)
	content := strings.Repeat("<>&", 27_000)
	makeArguments := func(value string) []byte {
		return []byte(fmt.Sprintf(`{"path":"fixture","content":%q}`, value))
	}
	encode := func(text string, arguments []byte) []byte {
		return canonicalTurnProjection(Turn{
			SchemaVersion: bridgeSchemaVersion, ResponseID: responseID, Model: openrouter.ApprovedModel,
			Created: 1, Generation: generation, Sequence: 1, AssistantText: text,
			FinishReason: "tool_calls",
			ToolCall:     &ToolCallProjection{ID: callID, Name: "write", Arguments: arguments},
		})
	}
	arguments := makeArguments(content)
	projection := encode(assistant, arguments)
	if len(projection) > target {
		return "", nil, nil, fmt.Errorf("base HTML projection length %d exceeds target %d", len(projection), target)
	}
	content += strings.Repeat("x", target-len(projection))
	arguments = makeArguments(content)
	projection = encode(assistant, arguments)
	if len(projection) != target {
		return "", nil, nil, fmt.Errorf("adjusted HTML projection length %d, want %d", len(projection), target)
	}
	if _, err := protocol.CanonicalArgumentsDigest("write", arguments); err != nil {
		return "", nil, nil, fmt.Errorf("validate maximum write arguments: %w", err)
	}
	return assistant, arguments, projection, nil
}

func canonicalTurnProjection(turn Turn) []byte {
	turn.JournalSeq = math.MaxUint64
	turn.JournalHash = strings.Repeat("0", 64)
	encoded, err := json.Marshal(turn)
	if err != nil {
		panic(fmt.Sprintf("encode fixture turn projection: %v", err))
	}
	return encoded
}

func projectedToolResponse(responseID, callID, assistant string, arguments []byte) []byte {
	var response bytes.Buffer
	appendChunk := func(delta, finish string) {
		fmt.Fprintf(&response,
			"data: {\"id\":%q,\"model\":%q,\"created\":1,\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}]}\n\n",
			responseID, openrouter.ApprovedModel, delta, finish)
	}
	if assistant != "" {
		appendChunk(fmt.Sprintf(`{"role":"assistant","content":%q}`, assistant), "null")
	}
	if callID != "" {
		appendChunk(fmt.Sprintf(`{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":"write","arguments":%q}}]}`,
			callID, string(arguments)), "null")
		appendChunk(`{}`, `"tool_calls"`)
	} else {
		appendChunk(`{}`, `"stop"`)
	}
	response.WriteString("data: [DONE]\n\n")
	return response.Bytes()
}

func padSSEToLength(raw []byte, target int) ([]byte, error) {
	if target < len(raw) {
		return nil, fmt.Errorf("SSE fixture length %d exceeds response budget %d", len(raw), target)
	}
	remaining := target - len(raw)
	encoded := make([]byte, 0, target)
	for offset := 0; offset < len(raw); {
		delimiter := bytes.Index(raw[offset:], []byte("\n\n"))
		if delimiter < 0 {
			return nil, errors.New("SSE fixture contains an unterminated event")
		}
		end := offset + delimiter
		frame := raw[offset:end]
		padding := 0
		if remaining > 0 && bytes.HasPrefix(frame, []byte("data: {")) {
			capacity := openrouter.MaxSSEEventBytes - len(frame)
			if capacity > 0 {
				padding = remaining
				if padding > capacity {
					padding = capacity
				}
			}
		}
		encoded = append(encoded, frame...)
		encoded = append(encoded, bytes.Repeat([]byte(" "), padding)...)
		encoded = append(encoded, raw[end:end+2]...)
		remaining -= padding
		offset = end + 2
	}
	if remaining != 0 {
		return nil, fmt.Errorf("SSE events cannot hold %d required padding bytes", remaining)
	}
	return encoded, nil
}

func assertUnknownExchangeOnly(t *testing.T, journal *audit.Journal, wantResponse []byte, label string) {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace %s journal: %v", label, err)
	}
	var found bool
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "provider_exchange":
			t.Fatalf("%s unexpectedly appended an accepted provider exchange", label)
		case "provider_exchange_unknown":
			found = true
			var unknown struct {
				Response []byte `json:"response"`
				Failure  string `json:"failure"`
			}
			if err := json.Unmarshal(record.Event.Data, &unknown); err != nil {
				t.Fatalf("decode %s UNKNOWN evidence: %v", label, err)
			}
			if !bytes.Equal(unknown.Response, wantResponse) || len(unknown.Failure) == 0 || len(unknown.Failure) > 512 {
				t.Fatalf("%s UNKNOWN evidence response/failure mismatch: response=%d failure=%d", label, len(unknown.Response), len(unknown.Failure))
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(record.Event.Data, &fields); err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"assistant_text", "tool_call", "arguments", "projection"} {
				if _, exists := fields[forbidden]; exists {
					t.Fatalf("%s UNKNOWN evidence duplicated rejected projection field %q", label, forbidden)
				}
			}
		}
	}
	if !found {
		t.Fatalf("%s lacks a durable UNKNOWN exchange record", label)
	}
}
