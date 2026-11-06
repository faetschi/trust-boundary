//go:build linux

package productdriver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/publication"
)

// pendingReview is a closed review turn kept open for its resolution: the prepared turn, whose
// closers include the publication finalizer, and the monotonic expiry deadline.
type pendingReview struct {
	turn     PreparedTurn
	deadline time.Time
}

// holdReviewLocked registers a closed review turn; the caller holds d.mu.
func (d *Driver) holdReviewLocked(turnID string, review *pendingReview) {
	if d.pending == nil {
		d.pending = map[string]*pendingReview{}
	}
	d.pending[turnID] = review
}

// takeReviewLocked removes a pending review; whoever takes it resolves and closes it exactly once.
// The caller holds d.mu.
func (d *Driver) takeReviewLocked(turnID string) *pendingReview {
	review := d.pending[turnID]
	delete(d.pending, turnID)
	return review
}

func (d *Driver) cancelRequested(turnID string) bool {
	turn, _ := d.catalog.Turn(turnID)
	return turn.CancelRequested
}

// Close closes every pending review without resolving it. Their turns stay awaiting review in the
// catalog until restart reconciliation.
func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var errs []error
	errs = append(errs, d.host.CloseRetainedCommands())
	for id, review := range d.pending {
		if err := review.turn.Close(); err != nil {
			errs = append(errs, err, d.catalog.MarkTurnUnknown(id, "command ownership did not settle at shutdown"))
		}
		delete(d.pending, id)
	}
	return errors.Join(errs...)
}

// ResolveTurn records the review decision durably, then publishes the retained candidate or
// discards it. Published needs a SUCCEEDED publication and the project measuring exactly the
// candidate; a failed publication with the project still at the admitted baseline is failed;
// anything else is unknown.
func (d *Driver) ResolveTurn(ctx context.Context, p productsession.Principal,
	r productsession.ResolveTurnRequest) (productsession.TurnReceipt, error) {
	// An expired review ends before its resolution can publish; expiry failures are already recorded
	// in the catalog as unknown turns.
	_ = d.ExpireReviews(d.now())
	d.host.Crash("before-review-resolution")
	d.mu.Lock()
	// The preflight scan may precede lock acquisition by arbitrarily long. Recheck the target
	// while holding the same mutex as its durable resolution and removal from pending.
	if review := d.pending[r.TurnID]; review != nil && !d.now().Before(review.deadline) {
		turn, _ := d.catalog.Turn(r.TurnID)
		_, err := d.discardReview(turn, d.takeReviewLocked(r.TurnID), productsession.ReasonExpired)
		if err != nil {
			d.mu.Unlock()
			return productsession.TurnReceipt{}, err
		}
	}
	turn, replayed, err := d.catalog.ResolveTurn(p, r)
	var review *pendingReview
	if err == nil {
		review = d.takeReviewLocked(r.TurnID)
	}
	d.mu.Unlock()
	switch {
	case err != nil:
		return productsession.TurnReceipt{}, err
	case review == nil && replayed && turn.State == productsession.StateTerminal:
		return DecodeReceipt(turn.Receipt)
	case review == nil:
		return productsession.TurnReceipt{}, fmt.Errorf("turn %s: %w", r.TurnID, ErrReviewNotRetained)
	}
	d.host.Crash("after-resolution")
	if turn.Resolution == productsession.DecisionDiscard {
		return d.discardReview(turn, review, productsession.ReasonUser)
	}
	defer func() { _ = review.turn.CloseUnlessRetained() }()
	if err := review.turn.SettleCommands(); err != nil {
		return productsession.TurnReceipt{}, errors.Join(err, d.catalog.MarkTurnUnknown(turn.ID, "command ownership did not settle before review publication"))
	}
	result, finalizeErr := review.turn.Finalize(ctx)
	after, measureErr := d.host.MeasureProjectBaseline(turn.ProjectID)
	switch {
	case finalizeErr == nil && result.Status == publication.StatusSucceeded && len(result.Objects) != 0 &&
		measureErr == nil && after == turn.CandidateDigest:
		d.host.Crash("after-publication")
		return d.completeReviewed(turn, productsession.OutcomePublished, "", result.TransactionID)
	case result.Status == publication.StatusFailed && measureErr == nil && after == turn.BaselineDigest:
		receipt, err := d.completeReviewed(turn, productsession.OutcomeFailed, "", "")
		return receipt, errors.Join(errors.New("publication failed"), finalizeErr, err)
	}
	return productsession.TurnReceipt{}, errors.Join(fmt.Errorf("product turn %s is unknown: publication outcome not established", turn.ID),
		finalizeErr, measureErr, d.catalog.MarkTurnUnknown(turn.ID, "the review publication outcome is not established"))
}

