//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

// g1UnknownStream is the F3 G1 first-turn response body with its [DONE] cut off: a 200 stream that
// the broker records as provider_exchange_unknown.
func g1UnknownStream(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("../../internal/broker/openrouter/testdata/opencode-go-g1-turn1.sse")
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.TrimSuffix(body, []byte("data: [DONE]\n\n"))
	if len(cut) == len(body) {
		t.Fatal("the G1 fixture no longer ends with [DONE]")
	}
	return string(cut)
}

func settledWorkerLifecycle(settlement piruntime.Settlement, joined bool, channelsErr error) *productionLifecycleEvidence {
	lifecycle := &productionLifecycleEvidence{}
	lifecycle.markWorkerLaunchAttempted()
	lifecycle.markWorkerLaunched(4242)
	lifecycle.markTaskAdmitted()
	lifecycle.markWorkerClosed(settlement, nil)
	lifecycle.markChannelsStarted()
	if joined {
		lifecycle.markChannelsJoined(channelsErr)
	}
	return lifecycle
}

var g1SettledWorker = piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true}

// F3 G1: the provider turn ended in an unknown exchange, the bridge closed the conversation, and the
// governed loops joined with that error, so cleanup quarantined every owner closer. That typed
// failure alone, with the supervisor loop clean, the worker settled and no command pending, leaves
// cleanup safe. Any other channel failure still quarantines.
func TestProviderTurnFailureAloneLeavesCleanupSafe(t *testing.T) {
	turnFailed := fmt.Errorf("%w; conversation closed: %w", providerbridge.ErrProviderTurnFailed,
		errors.New("registered provider exchange failed; response withheld"))
	pending := newProductionCommandRunner(nil)
	pending.leases = map[string]productionCommandLease{"lease-open": {}}
	for name, tc := range map[string]struct {
		lifecycle *productionLifecycleEvidence
		runner    *productionCommandRunner
		safe      bool
	}{
		"provider turn failure only": {settledWorkerLifecycle(g1SettledWorker, true, governedChannelsError(nil, turnFailed)), nil, true},
		"clean channels":             {settledWorkerLifecycle(g1SettledWorker, true, nil), nil, true},
		"supervisor loop failed too": {settledWorkerLifecycle(g1SettledWorker, true,
			governedChannelsError(errors.New("executor failed; result withheld"), turnFailed)), nil, false},
		"untyped bridge failure": {settledWorkerLifecycle(g1SettledWorker, true,
			governedChannelsError(nil, errors.New("provider channel request is replayed or out of order"))), nil, false},
		"worker settlement unknown": {settledWorkerLifecycle(piruntime.Settlement{State: piruntime.SettlementUnknown}, true,
			governedChannelsError(nil, turnFailed)), nil, false},
		"command pending":       {settledWorkerLifecycle(g1SettledWorker, true, governedChannelsError(nil, turnFailed)), pending, false},
		"channels never joined": {settledWorkerLifecycle(g1SettledWorker, false, nil), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			runner := tc.runner
			if runner == nil {
				runner = newProductionCommandRunner(nil)
			}
			if got := productionCleanupSafe(tc.lifecycle.snapshot(), runner); got != tc.safe {
				t.Fatalf("cleanup safe = %t, want %t", got, tc.safe)
			}
		})
	}
}

const (
	g1HostCallID      = "host-provider-call-a50ed162ff4c154d688b5037df854edc09ca18d8bdf4bca7"
	g1RequestDigest   = "d6151b610d50501e73ebb072712dfa9bfe6a078d159dfed498617a239b5e5c2e"
	g1TokenUpperBound = 1024
)

func g1HostCallEvent(kind, member, conversationID, callID, requestDigest string, tokens uint64) audit.Event {
	data, err := json.Marshal(map[string]any{"workflow_id": reconcileTestWorkflow, "conversation_id": conversationID,
		member: map[string]any{"call_id": callID, "request_digest": requestDigest, "token_upper_bound": tokens}})
	if err != nil {
		panic(err)
	}
	return audit.Event{Kind: kind, ID: conversationID + ":" + kind, Data: data}
}

