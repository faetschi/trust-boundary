//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/workflowrecovery"
)

type boundCatalogFixture struct {
	catalog       *piServiceWorkflowCatalog
	journal       *audit.Journal
	journalPath   string
	ownerRoot     string
	admissionHash string
}

// boundCatalog is a catalog with one checkpoint-bound, non-terminal admission: the state of a
// workflow that started and then ended unknown.
func boundCatalog(t *testing.T) boundCatalogFixture { return catalogFixture(t, true) }

// reservedCatalog is the same with the admission authorized but never checkpoint-bound.
func reservedCatalog(t *testing.T) boundCatalogFixture { return catalogFixture(t, false) }

func catalogFixture(t *testing.T, bind bool) boundCatalogFixture {
	t.Helper()
	root := privateGovernedTestRoot(t)
	ownerRoot := filepath.Join(root, "owner-store")
	if err := os.Mkdir(ownerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "workflow-catalog.jsonl")
	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	admissionHash := bareWorkflowDigest(digestBytes([]byte("settle-abandoned-admission")))
	entry := catalogReplayEntry(root, ownerRoot, admissionHash)
	reserved, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash, Entry: entry})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(audit.Event{Kind: "pi_workflow_admission_reserved",
		ID: workflowCatalogEventID("reserved", admissionHash), Data: reserved}); err != nil {
		t.Fatal(err)
	}
	catalog := reopenCatalogReplayJournal(t, journal, ownerRoot)
	initial := bareWorkflowDigest(digestBytes([]byte("settle-abandoned-initial")))
	incarnation := workflowrecovery.OwnerIncarnation{Digest: bareWorkflowDigest(digestBytes([]byte("settle-abandoned-incarnation"))),
		BootID: "settle-abandoned-boot", PID: os.Getpid(), StartTimeTicks: 1, PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()),
		CgroupIdentityDigest: bareWorkflowDigest(digestBytes([]byte("settle-abandoned-cgroup")))}
	store := workflowrecovery.OwnerStoreIdentity{Digest: bareWorkflowDigest(digestBytes([]byte("settle-abandoned-store"))), Path: entry.OwnerDBPath}
	if err := catalog.AuthorizeFreshWorkflow(context.Background(), catalogReplayWorkflowID, 1, initial, incarnation,
		entry.Limits.Deadline, bareWorkflowDigest(digestBytes([]byte("settle-abandoned-root"))), store); err != nil {
		t.Fatal(err)
	}
	if bind {
		bound, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash, Digest: initial})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := journal.Append(audit.Event{Kind: "pi_workflow_checkpoint_bound",
			ID: workflowCatalogEventID("checkpoint", admissionHash), Data: bound}); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	return boundCatalogFixture{catalog: reopenCatalogReplayJournal(t, journal, ownerRoot), journal: journal,
		journalPath: journalPath, ownerRoot: ownerRoot, admissionHash: admissionHash}
}

func TestAbandonedWorkflowBecomesTerminalAndSurvivesReopen(t *testing.T) {
	f := boundCatalog(t)
	trace, err := f.journal.Trace()
	if err != nil || trialPendingFrom(trace, trialTestTrace(), trialTestRoot, trialTestRegistration) == nil {
		t.Fatalf("an admission without a terminal must still block the reset (%v)", err)
	}
	if err := f.catalog.MarkAbandonedTerminal(f.admissionHash); err != nil {
		t.Fatal(err)
	}
	if err := f.catalog.MarkAbandonedTerminal(f.admissionHash); err != nil {
		t.Fatalf("marking twice must be a no-op: %v", err)
	}
	if trace, err = f.journal.Trace(); err != nil {
		t.Fatal(err)
	}
	if err := trialPendingFrom(trace, trialTestTrace(), trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("the abandoned workflow still blocks the reset: %v", err)
	}
	if err := f.catalog.Close(); err != nil {
		t.Fatal(err)
	}
	// A new process replays the terminal; it never claims a completion.
	reopened, err := audit.Open(f.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog := reopenCatalogReplayJournal(t, reopened, f.ownerRoot)
	defer catalog.Close()
	entry, _ := catalog.admissionEntry(f.admissionHash)
	if !entry.Terminal || !entry.TerminalAbandoned || entry.TerminalRunID != "" || entry.TerminalProofSequence != 0 {
		t.Fatalf("replayed entry = %+v", entry)
	}
	other := bareWorkflowDigest(digestBytes([]byte("other")))
	if err := catalog.MarkCheckpoint(f.admissionHash, entry.LatestCheckpointDigest, other); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("a terminal workflow advanced its checkpoint: %v", err)
	}
}

