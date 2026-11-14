//go:build linux

package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

// serveUntilStopped runs the real provider bridge over a pipe under requestCtx, asks for one turn, and
// returns the bridge's error once it stops.
func serveUntilStopped(t *testing.T, f *workflowVerticalFixture, requestCtx context.Context) error {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	go func() {
		buffer := make([]byte, 4096)
		for {
			if _, err := clientConn.Read(buffer); err != nil {
				return
			}
		}
	}()
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- providerbridge.Serve(requestCtx, serverConn, f.conversation) }()
	writeProviderNextFrame(t, clientConn, 1, "retry-next")
	select {
	case err := <-bridgeDone:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("the bridge did not stop when the request context ended")
	}
	return nil
}

// requireAbandonSettles runs cleanup over the joined channel error and then abandon, which must settle the
// request with a terminal provider settlement and free the barrier for a later request.
func requireAbandonSettles(t *testing.T, f *workflowVerticalFixture, channelsErr error, reason string) {
	t.Helper()
	h := newUnknownRequestHarness(t, f)
	if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), newProductionCommandRunner(nil)) {
		t.Fatalf("cleanup stayed quarantined: %v", channelsErr)
	}
	abandoned := h.abandoned(t, reason)
	if abandoned.Reconciliation.ProviderSettlement != providerSettlementTerminal ||
		abandoned.Reconciliation.WorkflowID != workflowVerticalID {
		t.Fatalf("the abandon record does not settle the workflow: %+v", abandoned.Reconciliation)
	}
	if err := h.requests.admit(t, 2, true); err != nil {
		t.Fatalf("a later request stayed blocked after the abandon: %v", err)
	}
}

// requireIdleBetweenTurns checks that the checkpoint awaits the next provider exchange with none reserved.
func requireIdleBetweenTurns(t *testing.T, f *workflowVerticalFixture, hostCalls uint64) {
	t.Helper()
	checkpoint, _ := f.ledger.Snapshot()
	call := checkpoint.Pins.HostCallReservation
	if checkpoint.Pins.ProviderDisposition != workflowrecovery.DispositionAwaitingProviderExchange || call == nil ||
		call.Disposition != workflowrecovery.HostCallTerminal || call.CapturedCall != nil || checkpoint.Counters.HostCalls != hostCalls {
		t.Fatalf("checkpoint is not idle between turns: pins=%+v counters=%+v", checkpoint.Pins, checkpoint.Counters)
	}
}

// The request context ends after a retryable provider failure is journaled and before its host call is
// settled (retry.go recordRetryableFailureLocked): the test wraps the workflow's CompleteHostCall to end the
// context first. The failed attempt's host call is still settled, no further attempt is reserved, and the
// conversation ends idle between turns, which cleanup and abandon settle. Before, CompleteHostCall failed on
// the ended context and left the attempt's host call reserved behind its retryable-failure record.
func TestRetryableAttemptAtTheRequestDeadlineEndsSettleable(t *testing.T) {
	f := prepareWorkflowVertical(t, workflowVerticalOptions{launchProof: true, retries: 1})
	requestCtx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.startConversation(t, func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		complete := config.CompleteHostCall
		config.CompleteHostCall = func(ctx context.Context, completion providerbridge.HostCallCompletion) error {
			cancel()
			return complete(ctx, completion)
		}
		return providerbridge.NewDevelopmentOfflineConversation(config, journal,
			[]providerbridge.DevelopmentProviderTurn{capRetryable, heldCallRead})
	})
	bridgeErr := serveUntilStopped(t, f, requestCtx)
	if bridgeErr != nil && !errors.Is(bridgeErr, providerbridge.ErrProviderTurnFailed) {
		t.Fatalf("the bridge ended without a journaled disposition: %v", bridgeErr)
	}
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if last := trace.Records[len(trace.Records)-1].Event.Kind; last != "provider_exchange_retryable_failure" {
		t.Fatalf("the journal does not end at the retryable failure: %s", last)
	}
	requireIdleBetweenTurns(t, f, 1)
	requireAbandonSettles(t, f, governedChannelsError(nil, bridgeErr), "retryable attempt at the request deadline; operator recovery")
}

// The request stops between turns: a read's result was released, and the request context ends before Pi asks
// for the next provider turn. Nothing is reserved or open, so the request is settleable. Before, abandon
// refused the awaited provider exchange although none was reserved.
func TestRequestStoppedBetweenTurnsEndsSettleable(t *testing.T) {
	f := newWorkflowVerticalFixture(t, true, 0, []providerbridge.DevelopmentProviderTurn{heldCallRead})
	executeOneRead(t, f)
	requireIdleBetweenTurns(t, f, 2)
	// Both governed loops return nil once the request context ends.
	requireAbandonSettles(t, f, governedChannelsError(nil, nil), "request stopped between turns; operator recovery")
}