// discardReview ends a pending review discarded after checking the project still holds the admitted
// baseline, and closes its retained handles.
func (d *Driver) discardReview(turn productsession.Turn, review *pendingReview, reason string) (productsession.TurnReceipt, error) {
	defer func() { _ = review.turn.CloseUnlessRetained() }()
	if err := review.turn.SettleCommands(); err != nil {
		return productsession.TurnReceipt{}, errors.Join(err, d.catalog.MarkTurnUnknown(turn.ID, "command ownership did not settle before review discard"))
	}
	if after, err := d.host.MeasureProjectBaseline(turn.ProjectID); err != nil || after != turn.BaselineDigest {
		return productsession.TurnReceipt{}, errors.Join(fmt.Errorf("product turn %s is unknown: project is not the admitted baseline", turn.ID),
			err, d.catalog.MarkTurnUnknown(turn.ID, "the project changed while the turn awaited review"))
	}
	return d.completeReviewed(turn, productsession.OutcomeDiscarded, reason, "")
}

// completeReviewed ends a turn that awaited review. Its receipt is the awaiting-review receipt with
// only the outcome, result baseline and publication transaction changed; usage repeats the review
// usage. A turn that reached review ended at a final answer, so its transcript is context-eligible.
func (d *Driver) completeReviewed(turn productsession.Turn, outcome, reason, transactionID string) (productsession.TurnReceipt, error) {
	receipt, err := DecodeReceipt(turn.Receipt)
	if err == nil {
		receipt.Outcome, receipt.PublicationTransactionID = outcome, transactionID
		receipt.ResultBaselineDigest = turn.BaselineDigest
		if outcome == productsession.OutcomePublished {
			receipt.ResultBaselineDigest = turn.CandidateDigest
		}
		var encoded []byte
		if encoded, err = EncodeReceipt(receipt); err == nil {
			err = d.catalog.CompleteTurn(turn.ID, productsession.TurnTerminal{Outcome: outcome, Settlement: receipt.Settlement,
				Receipt: encoded, Usage: turn.Usage, ResultBaselineDigest: receipt.ResultBaselineDigest, ContextEligible: true,
				Reason: reason})
		}
	}
	if err != nil {
		return productsession.TurnReceipt{}, errors.Join(err, d.catalog.MarkTurnUnknown(turn.ID, "the catalog refused the reviewed turn settlement"))
	}
	return receipt, nil
}

// ExpireReviews discards, with reason expired, every pending review whose deadline is not after now
// and that has no resolution. now should come from time.Now so the comparison is monotonic.
func (d *Driver) ExpireReviews(now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var errs []error
	for id, review := range d.pending {
		if turn, _ := d.catalog.Turn(id); !now.Before(review.deadline) && turn.Resolution == "" {
			_, err := d.discardReview(turn, d.takeReviewLocked(id), productsession.ReasonExpired)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CancelTurn records a cancel request, then ends what this process can end now: an admitted turn
// that is not running is refused CANCELLED, and a pending review without a resolution is discarded
// with reason cancelled. A running turn reads the request before linking and before every provider
// exchange. Any other turn keeps the request for its real disposition or restart reconciliation.
func (d *Driver) CancelTurn(_ context.Context, p productsession.Principal,
	r productsession.CancelTurnRequest) (productsession.Turn, error) {
	d.mu.Lock()
	if _, _, err := d.catalog.CancelTurn(p, r); err != nil {
		d.mu.Unlock()
		return productsession.Turn{}, err
	}
	d.host.Crash("after-cancel-recorded")
	turn, _ := d.catalog.Turn(r.TurnID)
	var err error
	var review *pendingReview
	switch {
	case turn.State == productsession.StateAwaitingReview && turn.Resolution == "":
		review = d.takeReviewLocked(turn.ID)
	case d.running[turn.ID]:
	case turn.State == productsession.StateAdmitted:
		if err = d.host.SettleRetainedCommands(turn.ID); err == nil {
			err = d.catalog.RefuseUnlinkedTurn(turn.ID, productsession.CodeCancelled)
		}
	}
	d.mu.Unlock()
	if review != nil {
		_, err = d.discardReview(turn, review, productsession.ReasonCancelled)
	}
	turn, _ = d.catalog.Turn(r.TurnID)
	return turn, err
}

// Candidate is the read-only view of the sealed candidate of a review turn this process holds
// open. held is false unless the turn is awaiting review and held here.
func (d *Driver) Candidate(turn productsession.Turn) (view productprotocol.CandidateView, held bool, err error) {
	d.mu.Lock()
	review := d.pending[turn.ID]
	d.mu.Unlock()
	if turn.State != productsession.StateAwaitingReview || review == nil {
		return productprotocol.CandidateView{}, false, nil
	}
	view, err = review.turn.Candidate(turn.ID, turn.CandidateDigest)
	return view, true, err
}
