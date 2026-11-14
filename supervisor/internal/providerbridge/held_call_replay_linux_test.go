//go:build linux

package providerbridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

// The journals that a held call leaves when it stops before its effect replay exactly as before: this change
// adds no record and no field, and abandon reads the workflow checkpoint, not these journals.
//   - stopped before Pi's proposal: the journal ends at the capture, and replay awaits the gate decision;
//   - the wrapper's context check before dispatch: it ends at the decision, and replay awaits the result;
//   - the durable executor's context check: it ends in an untyped UNKNOWN result, which replay refuses.
func TestHeldCallJournalsReplayUnchanged(t *testing.T) {
	read := []byte(`{"path":"x"}`)
	for name, tc := range map[string]struct {
		stop        func(t *testing.T, c *Conversation, turn Turn)
		disposition RecoveryDisposition
		refused     string
	}{
		"stopped before the proposal": {stop: func(*testing.T, *Conversation, Turn) {}, disposition: DispositionAwaitingGateDecision},
		"wrapper context check": {disposition: DispositionPendingProviderResult, stop: func(t *testing.T, c *Conversation, turn Turn) {
			proposal, decision := heldCallDecision(t, c, turn)
			wrapped, err := NewExecutor(c, &fixtureExecutor{})
			if err != nil {
				t.Fatal(err)
			}
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := wrapped.Execute(canceled, proposal, decision); err == nil {
				t.Fatal("a canceled call was executed")
			}
		}},
		"executor context check": {refused: "unresolved UNKNOWN", stop: func(t *testing.T, c *Conversation, turn Turn) {
			proposal, decision := heldCallDecision(t, c, turn)
			release := make(chan struct{})
			close(release)
			wrapped, err := NewExecutor(c, &releasedExecutor{started: make(chan struct{}), release: release, err: context.Canceled})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := wrapped.Execute(context.Background(), proposal, decision); err == nil {
				t.Fatal("a canceled call was executed")
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := recoveryTestConfig()
			path := filepath.Join(t.TempDir(), "provider.audit")
			journal := openRecoveryTestJournal(t, path)
			doer := &fixtureDoer{responses: []fixtureResponse{{responseID: "response-1", callID: "call-1", tool: "read", arguments: read}}}
			c, err := newWithDoer(cfg, ApprovedModel, doer, journal)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.AdmitPrompt("task-held", "synthetic held-call test"); err != nil {
				t.Fatal(err)
			}
			turn, err := c.NextTurn(context.Background())
			if err != nil || turn.ToolCall == nil {
				t.Fatalf("provider turn: %+v %v", turn, err)
			}
			tc.stop(t, c, turn)
			c.Close()
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := audit.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			projection, err := ReplayTranscript(reopened, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true)},
				&replayVerifierFixture{})
			if tc.refused != "" {
				if !errors.Is(err, ErrTranscriptReplay) || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("replay = %v, want the %q refusal", err, tc.refused)
				}
				return
			}
			if err != nil || projection.Disposition != tc.disposition || projection.CapturedCallCount != 1 || projection.ResultCount != 0 {
				t.Fatalf("replay = %+v %v, want disposition %q with one captured call and no result", projection, err, tc.disposition)
			}
		})
	}
}

func heldCallDecision(t *testing.T, c *Conversation, turn Turn) (protocol.Proposal, gate.Decision) {
	t.Helper()
	proposal := proposalFor(turn)
	matched, err := c.Correlate(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Evaluate(proposal, matched, recoveryTestPolicy(t, true))
	if err := c.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	return proposal, decision
}
