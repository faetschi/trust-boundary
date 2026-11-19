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
	"tbound/supervisor/internal/workflowrecovery"
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
		// The agent's own budget end is a treatment failure, never a rerun cause (thesis §7.5).
		"workflow deadline refusal": {sessionErr(fmt.Errorf("durably charge exact provider request and completion cap before HTTP dispatch: %w",
			workflowrecovery.ErrDeadline)), "run-budget-exhausted", ""},
		"token allowance refusal": {sessionErr(errors.Join(errors.New("host selected no positive provider completion-token cap"),
			workflowrecovery.ErrExhausted)), "run-budget-exhausted", ""},
		"effect allowance refusal": {errors.Join(fmt.Errorf("governed production channels failed before the turn ended: %w",
			governedChannelsError(fmt.Errorf("%w: %w", workflowrecovery.ErrEffectNotReserved, workflowrecovery.ErrExhausted), nil))),
			"run-budget-exhausted", ""},
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

// serveFailedTurn runs the next provider turn of f through Serve, the way the governed loops do, and returns the run
// error the service types and the loops' error that cleanup reads.
func serveFailedTurn(t *testing.T, f *workflowVerticalFixture) (runErr, channelsErr error) {
	t.Helper()
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
	writeProviderNextFrame(t, clientConn, 1, "failed-next")
	var bridgeErr error
	select {
	case bridgeErr = <-bridgeDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the bridge did not stop after the failed turn")
	}
	channelsErr = f.authority.heldCallChannelsError(governedChannelsError(nil, bridgeErr))
	return errors.Join(fmt.Errorf("governed production channels failed before the turn ended: %w", channelsErr), nil), channelsErr
}

// unknownAnswerOf is the service's answer to a run that returned runErr.
func unknownAnswerOf(t *testing.T, runErr error) piServiceResponse {
	t.Helper()
	server := newPiServiceTestServer(t, piServiceSocketRegistration{
		AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid(),
	}, func(context.Context, piServiceRequest) (json.RawMessage, error) { return nil, runErr })
	return roundTripPiServiceRequest(t, server.path, piServiceRequest{RequestID: "request-failed-turn-1", TaskID: "task-1", Prompt: "read task.txt"})
}

// The whole chain on the real workflow ledger: the provider answers 503 through the bounded retries, the bridge
// journals the last attempt as provider_exchange_unknown and Serve types the turn failure; the governed loops' error
// carries the outage to the service's typing with that record's hash; cleanup runs and abandon settles the request.
func TestProviderOutageThroughServeIsTypedWithItsJournalRecord(t *testing.T) {
	f := hostCallCapWorkflow(t, []providerbridge.DevelopmentProviderTurn{capRetryable, capRetryable, capRetryable}, 0)
	runErr, channelsErr := serveFailedTurn(t, f)
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	last := trace.Records[len(trace.Records)-1]
	if answer := unknownAnswerOf(t, runErr); answer.ErrorCode != piServiceProviderOutage ||
		last.Event.Kind != "provider_exchange_unknown" || answer.OutageEvidenceDigest != last.Hash {
		t.Fatalf("answer %+v, journal ends with %s %q; run error %v", answer, last.Event.Kind, last.Hash, runErr)
	}
	h := newUnknownRequestHarness(t, f)
	if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), newProductionCommandRunner(nil)) {
		t.Fatalf("cleanup stayed quarantined after the outage: %v", channelsErr)
	}
	if abandoned := h.abandoned(t, "provider outage; operator recovery"); abandoned.Reconciliation.WorkflowID != workflowVerticalID {
		t.Fatalf("abandon did not settle the workflow: %+v", abandoned.Reconciliation)
	}
}

// The F24 403 is a refusal of the key, and content can provoke a 403 (moderation): it is no outage. The turn
// failure is journaled, ends task-outcome-unknown (the host's outcome_unknown, the same rerun), and still cleans up
// and abandons.
func TestProvider403IsAPlainUnknownThatStillSettles(t *testing.T) {
	f := hostCallCapWorkflow(t, []providerbridge.DevelopmentProviderTurn{{ResponseID: "refused", FailStatus: 403,
		FailBody: `{"error":{"type":"forbidden","message":"An active OpenCode Go subscription is required"}}`}}, 0)
	runErr, channelsErr := serveFailedTurn(t, f)
	if _, turnFailure := channelsErr.(providerTurnFailure); !turnFailure {
		t.Fatalf("a 403 is a journaled provider turn failure: %v", channelsErr)
	}
	if answer := unknownAnswerOf(t, runErr); answer.ErrorCode != "task-outcome-unknown" || answer.OutageEvidenceDigest != "" {
		t.Fatalf("answer %+v; run error %v", answer, runErr)
	}
	h := newUnknownRequestHarness(t, f)
	if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), newProductionCommandRunner(nil)) {
		t.Fatalf("cleanup stayed quarantined after the 403: %v", channelsErr)
	}
	if abandoned := h.abandoned(t, "provider refusal; operator recovery"); abandoned.Reconciliation.WorkflowID != workflowVerticalID {
		t.Fatalf("abandon did not settle the workflow: %+v", abandoned.Reconciliation)
	}
}

// The real workflow ledger refuses the next provider turn once the read turn charged the whole token allowance:
// the run's budget end answers run-budget-exhausted, which the host types resource_exhaustion, never a rerun cause.
func TestTokenAllowanceEndThroughServeIsABudgetEnd(t *testing.T) {
	f := limitedWorkflow(t, []providerbridge.DevelopmentProviderTurn{capRead(1), capRead(2)}, 1, 7, 1024)
	runErr, _ := serveFailedTurn(t, f)
	if answer := unknownAnswerOf(t, runErr); answer.ErrorCode != "run-budget-exhausted" || answer.OutageEvidenceDigest != "" {
		t.Fatalf("answer %+v; run error %v", answer, runErr)
	}
}
