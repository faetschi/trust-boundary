//go:build linux

package productdriver

import (
	"errors"
	"testing"

	"tbound/supervisor/internal/productsession"
)

// closeHost is a Host whose only live method is CloseRetainedCommands.
type closeHost struct {
	Host
	err error
}

func (h closeHost) CloseRetainedCommands() error { return h.err }

// An error crossing the Host boundary keeps its identity through Close.
func TestCloseKeepsHostErrorIdentity(t *testing.T) {
	failure := errors.New("retained owner")
	for name, want := range map[string]error{"unsettled": ErrCommandsUnsettled, "other": failure} {
		err := New(Config{Host: closeHost{err: want}}).Close()
		if !errors.Is(err, want) {
			t.Errorf("%s: Close lost the host error: %v", name, err)
		}
	}
	if err := New(Config{Host: closeHost{}}).Close(); err != nil {
		t.Fatalf("Close with nothing retained: %v", err)
	}
}

func TestCandidateIsNotHeldWithoutAPendingReview(t *testing.T) {
	d := New(Config{Host: closeHost{}})
	for _, state := range []productsession.TurnState{productsession.StateAwaitingReview, productsession.StateTerminal} {
		if _, held, err := d.Candidate(productsession.Turn{ID: "t", State: state}); held || err != nil {
			t.Errorf("state %s: held=%v err=%v", state, held, err)
		}
	}
}

func TestRefusalMatchesItsCodeAndCause(t *testing.T) {
	err := error(&Refusal{Code: productsession.CodeStaleBaseline, Cause: ErrStaleBaseline})
	if !errors.Is(err, productsession.ErrCode(productsession.CodeStaleBaseline)) || !errors.Is(err, ErrStaleBaseline) ||
		errors.Is(err, ErrTurnCancelled) {
		t.Fatalf("refusal identity: %v", err)
	}
	var refusal *Refusal
	if !errors.As(errors.Join(errors.New("wrapped"), err), &refusal) || refusal.Code != productsession.CodeStaleBaseline {
		t.Fatalf("refusal not recoverable through a join: %v", err)
	}
}

func TestUnestablishedKeepsReasonAndCause(t *testing.T) {
	cause := errors.New("journal")
	var evidence *EvidenceError
	if !errors.As(Unestablished("the provider journal could not be read", cause), &evidence) ||
		evidence.Reason != "the provider journal could not be read" || evidence.Cause != cause ||
		!errors.Is(evidence, cause) {
		t.Fatalf("evidence error: %+v", evidence)
	}
}
