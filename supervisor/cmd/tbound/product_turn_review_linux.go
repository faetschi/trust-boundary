//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/publication"
)

// errProductReviewNotRetained marks a review turn whose workflow and finalizer are not held by this
// process: restart reconciliation, not this process, resolves it.
var errProductReviewNotRetained = errors.New("the review turn is not held by this process; restart reconciliation resolves it")

// productPendingReview is a closed review turn kept open for its resolution: the workflow, whose
// closers include the publication finalizer, and the monotonic expiry deadline.
type productPendingReview struct {
	w        *productTurnWorkflow
	publish  *PublicationRuntime
	deadline time.Time
}

// holdReviewLocked registers a closed review turn; the caller holds e.mu.
func (e *productTurnEnv) holdReviewLocked(turnID string, review *productPendingReview) {
	if e.pending == nil {
		e.pending = map[string]*productPendingReview{}
	}
	e.pending[turnID] = review
}

// takeReviewLocked removes a pending review; whoever takes it resolves and closes it exactly once.
// The caller holds e.mu.
func (e *productTurnEnv) takeReviewLocked(turnID string) *productPendingReview {
	review := e.pending[turnID]
	delete(e.pending, turnID)
	return review
}

func (e *productTurnEnv) cancelRequested(turnID string) bool {
	turn, _ := e.Catalog.Turn(turnID)
	return turn.CancelRequested
}

// Close closes every pending review without resolving it. Their turns stay awaiting review in the
// catalog until restart reconciliation.
func (e *productTurnEnv) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var errs []error
	e.retainedMu.Lock()
	retained := make([]*productTurnWorkflow, 0, len(e.retainedCommands))
	for w := range e.retainedCommands {
		retained = append(retained, w)
	}
	e.retainedMu.Unlock()
	for _, w := range retained {
		errs = append(errs, w.close())
	}
	for id, review := range e.pending {
		if err := review.w.close(); err != nil {
			errs = append(errs, err, e.Catalog.MarkTurnUnknown(id, "command ownership did not settle at shutdown"))
		}
		delete(e.pending, id)
	}
	return errors.Join(errs...)
}

// ResolveTurn records the review decision durably, then publishes the retained candidate or
// discards it. Published needs a SUCCEEDED publication and the project measuring exactly the
// candidate; a failed publication with the project still at the admitted baseline is failed;
// anything else is unknown.
func (e *productTurnEnv) ResolveTurn(ctx context.Context, p productsession.Principal,
	r productsession.ResolveTurnRequest) (productTurnReceipt, error) {
	// An expired review ends before its resolution can publish; expiry failures are already recorded
	// in the catalog as unknown turns.
	_ = e.ExpireReviews(time.Now())
	e.crash("before-review-resolution")
	e.mu.Lock()
	// The preflight scan may precede lock acquisition by arbitrarily long. Recheck the target
	// while holding the same mutex as its durable resolution and removal from pending.
	if review := e.pending[r.TurnID]; review != nil && !time.Now().Before(review.deadline) {
		turn, _ := e.Catalog.Turn(r.TurnID)
		_, err := e.discardReview(turn, e.takeReviewLocked(r.TurnID), productsession.ReasonExpired)
		if err != nil {
			e.mu.Unlock()
			return productTurnReceipt{}, err
		}
	}
	turn, replayed, err := e.Catalog.ResolveTurn(p, r)
	var review *productPendingReview
	if err == nil {
		review = e.takeReviewLocked(r.TurnID)
	}
	e.mu.Unlock()
	switch {
	case err != nil:
		return productTurnReceipt{}, err
	case review == nil && replayed && turn.State == productsession.StateTerminal:
		return decodeProductTurnReceipt(turn.Receipt)
	case review == nil:
		return productTurnReceipt{}, fmt.Errorf("turn %s: %w", r.TurnID, errProductReviewNotRetained)
	}
	e.crash("after-resolution")
	if turn.Resolution == productsession.DecisionDiscard {
		return e.discardReview(turn, review, productsession.ReasonUser)
	}
	defer func() { _ = review.w.close() }()
	if err := review.w.settleCommands(); err != nil {
		return productTurnReceipt{}, errors.Join(err, e.Catalog.MarkTurnUnknown(turn.ID, "command ownership did not settle before review publication"))
	}
	result, finalizeErr := review.publish.Finalize(ctx)
	after, measureErr := e.MeasureProjectBaseline(turn.ProjectID)
	switch {
	case finalizeErr == nil && result.Status == publication.StatusSucceeded && len(result.Objects) != 0 &&
		measureErr == nil && after == turn.CandidateDigest:
		e.crash("after-publication")
		return e.completeReviewed(turn, productsession.OutcomePublished, "", result.TransactionID)
	case result.Status == publication.StatusFailed && measureErr == nil && after == turn.BaselineDigest:
		receipt, err := e.completeReviewed(turn, productsession.OutcomeFailed, "", "")
		return receipt, errors.Join(errors.New("publication failed"), finalizeErr, err)
	}
	return productTurnReceipt{}, errors.Join(fmt.Errorf("product turn %s is unknown: publication outcome not established", turn.ID),
		finalizeErr, measureErr, e.Catalog.MarkTurnUnknown(turn.ID, "the review publication outcome is not established"))
}

