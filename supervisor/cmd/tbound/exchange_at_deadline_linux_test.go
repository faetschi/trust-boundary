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

// deadlineTransport is a provider that answers only after the request context has ended: it holds the
// exchange until done is closed, like an HTTP transport that aborts on the request deadline, and then
// waits a little longer, so the bridge sees the deadline before the exchange returns.
type deadlineTransport struct{ done <-chan struct{} }

func (s deadlineTransport) Next([]providerbridge.ScriptMessage) (providerbridge.ScriptTurn, error) {
	<-s.done
	time.Sleep(300 * time.Millisecond)
	return providerbridge.ScriptTurn{}, context.DeadlineExceeded
}

// The request deadline (productionSessionMaxRuntime, 1,200 s) lands while a provider exchange is in
// flight. providerbridge.Serve stops on the deadline before the exchange returns, but it does not return
// before the exchange is journaled: NextTurn holds the conversation mutex for the whole exchange
// (conversation.go NextTurn) and journals the canceled exchange as provider_exchange_unknown under it,
// and Serve's deferred conversation.Close takes the same mutex (channel.go Serve). So when Serve returns,
// the reserved host call is already closed as unknown, cleanup runs, and abandon settles the request. A
// scripted conversation books no USD spend, so the research route's ledger is not exercised here.
func TestProviderExchangeAtTheRequestDeadlineIsJournaledBeforeServeReturns(t *testing.T) {
	const reason = "request deadline during a provider exchange; operator recovery"
	f := prepareWorkflowVertical(t, workflowVerticalOptions{launchProof: true})
	accepted := time.Now().Add(10*time.Second - productionSessionMaxRuntime)
	requestCtx, cancel := context.WithDeadline(f.ctx, accepted.Add(productionSessionMaxRuntime))
	defer cancel()
	f.startConversation(t, func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		return providerbridge.NewScriptedConversation(config, journal, deadlineTransport{done: requestCtx.Done()})
	})

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go func() {
		// Drain the bridge's response frame, if any, so a write never blocks Serve.
		buffer := make([]byte, 4096)
		for {
			if _, err := clientConn.Read(buffer); err != nil {
				return
			}
		}
	}()
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- providerbridge.Serve(requestCtx, serverConn, f.conversation) }()
	writeProviderNextFrame(t, clientConn, 1, "deadline-next")
	var bridgeErr error
	select {
	case bridgeErr = <-bridgeDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the bridge did not stop at the request deadline")
	}
	if !errors.Is(requestCtx.Err(), context.DeadlineExceeded) || bridgeErr != nil {
		t.Fatalf("the bridge did not stop on the request deadline (it returns nil there): %v", bridgeErr)
	}

	// Read the journal the moment Serve returned: the exchange is already closed as unknown.
	checkpoint, _ := f.ledger.Snapshot()
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if call := checkpoint.Pins.HostCallReservation; call == nil || call.Disposition != workflowrecovery.HostCallReserved ||
		!unknownExchangeClosesHostCall(checkpoint, trace, workflowVerticalID, workflowVerticalConversation) {
		kinds := make([]string, 0, len(trace.Records))
		for _, record := range trace.Records {
			kinds = append(kinds, record.Event.Kind)
		}
		t.Fatalf("Serve returned before the exchange was closed as unknown: host call %+v, journal %s",
			checkpoint.Pins.HostCallReservation, strings.Join(kinds, ","))
	}

	channelsErr := f.authority.heldCallChannelsError(governedChannelsError(nil, bridgeErr))
	h := newUnknownRequestHarness(t, f)
	if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), newProductionCommandRunner(nil)) {
		t.Fatalf("cleanup stayed quarantined after an exchange the deadline ended: %v", channelsErr)
	}
	abandoned := h.abandoned(t, reason)
	if abandoned.Reconciliation.ProviderSettlement != providerSettlementBoundedRetainedLiability ||
		abandoned.Reconciliation.WorkflowID != workflowVerticalID {
		t.Fatalf("the abandon record does not name the bounded provider liability: %+v", abandoned.Reconciliation)
	}
}

// The request context ends after the provider exchange is journaled and before its host call is settled: the
// test wraps the workflow's CompleteHostCall to end the context first, which stands for a deadline landing in
// that window. The journaled exchange's host call must still be settled, so the turn ends with its tool call
// captured and never reserved, cleanup runs and abandon settles. Before, CompleteHostCall failed on the ended
// context and left the host call reserved behind a normal provider_exchange, which abandon refuses.
func TestProviderExchangeJournaledAtTheRequestDeadlineSettlesItsHostCall(t *testing.T) {
	const reason = "request deadline after a journaled provider exchange; operator recovery"
	f := prepareWorkflowVertical(t, workflowVerticalOptions{launchProof: true})
	requestCtx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.startConversation(t, func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		complete := config.CompleteHostCall
		config.CompleteHostCall = func(ctx context.Context, completion providerbridge.HostCallCompletion) error {
			cancel()
			return complete(ctx, completion)
		}
		return providerbridge.NewDevelopmentOfflineConversation(config, journal, []providerbridge.DevelopmentProviderTurn{heldCallRead})
	})

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
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
	writeProviderNextFrame(t, clientConn, 1, "journaled-next")
	var bridgeErr error
	select {
	case bridgeErr = <-bridgeDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the bridge did not stop when the request context ended")
	}

	checkpoint, _ := f.ledger.Snapshot()
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if last := trace.Records[len(trace.Records)-1].Event.Kind; last != "provider_exchange" {
		t.Fatalf("the journal does not end at the journaled exchange: %s", last)
	}
	if call := checkpoint.Pins.HostCallReservation; call == nil || call.Disposition != workflowrecovery.HostCallTerminal ||
		!capturedCallNeverReserved(checkpoint.Pins, workflowVerticalID, workflowVerticalConversation) {
		t.Fatalf("the journaled exchange's host call was not settled: %+v", checkpoint.Pins.HostCallReservation)
	}

	channelsErr := f.authority.heldCallChannelsError(governedChannelsError(nil, bridgeErr))
	h := newUnknownRequestHarness(t, f)
	if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), newProductionCommandRunner(nil)) {
		t.Fatalf("cleanup stayed quarantined: %v", channelsErr)
	}
	abandoned := h.abandoned(t, reason)
	if abandoned.Reconciliation.ProviderSettlement != providerSettlementTerminal ||
		abandoned.Reconciliation.WorkflowID != workflowVerticalID {
		t.Fatalf("the abandon record does not settle the workflow: %+v", abandoned.Reconciliation)
	}
}
