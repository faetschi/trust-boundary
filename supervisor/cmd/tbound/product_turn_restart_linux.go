//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workflowrecovery"
)

// productTurnUnrecordedWorker is the worker instance of a receipt settled after restart: the
// in-process worker's random identity died with its process and was never recorded.
const productTurnUnrecordedWorker = "pworker-unrecorded"

// projectPublicationDir holds a project's publication lifecycle journal and repository state.
func (e *productTurnEnv) projectPublicationDir(projectID string) string {
	key := sha256.Sum256([]byte(projectID))
	return filepath.Join(e.StateRoot, "projects", hex.EncodeToString(key[:]))
}

// ReconcileAfterRestart settles every unresolved turn of the catalog from retained evidence, per the
// P4 restart table. It runs once in a fresh process before any admission is served, and refuses to
// run while this process drives or holds any turn. It never publishes.
func (e *productTurnEnv) ReconcileAfterRestart(ctx context.Context) error {
	e.mu.Lock()
	busy := len(e.running) != 0 || len(e.pending) != 0
	e.mu.Unlock()
	if busy {
		return errors.New("restart reconciliation runs only before this process drives any turn")
	}
	var errs []error
	for _, turn := range e.Catalog.UnresolvedTurns() {
		switch turn.State {
		case productsession.StateAdmitted:
			errs = append(errs, e.Catalog.AbandonUnlinkedTurn(turn.ID))
		case productsession.StateLinked:
			errs = append(errs, e.reconcileLinkedTurn(ctx, turn))
		case productsession.StateAwaitingReview:
			errs = append(errs, e.reconcileReviewTurn(turn))
		}
	}
	return errors.Join(errs...)
}

