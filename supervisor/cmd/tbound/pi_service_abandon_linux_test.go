//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
)

// admitUnknown journals request index as accepted and then as an unknown terminal, the
// state a task outage or a lost outcome leaves.
func (h *incrementalJournal) admitUnknown(t testing.TB, index int) error {
	t.Helper()
	request, digest := incrementalRequest(index)
	if _, err := h.requests.begin(context.Background(), incrementalPeer(), request, digest); err != nil {
		return err
	}
	response := piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID,
		Status: "unknown", ErrorCode: "task-outcome-unknown"}
	if err := h.requests.complete(incrementalPeer().UID, request, digest, response); err != nil {
		t.Fatal(err)
	}
	return nil
}

func journalKinds(t testing.TB, journal *audit.Journal) []string {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(trace.Records))
	for _, record := range trace.Records {
		kinds = append(kinds, record.Event.Kind)
	}
	return kinds
}

// An unknown terminal blocks every later request. The admin settlement unblocks them,
// appends rather than rewrites, and survives a service restart.
func TestAbandonUnknownUnblocksLaterRequestsAndKeepsTheOriginalEntry(t *testing.T) {
	path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
	h := openIncrementalJournal(t, path)
	for index, unknown := range []bool{false, true} {
		var err error
		if unknown {
			err = h.admitUnknown(t, index+1)
		} else {
			err = h.admit(t, index+1, true)
		}
		if err != nil {
			t.Fatalf("seed %d: %v", index+1, err)
		}
	}
	if err := h.admit(t, 3, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
		t.Fatalf("an unknown terminal did not block before the settlement: %v", err)
	}
	before, err := h.audit.Trace()
	if err != nil {
		t.Fatal(err)
	}

	record, err := h.requests.abandonUnknown(context.Background(), "request-0002", "provider outage 2026-10-17, operator confirmed no effect")
	if err != nil || record.Event.Kind != "pi_service_request_abandoned" {
		t.Fatalf("abandon: record=%+v err=%v", record, err)
	}
	if err := h.admit(t, 3, true); err != nil {
		t.Fatalf("a later request stayed blocked after the settlement: %v", err)
	}

	after, err := h.audit.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for index, original := range before.Records {
		if after.Records[index].Hash != original.Hash {
			t.Fatalf("settlement rewrote record %d", index+1)
		}
	}
	if got := journalKinds(t, h.audit)[len(before.Records)]; got != "pi_service_request_abandoned" {
		t.Fatalf("settlement was not appended directly after the original entries: %s", got)
	}
	peer := incrementalPeer()
	request, digest := incrementalRequest(2)
	state, exists, err := h.requests.lookup(peer, request, digest)
	if err != nil || !exists || state.terminal == nil || state.terminal.Status != "unknown" ||
		state.terminal.ErrorCode != "task-outcome-unknown" {
		t.Fatalf("the original unknown terminal is not retained for replay: %+v exists=%v err=%v", state.terminal, exists, err)
	}
	h.close(t)

	// A fresh service start restores the settlement from the journal, still verifies
	// the completed priors, and does not ask the verifier about the abandoned one.
	restarted := openIncrementalJournal(t, path)
	defer restarted.close(t)
	if err := restarted.admit(t, 4, true); err != nil {
		t.Fatalf("the settlement was not restored after a restart: %v", err)
	}
	if restarted.calls["request-0001"] != 1 || restarted.calls["request-0003"] != 1 || restarted.calls["request-0002"] != 0 {
		t.Fatalf("restart verification covered the wrong priors: %v", restarted.calls)
	}
}

// A second unknown terminal still blocks until it is settled too.
func TestAbandonUnknownIsPerRequest(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	if err := h.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.requests.abandonUnknown(context.Background(), "request-0001", "outage one"); err != nil {
		t.Fatal(err)
	}
	if err := h.admitUnknown(t, 2); err != nil {
		t.Fatalf("admission after the first settlement: %v", err)
	}
	if err := h.admit(t, 3, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
		t.Fatalf("a later unknown terminal did not block: %v", err)
	}
}

