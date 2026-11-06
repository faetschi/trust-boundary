//go:build linux

package productdriver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"syscall"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/publication"
)

func (d *Driver) refuse(turnID string, code productsession.Code, cause error) error {
	if err := d.catalog.RefuseUnlinkedTurn(turnID, code); err != nil {
		return errors.Join(cause, err)
	}
	return &Refusal{Code: code, Cause: cause}
}

// abandon withdraws an admitted turn the driver could not compose; no provider or tool work happened.
func (d *Driver) abandon(turnID string, cause error) error {
	return errors.Join(cause, d.catalog.AbandonUnlinkedTurn(turnID))
}

// RunTurn runs one admitted turn to a settled catalog disposition: it rebuilds the predecessor
// context from retained evidence, checks the managed project against the admitted baseline,
// composes a fresh workflow, links the turn, drives the supervisor loop, and settles the outcome
// with a receipt. A launch refused before the link returns a *Refusal.
func (d *Driver) RunTurn(ctx context.Context, admission productsession.Admission) (_ productsession.TurnReceipt, err error) {
	d.mu.Lock()
	if d.running[admission.Turn.ID] {
		d.mu.Unlock()
		return productsession.TurnReceipt{}, fmt.Errorf("turn %s is already running", admission.Turn.ID)
	}
	if d.running == nil {
		d.running = map[string]bool{}
	}
	d.running[admission.Turn.ID] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.running, admission.Turn.ID)
		d.mu.Unlock()
	}()
	// The catalog state, not the admission copy: CancelTurn may have ended the turn already.
	turn, _ := d.catalog.Turn(admission.Turn.ID)
	if turn.State != productsession.StateAdmitted {
		return productsession.TurnReceipt{}, fmt.Errorf("turn %s is %s, not admitted", admission.Turn.ID, turn.State)
	}
	host := d.host
	if err := host.SettleRetainedCommands(turn.ID); err != nil {
		return productsession.TurnReceipt{}, err
	}
	if turn.CancelRequested {
		return productsession.TurnReceipt{}, d.refuse(turn.ID, productsession.CodeCancelled, ErrTurnCancelled)
	}
	session, err := d.catalog.Session(turn.Principal, turn.SessionID)
	if err != nil {
		return productsession.TurnReceipt{}, err
	}
	predecessor, err := host.PredecessorContext(turn, session.ProviderProfile)
	if err != nil {
		if errors.Is(err, audit.ErrAlreadyOpen) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EBUSY) ||
			errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
			return productsession.TurnReceipt{}, d.abandon(turn.ID, err)
		}
		return productsession.TurnReceipt{}, d.refuse(turn.ID, productsession.CodePredecessorEvidenceInvalid, err)
	}
	measured, err := host.MeasureProjectBaseline(turn.ProjectID)
	if err != nil {
		return productsession.TurnReceipt{}, d.abandon(turn.ID, err)
	}
	if measured != turn.BaselineDigest {
		return productsession.TurnReceipt{}, d.refuse(turn.ID, productsession.CodeStaleBaseline, ErrStaleBaseline)
	}
	prepared, err := host.PrepareTurn(ctx, turn, session.ProviderProfile, predecessor, func() bool { return d.cancelRequested(turn.ID) })
	if err != nil {
		if errors.Is(err, ErrCommandsUnsettled) {
			return productsession.TurnReceipt{}, err
		}
		if errors.Is(err, ErrStaleBaseline) {
			return productsession.TurnReceipt{}, d.refuse(turn.ID, productsession.CodeStaleBaseline, err)
		}
		return productsession.TurnReceipt{}, d.abandon(turn.ID, err)
	}
	heldForReview := false
	defer func() {
		if !heldForReview {
			err = errors.Join(err, prepared.CloseUnlessRetained())
		}
	}()
	// Both publication modes compose the finalizer at start; a review turn keeps it for its resolution.
	releasePublication, err := prepared.BeginPublication(ctx, turn.ProjectID)
	if err != nil {
		if cleanupErr := prepared.Close(); cleanupErr != nil {
			return productsession.TurnReceipt{}, errors.Join(err, ErrCommandsUnsettled, cleanupErr)
		}
		return productsession.TurnReceipt{}, d.abandon(turn.ID, err)
	}
	if d.cancelRequested(turn.ID) {
		if err := prepared.Close(); err != nil {
			return productsession.TurnReceipt{}, errors.Join(ErrCommandsUnsettled, err)
		}
		return productsession.TurnReceipt{}, d.refuse(turn.ID, productsession.CodeCancelled, ErrTurnCancelled)
	}
	host.Crash("before-link")
	if err := d.catalog.LinkTurn(turn.ID, prepared.Link()); err != nil {
		if cleanupErr := prepared.Close(); cleanupErr != nil {
			return productsession.TurnReceipt{}, errors.Join(err, ErrCommandsUnsettled, cleanupErr)
		}
		if errors.Is(err, productsession.ErrCode(productsession.CodeCancelled)) {
			return productsession.TurnReceipt{}, d.refuse(turn.ID, productsession.CodeCancelled, ErrTurnCancelled)
		}
		return productsession.TurnReceipt{}, err
	}

	s := settlement{d: d, turn: turn, prepared: prepared, predecessor: predecessor,
		resultBaseline: turn.BaselineDigest}
	s.work, s.runErr = prepared.Drive(ctx, turn)
	releasePublication()
	if err := prepared.SettleCommands(); err != nil {
		return s.unknown("command ownership did not settle", err)
	}
	resolved, err := prepared.Evidence(EvidenceResolved)
	if err != nil {
		return s.unknownEvidence(err)
	}
	sealed := resolved.Sealed
	switch {
	// A cancel request that arrives before the finalizer starts or the review is recorded still
	// prevents both; once the finalizer started, the turn reports its real disposition.
	case errors.Is(s.runErr, ErrTurnCancelled), s.runErr == nil && d.cancelRequested(turn.ID):
		s.outcome, s.runErr = productsession.OutcomeCancelled, nil
	case s.runErr != nil:
		s.outcome = productsession.OutcomeFailed
	case resolved.NoChange:
		s.outcome = productsession.OutcomeNoDiff
	case turn.PublicationMode == productsession.PublicationReview:
		s.outcome, s.candidate = "awaiting_review", sealed
	default:
		host.Crash("before-auto-publication")
		// This decision is the publication-start boundary, ordered with CancelTurn's durable record.
		// The running turn owns settlement afterwards; cancellation then only records its request.
		d.mu.Lock()
		publicationStarted := !d.cancelRequested(turn.ID)
		d.mu.Unlock()
		if !publicationStarted {
			s.outcome = productsession.OutcomeCancelled
			break
		}
		host.Crash("after-auto-publication-started")
		result, finalizeErr := prepared.Finalize(ctx)
		switch {
		case finalizeErr == nil && result.Status == publication.StatusSucceeded && len(result.Objects) != 0:
			s.outcome, s.resultBaseline, s.transactionID = productsession.OutcomePublished, sealed, result.TransactionID
		case result.Status == publication.StatusFailed:
			s.outcome, s.runErr = productsession.OutcomeFailed, errors.Join(errors.New("publication failed"), finalizeErr)
		default:
			return s.unknown("the publication outcome is not established", finalizeErr)
		}
	}
	// The project must hold exactly the tree this turn commits: the published sealed tree, or the
	// admitted baseline when nothing was published. Anything else is an external writer.
	after, err := host.MeasureProjectBaseline(turn.ProjectID)
	if err != nil {
		return s.unknown("the project could not be measured after the turn", err)
	}
	if after != s.resultBaseline {
		return s.unknown("the project changed outside the turn", nil)
	}
	if s.outcome != "awaiting_review" {
		return s.settle()
	}
	// The review is recorded and held under d.mu, so a resolution or cancellation sees both or neither;
	// a cancel recorded before this section is honoured here, one after it finds the held review.
	d.mu.Lock()
	receipt, err := s.settle()
	var cancelled *pendingReview
	if err == nil {
		heldForReview = true
		review := &pendingReview{turn: prepared,
			deadline: d.now().Add(time.Duration(d.reviewTimeoutSeconds) * time.Second)}
		if d.cancelRequested(turn.ID) {
			cancelled = review
		} else {
			d.holdReviewLocked(turn.ID, review)
		}
	}
	d.mu.Unlock()
	if cancelled != nil {
		reviewed, _ := d.catalog.Turn(turn.ID)
		return d.discardReview(reviewed, cancelled, productsession.ReasonCancelled)
	}
	if heldForReview {
		host.Crash("after-awaiting-review")
	}
	return receipt, err
}