// reconcileLinkedTurn settles a linked turn whose process died: failed with settlement
// reconciled-after-restart when the retained Store reconstructs cleanly, the owner checkpoint has
// no open reservation, the project still holds the admitted baseline and the publication journal
// has no record for the workflow. Anything else, or any evidence that cannot be read, is unknown.
func (e *productTurnEnv) reconcileLinkedTurn(ctx context.Context, turn productsession.Turn) error {
	unknown := func(reason string, cause error) error {
		return errors.Join(fmt.Errorf("product turn %s is unknown after restart: %s", turn.ID, reason), cause,
			e.Catalog.MarkTurnUnknown(turn.ID, reason))
	}
	paths, err := productTurnPathsFromLink(*turn.Link)
	if err != nil {
		return unknown("the retained evidence paths are invalid", err)
	}
	retained, err := openRetainedProductTurn(paths, e.storeOptions(turn), e.OpenCommandHistory)
	if err != nil {
		if errors.Is(err, errHistoricalBashContext) {
			if interrupted, _, proofErr := openInterruptedProductBash(ctx, paths, e.Registration.Digest, e.storeOptions(turn)); proofErr == nil {
				return unknown("the interrupted Bash scope is drained; run tbound product turns resolve to abandon the retained candidate", errors.Join(err, interrupted.Close()))
			}
		}
		return unknown("the retained Store or journals cannot be reopened", err)
	}
	defer retained.Close()
	bundle, err := retained.Store.EvidenceBundle()
	if err != nil {
		return unknown("the retained Store evidence cannot be read", err)
	}
	checkpoint, err := loadRetainedProductCheckpoint(ctx, filepath.Dir(paths.StoreJournalPath), turn.Link.WorkflowID)
	if err != nil {
		return unknown("the retained owner checkpoint cannot be read", err)
	}
	if err := e.verifyRetainedSoftRefusals(turn, retained, checkpoint.Pins); err != nil {
		return unknown("the retained soft refusal cannot be reproduced", err)
	}
	if err := productTurnChildResolved(bundle, checkpoint.Pins); err != nil {
		return unknown("the retained turn has unresolved Store or workflow state", err)
	}
	if after, err := e.MeasureProjectBaseline(turn.ProjectID); err != nil || after != turn.BaselineDigest {
		return unknown("the project is not the admitted baseline", err)
	}
	records, _, err := e.workflowPublication(turn.ProjectID, turn.Link.WorkflowID)
	if err != nil || records {
		return unknown("the publication journal names the workflow or cannot be read", err)
	}
	providerTrace, err := retained.ProviderJournal.Trace()
	if err != nil {
		return unknown("the retained provider journal cannot be read", err)
	}
	storeTrace, err := retained.StoreJournal.Trace()
	if err != nil {
		return unknown("the retained Store journal cannot be read", err)
	}
	commandTrace, err := retained.CommandJournal.Trace()
	if err != nil {
		return unknown("the retained command journal cannot be read", err)
	}
	// The dead process's elapsed time is not retained; the reserved duration bounds it.
	usage := productsession.Budget{HostCalls: int64(checkpoint.Counters.HostCalls), Tokens: int64(checkpoint.Counters.Tokens),
		DurationSeconds: turn.Reservation.DurationSeconds}
	link := turn.Link
	// shortcut: the tool sequence and denials of the dead worker are not rebuilt from the provider
	// journal; derive them once a receipt consumer needs them after a restart.
	receipt := productTurnReceipt{SchemaVersion: productTurnReceiptSchema,
		SessionID: turn.SessionID, TurnID: turn.ID, Ordinal: turn.Ordinal,
		WorkflowID: link.WorkflowID, RunID: link.RunID, ConversationID: link.ConversationID,
		UnverifiedInputs: []string{"runtime_digest", "image_digest", "extension_digest"},
		WorkerKind:       productsession.WorkerSupervisorLoop, WorkerInstance: productTurnUnrecordedWorker,
		ProviderJournalHead: auditHeadHash(providerTrace.Records), ProviderJournalCount: uint64(len(providerTrace.Records)),
		StoreJournalHead: auditHeadHash(storeTrace.Records), StoreJournalCount: uint64(len(storeTrace.Records)),
		CommandJournalHead: auditHeadHash(commandTrace.Records), CommandJournalCount: uint64(len(commandTrace.Records)),
		ImportedPrefixDigest: link.ImportedPrefixDigest, PredecessorReceiptDigest: turn.PredecessorReceiptDigest,
		BaselineDigest: turn.BaselineDigest, ResultBaselineDigest: turn.BaselineDigest,
		Outcome: productsession.OutcomeFailed, Settlement: productsession.SettlementReconciledAfterRestart,
		Usage: usage, ToolSequence: []string{}, Denials: []productTurnDenial{}}
	// A turn that died before its prompt was admitted has an empty provider journal, which a receipt
	// cannot commit, so it ends unknown here.
	encoded, err := encodeProductTurnReceipt(receipt)
	if err != nil {
		return unknown("the reconciled receipt cannot be built", err)
	}
	if err := e.Catalog.CompleteTurn(turn.ID, productsession.TurnTerminal{Outcome: productsession.OutcomeFailed,
		Settlement: receipt.Settlement, Receipt: encoded, Usage: usage, ResultBaselineDigest: turn.BaselineDigest}); err != nil {
		return unknown("the catalog refused the reconciled settlement", err)
	}
	return nil
}

