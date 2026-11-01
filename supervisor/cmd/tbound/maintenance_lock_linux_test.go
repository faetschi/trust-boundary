//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The reset (root) creates the lock file; the service (uid 995) can only open it. In the tests both
// sides run as the test user, which stands for root as the owner of the file.
func testMaintenanceLocks(t *testing.T) (service, reset maintenanceLock) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "maintenance.lock")
	uid := uint32(os.Geteuid())
	return maintenanceLock{path: path, ownerUID: uid}, maintenanceLock{path: path, ownerUID: uid, create: true}
}

func TestMaintenanceLockExcludesTheServiceAndResetsBothWays(t *testing.T) {
	service, reset := testMaintenanceLocks(t)
	// The service never creates the file: before the first reset (or tmpfiles) it is unavailable.
	if _, err := service.acquire(); !errors.Is(err, errMaintenanceUnavailable) {
		t.Fatalf("the service acquired a lock file that does not exist: %v", err)
	}
	if _, err := os.Stat(service.path); err == nil {
		t.Fatal("the service created the lock file")
	}
	releaseReset, err := reset.acquire()
	if err != nil {
		t.Fatal(err)
	}
	// A running reset refuses the admission as busy, and a second reset refuses to run.
	if _, err := service.acquire(); !errors.Is(err, errMaintenanceBusy) {
		t.Fatalf("the service was admitted during a reset: %v", err)
	}
	if _, err := reset.acquire(); !errors.Is(err, errMaintenanceBusy) {
		t.Fatalf("two resets ran at once: %v", err)
	}
	releaseReset()
	releaseReset() // idempotent
	releaseService, err := service.acquire()
	if err != nil {
		t.Fatalf("the service could not take a released lock: %v", err)
	}
	// An admitted workflow keeps a reset out until it is settled.
	if _, err := reset.acquire(); !errors.Is(err, errMaintenanceBusy) {
		t.Fatalf("a reset ran during an admitted workflow: %v", err)
	}
	releaseService()
	if release, err := reset.acquire(); err != nil {
		t.Fatalf("the reset could not take a released lock: %v", err)
	} else {
		release()
	}
}

