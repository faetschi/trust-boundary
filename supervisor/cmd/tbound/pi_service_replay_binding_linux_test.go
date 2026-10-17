//go:build linux

package main

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// restartedCorpusCatalog is the catalog a restarted service would hold: the same
// reserved entries, bound to the corpus the restarted service loaded.
func restartedCorpusCatalog(h *corpusServiceHarness, corpus *productionCorpus) *piServiceWorkflowCatalog {
	h.catalog.mu.Lock()
	defer h.catalog.mu.Unlock()
	entries := make(map[string]piServiceWorkflowEntry, len(h.catalog.entries))
	for key, entry := range h.catalog.entries {
		entries[key] = entry
	}
	byHost := make(map[string]string, len(h.catalog.byHostAdmissionID))
	for key, value := range h.catalog.byHostAdmissionID {
		byHost[key] = value
	}
	return &piServiceWorkflowCatalog{entries: entries, byHostAdmissionID: byHost,
		ownerScopes: make(map[string]piServiceWorkflowOwnerScope), corpus: corpus}
}

// A request that completed under one manifest, service config or registration is not
// replayed to a service that is bound to another. The refusal consumes nothing: the
// runner is not called and the journal gains no record.
func TestCorpusServiceTerminalReplayIsRefusedWhenTheBindingChanged(t *testing.T) {
	h := newCorpusServiceHarness(t)
	request := corpusRequest("tbr-replay-1", corpusTestCase, "compliant", 1)
	if response := roundTripPiServiceRequest(t, h.server.path, request); response.Status != "completed" {
		t.Fatalf("first admission: %+v", response)
	}
	if response := roundTripPiServiceRequest(t, h.server.path, request); response.Status != "completed" || h.server.calls.Load() != 1 {
		t.Fatalf("unchanged binding did not replay: %+v calls=%d", response, h.server.calls.Load())
	}

	changed := corpusTestManifest(t)
	for index := range changed.Cases {
		changed.Cases[index].Trial.Prompt += " (changed after the first run)"
	}
	changedConfig, changedDigest := installCorpusTestManifest(t, changed)
	if changedDigest == h.digest {
		t.Fatal("changed manifest has the same digest")
	}
	changedCorpus, err := loadProductionCorpus(changedConfig)
	if err != nil {
		t.Fatal(err)
	}
	same := restartedCorpusCatalog(h, h.catalog.corpus)
	for name, tc := range map[string]struct {
		catalog        *piServiceWorkflowCatalog
		config         piServiceConfig
		configDigest   string
		registrationID string
	}{
		"manifest pinned by the new config": {restartedCorpusCatalog(h, changedCorpus), changedConfig, h.configDigest, h.registrationID},
		"service config digest":             {same, h.config, corpusTestDigest("another service config"), h.registrationID},
		"registration identity":             {same, h.config, h.configDigest, corpusTestDigest("another registration")},
	} {
		h.server.requests.setTerminalReplayCheck(tc.catalog.terminalReplayCheck(tc.config, tc.configDigest, tc.registrationID))
		response := roundTripPiServiceRequest(t, h.server.path, request)
		if response.Status != "refused" || response.ErrorCode != "replay-binding-mismatch" || len(response.Receipt) != 0 {
			t.Fatalf("%s: stale terminal was replayed or the refusal is unclear: %+v", name, response)
		}
		if h.server.calls.Load() != 1 || h.journalRecords(t) != 2 {
			t.Fatalf("%s: refusal consumed an admission: calls=%d records=%d", name, h.server.calls.Load(), h.journalRecords(t))
		}
	}

	h.server.requests.setTerminalReplayCheck(same.terminalReplayCheck(h.config, h.configDigest, h.registrationID))
	if response := roundTripPiServiceRequest(t, h.server.path, request); response.Status != "completed" {
		t.Fatalf("the original binding no longer replays: %+v", response)
	}
}

// A request that passed the first screen while another task held the active slot is
// screened again once it holds the slot. If the other task reserved the same admission
// meanwhile, the request is refused unjournaled instead of becoming a terminal unknown
// that consumes its id and blocks later work.
func TestCorpusServiceRescreensAfterAcquiringTheActiveSlot(t *testing.T) {
	h := newCorpusServiceHarness(t)
	first := corpusRequest("tbr-race-a", corpusTestCase, "compliant", 1)
	second := corpusRequest("tbr-race-b", corpusTestCase, "compliant", 1)
	inResolve, release := make(chan struct{}), make(chan struct{})
	gate := func(request piServiceRequest) {
		if request.RequestID == first.RequestID {
			close(inResolve)
			<-release
		}
	}
	h.beforeResolve.Store(&gate)
	screened, firstDone := make(chan struct{}), make(chan struct{})
	h.server.requests.setAfterScreen(func(request piServiceRequest, active *atomic.Bool) {
		if request.RequestID != second.RequestID {
			return
		}
		close(screened)
		<-firstDone
		for active.Load() { // the first task has answered; wait for it to free the slot
			runtime.Gosched()
		}
	})

	firstResponse := make(chan piServiceResponse, 1)
	go func() { firstResponse <- roundTripPiServiceRequest(t, h.server.path, first) }()
	wait := func(name string, ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s", name)
		}
	}
	wait("the first task to start", inResolve)
	secondResponse := make(chan piServiceResponse, 1)
	go func() { secondResponse <- roundTripPiServiceRequest(t, h.server.path, second) }()
	wait("the second request to pass the first screen", screened)
	close(release)
	select {
	case response := <-firstResponse:
		if response.Status != "completed" {
			t.Fatalf("first request: %+v", response)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the first request")
	}
	close(firstDone)
	select {
	case response := <-secondResponse:
		if response.Status != "refused" || response.ErrorCode != "admission-already-used" {
			t.Fatalf("second request was not refused by the second screen: %+v", response)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the second request")
	}
	if h.server.calls.Load() != 1 || h.journalRecords(t) != 2 {
		t.Fatalf("the refused request was journaled or run: calls=%d records=%d", h.server.calls.Load(), h.journalRecords(t))
	}
	// Nothing is blocked: a different admission (attempt 2) is still accepted.
	h.server.requests.setAfterScreen(nil)
	next := corpusRequest("tbr-race-c", corpusTestCase, "compliant", 2)
	if response := roundTripPiServiceRequest(t, h.server.path, next); response.Status != "completed" {
		t.Fatalf("later work stayed blocked: %+v", response)
	}
}