// On the abandon path, a conversation that awaits its next provider exchange counts as quiescent only when no
// host call is reserved and none holds a captured call. A reserved host call without its unknown record, a
// held captured call, an open effect or a pending call keeps it refused.
func TestReconcileOwnerAcceptsAnIdleAwaitedExchangeOnly(t *testing.T) {
	idle := func(f *ownerFixture) {
		pins := &f.view.Checkpoint.Pins
		pins.ProviderDisposition = workflowrecovery.DispositionAwaitingProviderExchange
		pins.PendingCallDisposition = workflowrecovery.PendingCallTerminal
		pins.ProviderTranscripts = []workflowrecovery.TranscriptPin{{Ordinal: 1, ConversationID: "conversation-1",
			Disposition: workflowrecovery.DispositionAwaitingProviderExchange}}
		pins.HostCallReservation = &workflowrecovery.HostCallReservationPin{
			Binding:     workflowrecovery.HostCallBinding{CallID: g1HostCallID, RequestDigest: g1RequestDigest, TokenUpperBound: g1TokenUpperBound},
			Disposition: workflowrecovery.HostCallTerminal, OutcomeDigest: strings.Repeat("7", 64)}
		f.view.Checkpoint.Counters.HostCalls, f.view.Checkpoint.Counters.Tokens = 2, g1TokenUpperBound
	}
	pinned := func(mutate func(*workflowrecovery.Pins)) func(*ownerFixture) {
		return func(f *ownerFixture) { mutate(&f.view.Checkpoint.Pins) }
	}
	for name, tc := range map[string]struct {
		mutate     []func(*ownerFixture)
		reconciled bool
	}{
		"idle after a settled host call": {[]func(*ownerFixture){idle}, true},
		"idle before any host call": {[]func(*ownerFixture){idle, pinned(func(pins *workflowrecovery.Pins) {
			pins.HostCallReservation = nil
		})}, true},
		"host call reserved without its unknown record": {[]func(*ownerFixture){idle, pinned(func(pins *workflowrecovery.Pins) {
			pins.HostCallReservation.Disposition = workflowrecovery.HostCallReserved
		})}, false},
		"host call holding a captured call": {[]func(*ownerFixture){idle, pinned(func(pins *workflowrecovery.Pins) {
			pins.HostCallReservation.CapturedCall = &workflowrecovery.CapturedCallPin{WorkflowID: reconcileTestWorkflow,
				ConversationID: "conversation-1", HostCallID: g1HostCallID, ToolCallID: "call-held", OutcomeDigest: strings.Repeat("7", 64),
				RecoveryGeneration: 1, OwnerEpoch: 1, Disposition: workflowrecovery.CapturedCallAvailable}
		})}, false},
		"captured call retired after a DENY": {[]func(*ownerFixture){idle, pinned(func(pins *workflowrecovery.Pins) {
			pins.PendingCallDisposition = workflowrecovery.PendingCallRetired
			pins.HostCallReservation.CapturedCall = &workflowrecovery.CapturedCallPin{WorkflowID: reconcileTestWorkflow,
				ConversationID: "conversation-1", HostCallID: g1HostCallID, ToolCallID: "call-denied", OutcomeDigest: strings.Repeat("7", 64),
				RecoveryGeneration: 1, OwnerEpoch: 1, Disposition: workflowrecovery.CapturedCallRetired, RetirementDigest: strings.Repeat("9", 64)}
		})}, true},
		"captured call retired, pending call not": {[]func(*ownerFixture){idle, pinned(func(pins *workflowrecovery.Pins) {
			pins.HostCallReservation.CapturedCall = &workflowrecovery.CapturedCallPin{WorkflowID: reconcileTestWorkflow,
				ConversationID: "conversation-1", HostCallID: g1HostCallID, ToolCallID: "call-denied", OutcomeDigest: strings.Repeat("7", 64),
				RecoveryGeneration: 1, OwnerEpoch: 1, Disposition: workflowrecovery.CapturedCallRetired, RetirementDigest: strings.Repeat("9", 64)}
		})}, false},
		"effect still reserved": {[]func(*ownerFixture){idle, pinned(func(pins *workflowrecovery.Pins) {
			pins.EffectReservation = &workflowrecovery.EffectReservationPin{Disposition: workflowrecovery.EffectReservationReserved,
				Binding: workflowrecovery.EffectBinding{OperationID: "sessionrepo-effect-1", SpecDigest: strings.Repeat("8", 64)}}
		})}, false},
		"pending call in flight": {[]func(*ownerFixture){idle, pinned(func(pins *workflowrecovery.Pins) {
			pins.PendingCallDisposition = workflowrecovery.PendingCallInFlight
		})}, false},
		"older transcript still awaiting": {[]func(*ownerFixture){idle, pinned(func(pins *workflowrecovery.Pins) {
			pins.ProviderTranscripts = append([]workflowrecovery.TranscriptPin{{Ordinal: 1, ConversationID: "conversation-0",
				Disposition: workflowrecovery.DispositionAwaitingProviderExchange}}, pins.ProviderTranscripts...)
			pins.ProviderTranscripts[1].Ordinal = 2
		})}, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerFixture(t, func(f *ownerFixture) {
				for _, mutate := range tc.mutate {
					mutate(f)
				}
			})
			_, err := f.reconcile(t)
			if tc.reconciled && err != nil {
				t.Fatalf("an idle awaited exchange was refused: %v", err)
			}
			if !tc.reconciled && !errors.Is(err, ErrPiServiceUnreconciled) {
				t.Fatalf("an open awaited exchange was reconciled: %v", err)
			}
		})
	}
}
