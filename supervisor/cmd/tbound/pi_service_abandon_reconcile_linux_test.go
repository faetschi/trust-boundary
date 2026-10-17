//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
)

// fakeAbandonReconciler stands in for the host reconciliation of the workflow behind an
// unknown request. By default every request reconciles to a distinct, valid evidence set.
type fakeAbandonReconciler struct {
	calls   map[string]int
	failure map[string]error
	mutate  func(requestID string, evidence *piServiceReconciliation)
}

func newFakeAbandonReconciler() *fakeAbandonReconciler {
	return &fakeAbandonReconciler{calls: make(map[string]int), failure: make(map[string]error)}
}

func (f *fakeAbandonReconciler) Reconcile(_ context.Context, state piServiceRequestState) (piServiceReconciliation, error) {
	id := state.accepted.RequestID
	f.calls[id]++
	if err := f.failure[id]; err != nil {
		return piServiceReconciliation{}, err
	}
	evidence := goodReconciliation(id)
	if f.mutate != nil {
		f.mutate(id, &evidence)
	}
	return evidence, nil
}

func goodReconciliation(requestID string) piServiceReconciliation {
	return piServiceReconciliation{WorkflowID: "workflow-" + requestID,
		OwnerCheckpoint:  digestBytes([]byte("checkpoint " + requestID)),
		OwnerDrainProofs: []string{digestBytes([]byte("drain one " + requestID)), digestBytes([]byte("drain two " + requestID))},
		OwnerDrainBootID: "boot-one", Publication: digestBytes([]byte("publication " + requestID)),
		ProviderRoute: "research", Provider: digestBytes([]byte("provider " + requestID))}
}

func lastAbandonedEvent(t *testing.T, journal *audit.Journal) piServiceRequestAbandonedEvent {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for index := len(trace.Records) - 1; index >= 0; index-- {
		if trace.Records[index].Event.Kind != "pi_service_request_abandoned" {
			continue
		}
		var event piServiceRequestAbandonedEvent
		if err := unmarshalGovernedJSON(trace.Records[index].Event.Data, &event); err != nil {
			t.Fatal(err)
		}
		return event
	}
	t.Fatal("no abandon record")
	return piServiceRequestAbandonedEvent{}
}

// Drained and settled: the abandon record binds the reconciliation evidence and its digest,
// apart from the operator's reason.
func TestAbandonUnknownBindsTheReconciliationEvidenceApartFromTheReason(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	if err := h.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.requests.abandonUnknown(context.Background(), "request-0001", "operator ack: provider outage"); err != nil {
		t.Fatalf("a drained and settled workflow was not abandoned: %v", err)
	}
	event := lastAbandonedEvent(t, h.audit)
	want := goodReconciliation("request-0001")
	wantDigest, err := want.digest()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(event.Reconciliation, want) || event.ReconciliationDigest != wantDigest ||
		event.Reason != "operator ack: provider outage" || len(event.Reconciliation.OwnerDrainProofs) != 2 {
		t.Fatalf("the record does not bind the reconciliation evidence apart from the reason: %+v", event)
	}
	if h.reconciler.calls["request-0001"] != 1 {
		t.Fatalf("abandonment reconciled %d times", h.reconciler.calls["request-0001"])
	}
}

