//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

var heldCallRead = providerbridge.DevelopmentProviderTurn{ResponseID: "held-response-1", CallID: "held-tool-call-1", Tool: "read",
	Arguments: json.RawMessage(`{"path":"task.txt"}`)}

// heldCallDecided captures the fixture's first read and records its ALLOW decision, as the supervisor loop does
// before it calls the executor.
func heldCallDecided(t *testing.T, f *workflowVerticalFixture) (protocol.Proposal, gate.Decision) {
	t.Helper()
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.ToolCall == nil || turn.ToolCall.ID != heldCallRead.CallID {
		t.Fatalf("provider turn: turn=%+v err=%v", turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
		Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
	matched, err := f.conversation.Correlate(f.ctx, proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate the read: %+v %v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, f.policy)
	if err := f.conversation.RecordDecision(f.ctx, proposal, decision); err != nil {
		t.Fatal(err)
	}
	return proposal, decision
}

// executorEnd runs the decided call through the provider result wrapper over inner under ctx and returns the
// governed loops' joined error, built as piruntime.Supervisor.Serve and providerbridge.Serve wrap theirs.
func executorEnd(t *testing.T, f *workflowVerticalFixture, ctx context.Context, inner providerbridge.Executor,
	proposal protocol.Proposal, decision gate.Decision) error {
	t.Helper()
	executor, err := providerbridge.NewExecutor(f.conversation, inner)
	if err != nil {
		t.Fatal(err)
	}
	output, execErr := executor.Execute(ctx, proposal, decision)
	if execErr == nil || len(output) != 0 {
		t.Fatalf("the canceled call released a result: %s %v", output, execErr)
	}
	serveErr := fmt.Errorf("executor failed; result withheld: %w", execErr)
	_, nextErr := f.conversation.NextTurn(f.ctx)
	bridgeErr := fmt.Errorf("provider turn failed without a journaled disposition; conversation closed: %w", nextErr)
	return governedChannelsError(serveErr, bridgeErr)
}

// cancelBeforeExecute cancels the call's context after the provider result wrapper dispatched it and before
// the durable executor runs, so the durable executor's own context check refuses it.
type cancelBeforeExecute struct {
	inner  providerbridge.Executor
	cancel context.CancelFunc
}

func (e cancelBeforeExecute) Execute(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) (json.RawMessage, error) {
	e.cancel()
	return e.inner.Execute(ctx, proposal, decision)
}

// requireHeldCallNeverReserved checks that the fixture's read is still captured and unconsumed, that no effect
// reservation exists, and that the Store authorized nothing.
func requireHeldCallNeverReserved(t *testing.T, f *workflowVerticalFixture) {
	t.Helper()
	checkpoint, _ := f.ledger.Snapshot()
	call := checkpoint.Pins.HostCallReservation
	if call == nil || call.CapturedCall == nil || call.CapturedCall.Disposition != workflowrecovery.CapturedCallAvailable ||
		call.CapturedCall.ToolCallID != heldCallRead.CallID || checkpoint.Pins.EffectReservation != nil {
		t.Fatalf("checkpoint after the stopped call: pins=%+v", checkpoint.Pins)
	}
	trace, err := f.storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind == governedOperationAuthorizationKind || record.Event.Kind == "effect_intent" {
			t.Fatalf("the stopped call reached the Store: %s", record.Event.Kind)
		}
	}
}

// A captured call can end without its effect in three ways besides a refused reservation. In each, the
// checkpoint holds the call unconsumed with no effect reservation. Cleanup then drains and releases, abandon
// settles the request from that checkpoint fact alone, and a later request is admitted.
func TestHeldCallNeverReservedFreesCleanupAndAbandonSettles(t *testing.T) {
	for name, end := range map[string]func(*testing.T, *workflowVerticalFixture) error{
		// The request was accepted productionSessionMaxRuntime (1,200 s) ago less three seconds. The provider
		// reply arrives and is captured, then the request context expires before Pi's proposal is read: the
		// bridge and the supervisor loop both stop on the context and return nil.
		"request timeout before the proposal is read": func(t *testing.T, f *workflowVerticalFixture) error {
			accepted := time.Now().Add(3*time.Second - productionSessionMaxRuntime)
			requestCtx, cancel := context.WithDeadline(f.ctx, accepted.Add(productionSessionMaxRuntime))
			defer cancel()
			serverConn, clientConn := net.Pipe()
			defer clientConn.Close()
			bridgeDone := make(chan error, 1)
			go func() { bridgeDone <- providerbridge.Serve(requestCtx, serverConn, f.conversation) }()
			writeProviderNextFrame(t, clientConn, 1, "held-next")
			if frame := readProviderFrame(t, clientConn); frame["kind"] == "error" {
				t.Fatalf("the provider turn failed before the timeout: %v", frame)
			}
			var bridgeErr error
			select {
			case bridgeErr = <-bridgeDone:
			case <-time.After(30 * time.Second):
				t.Fatal("the bridge did not stop at the request deadline")
			}
			if !errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
				t.Fatalf("the bridge stopped before the request deadline: %v", bridgeErr)
			}
			// piruntime.Supervisor.Serve returns nil once its context is done.
			return governedChannelsError(nil, bridgeErr)
		},
		// The context ends after the gate decision and before the provider result wrapper dispatches the call:
		// the wrapper's own check closes the conversation and writes no record.
		"bridge context check before dispatch": func(t *testing.T, f *workflowVerticalFixture) error {
			proposal, decision := heldCallDecided(t, f)
			canceled, cancel := context.WithCancel(f.ctx)
			cancel()
			return executorEnd(t, f, canceled, f.durable, proposal, decision)
		},
		// The context ends after dispatch and before the durable executor reaches the Store: its own check
		// refuses, and the wrapper journals an untyped UNKNOWN result.
		"executor context check": func(t *testing.T, f *workflowVerticalFixture) error {
			proposal, decision := heldCallDecided(t, f)
			canceled, cancel := context.WithCancel(f.ctx)
			defer cancel()
			return executorEnd(t, f, canceled, cancelBeforeExecute{inner: f.durable, cancel: cancel}, proposal, decision)
		},
	} {
		t.Run(name, func(t *testing.T) {
			const reason = "held call never reserved; operator recovery"
			f := newWorkflowVerticalFixture(t, true, 0, []providerbridge.DevelopmentProviderTurn{heldCallRead})
			channelsErr := f.authority.heldCallChannelsError(end(t, f))
			requireHeldCallNeverReserved(t, f)
			h := newUnknownRequestHarness(t, f)
			if err := h.abandon(reason); !errors.Is(err, ErrPiServiceUnreconciled) ||
				!strings.Contains(err.Error(), "managed-root run") || !strings.Contains(err.Error(), "owner scope") {
				t.Fatalf("abandon before cleanup was not refused for the unreleased run and the undrained scope: %v", err)
			}
			if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), newProductionCommandRunner(nil)) {
				t.Fatalf("cleanup stayed quarantined after a call that was never reserved: %v", channelsErr)
			}
			abandoned := h.abandoned(t, reason)
			if abandoned.Reconciliation.ProviderSettlement != providerSettlementTerminal ||
				abandoned.Reconciliation.WorkflowID != workflowVerticalID {
				t.Fatalf("the abandon record does not settle the workflow: %+v", abandoned.Reconciliation)
			}
			if err := h.requests.admit(t, 2, true); err != nil {
				t.Fatalf("a later request stayed blocked after the abandon: %v", err)
			}
		})
	}
}

// A call whose effect was reserved and may have run is not settled from the checkpoint: the channel failure
// stays untyped, cleanup stays quarantined and abandon refuses.
func TestHeldCallWithAReservationStaysUnsettled(t *testing.T) {
	f, runner, channelsErr := commandCellFailure(t, errors.New("D09 command exit proof is missing or disagrees with container state"))
	channelsErr = f.authority.heldCallChannelsError(channelsErr)
	if _, typed := channelsErr.(effectNeverRanFailure); typed {
		t.Fatalf("a call with an effect reservation was typed as never run: %v", channelsErr)
	}
	h := newUnknownRequestHarness(t, f)
	if h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), runner) {
		t.Fatal("cleanup released the owners after a command that may have run")
	}
	if err := h.abandon("reserved call; operator recovery"); !errors.Is(err, ErrPiServiceUnreconciled) ||
		!strings.Contains(err.Error(), "pending-call-disposition") {
		t.Fatalf("abandon settled a call whose effect was reserved: %v", err)
	}
}