func g1Reserved() audit.Event {
	return g1HostCallEvent("provider_host_call_reserved", "binding", "conversation-1", g1HostCallID, g1RequestDigest, g1TokenUpperBound)
}

func g1Unknown() audit.Event {
	return g1HostCallEvent("provider_exchange_unknown", "host_call", "conversation-1", g1HostCallID, g1RequestDigest, g1TokenUpperBound)
}

// On the abandon path a reserved host call is disposed only when the provider journal of the
// checkpoint's latest conversation closes exactly that binding with provider_exchange_unknown and the
// reservation already booked its host call and token upper bound. Every other open call is refused.
func TestReconcileOwnerAcceptsOnlyAReservedHostCallClosedAsUnknown(t *testing.T) {
	reservedPins := func(f *ownerFixture) {
		f.view.Checkpoint.Pins.HostCallReservation = &workflowrecovery.HostCallReservationPin{
			Binding:     workflowrecovery.HostCallBinding{CallID: g1HostCallID, RequestDigest: g1RequestDigest, TokenUpperBound: g1TokenUpperBound},
			Disposition: workflowrecovery.HostCallReserved}
		f.view.Checkpoint.Counters.HostCalls, f.view.Checkpoint.Counters.Tokens = 1, g1TokenUpperBound
	}
	laterTurn := func(f *ownerFixture) {
		f.view.Checkpoint.Pins.ProviderDisposition = workflowrecovery.DispositionAwaitingProviderExchange
		f.view.Checkpoint.Pins.PendingCallDisposition = workflowrecovery.PendingCallTerminal
		f.view.Checkpoint.Pins.ProviderTranscripts = []workflowrecovery.TranscriptPin{{Ordinal: 1, ConversationID: "conversation-1",
			Disposition: workflowrecovery.DispositionAwaitingProviderExchange}}
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
	admitted := audit.Event{Kind: "provider_user_admitted", ID: "conversation-1:user:1", Data: []byte(`{}`)}
	for name, tc := range map[string]struct {
		mutate     []func(*ownerFixture)
		reconciled bool
	}{
		"first turn closed as unknown":    {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1Unknown())}, true},
		"later turn closed as unknown":    {[]func(*ownerFixture){reservedPins, laterTurn, journal(admitted, g1Reserved(), g1Unknown())}, true},
		"journal ends at the reservation": {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved())}, false},
		"no provider journal reader":      {[]func(*ownerFixture){reservedPins}, false},
		"provider journal unreadable": {[]func(*ownerFixture){reservedPins, func(f *ownerFixture) {
			f.providerTrace = func(string) (audit.Trace, error) { return audit.Trace{}, errors.New("unreadable") }
		}}, false},
		"closed by a completed exchange":    {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1HostCallEvent("provider_exchange", "host_call", "conversation-1", g1HostCallID, g1RequestDigest, g1TokenUpperBound))}, false},
		"a record after the unknown":        {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1Unknown(), g1Reserved())}, false},
		"unknown closes another call":       {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1HostCallEvent("provider_exchange_unknown", "host_call", "conversation-1", g1HostCallID[:len(g1HostCallID)-1]+"8", g1RequestDigest, g1TokenUpperBound))}, false},
		"unknown names another request":     {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1HostCallEvent("provider_exchange_unknown", "host_call", "conversation-1", g1HostCallID, strings.Repeat("0", 64), g1TokenUpperBound))}, false},
		"unknown names another token bound": {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1HostCallEvent("provider_exchange_unknown", "host_call", "conversation-1", g1HostCallID, g1RequestDigest, 2048))}, false},
		"reservation of another call":       {[]func(*ownerFixture){reservedPins, journal(admitted, g1HostCallEvent("provider_host_call_reserved", "binding", "conversation-1", "host-provider-call-other", g1RequestDigest, g1TokenUpperBound), g1Unknown())}, false},
		"reservation in another conversation": {[]func(*ownerFixture){reservedPins, journal(admitted,
			g1HostCallEvent("provider_host_call_reserved", "binding", "conversation-2", g1HostCallID, g1RequestDigest, g1TokenUpperBound), g1Unknown())}, false},
		"cost not booked": {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1Unknown()),
			func(f *ownerFixture) { f.view.Checkpoint.Counters.Tokens = 0 }}, false},
		"host call already unknown": {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1Unknown()),
			func(f *ownerFixture) {
				f.view.Checkpoint.Pins.HostCallReservation.Disposition = workflowrecovery.HostCallUnknown
			}}, false},
		"captured call on the reservation": {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1Unknown()),
			func(f *ownerFixture) {
				f.view.Checkpoint.Pins.HostCallReservation.CapturedCall = &workflowrecovery.CapturedCallPin{ToolCallID: "call-1"}
			}}, false},
		"exchange awaited without a closed call": {[]func(*ownerFixture){laterTurn, journal(admitted, g1Reserved(), g1Unknown())}, false},
		"closed call but a pending effect": {[]func(*ownerFixture){reservedPins, journal(admitted, g1Reserved(), g1Unknown()),
			func(f *ownerFixture) {
				f.view.Checkpoint.Pins.EffectReservation = &workflowrecovery.EffectReservationPin{Disposition: workflowrecovery.EffectReservationReserved}
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
				t.Fatalf("a reserved host call closed as unknown was refused: %v", err)
			}
			if !tc.reconciled && !errors.Is(err, ErrPiServiceUnreconciled) {
				t.Fatalf("an open host call was reconciled: %v", err)
			}
		})
	}
}

