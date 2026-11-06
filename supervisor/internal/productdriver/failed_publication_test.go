//go:build linux

package productdriver

import (
	"context"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/publication"
)

var (
	failedBaseline = "sha256:" + strings.Repeat("1", 64)
	failedPartial  = "sha256:" + strings.Repeat("2", 64)
	failedSealed   = "sha256:" + strings.Repeat("3", 64)
)

const failedTurnID = "psess-1/turn-1"

type failedCatalog struct {
	turnCatalog
	completed *productsession.TurnTerminal
	reviewed  bool
	unknown   []string
}

func (c *failedCatalog) Turn(id string) (productsession.Turn, bool) {
	return productsession.Turn{ID: id, SessionID: "psess-1", Ordinal: 1, ProjectID: "p", BaselineDigest: failedBaseline,
		PublicationMode: productsession.PublicationAuto, State: productsession.StateAdmitted,
		Reservation: productsession.Budget{DurationSeconds: 10}}, true
}
func (c *failedCatalog) Session(productsession.Principal, string) (productsession.Session, error) {
	return productsession.Session{}, nil
}
func (c *failedCatalog) LinkTurn(string, productsession.TurnLink) error { return nil }
func (c *failedCatalog) MarkTurnUnknown(_ string, reason string) error {
	c.unknown = append(c.unknown, reason)
	return nil
}
func (c *failedCatalog) CompleteTurn(_ string, t productsession.TurnTerminal) error {
	c.completed = &t
	return nil
}
func (c *failedCatalog) MarkAwaitingReview(string, productsession.TurnReview) error {
	c.reviewed = true
	return nil
}

type failedPredecessor struct{}

func (failedPredecessor) Digest() string                   { return "" }
func (failedPredecessor) PredecessorReceiptDigest() string { return "" }

// failedHost measures the project baseline first as admitted, then as the test sets it after the turn.
type failedHost struct {
	Host
	prepared PreparedTurn
	after    string
	measured int
}

func (h *failedHost) SettleRetainedCommands(string) error { return nil }
func (h *failedHost) Crash(string)                        {}
func (h *failedHost) PredecessorContext(productsession.Turn, string) (Predecessor, error) {
	return failedPredecessor{}, nil
}
func (h *failedHost) MeasureProjectBaseline(string) (string, error) {
	h.measured++
	if h.measured == 1 {
		return failedBaseline, nil
	}
	return h.after, nil
}
func (h *failedHost) PrepareTurn(context.Context, productsession.Turn, string, Predecessor, func() bool) (PreparedTurn, error) {
	return h.prepared, nil
}

type failedPrepared struct{}

func (failedPrepared) Link() productsession.TurnLink {
	return productsession.TurnLink{WorkflowID: "w", RunID: "r", SessionID: "psess-1", ConversationID: "c"}
}
func (failedPrepared) BeginPublication(context.Context, string) (func(), error) {
	return func() {}, nil
}
func (failedPrepared) Drive(context.Context, productsession.Turn) (Work, error) { return Work{}, nil }
func (failedPrepared) WorkerInstance() string                                   { return "i" }
func (failedPrepared) SettleCommands() error                                    { return nil }
func (failedPrepared) Candidate(string, string) (productprotocol.CandidateView, error) {
	return productprotocol.CandidateView{}, nil
}
func (failedPrepared) Close() error               { return nil }
func (failedPrepared) CloseUnlessRetained() error { return nil }
func (failedPrepared) Evidence(stage EvidenceStage) (Evidence, error) {
	if stage == EvidenceResolved {
		return Evidence{Sealed: failedSealed}, nil
	}
	head := strings.Repeat("a", 64)
	return Evidence{ProviderHead: head, StoreHead: head, ProviderCount: 1, StoreCount: 1, Started: time.Now()}, nil
}
func (failedPrepared) Finalize(context.Context) (publication.Result, error) {
	return publication.Result{Status: publication.StatusFailed}, nil
}

func runFailedPublication(t *testing.T, after string) (*failedCatalog, productsession.TurnReceipt, error) {
	t.Helper()
	catalog := &failedCatalog{}
	d := New(Config{Catalog: catalog, Host: &failedHost{prepared: failedPrepared{}, after: after}})
	receipt, err := d.RunTurn(context.Background(), productsession.Admission{Turn: productsession.Turn{ID: failedTurnID}})
	return catalog, receipt, err
}

// A failed publication that left the admitted tree in place is a failed turn with an unchanged baseline.
func TestFailedPublicationWithUnchangedBaselineIsFailed(t *testing.T) {
	catalog, receipt, err := runFailedPublication(t, failedBaseline)
	if err == nil || !strings.Contains(err.Error(), "publication failed") {
		t.Fatalf("want the publication failure, got %v", err)
	}
	if len(catalog.unknown) != 0 || catalog.completed == nil || catalog.completed.Outcome != productsession.OutcomeFailed ||
		catalog.completed.ResultBaselineDigest != failedBaseline || receipt.ResultBaselineDigest != failedBaseline {
		t.Fatalf("unknown=%v completed=%+v receipt=%+v", catalog.unknown, catalog.completed, receipt)
	}
}

// A failed publication that left a different tree (a partial apply) must end unknown, which blocks the
// session; it must never settle as failed with an unchanged baseline, or as published.
func TestFailedPublicationWithChangedProjectIsUnknown(t *testing.T) {
	catalog, receipt, err := runFailedPublication(t, failedPartial)
	if err == nil || !strings.Contains(err.Error(), "is unknown: the project changed outside the turn") {
		t.Fatalf("want an unknown turn, got %v", err)
	}
	if len(catalog.unknown) != 1 || catalog.unknown[0] != "the project changed outside the turn" {
		t.Fatalf("MarkTurnUnknown calls: %v", catalog.unknown)
	}
	if catalog.completed != nil || catalog.reviewed || receipt.Outcome != "" {
		t.Fatalf("an outcome was published: completed=%+v reviewed=%v receipt=%+v", catalog.completed, catalog.reviewed, receipt)
	}
}
