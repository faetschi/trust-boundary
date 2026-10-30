//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

// failingD09Invoker stands in for the D09 command runner of one lease: every Run fails with err and
// leaves no receipt.
type failingD09Invoker struct{ err error }

func (f failingD09Invoker) Run(context.Context, *sessionrepo.CommandView, sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, f.err
}

func (failingD09Invoker) TakeCommandReceipt(*sessionrepo.CommandView, sessionrepo.CommandSpec, sessionrepo.CommandResult,
	sessionrepo.CommandSettlement) (piruntime.D09CommandReceipt, error) {
	return piruntime.D09CommandReceipt{}, errors.New("a failed command has no receipt")
}

// F5 G1: the command cell's entrypoint refused the executable before the host acknowledged the start,
// so the command never ran, and the runner observed the cell gone (the D09 runner types that failure).
var entrypointFailedBeforeStart = errors.Join(protocol.ErrCommandNotStarted,
	errors.New("D09 command container did not start with the authorized identity"))

// commandCellFailure runs the F5 G1 shape over the real pieces: a read, then a Bash call whose command
// runner fails with runErr, through correlation, the gate, the durable decision, the Store effect and
// the provider result wrapper. The supervisor loop then fails with the executor error and closes IPC;
// Pi asks the bridge for the next turn, which the closed conversation refuses. It returns the governed
// loops' joined error and the command runner.
func commandCellFailure(t *testing.T, runErr error) (*workflowVerticalFixture, *productionCommandRunner, error) {
	t.Helper()
	commandJournal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "command-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commandJournal.Close() })
	runner := newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		return &productionD09BashRunner{leaseID: leaseID, inner: failingD09Invoker{err: runErr}, journal: commandJournal}, nil
	})
	f := newWorkflowVerticalFixture(t, true, 0, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "f5-response-1", CallID: "f5-tool-call-1", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "f5-response-2", CallID: "f5-tool-call-2", Tool: "bash",
			Arguments: json.RawMessage(`{"command":"sha256sum task.txt > ` + workflowVerticalBashOutput + `"}`)},
	}, runner)
	executeOneRead(t, f)

	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.ToolCall == nil || turn.ToolCall.Name != "bash" {
		t.Fatalf("Bash provider turn: turn=%+v err=%v", turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
		Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
	matched, err := f.conversation.Correlate(f.ctx, proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate the Bash call: %+v %v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, f.policy)
	if decision.Verdict != gate.Allow {
		t.Fatalf("the gate did not allow the Bash call: %+v", decision)
	}
	if err := f.conversation.RecordDecision(f.ctx, proposal, decision); err != nil {
		t.Fatal(err)
	}
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	output, execErr := executor.Execute(f.ctx, proposal, decision)
	if execErr == nil || len(output) != 0 {
		t.Fatalf("the failed command released a result: %s %v", output, execErr)
	}
	// As piruntime.Supervisor.Serve and providerbridge.Serve wrap their loop errors.
	serveErr := fmt.Errorf("executor failed; result withheld: %w", execErr)
	_, nextErr := f.conversation.NextTurn(f.ctx)
	bridgeErr := fmt.Errorf("provider turn failed without a journaled disposition; conversation closed: %w", nextErr)
	return f, runner, governedChannelsError(serveErr, bridgeErr)
}

// F5 G1 end to end: a Bash command cell that failed in its entrypoint before the command started
// leaves the request unknown. Abandon refuses while the owners are held. Cleanup then drains the owner
// scope and releases the managed-root run, abandon settles the request on the typed record of the
// call, and a later request is admitted. An untyped runner failure, where the command may have run,
// keeps cleanup quarantined and abandon refused.
func TestCommandCellFailureBeforeStartFreesCleanupAndAbandonSettles(t *testing.T) {
	const reason = "F5 G1 command cell failed before start; operator recovery"
	t.Run("entrypoint failed before start", func(t *testing.T) {
		f, runner, channelsErr := commandCellFailure(t, entrypointFailedBeforeStart)
		h := newUnknownRequestHarness(t, f)
		if err := h.abandon(reason); !errors.Is(err, ErrPiServiceUnreconciled) ||
			!strings.Contains(err.Error(), "managed-root run") || !strings.Contains(err.Error(), "owner scope") {
			t.Fatalf("abandon before cleanup was not refused for the unreleased run and the undrained scope: %v", err)
		}
		if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), runner) {
			t.Fatalf("cleanup stayed quarantined after a command cell that never started (pending=%d): %v", runner.Pending(), channelsErr)
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
	t.Run("command may have run", func(t *testing.T) {
		f, runner, channelsErr := commandCellFailure(t,
			errors.New("D09 command exit proof is missing or disagrees with container state"))
		h := newUnknownRequestHarness(t, f)
		if h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), runner) {
			t.Fatal("cleanup released the owners after a command that may have run")
		}
		if err := h.abandon(reason); !errors.Is(err, ErrPiServiceUnreconciled) || !strings.Contains(err.Error(), "pending-call-disposition") {
			t.Fatalf("abandon settled a command that may have run: %v", err)
		}
	})
}

