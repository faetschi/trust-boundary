//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/providerbridge"
)

// Register D30 as amended 2026-11-18 (owner decision A2): a run whose provider turn ended on an outage answers
// UNKNOWN with the typed code provider-outage and the hash of its provider_exchange_unknown record; any other
// UNKNOWN keeps task-outcome-unknown and carries no digest.
func TestProviderOutageRunEndsUnknownWithItsTypedCodeAndRecord(t *testing.T) {
	hash := strings.Repeat("5c", 32)
	// The error as the production session returns it: the bridge's turn failure, typed by the governed loops,
	// wrapped by the turn wait and joined with the session's other errors (nil when the rest settled).
	sessionErr := func(turnErr error, others ...error) error {
		bridge := fmt.Errorf("%w; conversation closed: %w", providerbridge.ErrProviderTurnFailed, turnErr)
		return errors.Join(append([]error{fmt.Errorf("governed production channels failed before the turn ended: %w",
			governedChannelsError(nil, bridge)), nil}, others...)...)
	}
	for name, tc := range map[string]struct {
		runErr     error
		wantCode   string
		wantDigest string
	}{
		"outage":              {sessionErr(providerbridge.ProviderOutage{RecordHash: hash}), piServiceProviderOutage, hash},
		"outage without hash": {sessionErr(providerbridge.ProviderOutage{RecordHash: "not-a-hash"}), "task-outcome-unknown", ""},
		"outage and a cleanup failure": {sessionErr(providerbridge.ProviderOutage{RecordHash: hash},
			errors.New("close settled production session before receipt: synthetic")), "task-outcome-unknown", ""},
		"other failure": {sessionErr(errors.New("registered provider exchange failed; response withheld")), "task-outcome-unknown", ""},
	} {
		t.Run(name, func(t *testing.T) {
			server := newPiServiceTestServer(t, piServiceSocketRegistration{
				AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid(),
			}, func(context.Context, piServiceRequest) (json.RawMessage, error) { return nil, tc.runErr })
			response := roundTripPiServiceRequest(t, server.path, piServiceRequest{RequestID: "request-outage-1", TaskID: "task-1", Prompt: "read task.txt"})
			if response.Status != "unknown" || response.ErrorCode != tc.wantCode || response.OutageEvidenceDigest != tc.wantDigest {
				t.Fatalf("response = %+v, want unknown/%s with digest %q", response, tc.wantCode, tc.wantDigest)
			}
			if !strings.Contains(piServiceRefusalMessage(response.Status, response.ErrorCode), "\nhint: ") {
				t.Fatalf("code %s has no hint", response.ErrorCode)
			}
			server.requests.mu.Lock()
			state, ok := server.requests.entries[piServiceRequestKey(os.Geteuid(), "request-outage-1")]
			server.requests.mu.Unlock()
			if !ok || state.terminal == nil || state.terminal.ErrorCode != tc.wantCode || state.terminal.OutageEvidenceDigest != tc.wantDigest {
				t.Fatalf("journaled terminal = %+v", state.terminal)
			}
		})
	}
}

// The whole chain on the real workflow ledger: the provider answers the first turn with the F24 403, the bridge
// journals it as provider_exchange_unknown and Serve types the turn failure; the governed loops' error carries the
// outage to the service's typing with that record's hash; cleanup runs and abandon settles the request.
func TestProviderOutageThroughServeIsTypedWithItsJournalRecord(t *testing.T) {
	f := hostCallCapWorkflow(t, []providerbridge.DevelopmentProviderTurn{{ResponseID: "outage", FailStatus: 403,
		FailBody: `{"error":{"type":"forbidden","message":"An active OpenCode Go subscription is required"}}`}}, 0)
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go func() {
		buffer := make([]byte, 4096)
		for {
			if _, err := clientConn.Read(buffer); err != nil {
				return
			}
		}
	}()
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- providerbridge.Serve(f.ctx, serverConn, f.conversation) }()
	writeProviderNextFrame(t, clientConn, 1, "outage-next")
	var bridgeErr error
	select {
	case bridgeErr = <-bridgeDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the bridge did not stop after the outage")
	}
	channelsErr := f.authority.heldCallChannelsError(governedChannelsError(nil, bridgeErr))
	runErr := errors.Join(fmt.Errorf("governed production channels failed before the turn ended: %w", channelsErr), nil)
	digest, ok := providerOutageEvidence(runErr)
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	last := trace.Records[len(trace.Records)-1]
	if !ok || last.Event.Kind != "provider_exchange_unknown" || digest != last.Hash {
		t.Fatalf("outage evidence %q (%t), journal ends with %s %q; bridge error %v", digest, ok, last.Event.Kind, last.Hash, bridgeErr)
	}
	h := newUnknownRequestHarness(t, f)
	if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), newProductionCommandRunner(nil)) {
		t.Fatalf("cleanup stayed quarantined after the outage: %v", channelsErr)
	}
	if abandoned := h.abandoned(t, "provider outage; operator recovery"); abandoned.Reconciliation.WorkflowID != workflowVerticalID {
		t.Fatalf("abandon did not settle the workflow: %+v", abandoned.Reconciliation)
	}
}
