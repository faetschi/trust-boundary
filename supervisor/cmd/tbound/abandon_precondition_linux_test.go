//go:build linux

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
)

// Abandon acts only on a stopped service. The running service opens the request journal once at start
// with audit.Open and holds it until it exits (pi_service_handoff_linux.go, runPiService), and
// abandon-unknown opens the same path with the same exclusive audit.Open before anything else
// (pi_service_abandon_linux.go, runAbandonUnknown). While the service holds it, the command's open fails
// with audit.ErrAlreadyOpen and nothing is settled. Once the service has closed it, the same open succeeds
// and abandonUnknown then settles only an unknown terminal: a request still running (accepted, no
// terminal) or a completed one is refused (TestAbandonUnknownRefusesEverythingExceptAnUnsettledUnknownTerminal).
func TestAbandonUnknownCannotOpenTheRequestJournalWhileTheServiceHoldsIt(t *testing.T) {
	path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
	service := openIncrementalJournal(t, path)
	if err := service.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	if err := service.admit(t, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := service.admit(t, 3, false); err != nil {
		t.Fatal(err)
	}
	if journal, err := audit.Open(path); !errors.Is(err, audit.ErrAlreadyOpen) {
		if journal != nil {
			_ = journal.Close()
		}
		t.Fatalf("the command opened the request journal while the service held it: %v", err)
	}
	service.close(t)

	journal, err := audit.Open(path)
	if err != nil {
		t.Fatalf("the command could not open the request journal after the service stopped: %v", err)
	}
	stopped, err := newPiServiceRequestJournal(journal)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	defer func() { _ = stopped.close() }()
	for name, id := range map[string]string{"completed request": "request-0002", "request still running": "request-0003"} {
		if _, err := stopped.abandonUnknown(context.Background(), id, "outage"); !errors.Is(err, ErrPiServiceRequestConflict) {
			t.Fatalf("%s: abandon was not refused: %v", name, err)
		}
	}
	// The unknown request reaches the workflow reconciler, which this journal lacks: still refused, nothing settled.
	if _, err := stopped.abandonUnknown(context.Background(), "request-0001", "outage"); !errors.Is(err, ErrPiServiceUnreconciled) {
		t.Fatalf("an unknown request was settled without a workflow reconciler: %v", err)
	}
}