// reconcileReviewTurn ends a review that did not survive the restart. A publish resolution whose
// workflow transaction SUCCEEDED with the project at the candidate is published. With no
// publication record for the workflow and the project at the admitted baseline, a discard
// resolution is discarded by the user and anything else discarded by the restart. Otherwise unknown.
func (e *productTurnEnv) reconcileReviewTurn(turn productsession.Turn) error {
	records, succeeded, err := e.workflowPublication(turn.ProjectID, turn.Link.WorkflowID)
	after, measureErr := e.MeasureProjectBaseline(turn.ProjectID)
	err = errors.Join(err, measureErr)
	switch {
	case err == nil && turn.Resolution == productsession.DecisionPublish && succeeded != "" && after == turn.CandidateDigest:
		_, err = e.completeReviewed(turn, productsession.OutcomePublished, "", succeeded)
	case err == nil && !records && after == turn.BaselineDigest:
		reason := productsession.ReasonRestart
		if turn.Resolution == productsession.DecisionDiscard {
			reason = productsession.ReasonUser
		}
		_, err = e.completeReviewed(turn, productsession.OutcomeDiscarded, reason, "")
	default:
		err = errors.Join(fmt.Errorf("product turn %s is unknown after restart: review disposition not established", turn.ID),
			err, e.Catalog.MarkTurnUnknown(turn.ID, "the review disposition is not established after restart"))
	}
	return err
}

// workflowPublication reads the project's publication lifecycle journal for one workflow: whether
// any pending, transaction or resolved record names it, and the transaction ID when the journal
// shows its transaction resolved SUCCEEDED. A project without a journal has no record.
func (e *productTurnEnv) workflowPublication(projectID, workflowID string) (bool, string, error) {
	path := filepath.Join(e.projectPublicationDir(projectID), "publication-audit.jsonl")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	journal, err := openExistingRecoveryJournal(path)
	if err != nil {
		return false, "", err
	}
	trace, err := journal.Trace()
	if err = errors.Join(err, journal.Close()); err != nil {
		return false, "", err
	}
	named := false
	var transaction, succeeded string
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "workflow_publication_pending", "workflow_publication_transaction", "workflow_publication_resolved":
		default:
			continue
		}
		var event struct {
			WorkflowID    string             `json:"workflow_id"`
			TransactionID string             `json:"transaction_id"`
			Status        publication.Status `json:"status"`
		}
		if err := json.Unmarshal(record.Event.Data, &event); err != nil {
			return false, "", fmt.Errorf("decode publication record %d: %w", record.Sequence, err)
		}
		if event.WorkflowID != workflowID {
			continue
		}
		named = true
		switch {
		case record.Event.Kind == "workflow_publication_transaction":
			transaction = event.TransactionID
		case record.Event.Kind == "workflow_publication_resolved" && event.Status == publication.StatusSucceeded &&
			event.TransactionID != "" && event.TransactionID == transaction:
			succeeded = transaction
		}
	}
	return named, succeeded, nil
}

// loadRetainedProductCheckpoint reads a turn's latest owner checkpoint read-only through its
// retained protected root. The root is opened existing-only with a drain verifier and fresh policy
// that refuse everything, so nothing can be created or resumed.
func loadRetainedProductCheckpoint(ctx context.Context, turnDir, workflowID string) (workflowrecovery.Checkpoint, error) {
	ownerStoreDir := filepath.Join(turnDir, "owner-store")
	boundary, err := workflowrecovery.NewLinuxHostBoundary(ownerStoreDir, uint32(os.Geteuid()), productTurnNoPriorOwner{},
		&productTurnFreshPolicy{})
	if err != nil {
		return workflowrecovery.Checkpoint{}, err
	}
	root, err := workflowrecovery.OpenExistingProtectedRoot(filepath.Join(turnDir, "recovery-root", "workflow-root.sqlite"),
		ownerStoreDir, boundary)
	if err != nil {
		return workflowrecovery.Checkpoint{}, err
	}
	defer root.Close()
	view, err := workflowrecovery.LoadSettledCheckpoint(ctx, filepath.Join(ownerStoreDir, "workflow.sqlite"), root, workflowID)
	if err != nil {
		return workflowrecovery.Checkpoint{}, err
	}
	if view.Digest != view.Checkpoint.ComputedCheckpointDigest || view.Checkpoint.Pins.WorkflowID != workflowID {
		return workflowrecovery.Checkpoint{}, errors.New("retained owner checkpoint does not belong to the turn's workflow")
	}
	return view.Checkpoint, nil
}
