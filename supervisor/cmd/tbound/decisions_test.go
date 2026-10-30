package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
)

// denyCorrelationBroker returns an empty correlation receipt, so every gate
// evaluation is a DENY. It lets a test exercise the pre-executor decision hook
// without a matching trusted capture.
type denyCorrelationBroker struct{}

func (denyCorrelationBroker) Correlate(context.Context, protocol.Proposal) (correlation.Decision, error) {
	return correlation.Decision{}, nil
}

type executorFunc func(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error)

func (f executorFunc) Execute(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) (json.RawMessage, error) {
	return f(ctx, proposal, decision)
}

type failingDecisionRecorder struct{ calls int }

func (r *failingDecisionRecorder) RecordDecision(context.Context, protocol.Proposal, gate.Decision) error {
	r.calls++
	return errors.New("decision recorder unavailable")
}

// TestServeRecordsDecisionFailClosed checks that a decision-recorder error
// withholds the result, never runs the executor, and fails the session closed
// on every platform.
func TestServeRecordsDecisionFailClosed(t *testing.T) {
	policy, err := gate.NewPolicy(gate.Profile{
		Version: gate.ProfileVersion,
		Rules:   []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}},
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

	recorder := &failingDecisionRecorder{}
	executed := false
	supervisor := &Supervisor{
		IPC: server, Broker: denyCorrelationBroker{}, Policy: policy,
		Executor: executorFunc(func(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error) {
			executed = true
			return json.RawMessage(`{}`), nil
		}),
		Decisions: recorder,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(context.Background()) }()

	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-denied-by-recorder", Tool: "write",
		Arguments: json.RawMessage(`{"path":"x","content":"y"}`),
	}
	if err := client.SendProposal(proposal); err != nil {
		t.Fatalf("send proposal: %v", err)
	}
	if _, err := client.ReceiveResult(); err == nil {
		t.Fatal("result was released despite a failing decision recorder")
	}
	if err := <-serveErr; err == nil {
		t.Fatal("supervisor returned nil despite a failing decision recorder")
	}
	if recorder.calls != 1 {
		t.Fatalf("decision recorder calls = %d; want 1", recorder.calls)
	}
	if executed {
		t.Fatal("executor ran after the decision recorder failed")
	}
}