// An unreconciled workflow (owner not drained, open ledger reservation, pending effect,
// publication in progress, or no reconciler at all) is refused: nothing is appended, the
// request stays quarantined and the barrier stays, and the message lists what is unreconciled.
func TestAbandonUnknownIsRefusedWhileTheWorkflowIsUnreconciled(t *testing.T) {
	for name, failure := range map[string]string{
		"owner not drained":          "owner epoch 2 drain proof is incomplete: superseded owner process is still live",
		"open ledger reservation":    "provider: 1 open USD ledger reservation",
		"pending effect":             "owner checkpoint: effect reservation is still reserved",
		"publication in progress":    "publication: publication-1 is pending without a resolution",
		"several unreconciled kinds": "owner not drained; provider: open reservation; publication: pending",
	} {
		t.Run(name, func(t *testing.T) {
			h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
			defer h.close(t)
			if err := h.admitUnknown(t, 1); err != nil {
				t.Fatal(err)
			}
			h.reconciler.failure["request-0001"] = fmt.Errorf("%w: %s", ErrPiServiceUnreconciled, failure)
			before := len(journalKinds(t, h.audit))
			_, err := h.requests.abandonUnknown(context.Background(), "request-0001", "operator ack")
			if !errors.Is(err, ErrPiServiceUnreconciled) || !strings.Contains(err.Error(), failure) {
				t.Fatalf("abandonment was not refused with the unreconciled list: %v", err)
			}
			if got := len(journalKinds(t, h.audit)); got != before {
				t.Fatalf("a refused abandonment wrote %d records", got-before)
			}
			if err := h.admit(t, 2, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
				t.Fatalf("the barrier was lifted without reconciliation: %v", err)
			}
		})
	}
	t.Run("no reconciler installed", func(t *testing.T) {
		h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
		defer h.close(t)
		if err := h.admitUnknown(t, 1); err != nil {
			t.Fatal(err)
		}
		h.requests.setReconciler(nil)
		if _, err := h.requests.abandonUnknown(context.Background(), "request-0001", "operator ack"); err == nil {
			t.Fatal("abandonment without any reconciler was accepted")
		}
	})
	t.Run("evidence that is not valid", func(t *testing.T) {
		h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
		defer h.close(t)
		if err := h.admitUnknown(t, 1); err != nil {
			t.Fatal(err)
		}
		h.reconciler.mutate = func(_ string, evidence *piServiceReconciliation) { evidence.OwnerDrainProofs = nil }
		if _, err := h.requests.abandonUnknown(context.Background(), "request-0001", "operator ack"); err == nil {
			t.Fatal("abandonment on evidence without a drain proof was accepted")
		}
	})
}

// The skip is not blind: every begin re-derives the evidence of every abandoned request and
// skips it only while the bound digests still match, including after a restart.
func TestAbandonedRequestIsSkippedOnlyWhileItsBoundEvidenceStillMatches(t *testing.T) {
	path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
	h := openIncrementalJournal(t, path)
	if err := h.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.requests.abandonUnknown(context.Background(), "request-0001", "operator ack"); err != nil {
		t.Fatal(err)
	}
	h.reconciler.calls = make(map[string]int)
	for index := 2; index <= 3; index++ {
		if err := h.admit(t, index, true); err != nil {
			t.Fatalf("begin %d: %v", index, err)
		}
	}
	if h.reconciler.calls["request-0001"] != 2 {
		t.Fatalf("begin skipped the abandoned request without re-checking it: %v", h.reconciler.calls)
	}
	for name, mutate := range map[string]func(*piServiceReconciliation){
		"owner checkpoint changed": func(e *piServiceReconciliation) { e.OwnerCheckpoint = digestBytes([]byte("rolled back")) },
		"publication changed":      func(e *piServiceReconciliation) { e.Publication = digestBytes([]byte("other")) },
		"provider changed":         func(e *piServiceReconciliation) { e.Provider = digestBytes([]byte("other")) },
		"drain proof changed":      func(e *piServiceReconciliation) { e.OwnerDrainProofs[1] = digestBytes([]byte("other")) },
		"owner epoch added": func(e *piServiceReconciliation) {
			e.OwnerDrainProofs = append(e.OwnerDrainProofs, digestBytes([]byte("third")))
		},
		"workflow changed": func(e *piServiceReconciliation) { e.WorkflowID = "workflow-other" },
	} {
		h.reconciler.mutate = func(_ string, evidence *piServiceReconciliation) { mutate(evidence) }
		if err := h.admit(t, 4, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
			t.Fatalf("%s: the barrier was lifted on evidence that no longer matches: %v", name, err)
		}
	}
	h.reconciler.mutate = nil
	h.reconciler.failure["request-0001"] = fmt.Errorf("%w: owner not drained", ErrPiServiceUnreconciled)
	if err := h.admit(t, 4, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
		t.Fatalf("a workflow that is no longer reconciled still lifted the barrier: %v", err)
	}
	delete(h.reconciler.failure, "request-0001")
	if err := h.admit(t, 4, true); err != nil {
		t.Fatalf("re-proven evidence did not lift the barrier again: %v", err)
	}
	h.close(t)

	// After a restart the record is restored from the journal and is still re-checked.
	restarted := openIncrementalJournal(t, path)
	defer restarted.close(t)
	restarted.reconciler.mutate = func(_ string, evidence *piServiceReconciliation) { evidence.Provider = digestBytes([]byte("restart")) }
	if err := restarted.admit(t, 5, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
		t.Fatalf("a restart skipped the abandoned request on changed evidence: %v", err)
	}
	restarted.reconciler.mutate = nil
	restarted.requests.setReconciler(nil)
	if err := restarted.admit(t, 5, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
		t.Fatalf("a service without a reconciler skipped the abandoned request: %v", err)
	}
	restarted.requests.setReconciler(restarted.reconciler)
	if err := restarted.admit(t, 5, true); err != nil {
		t.Fatalf("matching evidence after a restart did not lift the barrier: %v", err)
	}
}

