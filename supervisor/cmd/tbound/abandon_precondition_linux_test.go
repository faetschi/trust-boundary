//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// The service keeps the request journal, and with it abandon's lock, until every request task has ended. A
// listener that fails while the service context is live (here the listener is closed under the serve loop)
// must not end servePiServiceRequests, and so close the journal, while a task is still running.
func TestServiceKeepsTheRequestJournalUntilItsTasksEnd(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := newPiServiceTestServer(t, piServiceSocketRegistration{
		AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid(),
	}, func(_ context.Context, request piServiceRequest) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{"schema_version":"tbound-production-pi-receipt/v1","success":true,"request_id":"` + request.RequestID + `"}`), nil
	})
	responded := make(chan piServiceResponse, 1)
	go func() {
		responded <- roundTripPiServiceRequest(t, server.path, piServiceRequest{RequestID: "request-held-1", TaskID: "task-1", Prompt: "read task.txt"})
	}()
	<-started
	_ = server.listener.Close()
	select {
	case err := <-server.done:
		close(release)
		t.Fatalf("the serve loop ended, releasing the request journal, while a request task still ran: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	if response := <-responded; response.Status != "completed" {
		t.Fatalf("the running request did not complete: %+v", response)
	}
	select {
	case <-server.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the serve loop did not end after its last task")
	}
	server.done <- nil // the fixture's cleanup waits for one result
}
