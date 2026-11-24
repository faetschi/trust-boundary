//go:build linux

package productdriver

import (
	"context"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/publication"
)

type removalCatalog struct {
	failedCatalog
	mode  string
	scope *productsession.ProductScope
}

func (c *removalCatalog) Turn(id string) (productsession.Turn, bool) {
	turn, ok := c.failedCatalog.Turn(id)
	turn.PublicationMode, turn.Scope = c.mode, c.scope
	return turn, ok
}

type removalPrepared struct {
	failedPrepared
	removed int
}

func (p removalPrepared) Evidence(stage EvidenceStage) (Evidence, error) {
	if stage == EvidenceResolved {
		return Evidence{Sealed: failedSealed, Removed: p.removed}, nil
	}
	head := strings.Repeat("a", 64)
	return Evidence{ProviderHead: head, StoreHead: head, ProviderCount: 1, StoreCount: 1, Started: time.Now()}, nil
}

func (removalPrepared) Finalize(context.Context) (publication.Result, error) {
	return publication.Result{Status: publication.StatusSucceeded, TransactionID: "tx", Objects: make([]publication.ObjectResult, 1)}, nil
}

func intPtr(n int) *int { return &n }

func TestAutoTurnThatRemovesTooMuchAwaitsReview(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		limit   *int
		removed int
		review  bool
	}{
		{"one over the limit", productsession.PublicationAuto, intPtr(2), 3, true},
		{"exactly at the limit", productsession.PublicationAuto, intPtr(2), 2, false},
		{"limit zero, one removal", productsession.PublicationAuto, intPtr(0), 1, true},
		{"limit zero, none removed", productsession.PublicationAuto, intPtr(0), 0, false},
		{"no limit keeps auto publication", productsession.PublicationAuto, nil, 1000, false},
		{"review mode ignores the limit", productsession.PublicationReview, intPtr(5), 0, true},
	} {
		scope := &productsession.ProductScope{Writable: []string{"src"}, AutoPublishMaxRemovals: tc.limit}
		catalog := &removalCatalog{mode: tc.mode, scope: scope}
		after := failedSealed
		if tc.review {
			after = failedBaseline
		}
		d := New(Config{Catalog: catalog, Host: &failedHost{prepared: removalPrepared{removed: tc.removed}, after: after}})
		receipt, err := d.RunTurn(context.Background(), productsession.Admission{Turn: productsession.Turn{ID: failedTurnID}})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if tc.review {
			if catalog.review.Removed != tc.removed {
				t.Errorf("%s: review recorded %d removals, want %d", tc.name, catalog.review.Removed, tc.removed)
			}
			if !catalog.reviewed || catalog.completed != nil || receipt.Outcome != "awaiting_review" {
				t.Errorf("%s: reviewed=%v completed=%+v outcome=%q", tc.name, catalog.reviewed, catalog.completed, receipt.Outcome)
			}
		} else if catalog.reviewed || catalog.completed == nil || catalog.completed.Outcome != productsession.OutcomePublished {
			t.Errorf("%s: reviewed=%v completed=%+v", tc.name, catalog.reviewed, catalog.completed)
		}
	}
}