// A tampered abandon record keeps the barrier. One whose digest does not commit to its own
// evidence stops the service at start; one that is well formed but binds evidence the host does
// not find (a forged or stale record) loads and keeps the barrier at every begin.
func TestTamperedAbandonRecordKeepsTheBarrier(t *testing.T) {
	appendForged := func(t *testing.T, path string, mutate func(*piServiceRequestAbandonedEvent)) {
		t.Helper()
		h := openIncrementalJournal(t, path)
		if err := h.admitUnknown(t, 1); err != nil {
			t.Fatal(err)
		}
		state := h.requests.entries[piServiceRequestKey(incrementalPeer().UID, "request-0001")]
		event, err := newPiServiceRequestAbandonedEvent(state, "forged", goodReconciliation("request-0001"))
		if err != nil {
			t.Fatal(err)
		}
		mutate(&event)
		data, err := marshalPiServiceRequestAbandonedEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		key := piServiceRequestKey(incrementalPeer().UID, "request-0001")
		if _, err := h.audit.Append(audit.Event{Kind: "pi_service_request_abandoned", ID: key + "/abandoned", Data: data}); err != nil {
			t.Fatal(err)
		}
		h.close(t)
	}
	t.Run("digest does not commit to the evidence", func(t *testing.T) {
		path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
		appendForged(t, path, func(e *piServiceRequestAbandonedEvent) { e.ReconciliationDigest = digestBytes([]byte("other")) })
		journal, err := audit.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		if _, err := newPiServiceRequestJournal(journal); err == nil {
			t.Fatal("an abandon record whose digest does not commit to its evidence was accepted at start")
		}
	})
	t.Run("evidence that is not valid", func(t *testing.T) {
		path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
		appendForged(t, path, func(e *piServiceRequestAbandonedEvent) {
			e.Reconciliation.OwnerDrainProofs = nil
			e.ReconciliationDigest, _ = e.Reconciliation.digest()
		})
		journal, err := audit.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		if _, err := newPiServiceRequestJournal(journal); err == nil {
			t.Fatal("an abandon record without a drain proof was accepted at start")
		}
	})
	t.Run("well formed record of evidence the host does not find", func(t *testing.T) {
		path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
		appendForged(t, path, func(e *piServiceRequestAbandonedEvent) {
			e.Reconciliation.Publication = digestBytes([]byte("forged publication state"))
			e.ReconciliationDigest, _ = e.Reconciliation.digest()
		})
		h := openIncrementalJournal(t, path)
		defer h.close(t)
		for attempt := 0; attempt < 2; attempt++ {
			if err := h.admit(t, 2, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
				t.Fatalf("attempt %d: a forged abandon record lifted the barrier: %v", attempt, err)
			}
		}
	})
}

// Across a reboot the drain-proof digests differ by construction (they commit to the boot id
// they were observed on), so they are re-proven but not compared. Everything else still is.
func TestReconciliationEvidenceAcrossABootRequiresReprovenDrainButEqualState(t *testing.T) {
	bound := goodReconciliation("request-0001")
	rebooted := goodReconciliation("request-0001")
	rebooted.OwnerDrainBootID = "boot-two"
	rebooted.OwnerDrainProofs = []string{digestBytes([]byte("new one")), digestBytes([]byte("new two"))}
	if !bound.stillHolds(rebooted) {
		t.Fatal("a re-proven drain after a reboot does not hold")
	}
	fewer := rebooted
	fewer.OwnerDrainProofs = fewer.OwnerDrainProofs[:1]
	if bound.stillHolds(fewer) {
		t.Fatal("a drain over fewer owner epochs holds")
	}
	sameBootChanged := goodReconciliation("request-0001")
	sameBootChanged.OwnerDrainProofs[0] = digestBytes([]byte("changed"))
	if bound.stillHolds(sameBootChanged) {
		t.Fatal("a changed drain proof on the same boot holds")
	}
	other := rebooted
	other.Provider = digestBytes([]byte("other"))
	if bound.stillHolds(other) {
		t.Fatal("changed provider state holds across a reboot")
	}
}