func TestMaintenanceLockRefusesAFileTheServiceCouldHaveMade(t *testing.T) {
	service, reset := testMaintenanceLocks(t)
	release, err := reset.acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
	info, err := os.Stat(reset.path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("the reset must leave the lock file 0644 so that the service can open it: %v %v", info, err)
	}
	wrongOwner := service
	wrongOwner.ownerUID = service.ownerUID + 1
	if _, err := wrongOwner.acquire(); !errors.Is(err, errMaintenanceUnavailable) {
		t.Errorf("a lock file with another owner was taken: %v", err)
	}
	if err := os.Chmod(reset.path, 0o664); err != nil {
		t.Fatal(err)
	}
	if _, err := service.acquire(); !errors.Is(err, errMaintenanceUnavailable) {
		t.Errorf("a group-writable lock file was taken: %v", err)
	}
	if err := os.Chmod(reset.path, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.lock")
	if err := os.Symlink(reset.path, link); err != nil {
		t.Fatal(err)
	}
	linked := service
	linked.path = link
	if _, err := linked.acquire(); !errors.Is(err, errMaintenanceUnavailable) {
		t.Errorf("a symlinked lock file was followed: %v", err)
	}
}

// A fake service holds the lock for the whole life of an admitted workflow: the reset is refused
// until the task has settled, and the next admission is refused busy while a reset runs.
func TestServiceHoldsTheMaintenanceLockUntilTheWorkflowSettles(t *testing.T) {
	service, reset := testMaintenanceLocks(t)
	if release, err := reset.acquire(); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	started := make(chan struct{})
	finish := make(chan struct{})
	registration := piServiceSocketRegistration{AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid()}
	var runs atomic.Int32
	runTask := func(ctx context.Context, request piServiceRequest) (json.RawMessage, error) {
		runs.Add(1)
		close(started)
		select {
		case <-finish:
		case <-ctx.Done():
		}
		return json.RawMessage(`{"success":true,"request_id":"` + request.RequestID + `"}`), nil
	}
	var acquired, released atomic.Int32
	counting := func() (func(), error) {
		release, err := service.acquire()
		if err != nil {
			return nil, err
		}
		acquired.Add(1)
		return func() { released.Add(1); release() }, nil
	}
	server := newPiServiceTestServerLocked(t, registration, runTask, counting)
	first := make(chan piServiceResponse, 1)
	go func() {
		first <- roundTripPiServiceRequest(t, server.path, piServiceRequest{RequestID: "request-1", TaskID: "task-1", Prompt: "work"})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the task did not start")
	}
	if _, err := reset.acquire(); !errors.Is(err, errMaintenanceBusy) {
		t.Fatalf("a reset ran while a workflow was admitted: %v", err)
	}
	close(finish)
	if response := <-first; response.Status != "completed" {
		t.Fatalf("the task did not complete: %+v", response)
	}
	deadline := time.Now().Add(5 * time.Second)
	for released.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if acquired.Load() != 1 || released.Load() != 1 {
		t.Fatalf("the lock was acquired %d times and released %d times for one workflow", acquired.Load(), released.Load())
	}
	var release func()
	for {
		var err error
		if release, err = reset.acquire(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lock was not released when the workflow settled: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A reset in progress refuses the admission as busy before anything is journaled or run.
	refused := roundTripPiServiceRequest(t, server.path, piServiceRequest{RequestID: "request-2", TaskID: "task-2", Prompt: "work"})
	if refused.Status != "busy" || refused.ErrorCode != "maintenance-in-progress" || runs.Load() != 1 {
		t.Fatalf("an admission during a reset: %+v (runs %d)", refused, runs.Load())
	}
	release()
}

func TestServiceRefusesAdmissionWhenTheMaintenanceLockIsUnavailable(t *testing.T) {
	service, _ := testMaintenanceLocks(t) // no reset ever created the file
	registration := piServiceSocketRegistration{AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid()}
	runTask := func(context.Context, piServiceRequest) (json.RawMessage, error) {
		t.Error("the task ran without the maintenance lock")
		return nil, errors.New("must not run")
	}
	server := newPiServiceTestServerLocked(t, registration, runTask, service.acquire)
	response := roundTripPiServiceRequest(t, server.path, piServiceRequest{RequestID: "request-1", TaskID: "task-1", Prompt: "work"})
	if response.Status != "refused" || response.ErrorCode != "maintenance-lock-unavailable" {
		t.Fatalf("admission without a lock file: %+v", response)
	}
}

// The slot and the maintenance lock are released before the terminal answer is written. A client that
// has read "completed" may send its next request at once; a release that lagged the answer refused
// that request as busy (seen on a hosted runner).
func TestServiceFreesTheSlotBeforeItWritesTheTerminalAnswer(t *testing.T) {
	registration := piServiceSocketRegistration{AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid()}
	var released atomic.Bool
	slowRelease := func() (func(), error) {
		return func() { time.Sleep(300 * time.Millisecond); released.Store(true) }, nil
	}
	runTask := func(ctx context.Context, request piServiceRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"success":true,"request_id":"` + request.RequestID + `"}`), nil
	}
	server := newPiServiceTestServerLocked(t, registration, runTask, slowRelease)
	first := roundTripPiServiceRequest(t, server.path, piServiceRequest{RequestID: "request-1", TaskID: "task-1", Prompt: "work"})
	if first.Status != "completed" {
		t.Fatalf("the first task did not complete: %+v", first)
	}
	if !released.Load() {
		t.Fatal("the client read the terminal answer while the slot and the maintenance lock were still held")
	}
	if next := roundTripPiServiceRequest(t, server.path, piServiceRequest{RequestID: "request-2", TaskID: "task-2", Prompt: "work"}); next.Status == "busy" {
		t.Fatalf("the request after a completed answer was refused as busy: %+v", next)
	}
}

// The terminal is visible to a retry as soon as it is journaled. A retry that read it ahead of the
// release would send its next request into a slot that is still held, the same refusal by another path.
func TestServiceAnswersAReplayedTerminalOnlyAfterTheSlotIsFree(t *testing.T) {
	registration := piServiceSocketRegistration{AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid()}
	releasing, retryAtLookup := make(chan struct{}), make(chan struct{})
	var releasingOnce, retryAtLookupOnce sync.Once
	var released atomic.Bool
	// The release outlasts the retry's arrival at the lookup, however late the retry is scheduled.
	slowRelease := func() (func(), error) {
		return func() {
			releasingOnce.Do(func() { close(releasing) })
			select {
			case <-retryAtLookup:
			case <-time.After(5 * time.Second):
			}
			time.Sleep(300 * time.Millisecond)
			released.Store(true)
		}, nil
	}
	runTask := func(ctx context.Context, request piServiceRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"success":true,"request_id":"` + request.RequestID + `"}`), nil
	}
	server := newPiServiceTestServerLocked(t, registration, runTask, slowRelease)
	// The screen runs immediately before the lookup, so it marks the retry's arrival there.
	server.requests.setAdmissionScreen(func(piServicePeerCredentials, piServiceRequest) string {
		select {
		case <-releasing:
			retryAtLookupOnce.Do(func() { close(retryAtLookup) })
		default:
		}
		return ""
	})
	request := piServiceRequest{RequestID: "request-1", TaskID: "task-1", Prompt: "work"}
	firstDone := make(chan piServiceResponse, 1)
	go func() { firstDone <- roundTripPiServiceRequest(t, server.path, request) }()
	select {
	case <-releasing:
	case <-time.After(5 * time.Second):
		t.Fatal("the task never reached its release")
	}
	replay := roundTripPiServiceRequest(t, server.path, request)
	if replay.Status != "completed" {
		t.Fatalf("the retry did not return the stored terminal: %+v", replay)
	}
	if !released.Load() {
		t.Fatal("the retry read the terminal while the slot and the maintenance lock were still held")
	}
	if next := roundTripPiServiceRequest(t, server.path, piServiceRequest{RequestID: "request-2", TaskID: "task-2", Prompt: "work"}); next.Status == "busy" {
		t.Fatalf("the request after a replayed terminal was refused as busy: %+v", next)
	}
	if first := <-firstDone; first.Status != "completed" {
		t.Fatalf("the first task did not complete: %+v", first)
	}
	if calls := server.calls.Load(); calls != 1 {
		t.Fatalf("the runner ran %d times, the retry must not run it again", calls)
	}
}

// A retry can read its request as unsettled just before the running task settles it and frees the slot.
// It must then answer with the stored terminal instead of taking the slot and running the task again.
func TestServiceAnswersARetryThatReadItsRequestBeforeItSettled(t *testing.T) {
	registration := piServiceSocketRegistration{AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid()}
	started, finish := make(chan struct{}), make(chan struct{})
	var startedOnce sync.Once
	runTask := func(ctx context.Context, request piServiceRequest) (json.RawMessage, error) {
		startedOnce.Do(func() { close(started) })
		<-finish
		return json.RawMessage(`{"success":true,"request_id":"` + request.RequestID + `"}`), nil
	}
	server := newPiServiceTestServer(t, registration, runTask)
	request := piServiceRequest{RequestID: "request-1", TaskID: "task-1", Prompt: "work"}
	firstDone, settled := make(chan piServiceResponse, 1), make(chan struct{})
	go func() {
		firstDone <- roundTripPiServiceRequest(t, server.path, request)
		close(settled)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the task never started")
	}
	retryRead := make(chan struct{})
	server.requests.setAfterScreen(func(piServiceRequest, *atomic.Bool) {
		close(retryRead)
		<-settled
	})
	retryDone := make(chan piServiceResponse, 1)
	go func() { retryDone <- roundTripPiServiceRequest(t, server.path, request) }()
	select {
	case <-retryRead:
	case <-time.After(5 * time.Second):
		t.Fatal("the retry never read its request")
	}
	close(finish)
	first, retry := <-firstDone, <-retryDone
	if first.Status != "completed" || retry.Status != "completed" || string(retry.Receipt) != string(first.Receipt) {
		t.Fatalf("the retry did not return the stored terminal: first %+v, retry %+v", first, retry)
	}
	if calls := server.calls.Load(); calls != 1 {
		t.Fatalf("the runner ran %d times, the retry must not run it again", calls)
	}
}