// discardReview ends a pending review discarded after checking the project still holds the admitted
// baseline, and closes its retained handles.
func (e *productTurnEnv) discardReview(turn productsession.Turn, review *productPendingReview, reason string) (productTurnReceipt, error) {
	defer func() { _ = review.w.close() }()
	if err := review.w.settleCommands(); err != nil {
		return productTurnReceipt{}, errors.Join(err, e.Catalog.MarkTurnUnknown(turn.ID, "command ownership did not settle before review discard"))
	}
	if after, err := e.MeasureProjectBaseline(turn.ProjectID); err != nil || after != turn.BaselineDigest {
		return productTurnReceipt{}, errors.Join(fmt.Errorf("product turn %s is unknown: project is not the admitted baseline", turn.ID),
			err, e.Catalog.MarkTurnUnknown(turn.ID, "the project changed while the turn awaited review"))
	}
	return e.completeReviewed(turn, productsession.OutcomeDiscarded, reason, "")
}

// completeReviewed ends a turn that awaited review. Its receipt is the awaiting-review receipt with
// only the outcome, result baseline and publication transaction changed; usage repeats the review
// usage. A turn that reached review ended at a final answer, so its transcript is context-eligible.
func (e *productTurnEnv) completeReviewed(turn productsession.Turn, outcome, reason, transactionID string) (productTurnReceipt, error) {
	receipt, err := decodeProductTurnReceipt(turn.Receipt)
	if err == nil {
		receipt.Outcome, receipt.PublicationTransactionID = outcome, transactionID
		receipt.ResultBaselineDigest = turn.BaselineDigest
		if outcome == productsession.OutcomePublished {
			receipt.ResultBaselineDigest = turn.CandidateDigest
		}
		var encoded []byte
		if encoded, err = encodeProductTurnReceipt(receipt); err == nil {
			err = e.Catalog.CompleteTurn(turn.ID, productsession.TurnTerminal{Outcome: outcome, Settlement: receipt.Settlement,
				Receipt: encoded, Usage: turn.Usage, ResultBaselineDigest: receipt.ResultBaselineDigest, ContextEligible: true,
				Reason: reason})
		}
	}
	if err != nil {
		return productTurnReceipt{}, errors.Join(err, e.Catalog.MarkTurnUnknown(turn.ID, "the catalog refused the reviewed turn settlement"))
	}
	return receipt, nil
}

// ExpireReviews discards, with reason expired, every pending review whose deadline is not after now
// and that has no resolution. now should come from time.Now so the comparison is monotonic.
func (e *productTurnEnv) ExpireReviews(now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var errs []error
	for id, review := range e.pending {
		if turn, _ := e.Catalog.Turn(id); !now.Before(review.deadline) && turn.Resolution == "" {
			_, err := e.discardReview(turn, e.takeReviewLocked(id), productsession.ReasonExpired)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CancelTurn records a cancel request, then ends what this process can end now: an admitted turn
// that is not running is refused CANCELLED, and a pending review without a resolution is discarded
// with reason cancelled. A running turn reads the request before linking and before every provider
// exchange. Any other turn keeps the request for its real disposition or restart reconciliation.
func (e *productTurnEnv) CancelTurn(_ context.Context, p productsession.Principal,
	r productsession.CancelTurnRequest) (productsession.Turn, error) {
	e.mu.Lock()
	if _, _, err := e.Catalog.CancelTurn(p, r); err != nil {
		e.mu.Unlock()
		return productsession.Turn{}, err
	}
	e.crash("after-cancel-recorded")
	turn, _ := e.Catalog.Turn(r.TurnID)
	var err error
	var review *productPendingReview
	switch {
	case turn.State == productsession.StateAwaitingReview && turn.Resolution == "":
		review = e.takeReviewLocked(turn.ID)
	case e.running[turn.ID]:
	case turn.State == productsession.StateAdmitted:
		if err = e.settleRetainedProductCommands(turn.ID); err == nil {
			err = e.Catalog.RefuseUnlinkedTurn(turn.ID, productsession.CodeCancelled)
		}
	}
	e.mu.Unlock()
	if review != nil {
		_, err = e.discardReview(turn, review, productsession.ReasonCancelled)
	}
	turn, _ = e.Catalog.Turn(r.TurnID)
	return turn, err
}