func writeProviderNextFrame(t *testing.T, writer io.Writer, sequence int, requestID string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": "tbound-provider-bridge/v1", "sequence": sequence, "kind": "next", "request_id": requestID})
	if err != nil {
		t.Fatal(err)
	}
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	if _, err := writer.Write(append(frame, body...)); err != nil {
		t.Fatal(err)
	}
}

func readProviderFrame(t *testing.T, reader io.Reader) map[string]any {
	t.Helper()
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint32(prefix[:]))
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if err := json.Unmarshal(body, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

// executeOneRead runs one provider turn that proposes a read through correlation, the gate, the
// durable decision and the Store effect, as the governed loops do.
func executeOneRead(t *testing.T, f *workflowVerticalFixture) {
	t.Helper()
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("first provider turn: turn=%+v err=%v", turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
		Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
	matched, err := f.conversation.Correlate(f.ctx, proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate the first read: %+v %v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, f.policy)
	if err := f.conversation.RecordDecision(f.ctx, proposal, decision); err != nil {
		t.Fatal(err)
	}
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(f.ctx, proposal, decision); err != nil {
		t.Fatalf("execute the first read: %v", err)
	}
}

// The F3 G1 failure end to end over the real pieces: a workflow ledger and provider conversation
// whose turn ends in an unknown exchange (the G1 stream cut before [DONE]), served over the real
// bridge channel; the governed-loop classification and the cleanup guard; the owner-scope drain
// receipt and the managed-root run release that the closers write; and abandon-unknown with the
// production reconciler over the resulting catalog, publication journal, owner checkpoint and
// provider journal. Before the cleanup ran, the same abandon refuses, as it did on F3.
func TestG1UnknownExchangeFreesCleanupAndAbandonSettles(t *testing.T) {
	for name, laterTurn := range map[string]bool{"first turn": false, "turn after a tool result": true} {
		t.Run(name, func(t *testing.T) { runG1UnknownExchange(t, laterTurn) })
	}
}

func runG1UnknownExchange(t *testing.T, laterTurn bool) {
	unknown := providerbridge.DevelopmentProviderTurn{FailStatus: http.StatusOK, FailBody: g1UnknownStream(t)}
	turns := []providerbridge.DevelopmentProviderTurn{unknown}
	if laterTurn {
		turns = []providerbridge.DevelopmentProviderTurn{{ResponseID: "g1-response-1", CallID: "g1-tool-call-1", Tool: "read",
			Arguments: json.RawMessage(`{"path":"task.txt"}`)}, unknown}
	}
	f := newWorkflowVerticalFixture(t, true, 0, turns)
	if laterTurn {
		executeOneRead(t, f)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- providerbridge.Serve(f.ctx, serverConn, f.conversation) }()
	writeProviderNextFrame(t, clientConn, 1, "g1-next")
	if frame := readProviderFrame(t, clientConn); frame["kind"] != "error" {
		t.Fatalf("the unknown exchange released a turn: %v", frame)
	}
	var bridgeErr error
	select {
	case bridgeErr = <-bridgeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge did not close after the failed turn")
	}
	channelsErr := governedChannelsError(nil, bridgeErr)
	checkpoint, checkpointDigest := f.ledger.Snapshot()
	if call := checkpoint.Pins.HostCallReservation; call == nil || call.Disposition != workflowrecovery.HostCallReserved {
		t.Fatalf("the failed turn did not leave its host call reserved: %+v", checkpoint.Pins.HostCallReservation)
	}

	hostRoot := privateGovernedTestRoot(t)
	cgroupRoot, auditRoot, livePath := filepath.Join(hostRoot, "cells"), filepath.Join(hostRoot, "audit"), filepath.Join(hostRoot, "live")
	for _, path := range []string{cgroupRoot, auditRoot, livePath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	live, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	publicationPath := filepath.Join(auditRoot, "publication-audit.jsonl")
	publicationJournal, err := audit.Open(publicationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer publicationJournal.Close()
	lease, err := newFakeManagedLiveRootAuthority().acquire(f.ctx, live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest}, publicationJournal,
		"g1-run", workflowVerticalID, "g1-publication", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.completeAdmissionRecovery(f.ctx); err != nil {
		t.Fatal(err)
	}
	lease.markTaskAdmitted()

	requests := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer requests.close(t)
	if err := requests.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	request, payloadDigest := incrementalRequest(1)
	state, exists, err := requests.requests.lookup(incrementalPeer(), request, payloadDigest)
	if err != nil || !exists {
		t.Fatalf("the unknown G1 request is not journaled: %v", err)
	}

	incarnation := workflowrecovery.OwnerIncarnation{Digest: strings.Repeat("b", 64), BootID: "different-boot", PID: 424242,
		StartTimeTicks: 1, PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64)}
	scopeName := workflowOwnerScopeName(workflowVerticalID, 1)
	scopePath := filepath.Join(cgroupRoot, scopeName)
	if err := os.Mkdir(scopePath, 0o700); err != nil {
		t.Fatal(err)
	}
	catalogJournal, err := audit.Open(filepath.Join(hostRoot, "catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalogJournal.Close()
	entry := piServiceWorkflowEntry{AdmissionRecordHash: state.acceptedRecord.Hash, WorkflowID: workflowVerticalID,
		SessionID: workflowVerticalSessionID, RunID: "g1-run", ConversationID: workflowVerticalConversation,
		ProviderJournalPath: f.providerJournalPath, OwnerDBPath: f.ownerStoreIdentity.Path, OwnerStoreIdentity: f.ownerStoreIdentity,
		FreshAuthorizationUsed: true, CheckpointBound: true, RootIdentityDigest: checkpoint.Pins.RootIdentityDigest,
		Limits: checkpoint.Limits}
	catalog := &piServiceWorkflowCatalog{journal: catalogJournal, entries: map[string]piServiceWorkflowEntry{entry.AdmissionRecordHash: entry},
		byHostAdmissionID: map[string]string{}, ownerScopes: map[string]piServiceWorkflowOwnerScope{
			workflowOwnerEpochKey(workflowVerticalID, 1): {AdmissionHash: entry.AdmissionRecordHash, WorkflowID: workflowVerticalID,
				OwnerEpoch: 1, Incarnation: incarnation, ScopeName: scopeName, ScopePath: scopePath,
				ScopeIdentityDigest: "sha256:" + strings.Repeat("f", 64)}}}

	view := workflowrecovery.SettledCheckpointView{Checkpoint: checkpoint, Digest: checkpointDigest, OwnerStoreIdentity: f.ownerStoreIdentity,
		OwnerEpochs: []workflowrecovery.OwnerEpochIdentity{{Epoch: 1, Incarnation: incarnation}}}
	verifier := catalogOwnerDrainVerifier{catalog: catalog, cgroupRoot: cgroupRoot, cgroupRootDigest: "sha256:" + strings.Repeat("a", 64),
		containment: piruntime.D09ContainmentProfile, settlement: piruntime.D09SettlementProfile,
		openScope: func(*os.File, string, string, string, string) (ownerScopeDrainer, error) {
			return nil, errors.New("the owner scope is still held by the quarantined session")
		}}
	journals := map[string]*audit.Journal{f.providerJournalPath: f.providerJournal, publicationPath: publicationJournal}
	readJournal := func(path string) (audit.Trace, bool, error) {
		journal, ok := journals[path]
		if !ok {
			return audit.Trace{}, false, nil
		}
		trace, err := journal.Trace()
		return trace, true, err
	}
	reconciler := &piServiceUnknownReconciler{catalog: catalog, route: piruntime.D09ManagedPiProviderRouteEngineering,
		auditRoot: auditRoot, readJournal: readJournal,
		owner: func(ctx context.Context, entry piServiceWorkflowEntry) (piServiceOwnerReconciliation, error) {
			return catalog.reconcileSettledOwner(ctx, entry, view, verifier, cgroupRoot, func(conversationID string) (audit.Trace, error) {
				trace, _, err := readJournal(providerJournalPathFor(auditRoot, entry, conversationID))
				return trace, err
			})
		}}
	requests.requests.setReconciler(reconciler)

	const reason = "G1 provider exchange unknown; operator recovery"
	if _, err := requests.requests.abandonUnknown(f.ctx, request.RequestID, reason); !errors.Is(err, ErrPiServiceUnreconciled) ||
		!strings.Contains(err.Error(), "managed-root run") || !strings.Contains(err.Error(), "owner scope") {
		t.Fatalf("abandon before cleanup was not refused for the unreleased run and the undrained scope: %v", err)
	}

	lifecycle := settledWorkerLifecycle(g1SettledWorker, true, channelsErr)
	runner := newProductionCommandRunner(nil)
	removeScope := func() error {
		if err := os.Remove(scopePath); err != nil {
			return err
		}
		return catalog.MarkOwnerScopeDrained(entry.AdmissionRecordHash, workflowVerticalID, 1, incarnation, cgroupRoot, "session_cleanup_complete")
	}
	closers := []func() error{lease.close, removeScope}
	var quarantine productionCleanupQuarantineState
	if err := productionSessionCloseIfUnsafe(lifecycle.snapshot(), runner, nil); err != nil {
		t.Fatalf("the session close guard kept the owners after the provider turn failure: %v", err)
	}
	if !lease.canRelease() {
		t.Fatal("the managed-root lease cannot be released after the provider turn failure")
	}
	if retainProductionCleanupIfUnsafe(&quarantine, lifecycle.snapshot(), runner, closers) {
		t.Fatal("the binder quarantined the owner closers after the provider turn failure")
	}
	for index := len(closers) - 1; index >= 0; index-- {
		if err := closers[index](); err != nil {
			t.Fatalf("owner closer %d: %v", index, err)
		}
	}
	if kinds := journalKinds(t, catalogJournal); len(kinds) != 1 || kinds[0] != "pi_workflow_owner_scope_drained" {
		t.Fatalf("catalog records after cleanup: %v", kinds)
	}
	if kinds := journalKinds(t, publicationJournal); kinds[len(kinds)-1] != "managed_live_root_run_released" {
		t.Fatalf("publication records after cleanup: %v", kinds)
	}

	record, err := requests.requests.abandonUnknown(f.ctx, request.RequestID, reason)
	if err != nil {
		t.Fatalf("abandon after cleanup: %v", err)
	}
	var abandoned piServiceRequestAbandonedEvent
	if err := json.Unmarshal(record.Event.Data, &abandoned); err != nil {
		t.Fatal(err)
	}
	if abandoned.Reconciliation.ProviderSettlement != providerSettlementBoundedRetainedLiability ||
		abandoned.Reconciliation.WorkflowID != workflowVerticalID {
		t.Fatalf("the abandon record does not name the bounded provider liability: %+v", abandoned.Reconciliation)
	}
	if err := requests.admit(t, 2, true); err != nil {
		t.Fatalf("a later request stayed blocked after the abandon: %v", err)
	}
}