// settlement is a linked turn on its way to a catalog disposition.
type settlement struct {
	d              *Driver
	turn           productsession.Turn
	prepared       PreparedTurn
	predecessor    Predecessor
	work           Work
	runErr         error
	outcome        string
	resultBaseline string
	transactionID  string
	candidate      string
}

// unknown records a linked turn whose disposition cannot be established. It never claims success.
func (s *settlement) unknown(reason string, cause error) (productsession.TurnReceipt, error) {
	return productsession.TurnReceipt{}, errors.Join(fmt.Errorf("product turn %s is unknown: %s", s.turn.ID, reason), cause,
		s.runErr, s.d.catalog.MarkTurnUnknown(s.turn.ID, reason))
}

// unknownEvidence records a turn whose host could not establish its evidence.
func (s *settlement) unknownEvidence(err error) (productsession.TurnReceipt, error) {
	var evidence *evidenceError
	if errors.As(err, &evidence) {
		return s.unknown(evidence.Reason, evidence.Cause)
	}
	return s.unknown("the turn evidence could not be read", err)
}

// settle writes the receipt of the closed turn and records its disposition in the catalog.
func (s *settlement) settle() (productsession.TurnReceipt, error) {
	if err := s.prepared.SettleCommands(); err != nil {
		return s.unknown("command ownership did not settle", err)
	}
	ev, err := s.prepared.Evidence(EvidenceSettlement)
	if err != nil {
		return s.unknownEvidence(err)
	}
	link := s.prepared.Link()
	reservation := s.turn.Reservation
	// shortcut: elapsed time after the ledger deadline (settlement, publication) is not billed beyond
	// the reservation; bill it once the catalog can record an overrun.
	elapsed := min(int64(math.Ceil(time.Since(ev.Started).Seconds())), reservation.DurationSeconds)
	usage := productsession.Budget{HostCalls: ev.HostCalls, Tokens: ev.Tokens,
		DurationSeconds: elapsed}
	receipt := productsession.TurnReceipt{SchemaVersion: productsession.ReceiptSchema,
		SessionID: s.turn.SessionID, TurnID: s.turn.ID, Ordinal: s.turn.Ordinal,
		WorkflowID: link.WorkflowID, RunID: link.RunID, ConversationID: link.ConversationID,
		UnverifiedInputs: []string{"runtime_digest", "image_digest", "extension_digest"},
		WorkerKind:       productsession.WorkerSupervisorLoop, WorkerInstance: s.prepared.WorkerInstance(),
		ProviderJournalHead: ev.ProviderHead, ProviderJournalCount: ev.ProviderCount,
		StoreJournalHead: ev.StoreHead, StoreJournalCount: ev.StoreCount,
		CommandJournalHead: ev.CommandHead, CommandJournalCount: ev.CommandCount,
		ImportedPrefixDigest: s.predecessor.Digest(), PredecessorReceiptDigest: s.predecessor.PredecessorReceiptDigest(),
		BaselineDigest: s.turn.BaselineDigest, ResultBaselineDigest: s.resultBaseline,
		Outcome: s.outcome, Settlement: productsession.SettlementSupervisorLoopClosed,
		PublicationTransactionID: s.transactionID, Usage: usage,
		ToolSequence: s.work.ToolSequence, Denials: s.work.Denials}
	if receipt.ToolSequence == nil {
		receipt.ToolSequence, receipt.Denials = []string{}, []productsession.ReceiptDenial{}
	}
	encoded, err := EncodeReceipt(receipt)
	if err != nil {
		return s.unknown("the turn receipt could not be built", err)
	}
	if s.outcome == "awaiting_review" {
		err = s.d.catalog.MarkAwaitingReview(s.turn.ID, productsession.TurnReview{Settlement: receipt.Settlement,
			Receipt: encoded, Usage: usage, CandidateDigest: s.candidate})
	} else {
		err = s.d.catalog.CompleteTurn(s.turn.ID, productsession.TurnTerminal{Outcome: s.outcome, Settlement: receipt.Settlement,
			Receipt: encoded, Usage: usage, ResultBaselineDigest: s.resultBaseline, ContextEligible: s.work.FinalAnswer})
	}
	if err != nil {
		return s.unknown("the catalog refused the turn settlement", err)
	}
	return receipt, s.runErr
}
