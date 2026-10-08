package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sync"
	"testing"

	providerbroker "tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/piruntime"
)

const (
	integrationCallIssuer     = "fixture/provider-tool-call/v1"
	integrationResponseIssuer = "fixture/provider-response/v1"
	integrationToken          = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestProposalTraversesIPCBrokerGateExecutorAndResult(t *testing.T) {
	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-1",
		Tool:          "bash",
		Arguments:     json.RawMessage(`{"command":"true"}`),
	}
	digest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := protocol.NewStream(integrationCallIssuer, integrationResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	capture := protocol.TrustedCapture{
		ResponseID: correlation.Identifier{Issuer: integrationResponseIssuer, Opaque: "response-1"},
		ToolCallID: correlation.Identifier{Issuer: integrationCallIssuer, Opaque: proposal.ToolCallID},
		ToolName:   proposal.Tool, RawArguments: append(json.RawMessage(nil), proposal.Arguments...),
		CanonicalizationProfile:  protocol.CanonicalizationProfile,
		CanonicalArgumentsDigest: digest, Sequence: 1, Generation: "g0",
	}
	if decision := stream.Capture(capture); !decision.Accepted {
		t.Fatalf("stub broker capture failed: %+v", decision)
	}
	policy, err := gate.NewPolicy(gate.Profile{
		Version: gate.ProfileVersion,
		Rules:   []gate.Rule{{Tool: "bash", Effect: gate.EffectAllow}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := ipc.NewPipe(integrationToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()

	var mu sync.Mutex
	events := make([]string, 0, 2)
	broker := &streamBroker{stream: stream, events: &events, mu: &mu}
	supervisor := &Supervisor{IPC: server, Broker: broker, Policy: policy, NoEffect: &piruntime.NoEffectSession{}}
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(context.Background()) }()

	writeErr := make(chan error, 1)
	go func() { writeErr <- client.SendProposal(proposal) }()
	result, err := client.ReceiveResult()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "ALLOW" || result.ReasonCode != "policy_rule_allow" ||
		result.PolicyDigest != policy.Digest() || result.CanonicalArgumentsDigest != digest ||
		result.ResponseID == nil || result.ResponseID.Opaque != "response-1" || string(result.Output) != `{"status":"stubbed-no-effect"}` {
		t.Fatalf("unexpected integrated result: %+v", result)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("supervisor did not shut down cleanly: %v", err)
	}
	mu.Lock()
	gotEvents := append([]string(nil), events...)
	mu.Unlock()
	if !reflect.DeepEqual(gotEvents, []string{"broker-correlation"}) {
		t.Fatalf("no-effect path invoked or reordered effects: %v", gotEvents)
	}
}

func TestConcreteProviderBrokerTraversesIPCToGateAndResult(t *testing.T) {
	arguments := json.RawMessage(`{"command":"true"}`)
	profile := providerbroker.Profile{
		ID: "integration-openrouter-profile-v1", Model: "vendor/model:free",
		Messages:       []openrouter.Message{{Role: openrouter.User, Content: integrationStringPointer("trusted integration prompt")}},
		ToolManifest:   providerbroker.DeclaredToolManifest(),
		ToolCallIssuer: integrationCallIssuer, ResponseIDIssuer: integrationResponseIssuer,
		Generation: "g0",
	}
	transport := &integrationDoer{response: integrationToolStream("response-concrete", "call-concrete", "bash", string(arguments))}
	broker, err := providerbroker.New(profile, transport)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := broker.Exchange(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if captured.ResponseID() != "response-concrete" {
		t.Fatalf("provider response ID = %q", captured.ResponseID())
	}

	policy, err := gate.NewPolicy(gate.Profile{
		Version: gate.ProfileVersion,
		Rules:   []gate.Rule{{Tool: "bash", Effect: gate.EffectAllow}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := ipc.NewPipe(integrationToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	supervisor := &Supervisor{IPC: server, Broker: broker, Policy: policy, NoEffect: &piruntime.NoEffectSession{}}
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(context.Background()) }()

	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-concrete", Tool: "bash", Arguments: arguments,
	}
	writeErr := make(chan error, 1)
	go func() { writeErr <- client.SendProposal(proposal) }()
	result, err := client.ReceiveResult()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "ALLOW" || result.ReasonCode != "policy_rule_allow" ||
		result.ToolCallID != "call-concrete" || result.Tool != "bash" || result.Sequence != 1 ||
		result.ResponseID == nil || result.ResponseID.Issuer != integrationResponseIssuer ||
		result.ResponseID.Opaque != "response-concrete" || len(result.CanonicalArgumentsDigest) == 0 ||
		string(result.Output) != `{"status":"stubbed-no-effect"}` {
		t.Fatalf("unexpected concrete broker result: %+v", result)
	}
	if len(broker.Records()) != 1 || transport.calls != 1 {
		t.Fatalf("provider exchange was not recorded exactly once: records=%d calls=%d", len(broker.Records()), transport.calls)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("supervisor did not shut down cleanly: %v", err)
	}
}

type streamBroker struct {
	stream *protocol.Stream
	events *[]string
	mu     *sync.Mutex
}

func (b *streamBroker) Correlate(_ context.Context, proposal protocol.Proposal) (correlation.Decision, error) {
	b.mu.Lock()
	*b.events = append(*b.events, "broker-correlation")
	b.mu.Unlock()
	encoded, err := protocol.MarshalProposal(proposal)
	if err != nil {
		return correlation.Decision{}, err
	}
	return b.stream.Propose(encoded), nil
}

type integrationDoer struct {
	response []byte
	calls    int
}

func (d *integrationDoer) Do(request *http.Request) (*http.Response, error) {
	if _, err := io.ReadAll(request.Body); err != nil {
		return nil, err
	}
	d.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(d.response)),
		Request:    request,
	}, nil
}

func integrationToolStream(responseID, callID, tool, arguments string) []byte {
	chunk := func(delta any, finish any) []byte {
		payload, err := json.Marshal(map[string]any{
			"id": responseID, "model": "vendor/model:free", "created": 123,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		if err != nil {
			panic(err)
		}
		return append(append([]byte("data: "), payload...), []byte("\n\n")...)
	}
	first := chunk(map[string]any{
		"role": "assistant",
		"tool_calls": []any{map[string]any{
			"index": 0, "id": callID, "type": "function",
			"function": map[string]any{"name": tool, "arguments": arguments},
		}},
	}, nil)
	finish := chunk(map[string]any{}, "tool_calls")
	return append(append(first, finish...), []byte("data: [DONE]\n\n")...)
}

func integrationStringPointer(value string) *string { return &value }