const notStartedToolCall = "call-bash"

func notStartedResult(mutate func(map[string]any)) audit.Event {
	fields := map[string]any{"workflow_id": reconcileTestWorkflow, "conversation_id": "conversation-1",
		"response_id": "response-2", "tool_call_id": notStartedToolCall, "generation": "g1",
		"executor_output_digest": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"outcome":                "UNKNOWN", "failure": "trusted executor failed; provider result withheld", "command_not_started": true}
	if mutate != nil {
		mutate(fields)
	}
	data, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return audit.Event{Kind: "provider_tool_result_unknown", ID: "conversation-1:unknown-result", Data: data}
}

// On the abandon path the Bash call the checkpoint still holds (captured, its gate decision awaited,
// its effect reserved) counts as disposed only when the provider journal of that conversation ends
// with the call's UNKNOWN result typed as a command that never started, with no executor output.
// Anything else stays refused: an untyped or partial result, another call, a later record, or pins
// that do not hold exactly that one call.
func TestReconcileOwnerAcceptsOnlyACommandThatNeverStarted(t *testing.T) {
	heldCall := func(f *ownerFixture) {
		pins := &f.view.Checkpoint.Pins
		pins.ProviderDisposition = workflowrecovery.DispositionAwaitingGateDecision
		pins.PendingCallDisposition = workflowrecovery.PendingCallCaptured
		pins.ProviderTranscripts = []workflowrecovery.TranscriptPin{{Ordinal: 1, ConversationID: "conversation-1",
			Disposition: workflowrecovery.DispositionAwaitingGateDecision}}
		pins.HostCallReservation = &workflowrecovery.HostCallReservationPin{
			Binding:     workflowrecovery.HostCallBinding{CallID: g1HostCallID, RequestDigest: g1RequestDigest, TokenUpperBound: g1TokenUpperBound},
			Disposition: workflowrecovery.HostCallTerminal, OutcomeDigest: strings.Repeat("7", 64),
			CapturedCall: &workflowrecovery.CapturedCallPin{WorkflowID: reconcileTestWorkflow, ConversationID: "conversation-1",
				HostCallID: g1HostCallID, ToolCallID: notStartedToolCall, OutcomeDigest: strings.Repeat("7", 64),
				RecoveryGeneration: 1, OwnerEpoch: 1, Disposition: workflowrecovery.CapturedCallConsumed}}
		pins.EffectReservation = &workflowrecovery.EffectReservationPin{Disposition: workflowrecovery.EffectReservationReserved,
			Binding: workflowrecovery.EffectBinding{OperationID: "sessionrepo-effect-1", SpecDigest: strings.Repeat("8", 64),
				WorkflowID: reconcileTestWorkflow, ConversationID: "conversation-1", HostCallID: g1HostCallID,
				ToolCallID: notStartedToolCall, HostCallOutcomeDigest: strings.Repeat("7", 64), RecoveryGeneration: 1, OwnerEpoch: 1}}
		f.view.Checkpoint.Counters.HostCalls, f.view.Checkpoint.Counters.Tokens = 2, g1TokenUpperBound
	}
	journal := func(events ...audit.Event) func(*ownerFixture) {
		return func(f *ownerFixture) {
			trace := reconcileTestTrace(t, events...)
			f.providerTrace = func(conversationID string) (audit.Trace, error) {
				if conversationID != "conversation-1" {
					return audit.Trace{}, errors.New("no such conversation")
				}
				return trace, nil
			}
		}
	}
	decision := audit.Event{Kind: "provider_gate_decision", ID: "conversation-1:decision:1", Data: []byte(`{}`)}
	field := func(name string, value any) func(map[string]any) {
		return func(fields map[string]any) {
			if value == nil {
				delete(fields, name)
			} else {
				fields[name] = value
			}
		}
	}
	for name, tc := range map[string]struct {
		mutate     []func(*ownerFixture)
		reconciled bool
	}{
		"command never started":     {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(nil))}, true},
		"untyped unknown result":    {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(field("command_not_started", nil)))}, false},
		"command may have run":      {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(field("command_not_started", false)))}, false},
		"output partially released": {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(field("executor_output", []byte(`{"stdout":"partial"}`))))}, false},
		"output digest of other bytes": {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(field("executor_output_digest",
			"sha256:"+strings.Repeat("0", 64))))}, false},
		"result of another call":         {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(field("tool_call_id", "call-other")))}, false},
		"result in another workflow":     {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(field("workflow_id", "workflow-other")))}, false},
		"result in another conversation": {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(field("conversation_id", "conversation-2")))}, false},
		"result not UNKNOWN":             {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(field("outcome", "SUCCESS")))}, false},
		"a record after the result":      {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(nil), decision)}, false},
		"journal ends at the decision":   {[]func(*ownerFixture){heldCall, journal(decision)}, false},
		"no provider journal reader":     {[]func(*ownerFixture){heldCall}, false},
		"effect reserved for another call": {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(nil)), func(f *ownerFixture) {
			f.view.Checkpoint.Pins.EffectReservation.Binding.ToolCallID = "call-other"
		}}, false},
		"effect already unknown": {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(nil)), func(f *ownerFixture) {
			f.view.Checkpoint.Pins.EffectReservation.Disposition = workflowrecovery.EffectReservationUnknown
		}}, false},
		"captured call not consumed": {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(nil)), func(f *ownerFixture) {
			f.view.Checkpoint.Pins.HostCallReservation.CapturedCall.Disposition = workflowrecovery.CapturedCallAvailable
		}}, false},
		"host call still reserved": {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(nil)), func(f *ownerFixture) {
			f.view.Checkpoint.Pins.HostCallReservation.Disposition = workflowrecovery.HostCallReserved
		}}, false},
		"pending call in flight": {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(nil)), func(f *ownerFixture) {
			f.view.Checkpoint.Pins.PendingCallDisposition = workflowrecovery.PendingCallInFlight
		}}, false},
		"no effect reserved": {[]func(*ownerFixture){heldCall, journal(decision, notStartedResult(nil)), func(f *ownerFixture) {
			f.view.Checkpoint.Pins.EffectReservation = nil
		}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerFixture(t, func(f *ownerFixture) {
				for _, mutate := range tc.mutate {
					mutate(f)
				}
			})
			_, err := f.reconcile(t)
			if tc.reconciled && err != nil {
				t.Fatalf("a Bash call whose command never started was refused: %v", err)
			}
			if !tc.reconciled && !errors.Is(err, ErrPiServiceUnreconciled) {
				t.Fatalf("an open Bash call was reconciled: %v", err)
			}
		})
	}
}
