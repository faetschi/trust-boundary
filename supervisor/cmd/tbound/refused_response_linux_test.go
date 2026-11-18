//go:build linux

package main

import (
	"errors"
	"net"
	"testing"
	"time"

	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

// SB-05 on the real workflow ledger: the provider's second turn reuses the tool-call ID of the first read. The
// bridge refuses that response before it journals a normal exchange and closes its host call as unknown, so
// Serve types the failure, cleanup runs and abandon settles the request with the bounded provider liability.
// Before, the response was journaled as a normal exchange, the correlator then refused the call, and the host
// call stayed reserved with no disposition: abandon refused and the guest stayed wedged, as in F26.
func TestReusedToolCallIDEndsUnknownAndAbandonSettles(t *testing.T) {
	const reason = "provider reused a tool-call id; operator recovery"
	reused := capRead(2)
	reused.CallID = capRead(1).CallID
	f := hostCallCapWorkflow(t, []providerbridge.DevelopmentProviderTurn{capRead(1), reused}, 1)

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go func() {
		// Drain the bridge's response frame, so a write never blocks Serve.
		buffer := make([]byte, 4096)
		for {
			if _, err := clientConn.Read(buffer); err != nil {
				return
			}
		}
	}()
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- providerbridge.Serve(f.ctx, serverConn, f.conversation) }()
	writeProviderNextFrame(t, clientConn, 1, "reused-call-next")
	var bridgeErr error
	select {
	case bridgeErr = <-bridgeDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the bridge did not stop after the refused response")
	}
	if !errors.Is(bridgeErr, providerbridge.ErrProviderTurnFailed) {
		t.Fatalf("the refused response is not a typed provider turn failure: %v", bridgeErr)
	}

	checkpoint, _ := f.ledger.Snapshot()
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if call := checkpoint.Pins.HostCallReservation; call == nil || call.Disposition != workflowrecovery.HostCallReserved ||
		!unknownExchangeClosesHostCall(checkpoint, trace, workflowVerticalID, workflowVerticalConversation) {
		t.Fatalf("the refused response's host call is not closed as unknown: %+v, last record %s",
			checkpoint.Pins.HostCallReservation, trace.Records[len(trace.Records)-1].Event.Kind)
	}

	channelsErr := f.authority.heldCallChannelsError(governedChannelsError(nil, bridgeErr))
	h := newUnknownRequestHarness(t, f)
	if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), newProductionCommandRunner(nil)) {
		t.Fatalf("cleanup stayed quarantined after the refused response: %v", channelsErr)
	}
	abandoned := h.abandoned(t, reason)
	if abandoned.Reconciliation.ProviderSettlement != providerSettlementBoundedRetainedLiability ||
		abandoned.Reconciliation.WorkflowID != workflowVerticalID {
		t.Fatalf("the abandon record does not name the bounded provider liability: %+v", abandoned.Reconciliation)
	}
}
