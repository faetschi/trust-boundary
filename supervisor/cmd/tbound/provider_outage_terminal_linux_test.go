//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/providerbridge"
)

// Register D30 as amended 2026-11-18 (owner decision A2): a run whose provider turn ended on an outage answers
// UNKNOWN with the typed code provider-outage and the hash of its provider_exchange_unknown record; any other
// UNKNOWN keeps task-outcome-unknown and carries no digest.
func TestProviderOutageRunEndsUnknownWithItsTypedCodeAndRecord(t *testing.T) {
	hash := strings.Repeat("5c", 32)
	// The error as the production session returns it: the bridge's turn failure, typed by the governed loops,
	// wrapped by the channel join and joined with a cleanup error.
	sessionErr := func(turnErr error) error {
		bridge := fmt.Errorf("%w; conversation closed: %w", providerbridge.ErrProviderTurnFailed, turnErr)
		return errors.Join(fmt.Errorf("governed production channels did not settle cleanly: %w", governedChannelsError(nil, bridge)),
			errors.New("close composed production session: synthetic"))
	}
	for name, tc := range map[string]struct {
		runErr     error
		wantCode   string
		wantDigest string
	}{
		"outage":              {sessionErr(providerbridge.ProviderOutage{RecordHash: hash}), piServiceProviderOutage, hash},
		"outage without hash": {sessionErr(providerbridge.ProviderOutage{RecordHash: "not-a-hash"}), "task-outcome-unknown", ""},
		"other failure":       {sessionErr(errors.New("registered provider exchange failed; response withheld")), "task-outcome-unknown", ""},
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