func TestCatalogReplayRefusesAMalformedAbandonedTerminal(t *testing.T) {
	for name, event := range map[string]piServiceWorkflowEvent{
		"unknown outcome":   {Outcome: "bogus"},
		"abandoned + link":  {Outcome: workflowTerminalAbandonedUnknown, RunID: "run-1"},
		"abandoned, digest": {Outcome: workflowTerminalAbandonedUnknown, Digest: bareWorkflowDigest(digestBytes([]byte("not the latest")))},
	} {
		t.Run(name, func(t *testing.T) {
			f := boundCatalog(t)
			entry, _ := f.catalog.admissionEntry(f.admissionHash)
			event.SchemaVersion, event.AdmissionHash = workflowCatalogSchema, f.admissionHash
			if event.Digest == "" {
				event.Digest = entry.LatestCheckpointDigest
			}
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.journal.Append(audit.Event{Kind: "pi_workflow_terminal",
				ID: workflowCatalogEventID("terminal", f.admissionHash), Data: data}); err != nil {
				t.Fatal(err)
			}
			_ = f.catalog.Close()
			reopened, err := audit.Open(f.journalPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loadPiServiceWorkflowCatalog(reopened); !errors.Is(err, ErrPiWorkflowCatalog) {
				t.Fatalf("replayed: %v", err)
			}
		})
	}
}

// markingReconciler is the fake reconciler plus the catalog hook, recording what the request
// journal held when the hook ran.
type markingReconciler struct {
	*fakeAbandonReconciler
	journal *audit.Journal
	kinds   []string
	marks   int
	t       *testing.T
	failure error
}

func (m *markingReconciler) markAbandoned(piServiceRequestState) error {
	m.marks++
	m.kinds = journalKinds(m.t, m.journal)
	return m.failure
}

func TestAbandonUnknownClosesTheWorkflowAfterWritingTheAbandonRecord(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	marker := &markingReconciler{fakeAbandonReconciler: newFakeAbandonReconciler(), journal: h.audit, t: t}
	h.requests.setReconciler(marker)
	if err := h.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.requests.abandonUnknown(context.Background(), "request-0001", "outage"); err != nil {
		t.Fatal(err)
	}
	if len(marker.kinds) == 0 || marker.kinds[len(marker.kinds)-1] != "pi_service_request_abandoned" {
		t.Fatalf("the workflow was closed before the abandon record: %v", marker.kinds)
	}
}

func TestAbandonUnknownReportsAWorkflowItCouldNotClose(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	h.requests.setReconciler(&markingReconciler{fakeAbandonReconciler: newFakeAbandonReconciler(), journal: h.audit, t: t, failure: ErrPiWorkflowCatalog})
	if err := h.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	_, err := h.requests.abandonUnknown(context.Background(), "request-0001", "outage")
	if !errors.Is(err, ErrPiWorkflowCatalog) || !strings.Contains(err.Error(), "not closed") {
		t.Fatalf("err = %v", err)
	}
}

// A completed request is verified, and so made terminal in the catalog, when it completes. The
// next admission then finds the verification already done.
func TestCompletedRequestIsSettledAtCompletionNotAtTheNextAdmission(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	if err := h.admit(t, 1, true); err != nil {
		t.Fatal(err)
	}
	if h.total != 0 {
		t.Fatalf("verification ran before settleCompleted: %d", h.total)
	}
	request, _ := incrementalRequest(1)
	if err := h.requests.settleCompleted(context.Background(), incrementalPeer().UID, request); err != nil {
		t.Fatal(err)
	}
	if h.total != 1 {
		t.Fatalf("verifier calls = %d, want 1", h.total)
	}
	if err := h.admit(t, 2, false); err != nil || h.total != 1 {
		t.Fatalf("the next admission re-verified (calls %d, err %v)", h.total, err)
	}
	// request-0002 is accepted but not completed: settleCompleted leaves it alone.
	unfinished, _ := incrementalRequest(2)
	if err := h.requests.settleCompleted(context.Background(), incrementalPeer().UID, unfinished); err != nil || h.total != 1 {
		t.Fatalf("an unfinished request was verified (calls %d, err %v)", h.total, err)
	}
}

func TestCompletedRequestSettlementFailureLeavesItForTheNextAdmission(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	if err := h.admit(t, 1, true); err != nil {
		t.Fatal(err)
	}
	h.failID = "request-0001"
	request, _ := incrementalRequest(1)
	if err := h.requests.settleCompleted(context.Background(), incrementalPeer().UID, request); err == nil {
		t.Fatal("a failed verification was swallowed")
	}
	if err := h.admit(t, 2, false); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
		t.Fatalf("the unverified prior no longer blocks: %v", err)
	}
}
