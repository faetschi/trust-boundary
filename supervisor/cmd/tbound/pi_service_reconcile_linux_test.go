//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/usdledger"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workflowrecovery"
)

const reconcileTestWorkflow = "workflow-reconcile-1"

func reconcileTestTrace(t *testing.T, events ...audit.Event) audit.Trace {
	t.Helper()
	journal, err := audit.Open(filepath.Join(incrementalTestRoot(t), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	for _, event := range events {
		if _, err := journal.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

var reconcileTestRoot = workflow.RootIdentity{Device: 7, Inode: 9}

func publicationEvent(kind, id string, fields map[string]any) audit.Event {
	data, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return audit.Event{Kind: kind, ID: id, Data: data}
}

func publicationPending(workflowID, publicationID string) audit.Event {
	return publicationEvent("workflow_publication_pending", managedWorkflowEventID(reconcileTestRoot, workflowID, publicationID),
		map[string]any{"root": reconcileTestRoot, "workflow_id": workflowID, "owner_epoch": 1, "publication_id": publicationID})
}

func publicationTransaction(workflowID, publicationID string) audit.Event {
	return publicationEvent("workflow_publication_transaction",
		managedWorkflowEventID(reconcileTestRoot, workflowID, publicationID)+"/transaction",
		map[string]any{"root": reconcileTestRoot, "workflow_id": workflowID, "publication_id": publicationID, "transaction_id": "tx-1"})
}

func publicationResolved(workflowID, publicationID string) audit.Event {
	return publicationEvent("workflow_publication_resolved", managedWorkflowEventID(reconcileTestRoot, workflowID, publicationID)+"/resolved",
		map[string]any{"root": reconcileTestRoot, "workflow_id": workflowID, "publication_id": publicationID,
			"transaction_id": "tx-1", "status": "succeeded"})
}

func runAdmitted(workflowID, runID string) audit.Event {
	return publicationEvent("managed_live_root_run_admitted",
		managedLiveRootRunEventID(reconcileTestRoot, "sha256:"+strings.Repeat("a", 64), runID, 1),
		map[string]any{"root": reconcileTestRoot, "registration_digest": "sha256:" + strings.Repeat("a", 64), "run_id": runID,
			"workflow_id": workflowID, "publication_id": "publication-" + workflowID, "owner_epoch": 1})
}

func runReleased(workflowID, runID string) audit.Event {
	return publicationEvent("managed_live_root_run_released",
		managedLiveRootRunEventID(reconcileTestRoot, "sha256:"+strings.Repeat("a", 64), runID, 1)+"/released",
		map[string]any{"root": reconcileTestRoot, "registration_digest": "sha256:" + strings.Repeat("a", 64), "run_id": runID,
			"workflow_id": workflowID, "publication_id": "publication-" + workflowID, "owner_epoch": 1, "outcome": "publication-succeeded"})
}

// Publication state of the workflow: in progress (pending without a resolution, a run that was
// admitted and never released) is unreconciled; settled state reconciles, and its digest is
// unaffected by other workflows' records.
func TestReconcilePublicationRequiresEveryPublicationAndRunToBeResolved(t *testing.T) {
	settled := []audit.Event{runAdmitted(reconcileTestWorkflow, "run-1"), publicationPending(reconcileTestWorkflow, "publication-1"),
		publicationTransaction(reconcileTestWorkflow, "publication-1"), publicationResolved(reconcileTestWorkflow, "publication-1"),
		runReleased(reconcileTestWorkflow, "run-1")}
	digest, err := workflowPublicationEvidence(reconcileTestTrace(t, settled...), reconcileTestWorkflow)
	if err != nil || !validDigestText(digest) {
		t.Fatalf("settled publication state was not reconciled: digest=%q err=%v", digest, err)
	}
	empty, err := workflowPublicationEvidence(reconcileTestTrace(t), reconcileTestWorkflow)
	if err != nil || !validDigestText(empty) || empty == digest {
		t.Fatalf("a workflow without publication records: digest=%q err=%v", empty, err)
	}

	otherOpen := append(append([]audit.Event(nil), settled...), publicationPending("workflow-other", "publication-9"),
		runAdmitted("workflow-other", "run-9"))
	if again, err := workflowPublicationEvidence(reconcileTestTrace(t, otherOpen...), reconcileTestWorkflow); err != nil || again != digest {
		t.Fatalf("another workflow's records changed this workflow's evidence: %q vs %q err=%v", again, digest, err)
	}

	for name, events := range map[string][]audit.Event{
		"publication pending without a resolution": {runAdmitted(reconcileTestWorkflow, "run-1"),
			publicationPending(reconcileTestWorkflow, "publication-1"), runReleased(reconcileTestWorkflow, "run-1")},
		"transaction without a resolution": {runAdmitted(reconcileTestWorkflow, "run-1"),
			publicationPending(reconcileTestWorkflow, "publication-1"), publicationTransaction(reconcileTestWorkflow, "publication-1"),
			runReleased(reconcileTestWorkflow, "run-1")},
		"run admitted and never released": {runAdmitted(reconcileTestWorkflow, "run-1")},
	} {
		if _, err := workflowPublicationEvidence(reconcileTestTrace(t, events...), reconcileTestWorkflow); !errors.Is(err, ErrPiServiceUnreconciled) {
			t.Fatalf("%s: publication in progress was reconciled: %v", name, err)
		}
	}
	if _, err := workflowPublicationEvidence(reconcileTestTrace(t,
		audit.Event{Kind: "workflow_publication_pending", ID: "x", Data: []byte("not json")}), reconcileTestWorkflow); !errors.Is(err, ErrPiServiceUnreconciled) {
		t.Fatalf("an unreadable publication record was reconciled: %v", err)
	}
}

func providerJournalEvents(kinds ...string) []audit.Event {
	events := make([]audit.Event, 0, len(kinds))
	for index, kind := range kinds {
		events = append(events, audit.Event{Kind: kind, ID: fmt.Sprintf("conversation-1:%s:%d", kind, index+1), Data: []byte(`{}`)})
	}
	return events
}

// The engineering route has no ledger: every provider request that was dispatched (a recorded
// host-call reservation) must have a recorded disposition, an exchange or an unknown exchange.
func TestReconcileEngineeringProviderRequiresADispositionForEveryDispatchedRequest(t *testing.T) {
	entry := piServiceWorkflowEntry{WorkflowID: reconcileTestWorkflow, ConversationID: "conversation-1",
		ProviderJournalPath: "/var/lib/tbound/audit/run-1-provider-audit.jsonl", Limits: workflowrecovery.Limits{MaxHostCalls: 4}}
	for name, tc := range map[string]struct {
		kinds   []string
		missing bool
		refused bool
	}{
		"exchange recorded":                              {kinds: []string{"provider_user_admitted", "provider_host_call_reserved", "provider_exchange"}},
		"unknown exchange recorded":                      {kinds: []string{"provider_user_admitted", "provider_host_call_reserved", "provider_exchange_unknown"}},
		"two requests both disposed":                     {kinds: []string{"provider_host_call_reserved", "provider_exchange", "provider_host_call_reserved", "provider_exchange_unknown"}},
		"never dispatched":                               {kinds: []string{"provider_user_admitted"}},
		"journal never created":                          {missing: true},
		"dispatched without a disposition":               {kinds: []string{"provider_host_call_reserved"}, refused: true},
		"second request has no disposition":              {kinds: []string{"provider_host_call_reserved", "provider_exchange", "provider_host_call_reserved"}, refused: true},
		"reserved twice before an exchange":              {kinds: []string{"provider_host_call_reserved", "provider_host_call_reserved", "provider_exchange"}, refused: true},
		"disposition recorded before a reservation only": {kinds: []string{"provider_exchange", "provider_host_call_reserved"}, refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			trace := reconcileTestTrace(t, providerJournalEvents(tc.kinds...)...)
			r := &piServiceUnknownReconciler{route: piruntime.D09ManagedPiProviderRouteEngineering, auditRoot: productionAuditRootDefault,
				readJournal: func(path string) (audit.Trace, bool, error) {
					if path != entry.ProviderJournalPath {
						t.Fatalf("read an unexpected journal %s", path)
					}
					return trace, !tc.missing, nil
				}}
			digest, _, err := r.reconcileProvider(context.Background(), entry, []string{"conversation-1"})
			if tc.refused {
				if !errors.Is(err, ErrPiServiceUnreconciled) || !strings.Contains(err.Error(), "disposition") {
					t.Fatalf("an undisposed provider request was reconciled: digest=%q err=%v", digest, err)
				}
				return
			}
			if err != nil || !validDigestText(digest) {
				t.Fatalf("digest=%q err=%v", digest, err)
			}
		})
	}
}

// The research route is booked in the USD ledger: an OPEN reservation of this workflow is
// unreconciled, an UNCERTAIN one is not (it is booked at its worst case), and a settled one is
// not. The probe covers every conversation of the workflow and its whole host-call limit.
func TestReconcileResearchProviderRefusesAnOpenLedgerReservation(t *testing.T) {
	entry := piServiceWorkflowEntry{WorkflowID: reconcileTestWorkflow, ConversationID: "conversation-1", Limits: workflowrecovery.Limits{MaxHostCalls: 4}}
	units := func(states ...string) []usdledger.UnitInfo {
		var result []usdledger.UnitInfo
		for index, state := range states {
			result = append(result, usdledger.UnitInfo{ID: fmt.Sprintf("research-call/%d", index), State: state, ReservedMicroUSD: 6077})
		}
		return result
	}
	var probedWorkflow string
	var probedConversations []string
	var probedTurns uint64
	var current []usdledger.UnitInfo
	r := &piServiceUnknownReconciler{route: piruntime.D09ManagedPiProviderRouteResearch, auditRoot: productionAuditRootDefault,
		spendUnits: func(workflowID string, conversations []string, turns uint64) ([]usdledger.UnitInfo, error) {
			probedWorkflow, probedConversations, probedTurns = workflowID, conversations, turns
			return current, nil
		}}
	conversations := []string{"conversation-1", reconcileTestWorkflow + "-conversation-g2"}
	current = units(usdledger.UnitStateUncertain, usdledger.UnitStateSettled)
	uncertain, _, err := r.reconcileProvider(context.Background(), entry, conversations)
	if err != nil || !validDigestText(uncertain) {
		t.Fatalf("uncertain and settled units were refused: %v", err)
	}
	if probedWorkflow != reconcileTestWorkflow || probedTurns != 4 || strings.Join(probedConversations, ",") != strings.Join(conversations, ",") {
		t.Fatalf("the probe did not cover the workflow: %q %v %d", probedWorkflow, probedConversations, probedTurns)
	}
	current = units(usdledger.UnitStateSettled, usdledger.UnitStateOpen)
	if _, _, err := r.reconcileProvider(context.Background(), entry, conversations); !errors.Is(err, ErrPiServiceUnreconciled) ||
		!strings.Contains(err.Error(), "open") {
		t.Fatalf("an OPEN ledger reservation was reconciled: %v", err)
	}
	current = units(usdledger.UnitStateSettled)
	settled, _, err := r.reconcileProvider(context.Background(), entry, conversations)
	if err != nil || settled == uncertain {
		t.Fatalf("the evidence does not distinguish the unit states: %q vs %q err=%v", settled, uncertain, err)
	}
	r.spendUnits = func(string, []string, uint64) ([]usdledger.UnitInfo, error) {
		return nil, errors.New("ledger unreadable")
	}
	if _, _, err := r.reconcileProvider(context.Background(), entry, conversations); !errors.Is(err, ErrPiServiceUnreconciled) {
		t.Fatalf("an unreadable ledger was reconciled: %v", err)
	}
	r.spendUnits = nil
	if _, _, err := r.reconcileProvider(context.Background(), entry, conversations); !errors.Is(err, ErrPiServiceUnreconciled) {
		t.Fatalf("a research route without a ledger probe was reconciled: %v", err)
	}
}

// ownerFixture is a catalog whose workflow has one drained owner epoch, a settled checkpoint
// view of it, and the drain verifier over both.
type ownerFixture struct {
	catalog  *piServiceWorkflowCatalog
	entry    piServiceWorkflowEntry
	verifier catalogOwnerDrainVerifier
	view     workflowrecovery.SettledCheckpointView
	owner    workflowrecovery.OwnerEpochIdentity
	root     string
	// providerTrace reads the provider journal of one conversation; nil means none is installed.
	providerTrace func(conversationID string) (audit.Trace, error)
}

func newOwnerFixture(t *testing.T, mutate func(*ownerFixture)) *ownerFixture {
	t.Helper()
	root := t.TempDir()
	hash := strings.Repeat("a", 64)
	incarnation := workflowrecovery.OwnerIncarnation{Digest: strings.Repeat("b", 64), BootID: "different-boot", PID: 424242,
		StartTimeTicks: 1, PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64)}
	owner := workflowrecovery.OwnerEpochIdentity{Epoch: 1, Incarnation: incarnation}
	scopeName := workflowOwnerScopeName(reconcileTestWorkflow, 1)
	store := workflowrecovery.OwnerStoreIdentity{Path: "/var/lib/tbound/owners/" + reconcileTestWorkflow + ".sqlite", Digest: strings.Repeat("d", 64)}
	limits := workflowrecovery.Limits{MaxHostCalls: 4, MaxTokens: 1000, Deadline: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	entry := piServiceWorkflowEntry{AdmissionRecordHash: hash, WorkflowID: reconcileTestWorkflow, SessionID: "session-1", RunID: "session-1",
		ConversationID: "conversation-1", OwnerDBPath: store.Path, FreshAuthorizationUsed: true, CheckpointBound: true,
		RootIdentityDigest: strings.Repeat("e", 64), OwnerStoreIdentity: store, Limits: limits}
	catalog := &piServiceWorkflowCatalog{entries: map[string]piServiceWorkflowEntry{hash: entry},
		ownerScopes: map[string]piServiceWorkflowOwnerScope{workflowOwnerEpochKey(reconcileTestWorkflow, 1): {
			AdmissionHash: hash, WorkflowID: reconcileTestWorkflow, OwnerEpoch: 1, Incarnation: incarnation, ScopeName: scopeName,
			ScopePath: filepath.Join(root, scopeName), ScopeIdentityDigest: "sha256:" + strings.Repeat("f", 64), Drained: true}}}
	pins := workflowrecovery.Pins{WorkflowID: reconcileTestWorkflow, SessionID: "session-1", RecoveryGeneration: 1, OwnerEpoch: 1,
		RootIdentityDigest: entry.RootIdentityDigest, ProviderDisposition: workflowrecovery.DispositionNone,
		PendingCallDisposition: workflowrecovery.PendingCallNone}
	checkpoint := workflowrecovery.Checkpoint{Pins: pins, Limits: limits, ComputedCheckpointDigest: strings.Repeat("9", 64)}
	f := &ownerFixture{catalog: catalog, entry: entry, owner: owner, root: root,
		verifier: catalogOwnerDrainVerifier{catalog: catalog, cgroupRoot: root, cgroupRootDigest: "sha256:" + strings.Repeat("a", 64),
			containment: piruntime.D09ContainmentProfile, settlement: piruntime.D09SettlementProfile},
		view: workflowrecovery.SettledCheckpointView{Checkpoint: checkpoint, Digest: checkpoint.ComputedCheckpointDigest,
			OwnerStoreIdentity: store, OwnerEpochs: []workflowrecovery.OwnerEpochIdentity{owner}}}
	if mutate != nil {
		mutate(f)
	}
	return f
}

func (f *ownerFixture) reconcile(t *testing.T) (piServiceOwnerReconciliation, error) {
	t.Helper()
	return f.catalog.reconcileSettledOwner(context.Background(), f.entry, f.view, f.verifier, f.root, f.providerTrace)
}

// Owner side: drained and settled reconciles and returns the drain-proof digest per epoch; a
// pending effect, host call, provider exchange or captured call, a live or undrained owner, and
// an owner epoch the checkpoint does not cover are each unreconciled.
func TestReconcileOwnerRequiresDrainedScopesAndSettledDispositions(t *testing.T) {
	good, err := newOwnerFixture(t, nil).reconcile(t)
	if err != nil {
		t.Fatalf("a drained and settled owner was not reconciled: %v", err)
	}
	if good.Checkpoint != "sha256:"+strings.Repeat("9", 64) || len(good.Proofs) != 1 || !validDigestText(good.Proofs[0]) ||
		good.BootID == "" {
		t.Fatalf("owner evidence is incomplete: %+v", good)
	}

	bootID, startTicks := currentProcessIdentityForTest(t)
	for name, mutate := range map[string]func(*ownerFixture){
		"pending effect reservation": func(f *ownerFixture) {
			f.view.Checkpoint.Pins.EffectReservation = &workflowrecovery.EffectReservationPin{Disposition: workflowrecovery.EffectReservationReserved}
		},
		"pending host call": func(f *ownerFixture) {
			f.view.Checkpoint.Pins.HostCallReservation = &workflowrecovery.HostCallReservationPin{Disposition: workflowrecovery.HostCallReserved}
		},
		"pending provider call": func(f *ownerFixture) {
			f.view.Checkpoint.Pins.PendingCallDisposition = workflowrecovery.PendingCallInFlight
		},
		"provider exchange awaited": func(f *ownerFixture) {
			f.view.Checkpoint.Pins.ProviderDisposition = workflowrecovery.DispositionAwaitingProviderExchange
			f.view.Checkpoint.Pins.ProviderTranscripts = []workflowrecovery.TranscriptPin{{Ordinal: 1, ConversationID: "conversation-1",
				Disposition: workflowrecovery.DispositionAwaitingProviderExchange}}
		},
		"owner process still live": func(f *ownerFixture) {
			f.owner.Incarnation.BootID, f.owner.Incarnation.PID, f.owner.Incarnation.StartTimeTicks = bootID, os.Getpid(), startTicks
			f.view.OwnerEpochs = []workflowrecovery.OwnerEpochIdentity{f.owner}
			key := workflowOwnerEpochKey(reconcileTestWorkflow, 1)
			scope := f.catalog.ownerScopes[key]
			scope.Incarnation = f.owner.Incarnation
			f.catalog.ownerScopes[key] = scope
		},
		"scope bound but not drained": func(f *ownerFixture) {
			key := workflowOwnerEpochKey(reconcileTestWorkflow, 1)
			scope := f.catalog.ownerScopes[key]
			scope.Drained = false
			f.catalog.ownerScopes[key] = scope
			f.verifier.openScope = func(*os.File, string, string, string, string) (ownerScopeDrainer, error) {
				return nil, syscall.ENOENT
			}
		},
		"owner epoch the checkpoint does not cover": func(f *ownerFixture) {
			f.catalog.ownerScopes[workflowOwnerEpochKey(reconcileTestWorkflow, 2)] = piServiceWorkflowOwnerScope{
				AdmissionHash: f.entry.AdmissionRecordHash, WorkflowID: reconcileTestWorkflow, OwnerEpoch: 2,
				Incarnation: f.owner.Incarnation, ScopeName: workflowOwnerScopeName(reconcileTestWorkflow, 2), Drained: false}
		},
		"checkpoint of another workflow": func(f *ownerFixture) { f.view.Checkpoint.Pins.WorkflowID = "workflow-other" },
		"checkpoint digest not computed": func(f *ownerFixture) { f.view.Digest = strings.Repeat("8", 64) },
		"another owner store":            func(f *ownerFixture) { f.view.OwnerStoreIdentity.Digest = strings.Repeat("7", 64) },
		"no owner epochs":                func(f *ownerFixture) { f.view.OwnerEpochs = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerFixture(t, mutate)
			if _, err := f.reconcile(t); !errors.Is(err, ErrPiServiceUnreconciled) {
				t.Fatalf("an unreconciled owner was reconciled: %v", err)
			}
		})
	}
}

// Several unreconciled items are all listed, not only the first.
func TestReconcileOwnerListsEveryUnreconciledItem(t *testing.T) {
	f := newOwnerFixture(t, func(f *ownerFixture) {
		f.view.Checkpoint.Pins.EffectReservation = &workflowrecovery.EffectReservationPin{Disposition: workflowrecovery.EffectReservationReserved}
		key := workflowOwnerEpochKey(reconcileTestWorkflow, 1)
		scope := f.catalog.ownerScopes[key]
		scope.Drained = false
		f.catalog.ownerScopes[key] = scope
		f.verifier.openScope = func(*os.File, string, string, string, string) (ownerScopeDrainer, error) { return nil, syscall.ENOENT }
	})
	_, err := f.reconcile(t)
	if !errors.Is(err, ErrPiServiceUnreconciled) || !strings.Contains(err.Error(), "owner checkpoint") ||
		!strings.Contains(err.Error(), "owner scope") {
		t.Fatalf("the effect and the undrained owner are not both listed: %v", err)
	}
}

// reconcileFixture assembles the whole reconciler with the owner, journal and ledger seams
// faked, around a catalog that holds the request's workflow entry.
func newReconcileFixture(t *testing.T, entry *piServiceWorkflowEntry) (*piServiceUnknownReconciler, piServiceRequestState) {
	t.Helper()
	hash := strings.Repeat("a", 64)
	state := piServiceRequestState{
		accepted:       piServiceRequestAcceptedEvent{SchemaVersion: "tbound-pi-service-request-accepted/v1", ClientUID: 1000, RequestID: "request-0001"},
		acceptedRecord: audit.Record{Sequence: 1, Hash: hash},
		terminal:       &piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: "request-0001", Status: "unknown"}}
	catalog := &piServiceWorkflowCatalog{entries: map[string]piServiceWorkflowEntry{}, ownerScopes: map[string]piServiceWorkflowOwnerScope{}}
	if entry != nil {
		entry.AdmissionRecordHash = hash
		catalog.entries[hash] = *entry
	}
	r := &piServiceUnknownReconciler{catalog: catalog, route: piruntime.D09ManagedPiProviderRouteResearch, auditRoot: productionAuditRootDefault,
		readJournal: func(string) (audit.Trace, bool, error) { return audit.Trace{}, false, nil },
		spendUnits:  func(string, []string, uint64) ([]usdledger.UnitInfo, error) { return nil, nil },
		owner: func(context.Context, piServiceWorkflowEntry) (piServiceOwnerReconciliation, error) {
			return piServiceOwnerReconciliation{Checkpoint: digestBytes([]byte("checkpoint")), Proofs: []string{digestBytes([]byte("drain"))},
				BootID: "boot-1", ConversationIDs: []string{"conversation-1"}}, nil
		}}
	return r, state
}

func reconcileTestEntry() *piServiceWorkflowEntry {
	return &piServiceWorkflowEntry{WorkflowID: reconcileTestWorkflow, ConversationID: "conversation-1",
		ProviderJournalPath: "/var/lib/tbound/audit/run-1-provider-audit.jsonl", CheckpointBound: true,
		Limits: workflowrecovery.Limits{MaxHostCalls: 4}}
}

func TestUnknownReconcilerComposesTheOwnerPublicationAndProviderEvidence(t *testing.T) {
	r, state := newReconcileFixture(t, reconcileTestEntry())
	evidence, err := r.Reconcile(context.Background(), state)
	if err != nil || !evidence.valid() || evidence.WorkflowID != reconcileTestWorkflow || evidence.ProviderRoute != "research" ||
		evidence.OwnerDrainBootID != "boot-1" || len(evidence.OwnerDrainProofs) != 1 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	again, err := r.Reconcile(context.Background(), state)
	if err != nil || !evidence.stillHolds(again) {
		t.Fatalf("a second reconciliation does not hold against the first: %v", err)
	}
	r.spendUnits = func(string, []string, uint64) ([]usdledger.UnitInfo, error) {
		return []usdledger.UnitInfo{{ID: "research-call/1", State: usdledger.UnitStateUncertain, ReservedMicroUSD: 1}}, nil
	}
	changed, err := r.Reconcile(context.Background(), state)
	if err != nil || evidence.stillHolds(changed) {
		t.Fatalf("a changed ledger unit does not change the evidence: %v", err)
	}

	// A request that never reserved a workflow: nothing could have run, and the evidence says so.
	none, noneState := newReconcileFixture(t, nil)
	noWorkflow, err := none.Reconcile(context.Background(), noneState)
	if err != nil || !noWorkflow.valid() || !noWorkflow.NoWorkflow || noWorkflow.WorkflowID != "" {
		t.Fatalf("no-workflow evidence=%+v err=%v", noWorkflow, err)
	}
	none.catalog.entries[noneState.acceptedRecord.Hash] = *reconcileTestEntry()
	if again, err := none.Reconcile(context.Background(), noneState); err != nil || noWorkflow.stillHolds(again) {
		t.Fatalf("a workflow that appeared after the no-workflow proof still holds: %v", err)
	}
}

func TestAbandonedPriorIsRecheckedOnTheRouteItsRecordBinds(t *testing.T) {
	r, state := newReconcileFixture(t, reconcileTestEntry())
	r.route = piruntime.D09ManagedPiProviderRouteEngineering
	evidence, err := r.Reconcile(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := newPiServiceRequestAbandonedEvent(state, "DR drill", evidence)
	if err != nil {
		t.Fatal(err)
	}
	state.abandoned = &abandoned
	r.route = piruntime.D09ManagedPiProviderRouteResearch
	if err := (&piServiceRequestJournal{}).recheckAbandoned(context.Background(), r, state); err != nil {
		t.Fatalf("an engineering-route abandon record keeps the barrier under the research service: %v", err)
	}
}

func TestUnknownReconcilerListsEveryUnreconciledKindAndRefusesNonUnknownRequests(t *testing.T) {
	r, state := newReconcileFixture(t, reconcileTestEntry())
	r.owner = func(context.Context, piServiceWorkflowEntry) (piServiceOwnerReconciliation, error) {
		return piServiceOwnerReconciliation{}, fmt.Errorf("%w: owner scope 1 is not drained", ErrPiServiceUnreconciled)
	}
	r.spendUnits = func(string, []string, uint64) ([]usdledger.UnitInfo, error) {
		return []usdledger.UnitInfo{{ID: "research-call/1", State: usdledger.UnitStateOpen, ReservedMicroUSD: 1}}, nil
	}
	r.readJournal = func(string) (audit.Trace, bool, error) {
		return reconcileTestTrace(t, publicationPending(reconcileTestWorkflow, "publication-1")), true, nil
	}
	_, err := r.Reconcile(context.Background(), state)
	if !errors.Is(err, ErrPiServiceUnreconciled) {
		t.Fatalf("unreconciled workflow was reconciled: %v", err)
	}
	for _, want := range []string{"owner scope 1 is not drained", "open", "pending"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not list %q: %v", want, err)
		}
	}
	for name, mutate := range map[string]func(*piServiceRequestState){
		"completed terminal": func(s *piServiceRequestState) { s.terminal.Status = "completed" },
		"no terminal":        func(s *piServiceRequestState) { s.terminal = nil },
	} {
		s := state
		response := *state.terminal
		s.terminal = &response
		mutate(&s)
		if _, err := r.Reconcile(context.Background(), s); !errors.Is(err, ErrPiServiceUnreconciled) {
			t.Fatalf("%s: reconciled: %v", name, err)
		}
	}
}

// A workflow whose catalog entry never bound a checkpoint has no owner epoch to drain, and may
// not have a scope bound either.
func TestUnknownReconcilerHandlesAWorkflowThatNeverBoundAnOwner(t *testing.T) {
	entry := reconcileTestEntry()
	entry.CheckpointBound = false
	r, state := newReconcileFixture(t, entry)
	r.owner = nil // the owner database must not be consulted
	evidence, err := r.Reconcile(context.Background(), state)
	if err != nil || !evidence.valid() || !evidence.OwnerNeverBound || len(evidence.OwnerDrainProofs) != 0 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	r.catalog.ownerScopes[workflowOwnerEpochKey(reconcileTestWorkflow, 1)] = piServiceWorkflowOwnerScope{
		AdmissionHash: state.acceptedRecord.Hash, WorkflowID: reconcileTestWorkflow, OwnerEpoch: 1}
	if _, err := r.Reconcile(context.Background(), state); !errors.Is(err, ErrPiServiceUnreconciled) {
		t.Fatalf("a bound owner scope without a bound checkpoint was reconciled: %v", err)
	}
}

// workflowPublicationEvidence digests the workflow's records in the publication journal and
// requires every one of its publications to be resolved and every managed-root run it was
// admitted under to be released or reconciled. Records of other workflows, whatever their
// state, are ignored and do not move the digest, which is stable once the workflow is
// quiescent.
func workflowPublicationEvidence(trace audit.Trace, workflowID string) (string, error) {
	if !validIdentityText(workflowID) {
		return "", unreconciledf("publication: the workflow id is not valid")
	}
	index, err := indexPublicationRecords(trace)
	if err != nil {
		return "", err
	}
	return publicationEvidence(index[workflowID], workflowID)
}
