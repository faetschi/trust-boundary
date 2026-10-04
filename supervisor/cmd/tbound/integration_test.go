package main

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
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
	executor := &stubExecutor{events: &events, mu: &mu}
	supervisor := &Supervisor{IPC: server, Broker: broker, Policy: policy, Executor: executor}
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
		result.ResponseID == nil || result.ResponseID.Opaque != "response-1" || string(result.Output) != `{"status":"stub-executed"}` {
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
	if !reflect.DeepEqual(gotEvents, []string{"broker-correlation", "stub-executor"}) {
		t.Fatalf("broker/gate/executor ordering = %v", gotEvents)
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

type stubExecutor struct {
	events *[]string
	mu     *sync.Mutex
}

func (e *stubExecutor) Execute(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error) {
	e.mu.Lock()
	*e.events = append(*e.events, "stub-executor")
	e.mu.Unlock()
	return json.RawMessage(`{"status":"stub-executed"}`), nil
}
