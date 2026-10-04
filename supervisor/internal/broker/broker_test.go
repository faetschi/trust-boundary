package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
)

const (
	testCallIssuer     = "fixture/openrouter-tool-call/v1"
	testResponseIssuer = "fixture/openrouter-response/v1"
)

type fakeReply struct {
	status int
	body   []byte
	err    error
}

type fakeDoer struct {
	replies  []fakeReply
	requests []fakeRequest
}

type fakeRequest struct {
	method string
	url    string
	header http.Header
	body   []byte
}

func (d *fakeDoer) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	d.requests = append(d.requests, fakeRequest{
		method: request.Method, url: request.URL.String(), header: request.Header.Clone(), body: append([]byte(nil), body...),
	})
	if len(d.replies) == 0 {
		return nil, errors.New("fake provider has no scripted reply")
	}
	reply := d.replies[0]
	d.replies = d.replies[1:]
	if reply.err != nil {
		return nil, reply.err
	}
	return &http.Response{
		StatusCode: reply.status,
		Body:       io.NopCloser(bytes.NewReader(reply.body)),
		Request:    request,
	}, nil
}

func TestExchangeBuildsAndRecordsExactRegisteredRequest(t *testing.T) {
	profile := testProfile()
	profile.Messages = []openrouter.Message{{Role: openrouter.User, Content: stringPointer("trusted prompt")}}
	doer := &fakeDoer{replies: []fakeReply{{status: http.StatusOK, body: toolResponse("response-1", "call-1", "read", `{"path":"README.md"}`)}}}
	broker, err := New(profile, doer)
	if err != nil {
		t.Fatal(err)
	}

	captured, err := broker.Exchange(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if captured.ResponseID() != "response-1" || captured.FinishReason() != "tool_calls" {
		t.Fatalf("captured response = %q / %q", captured.ResponseID(), captured.FinishReason())
	}

	built, err := openrouter.BuildRequest(profile.Model, profile.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(doer.requests) != 1 {
		t.Fatalf("transport calls = %d, want one", len(doer.requests))
	}
	actual := doer.requests[0]
	if actual.method != http.MethodPost || actual.url != openrouter.Endpoint ||
		actual.header.Get("Content-Type") != "application/json" || actual.header.Get("Accept") != "text/event-stream" ||
		!bytes.Equal(actual.body, built.Body()) {
		t.Fatalf("transport request did not match the registered OpenRouter request: %+v", actual)
	}

	records := broker.Records()
	if len(records) != 1 || !records[0].HasResponse() || records[0].StatusCode() != http.StatusOK ||
		!bytes.Equal(records[0].Request().Body(), built.Body()) ||
		!bytes.Equal(records[0].ResponseBody(), captured.RawResponse()) || records[0].Failure() != "" {
		t.Fatalf("in-memory exchange record differs from sent/captured bytes: %+v", records)
	}
	recordedRequest := records[0].Request()
	if recordedRequest.ProfileID() != profile.ID || recordedRequest.Method() != http.MethodPost ||
		recordedRequest.URL() != openrouter.Endpoint || recordedRequest.Header().Get("Content-Type") != "application/json" ||
		recordedRequest.Header().Get("Accept") != "text/event-stream" {
		t.Fatalf("request record omitted exact routing metadata: %+v", recordedRequest)
	}
	var requestBody struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(records[0].Request().Body(), &requestBody); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(requestBody.Tools))
	for index, tool := range requestBody.Tools {
		names[index] = tool.Function.Name
	}
	if !reflect.DeepEqual(names, DeclaredToolManifest()) {
		t.Fatalf("provider request tools = %v, want %v", names, DeclaredToolManifest())
	}

	mutatedBody := records[0].Request().Body()
	mutatedBody[0] ^= 0xff
	mutatedResponse := records[0].ResponseBody()
	mutatedResponse[0] ^= 0xff
	if !bytes.Equal(records[0].Request().Body(), built.Body()) || !bytes.Equal(records[0].ResponseBody(), captured.RawResponse()) {
		t.Fatal("record accessors exposed mutable request or response bytes")
	}
}

func TestProfileRequiresTheExactDeclaredManifest(t *testing.T) {
	tests := map[string][]string{
		"missing":   {"read", "write", "edit"},
		"extra":     {"read", "write", "edit", "bash", "exec"},
		"reordered": {"read", "write", "bash", "edit"},
		"duplicate": {"read", "write", "edit", "read"},
	}
	for name, manifest := range tests {
		t.Run(name, func(t *testing.T) {
			profile := testProfile()
			profile.ToolManifest = manifest
			if _, err := New(profile, &fakeDoer{}); err == nil {
				t.Fatal("profile outside the four-tool manifest was registered")
			}
		})
	}
	if _, err := New(testProfile(), nil); err == nil {
		t.Fatal("broker without an injected transport was registered")
	}
}

func TestProposalCorrelationBindsTrustedResponseCallNameAndDigest(t *testing.T) {
	args := `{"path":"README.md"}`
	broker, _ := newTestBroker(t, toolResponse("response-42", "call-42", "read", args))
	if _, err := broker.Exchange(context.Background()); err != nil {
		t.Fatal(err)
	}
	proposal := testProposal("call-42", "read", args)
	digest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := broker.Correlate(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Accepted || decision.ReasonCode != correlation.ReasonMatched || decision.StreamClosed ||
		decision.Proposal == nil || decision.BrokerCapture == nil {
		t.Fatalf("proposal did not match its provider capture: %+v", decision)
	}
	if decision.Proposal.ResponseID != (correlation.Identifier{Issuer: testResponseIssuer, Opaque: "response-42"}) ||
		decision.Proposal.ToolCallID != (correlation.Identifier{Issuer: testCallIssuer, Opaque: "call-42"}) ||
		decision.Proposal.ToolName != "read" || decision.Proposal.CanonicalArgumentsDigest != digest ||
		decision.BrokerCapture.ResponseID != decision.Proposal.ResponseID ||
		decision.BrokerCapture.ToolCallID != decision.Proposal.ToolCallID ||
		decision.BrokerCapture.ToolName != decision.Proposal.ToolName ||
		decision.BrokerCapture.CanonicalArgumentsDigest != decision.Proposal.CanonicalArgumentsDigest {
		t.Fatalf("correlation receipt lost provider binding fields: %+v", decision)
	}
}

func TestUnknownDuplicateStaleAndDigestMismatchedCallsAreRejected(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		broker, _ := newTestBroker(t)
		decision, err := broker.Correlate(context.Background(), testProposal("never-captured", "read", `{"path":"x"}`))
		if err != nil || decision.Accepted || decision.ReasonCode != correlation.ReasonUnknownCall || !decision.StreamClosed {
			t.Fatalf("unknown call result = %+v, %v", decision, err)
		}
	})

	t.Run("duplicate provider call ID", func(t *testing.T) {
		response := toolResponse("response-1", "duplicate-call", "read", `{"path":"x"}`)
		broker, _ := newTestBroker(t, response, toolResponse("response-2", "duplicate-call", "read", `{"path":"y"}`))
		if _, err := broker.Exchange(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := broker.Exchange(context.Background()); err == nil || !strings.Contains(err.Error(), string(correlation.ReasonDuplicateCallID)) {
			t.Fatalf("duplicate provider call was not rejected: %v", err)
		}
	})

	t.Run("stale replay", func(t *testing.T) {
		broker, _ := newTestBroker(t,
			toolResponse("response-1", "call-1", "read", `{"path":"x"}`),
			toolResponse("response-2", "call-2", "read", `{"path":"y"}`),
		)
		for index := 0; index < 2; index++ {
			if _, err := broker.Exchange(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		first := testProposal("call-1", "read", `{"path":"x"}`)
		if decision, err := broker.Correlate(context.Background(), first); err != nil || !decision.Accepted {
			t.Fatalf("first call failed: %+v, %v", decision, err)
		}
		if decision, err := broker.Correlate(context.Background(), testProposal("call-2", "read", `{"path":"y"}`)); err != nil || !decision.Accepted {
			t.Fatalf("second call failed: %+v, %v", decision, err)
		}
		decision, err := broker.Correlate(context.Background(), first)
		if err != nil || decision.Accepted || decision.ReasonCode != correlation.ReasonReplay || !decision.StreamClosed {
			t.Fatalf("stale call result = %+v, %v", decision, err)
		}
	})

	t.Run("digest mismatch", func(t *testing.T) {
		broker, _ := newTestBroker(t, toolResponse("response-1", "call-1", "write", `{"path":"x","content":"expected"}`))
		if _, err := broker.Exchange(context.Background()); err != nil {
			t.Fatal(err)
		}
		decision, err := broker.Correlate(context.Background(), testProposal("call-1", "write", `{"path":"x","content":"changed"}`))
		if err != nil || decision.Accepted || decision.ReasonCode != correlation.ReasonArgumentDigestMismatch || !decision.StreamClosed {
			t.Fatalf("digest mismatch result = %+v, %v", decision, err)
		}
	})

	t.Run("exact tool name", func(t *testing.T) {
		broker, _ := newTestBroker(t, toolResponse("response-1", "call-1", "read", `{"path":"x"}`))
		if _, err := broker.Exchange(context.Background()); err != nil {
			t.Fatal(err)
		}
		decision, err := broker.Correlate(context.Background(), testProposal("call-1", "write", `{"path":"x","content":"y"}`))
		if err != nil || decision.Accepted || decision.ReasonCode != correlation.ReasonToolNameMismatch || !decision.StreamClosed {
			t.Fatalf("tool-name mismatch result = %+v, %v", decision, err)
		}
	})
}

func TestUnknownProviderToolIsRejectedByTheManifest(t *testing.T) {
	broker, _ := newTestBroker(t, toolResponse("response-1", "call-1", "powershell", `{"command":"true"}`))
	if _, err := broker.Exchange(context.Background()); err == nil {
		t.Fatal("provider response outside the declared tool manifest was accepted")
	}
	if records := broker.Records(); len(records) != 1 || records[0].Failure() == "" {
		t.Fatalf("rejected provider response was not retained in the exchange record: %+v", records)
	}
}

func TestTransportFailureIsRecordedAndClosesBroker(t *testing.T) {
	doer := &fakeDoer{replies: []fakeReply{{err: errors.New("offline")}}}
	broker, err := New(testProfile(), doer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Exchange(context.Background()); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("transport failure was not returned: %v", err)
	}
	if records := broker.Records(); len(records) != 1 || records[0].Request().URL() != openrouter.Endpoint || records[0].HasResponse() || records[0].Failure() == "" {
		t.Fatalf("failed request was not recorded: %+v", records)
	}
	if _, err := broker.Exchange(context.Background()); err == nil || len(doer.requests) != 1 {
		t.Fatalf("closed broker retried provider transport: err=%v requests=%d", err, len(doer.requests))
	}
	decision, err := broker.Correlate(context.Background(), testProposal("call-1", "read", `{"path":"x"}`))
	if err != nil || decision.ReasonCode != correlation.ReasonSessionClosed || !decision.StreamClosed {
		t.Fatalf("closed broker accepted correlation: %+v, %v", decision, err)
	}
}

func TestNonSuccessHTTPStatusFailsClosedAndRecordsResponse(t *testing.T) {
	doer := &fakeDoer{replies: []fakeReply{{status: http.StatusFound, body: []byte("redirect")}}}
	broker, err := New(testProfile(), doer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Exchange(context.Background()); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("non-success provider status was accepted: %v", err)
	}
	if records := broker.Records(); len(records) != 1 || records[0].StatusCode() != http.StatusFound || string(records[0].ResponseBody()) != "redirect" {
		t.Fatalf("HTTP failure response was not retained: %+v", records)
	}
}

func TestResponseModelMustMatchRegisteredProfile(t *testing.T) {
	response := toolResponse("response-1", "call-1", "read", `{"path":"x"}`)
	response = bytes.ReplaceAll(response, []byte("vendor/model:free"), []byte("other/model"))
	broker, _ := newTestBroker(t, response)
	if _, err := broker.Exchange(context.Background()); err == nil || !strings.Contains(err.Error(), "model differs") {
		t.Fatalf("provider response for another model was accepted: %v", err)
	}
}

func TestRegisteredProfileIsDefensivelyCopied(t *testing.T) {
	profile := testProfile()
	prompt := "original"
	profile.Messages = []openrouter.Message{{Role: openrouter.User, Content: &prompt}}
	doer := &fakeDoer{replies: []fakeReply{{status: http.StatusOK, body: toolResponse("response-1", "call-1", "read", `{"path":"x"}`)}}}
	broker, err := New(profile, doer)
	if err != nil {
		t.Fatal(err)
	}
	prompt = "mutated after registration"
	profile.ToolManifest[0] = "exec"
	if _, err := broker.Exchange(context.Background()); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(doer.requests[0].body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 1 || body.Messages[0].Content != "original" {
		t.Fatalf("registered prompt changed after caller mutation: %+v", body.Messages)
	}
}

func newTestBroker(t *testing.T, responses ...[]byte) (*Broker, *fakeDoer) {
	t.Helper()
	replies := make([]fakeReply, len(responses))
	for index, response := range responses {
		replies[index] = fakeReply{status: http.StatusOK, body: response}
	}
	doer := &fakeDoer{replies: replies}
	broker, err := New(testProfile(), doer)
	if err != nil {
		t.Fatal(err)
	}
	return broker, doer
}

func testProfile() Profile {
	return Profile{
		ID: "fixture-openrouter-profile-v1", Model: "vendor/model:free",
		Messages:         []openrouter.Message{{Role: openrouter.User, Content: stringPointer("trusted fixture prompt")}},
		ToolManifest:     DeclaredToolManifest(),
		ToolCallIssuer:   testCallIssuer,
		ResponseIDIssuer: testResponseIssuer,
		Generation:       "g0",
	}
}

func testProposal(callID, tool, arguments string) protocol.Proposal {
	return protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    callID,
		Tool:          tool,
		Arguments:     json.RawMessage(arguments),
	}
}

func toolResponse(responseID, callID, tool, arguments string) []byte {
	first := sseChunk(responseID, map[string]any{
		"role": "assistant",
		"tool_calls": []any{map[string]any{
			"index": 0, "id": callID, "type": "function",
			"function": map[string]any{"name": tool, "arguments": arguments},
		}},
	}, nil)
	finish := sseChunk(responseID, map[string]any{}, "tool_calls")
	return append(append(first, finish...), []byte("data: [DONE]\n\n")...)
}

func sseChunk(responseID string, delta any, finish any) []byte {
	chunk := map[string]any{
		"id": responseID, "model": "vendor/model:free", "created": 123,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	raw, err := json.Marshal(chunk)
	if err != nil {
		panic(fmt.Sprintf("encode fixture chunk: %v", err))
	}
	return append(append([]byte("data: "), raw...), []byte("\n\n")...)
}

func stringPointer(value string) *string { return &value }
