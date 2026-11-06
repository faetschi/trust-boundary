//go:build linux

package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/usdledger"
	"tbound/supervisor/internal/workflowrecovery"
)

// Round 2, item 1. The cached settlement of a prior is tied to the workflow's checkpoint and
// fence head in the protected-root database, which the settlement verification reads besides
// the owner database and the store journal.
func TestCachedSettlementIsReverifiedWhenItsProtectedRootStateChanged(t *testing.T) {
	f := newEvidenceFixture(t)
	defer f.h.close(t)
	rootState := map[string]string{}
	for index := 1; index <= 8; index++ {
		rootState[fmt.Sprintf("request-%04d", index)] = "checkpoint-1/fence-1"
	}
	f.h.requests.setEvidenceState(func(state piServiceRequestState) string { return rootState[state.accepted.RequestID] })
	for index := 1; index <= 3; index++ {
		if err := f.h.admit(t, index, true); err != nil {
			t.Fatalf("seed %d: %v", index, err)
		}
	}
	f.h.total = 0
	if err := f.h.admit(t, 4, true); err != nil || f.h.total != 1 {
		t.Fatalf("steady admission verified %d priors (err=%v), want 1", f.h.total, err)
	}
	// The owner database, its journal and every other file are untouched. Only the protected-root
	// checkpoint or fence of request 1 moved, and the verifier now refuses it.
	rootState["request-0001"] = "checkpoint-2/fence-2"
	calls := f.h.calls["request-0001"]
	f.h.requests.settlementVerifier = func(_ context.Context, prior piServiceRequestState) error {
		f.h.calls[prior.accepted.RequestID]++
		if prior.accepted.RequestID == "request-0001" {
			return errors.New("protected-root head differs from the settled checkpoint")
		}
		return nil
	}
	records := len(journalKinds(t, f.h.audit))
	if err := f.h.admit(t, 5, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
		t.Fatalf("a cached prior whose protected-root state changed was not re-verified and refused: %v", err)
	}
	if f.h.calls["request-0001"] != calls+1 {
		t.Fatalf("the prior was verified %d more times, want 1", f.h.calls["request-0001"]-calls)
	}
	if got := len(journalKinds(t, f.h.audit)); got != records {
		t.Fatalf("a refused admission appended %d records", got-records)
	}
}

