//go:build linux

package productdriver

import (
	"context"
	"errors"
	"fmt"

	"tbound/supervisor/internal/productsession"
)

// unrecordedWorker is the worker instance of a receipt settled after restart: the
// in-process worker's random identity died with its process and was never recorded.
const unrecordedWorker = "pworker-unrecorded"

// ReconcileAfterRestart settles every unresolved turn of the catalog from retained evidence, per the
// P4 restart table. It runs once in a fresh process before any admission is served, and refuses to
// run while this process drives or holds any turn. It never publishes.
func (d *Driver) ReconcileAfterRestart(ctx context.Context) error {
	d.mu.Lock()
	busy := len(d.running) != 0 || len(d.pending) != 0
	d.mu.Unlock()
	if busy {
		return errors.New("restart reconciliation runs only before this process drives any turn")
	}
	var errs []error
	for _, turn := range d.catalog.UnresolvedTurns() {
		switch turn.State {
		case productsession.StateAdmitted:
			errs = append(errs, d.catalog.AbandonUnlinkedTurn(turn.ID))
		case productsession.StateLinked:
			errs = append(errs, d.reconcileLinkedTurn(ctx, turn))
		case productsession.StateAwaitingReview:
			errs = append(errs, d.reconcileReviewTurn(turn))
		}
	}
	return errors.Join(errs...)
}

// reconcileLinkedTurn settles a linked turn whose process died: failed with settlement
// reconciled-after-restart when the retained Store reconstructs cleanly, the owner checkpoint has
// no open reservation, the project still holds the admitted baseline and the publication journal
// has no record for the workflow. Anything else, or any evidence that cannot be read, is unknown.
func (d *Driver) reconcileLinkedTurn(ctx context.Context, turn productsession.Turn) error {
	unknown := func(reason string, cause error) error {
		return errors.Join(fmt.Errorf("product turn %s is unknown after restart: %s", turn.ID, reason), cause,
			d.catalog.MarkTurnUnknown(turn.ID, reason))
	}
	ev, err := d.host.ReconcileLinkedTurn(ctx, turn)
	if err != nil {
		var evidence *evidenceError
		if errors.As(err, &evidence) {
			return unknown(evidence.Reason, evidence.Cause)
		}
		return unknown("the retained evidence cannot be read", err)
	}
	// The dead process's elapsed time is not retained; the reserved duration bounds it.
	usage := productsession.Budget{HostCalls: ev.HostCalls, Tokens: ev.Tokens,
		DurationSeconds: turn.Reservation.DurationSeconds}
	link := turn.Link
	receipt := productsession.TurnReceipt{SchemaVersion: productsession.ReceiptSchema,
		SessionID: turn.SessionID, TurnID: turn.ID, Ordinal: turn.Ordinal,
		WorkflowID: link.WorkflowID, RunID: link.RunID, ConversationID: link.ConversationID,
		UnverifiedInputs: []string{"runtime_digest", "image_digest", "extension_digest"},
		WorkerKind:       productsession.WorkerSupervisorLoop, WorkerInstance: unrecordedWorker,
		ProviderJournalHead: ev.ProviderHead, ProviderJournalCount: ev.ProviderCount,
		StoreJournalHead: ev.StoreHead, StoreJournalCount: ev.StoreCount,
		CommandJournalHead: ev.CommandHead, CommandJournalCount: ev.CommandCount,
		ImportedPrefixDigest: link.ImportedPrefixDigest, PredecessorReceiptDigest: turn.PredecessorReceiptDigest,
		BaselineDigest: turn.BaselineDigest, ResultBaselineDigest: turn.BaselineDigest,
		Outcome: productsession.OutcomeFailed, Settlement: productsession.SettlementReconciledAfterRestart,
		Usage: usage, ToolSequence: []string{}, Denials: []productsession.ReceiptDenial{}}
	// A turn that died before its prompt was admitted has an empty provider journal, which a receipt
	// cannot commit, so it ends unknown here.
	encoded, err := EncodeReceipt(receipt)
	if err != nil {
		return unknown("the reconciled receipt cannot be built", err)
	}
	if err := d.catalog.CompleteTurn(turn.ID, productsession.TurnTerminal{Outcome: productsession.OutcomeFailed,
		Settlement: receipt.Settlement, Receipt: encoded, Usage: usage, ResultBaselineDigest: turn.BaselineDigest}); err != nil {
		return unknown("the catalog refused the reconciled settlement", err)
	}
	return nil
}

// reconcileReviewTurn ends a review that did not survive the restart. A publish resolution whose
// workflow transaction SUCCEEDED with the project at the candidate is published. With no
// publication record for the workflow and the project at the admitted baseline, a discard
// resolution is discarded by the user and anything else discarded by the restart. Otherwise unknown.
func (d *Driver) reconcileReviewTurn(turn productsession.Turn) error {
	records, succeeded, err := d.host.WorkflowPublication(turn.ProjectID, turn.Link.WorkflowID)
	after, measureErr := d.host.MeasureProjectBaseline(turn.ProjectID)
	err = errors.Join(err, measureErr)
	switch {
	case err == nil && turn.Resolution == productsession.DecisionPublish && succeeded != "" && after == turn.CandidateDigest:
		_, err = d.completeReviewed(turn, productsession.OutcomePublished, "", succeeded)
	case err == nil && !records && after == turn.BaselineDigest:
		reason := productsession.ReasonRestart
		if turn.Resolution == productsession.DecisionDiscard {
			reason = productsession.ReasonUser
		}
		_, err = d.completeReviewed(turn, productsession.OutcomeDiscarded, reason, "")
	default:
		err = errors.Join(fmt.Errorf("product turn %s is unknown after restart: review disposition not established", turn.ID),
			err, d.catalog.MarkTurnUnknown(turn.ID, "the review disposition is not established after restart"))
	}
	return err
}