// Only an unknown terminal can be settled this way. A completed request, an accepted
// request without a terminal (the exact-id replay resolves that one), an unknown id, a
// second settlement and a missing reason are refused and write nothing.
func TestAbandonUnknownRefusesEverythingExceptAnUnsettledUnknownTerminal(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	if err := h.admit(t, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := h.admitUnknown(t, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := h.requests.abandonUnknown(context.Background(), "request-0002", "outage"); err != nil {
		t.Fatal(err)
	}
	if err := h.admit(t, 3, false); err != nil {
		t.Fatal(err)
	}
	records := len(journalKinds(t, h.audit))
	for name, tc := range map[string]struct{ id, reason string }{
		"completed request":        {"request-0001", "reason"},
		"already abandoned":        {"request-0002", "reason again"},
		"accepted without outcome": {"request-0003", "reason"},
		"unknown request id":       {"request-9999", "reason"},
		"empty reason":             {"request-0002", ""},
		"blank reason":             {"request-0002", "   "},
		"control character":        {"request-0002", "bad\nreason"},
		"oversized reason":         {"request-0002", strings.Repeat("r", 513)},
	} {
		if _, err := h.requests.abandonUnknown(context.Background(), tc.id, tc.reason); err == nil {
			t.Fatalf("%s: settlement was accepted", name)
		}
	}
	if got := len(journalKinds(t, h.audit)); got != records {
		t.Fatalf("a refused settlement wrote %d records", got-records)
	}
}

// A settlement that does not name the unknown terminal it settles is not accepted at
// service start, so a forged or mismatched record cannot unblock the journal.
func TestAbandonUnknownRecordIsBoundToTheTerminalAtServiceStart(t *testing.T) {
	cases := map[string]func(*piServiceRequestAbandonedEvent){
		"another terminal digest": func(e *piServiceRequestAbandonedEvent) { e.TerminalDigest = digestBytes([]byte("other")) },
		"another payload digest":  func(e *piServiceRequestAbandonedEvent) { e.PayloadDigest = digestBytes([]byte("other")) },
		"another settlement":      func(e *piServiceRequestAbandonedEvent) { e.Settlement = "completed" },
		"another schema":          func(e *piServiceRequestAbandonedEvent) { e.SchemaVersion = "tbound-pi-service-request-abandoned/v0" },
		"empty reason":            func(e *piServiceRequestAbandonedEvent) { e.Reason = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
			h := openIncrementalJournal(t, path)
			if err := h.admitUnknown(t, 1); err != nil {
				t.Fatal(err)
			}
			state := h.requests.entries[piServiceRequestKey(incrementalPeer().UID, "request-0001")]
			event, err := newPiServiceRequestAbandonedEvent(state, "outage", goodReconciliation("request-0001"))
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
			journal, err := audit.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			if _, err := newPiServiceRequestJournal(journal); err == nil {
				t.Fatal("a settlement that does not bind the unknown terminal was accepted at service start")
			}
		})
	}
}

// The admin command is root-only: a non-root caller is refused before the journal or
// the service registration is touched.
func TestAbandonUnknownCommandIsRootOnly(t *testing.T) {
	previous := abandonUnknownEffectiveUID
	abandonUnknownEffectiveUID = func() int { return 1000 }
	defer func() { abandonUnknownEffectiveUID = previous }()
	var stdout, stderr bytes.Buffer
	err := runAbandonUnknown([]string{"--request-id", "request-0001", "--reason", "outage"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "root") || stdout.Len() != 0 {
		t.Fatalf("non-root caller was not refused: err=%v stdout=%q", err, stdout.String())
	}
	for _, args := range [][]string{{}, {"--request-id", "r"}, {"--reason", "x"}, {"--request-id", "r", "--reason", "x", "extra"}} {
		abandonUnknownEffectiveUID = func() int { return 0 }
		if err := runAbandonUnknown(args, &stdout, &stderr); err == nil {
			t.Fatalf("arguments %v were accepted", args)
		}
	}
}