// The production state value is a cheap read of the workflow's own head in the real
// protected-root database: it moves with the checkpoint, the fence and the owner epoch history
// of that workflow, and with nothing else.
func TestProtectedRootWorkflowStateMovesWithTheWorkflowHeadOnly(t *testing.T) {
	root := t.TempDir()
	rootDir, ownerRoot := filepath.Join(root, "root"), filepath.Join(root, "owners")
	for _, dir := range []string{rootDir, ownerRoot} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	boundary, err := workflowrecovery.NewLinuxHostBoundary(ownerRoot, uint32(os.Geteuid()), verticalNoPriorOwnerDrain{}, &piServiceWorkflowCatalog{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rootDir, "root.sqlite")
	protected, err := workflowrecovery.CreateFreshProtectedRoot(path, ownerRoot, boundary)
	if err != nil {
		t.Fatal(err)
	}
	defer protected.Close()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	digest := func(c string) string { return strings.Repeat(c, 64) }
	insert := func(workflowID string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO workflow_root_state(workflow_id,owner_store_path,owner_store_identity_digest,workflow_deadline_utc,
			clock_boot_id,clock_anchor_wall_utc,clock_anchor_boot_time_ns,clock_observed_utc,clock_observed_boot_time_ns,clock_expired,
			clock_uncertain,checkpoint_epoch,checkpoint_digest,checkpoint_incarnation_digest,fence_epoch,fence_incarnation_digest,
			fresh_authorization_digest,revision) VALUES(?,?,?,?,?,?,?,?,?,0,0,1,?,?,1,?,?,1)`, workflowID, "/o/"+workflowID, digest("a"),
			"2026-10-17T12:00:00Z", "boot", "2026-10-17T11:00:00Z", 0, "2026-10-17T11:00:00Z", 0, digest("b"), digest("c"), digest("c"), digest("d")); err != nil {
			t.Fatal(err)
		}
	}
	absent := protectedRootWorkflowState(path, "workflow-a")
	insert("workflow-a")
	insert("workflow-b")
	first := protectedRootWorkflowState(path, "workflow-a")
	if first == "" || first == absent {
		t.Fatalf("the state of a present workflow is not distinct from an absent one: %q %q", first, absent)
	}
	if again := protectedRootWorkflowState(path, "workflow-a"); again != first {
		t.Fatal("an unchanged workflow state moved")
	}
	for name, update := range map[string]string{
		"checkpoint digest": `UPDATE workflow_root_state SET checkpoint_digest='` + digest("e") + `' WHERE workflow_id='workflow-a'`,
		"fence epoch":       `UPDATE workflow_root_state SET fence_epoch=2 WHERE workflow_id='workflow-a'`,
		"fence incarnation": `UPDATE workflow_root_state SET fence_incarnation_digest='` + digest("f") + `' WHERE workflow_id='workflow-a'`,
		"revision":          `UPDATE workflow_root_state SET revision=2 WHERE workflow_id='workflow-a'`,
		"owner epoch drain": `INSERT INTO workflow_root_owner_epochs VALUES('workflow-a',1,x'7b7d','` + digest("c") + `',1,'` + digest("9") + `')`,
	} {
		before := protectedRootWorkflowState(path, "workflow-a")
		if _, err := db.Exec(update); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if after := protectedRootWorkflowState(path, "workflow-a"); after == before {
			t.Fatalf("%s: the state did not move", name)
		}
	}
	other := protectedRootWorkflowState(path, "workflow-b")
	if _, err := db.Exec(`UPDATE workflow_root_state SET revision=9 WHERE workflow_id='workflow-a'`); err != nil {
		t.Fatal(err)
	}
	if protectedRootWorkflowState(path, "workflow-b") != other {
		t.Fatal("a change of another workflow moved this workflow's state")
	}
	if missing := protectedRootWorkflowState(filepath.Join(rootDir, "missing.sqlite"), "workflow-a"); missing == "" || missing == first {
		t.Fatalf("an unreadable protected root is not its own state: %q", missing)
	}
}

// Round 2, item 2. One admission reads and verifies the publication journal once, however many
// abandoned priors it re-checks, and indexes it by workflow.
func TestBeginReadsThePublicationJournalOnceForAllAbandonedPriors(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	const abandoned = 4
	catalogJournal, err := audit.Open(filepath.Join(incrementalTestRoot(t), "catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalogJournal.Close()
	catalog := &piServiceWorkflowCatalog{journal: catalogJournal, entries: map[string]piServiceWorkflowEntry{}, ownerScopes: map[string]piServiceWorkflowOwnerScope{}}
	var publicationReads atomic.Int64
	trace := reconcileTestTrace(t, publicationPending("workflow-2", "publication-2"), publicationResolved("workflow-2", "publication-2"))
	r := &piServiceUnknownReconciler{catalog: catalog, route: piruntime.D09ManagedPiProviderRouteResearch, auditRoot: productionAuditRootDefault,
		readJournal: func(path string) (audit.Trace, bool, error) {
			if strings.HasSuffix(path, "publication-audit.jsonl") {
				publicationReads.Add(1)
			}
			return trace, true, nil
		},
		spendUnits: func(string, []string, uint64) ([]usdledger.UnitInfo, error) { return nil, nil },
		owner: func(context.Context, piServiceWorkflowEntry) (piServiceOwnerReconciliation, error) {
			return piServiceOwnerReconciliation{Checkpoint: digestBytes([]byte("checkpoint")), Proofs: []string{digestBytes([]byte("drain"))},
				BootID: "boot-1", ConversationIDs: []string{"conversation-1"}}, nil
		}}
	h.requests.setReconciler(r)
	for index := 1; index <= abandoned; index++ {
		if err := h.admitUnknown(t, index); err != nil {
			t.Fatal(err)
		}
		requestID := fmt.Sprintf("request-%04d", index)
		state := h.requests.entries[piServiceRequestKey(incrementalPeer().UID, requestID)]
		catalog.entries[state.acceptedRecord.Hash] = piServiceWorkflowEntry{AdmissionRecordHash: state.acceptedRecord.Hash,
			WorkflowID: fmt.Sprintf("workflow-%d", index), ConversationID: "conversation-1", CheckpointBound: true,
			Limits: workflowrecovery.Limits{MaxHostCalls: 4}}
		if _, err := h.requests.abandonUnknown(context.Background(), requestID, "operator ack"); err != nil {
			t.Fatalf("abandon %d: %v", index, err)
		}
	}
	publicationReads.Store(0)
	if err := h.admit(t, abandoned+1, true); err != nil {
		t.Fatal(err)
	}
	if got := publicationReads.Load(); got != 1 {
		t.Fatalf("one admission with %d abandoned priors read the publication journal %d times, want 1", abandoned, got)
	}
	// Nothing is carried across admissions: the next begin reads a fresh snapshot, and a
	// publication that is pending now refuses it from that snapshot alone.
	publicationReads.Store(0)
	trace = reconcileTestTrace(t, publicationPending("workflow-2", "publication-2"), publicationResolved("workflow-2", "publication-2"),
		publicationPending("workflow-3", "publication-3"))
	if err := h.admit(t, abandoned+2, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("a publication pending in the fresh snapshot of workflow-3 did not refuse the admission: %v", err)
	}
	if got := publicationReads.Load(); got != 1 {
		t.Fatalf("the refused admission read the publication journal %d times, want 1", got)
	}
}

// Round 2, item 3. The provider settlement of the evidence says what was proven: terminal when
// every exchange has a terminal record, bounded-retained-liability when any exchange is
// UNCERTAIN in the ledger or unknown in the provider-bridge journal.
func TestReconcileProviderSettlementNamesWhatWasProven(t *testing.T) {
	entry := piServiceWorkflowEntry{WorkflowID: reconcileTestWorkflow, ConversationID: "conversation-1",
		ProviderJournalPath: "/var/lib/tbound/audit/run-1-provider-audit.jsonl", Limits: workflowrecovery.Limits{MaxHostCalls: 4}}
	research := func(states ...string) *piServiceUnknownReconciler {
		var units []usdledger.UnitInfo
		for index, state := range states {
			units = append(units, usdledger.UnitInfo{ID: fmt.Sprintf("research-call/%d", index), State: state, ReservedMicroUSD: 6077})
		}
		return &piServiceUnknownReconciler{route: piruntime.D09ManagedPiProviderRouteResearch, auditRoot: productionAuditRootDefault,
			spendUnits: func(string, []string, uint64) ([]usdledger.UnitInfo, error) { return units, nil }}
	}
	engineering := func(kinds ...string) *piServiceUnknownReconciler {
		trace := reconcileTestTrace(t, providerJournalEvents(kinds...)...)
		return &piServiceUnknownReconciler{route: piruntime.D09ManagedPiProviderRouteEngineering, auditRoot: productionAuditRootDefault,
			readJournal: func(string) (audit.Trace, bool, error) { return trace, true, nil }}
	}
	for name, tc := range map[string]struct {
		reconciler *piServiceUnknownReconciler
		want       string
	}{
		"research, no unit":             {research(), providerSettlementTerminal},
		"research, settled units":       {research(usdledger.UnitStateSettled, usdledger.UnitStateSettled), providerSettlementTerminal},
		"research, one uncertain unit":  {research(usdledger.UnitStateSettled, usdledger.UnitStateUncertain), providerSettlementBoundedRetainedLiability},
		"engineering, exchanges":        {engineering("provider_host_call_reserved", "provider_exchange"), providerSettlementTerminal},
		"engineering, never dispatched": {engineering("provider_user_admitted"), providerSettlementTerminal},
		"engineering, unknown exchange": {engineering("provider_host_call_reserved", "provider_exchange", "provider_host_call_reserved", "provider_exchange_unknown"), providerSettlementBoundedRetainedLiability},
	} {
		t.Run(name, func(t *testing.T) {
			_, settlement, err := tc.reconciler.reconcileProvider(context.Background(), entry, []string{"conversation-1"})
			if err != nil || settlement != tc.want {
				t.Fatalf("settlement=%q err=%v, want %q", settlement, err, tc.want)
			}
		})
	}
	// The whole reconciliation carries it, and a request without a workflow has nothing to settle.
	r, state := newReconcileFixture(t, reconcileTestEntry())
	r.spendUnits = func(string, []string, uint64) ([]usdledger.UnitInfo, error) {
		return []usdledger.UnitInfo{{ID: "research-call/1", State: usdledger.UnitStateUncertain, ReservedMicroUSD: 1}}, nil
	}
	evidence, err := r.Reconcile(context.Background(), state)
	if err != nil || evidence.ProviderSettlement != providerSettlementBoundedRetainedLiability || !evidence.valid() {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	none, noneState := newReconcileFixture(t, nil)
	noWorkflow, err := none.Reconcile(context.Background(), noneState)
	if err != nil || noWorkflow.ProviderSettlement != providerSettlementTerminal || !noWorkflow.valid() {
		t.Fatalf("no-workflow evidence=%+v err=%v", noWorkflow, err)
	}
}

// The provider settlement is part of the evidence: it must be one of the two values, the
// reconciliation digest commits to it, and a record bound to the other value no longer holds.
func TestProviderSettlementIsBoundIntoTheReconciliationDigest(t *testing.T) {
	terminal := goodReconciliation("request-0001")
	if terminal.ProviderSettlement != providerSettlementTerminal || !terminal.valid() {
		t.Fatalf("the good evidence is not terminal: %+v", terminal)
	}
	bounded := terminal
	bounded.ProviderSettlement = providerSettlementBoundedRetainedLiability
	terminalDigest, err := terminal.digest()
	if err != nil {
		t.Fatal(err)
	}
	boundedDigest, err := bounded.digest()
	if err != nil || boundedDigest == terminalDigest || !bounded.valid() {
		t.Fatalf("the digest does not commit to the provider settlement: %q %q %v", terminalDigest, boundedDigest, err)
	}
	if terminal.stillHolds(bounded) || bounded.stillHolds(terminal) {
		t.Fatal("evidence bound to one provider settlement still holds against the other")
	}
	for _, invalid := range []string{"", "quiescent", "Terminal"} {
		mutated := terminal
		mutated.ProviderSettlement = invalid
		if mutated.valid() {
			t.Fatalf("provider settlement %q was accepted", invalid)
		}
	}
	// The abandon record keeps the value apart from the reason and refuses a forged one.
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	if err := h.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	h.reconciler.mutate = func(_ string, e *piServiceReconciliation) {
		e.ProviderSettlement = providerSettlementBoundedRetainedLiability
	}
	if _, err := h.requests.abandonUnknown(context.Background(), "request-0001", "operator ack"); err != nil {
		t.Fatal(err)
	}
	event := lastAbandonedEvent(t, h.audit)
	if event.Reconciliation.ProviderSettlement != providerSettlementBoundedRetainedLiability {
		t.Fatalf("the abandon record lost the provider settlement: %+v", event.Reconciliation)
	}
	forged := event
	forged.Reconciliation.ProviderSettlement = providerSettlementTerminal
	if forged.bindsTerminal(h.requests.entries[piServiceRequestKey(incrementalPeer().UID, "request-0001")]) {
		t.Fatal("a record whose provider settlement was rewritten still binds its digest")
	}
}

// The abandon command's help says what abandonment does and does not claim.
func TestAbandonUnknownHelpStatesThatRemoteQuiescenceIsNotClaimed(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runAbandonUnknown([]string{"-h"}, &stdout, &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help returned %v", err)
	}
	for _, want := range []string{"remote quiescence", "bounded-retained-liability", "worst case"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("the help does not state %q:\n%s", want, stderr.String())
		}
	}
}
