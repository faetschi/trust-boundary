//go:build linux

package productdriver

import (
	"context"
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
	var evidence *evidenceError
	if !errors.As(Unestablished("the provider journal could not be read", cause), &evidence) ||
		evidence.Reason != "the provider journal could not be read" || evidence.Cause != cause ||
		!errors.Is(evidence, cause) {
		t.Fatalf("evidence error: %+v", evidence)
	}
}

// cancelCatalog records a cancel request and an unlinked-turn refusal for an admitted turn.
type cancelCatalog struct {
	turnCatalog
	refused bool
}

func (c *cancelCatalog) CancelTurn(productsession.Principal, productsession.CancelTurnRequest) (productsession.Turn, bool, error) {
	return productsession.Turn{}, false, nil
}

func (c *cancelCatalog) Turn(id string) (productsession.Turn, bool) {
	return productsession.Turn{ID: id, State: productsession.StateAdmitted}, true
}

func (c *cancelCatalog) RefuseUnlinkedTurn(string, productsession.Code) error {
	c.refused = true
	return nil
}

type cancelHost struct {
	Host
	crash func(point string)
}

func (h cancelHost) Crash(point string)                  { h.crash(point) }
func (h cancelHost) SettleRetainedCommands(string) error { return nil }

// The durable cancel record is written under the driver mutex, which arbitrates resolution and
// publication start; the mutex is still held when the record is visible to the crash hook.
func TestCancelRecordsUnderTheDriverMutex(t *testing.T) {
	catalog := &cancelCatalog{}
	var d *Driver
	checked := false
	d = New(Config{Catalog: catalog, Host: cancelHost{crash: func(point string) {
		if point != "after-cancel-recorded" {
			return
		}
		checked = true
		if d.mu.TryLock() {
			d.mu.Unlock()
			t.Error("cancellation recorded outside the driver mutex")
		}
	}}})
	if _, err := d.CancelTurn(context.Background(), 1, productsession.CancelTurnRequest{TurnID: "t"}); err != nil {
		t.Fatal(err)
	}
	if !checked || !catalog.refused {
		t.Fatalf("hook ran=%v refused=%v", checked, catalog.refused)
	}
}
