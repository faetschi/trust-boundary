//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"syscall"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

// productTurnRefusal is a launch the driver refused before linking the turn: the catalog recorded
// the turn as failed with a full refund. errors.Is matches productsession.ErrCode(Code).
type productTurnRefusal struct {
	Code  productsession.Code
	Cause error
}

func (r *productTurnRefusal) Error() string {
	return fmt.Sprintf("product turn launch refused (%s): %v", r.Code, r.Cause)
}

func (r *productTurnRefusal) Unwrap() []error {
	return []error{productsession.ErrCode(r.Code), r.Cause}
}

func (e *productTurnEnv) refuse(turnID string, code productsession.Code, cause error) error {
	if err := e.Catalog.RefuseUnlinkedTurn(turnID, code); err != nil {
		return errors.Join(cause, err)
	}
	return &productTurnRefusal{Code: code, Cause: cause}
}

// abandon withdraws an admitted turn the driver could not compose; no provider or tool work happened.
func (e *productTurnEnv) abandon(turnID string, cause error) error {
	return errors.Join(cause, e.Catalog.AbandonUnlinkedTurn(turnID))
}

// RunTurn runs one admitted turn to a settled catalog disposition: it rebuilds the predecessor
// context from retained evidence, checks the managed project against the admitted baseline,
// composes a fresh workflow, links the turn, drives the supervisor loop, and settles the outcome
// with a receipt. A launch refused before the link returns a *productTurnRefusal.
func (e *productTurnEnv) RunTurn(ctx context.Context, admission productsession.Admission) (_ productTurnReceipt, err error) {
	e.mu.Lock()
	if e.running[admission.Turn.ID] {
		e.mu.Unlock()
		return productTurnReceipt{}, fmt.Errorf("turn %s is already running", admission.Turn.ID)
	}
	if e.running == nil {
		e.running = map[string]bool{}
	}
	e.running[admission.Turn.ID] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, admission.Turn.ID)
		e.mu.Unlock()
	}()
	// The catalog state, not the admission copy: CancelTurn may have ended the turn already.
	turn, _ := e.Catalog.Turn(admission.Turn.ID)
	if turn.State != productsession.StateAdmitted {
		return productTurnReceipt{}, fmt.Errorf("turn %s is %s, not admitted", admission.Turn.ID, turn.State)
	}
	host := e.host()
	if err := host.SettleRetainedCommands(turn.ID); err != nil {
		return productTurnReceipt{}, err
	}
	if turn.CancelRequested {
		return productTurnReceipt{}, e.refuse(turn.ID, productsession.CodeCancelled, errProductTurnCancelled)
	}
	session, err := e.Catalog.Session(turn.Principal, turn.SessionID)
	if err != nil {
		return productTurnReceipt{}, err
	}
	predecessor, err := host.PredecessorContext(turn, session.ProviderProfile)
	if err != nil {
		if errors.Is(err, audit.ErrAlreadyOpen) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EBUSY) ||
			errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
			return productTurnReceipt{}, e.abandon(turn.ID, err)
		}
		return productTurnReceipt{}, e.refuse(turn.ID, productsession.CodePredecessorEvidenceInvalid, err)
	}
	measured, err := host.MeasureProjectBaseline(turn.ProjectID)
	if err != nil {
		return productTurnReceipt{}, e.abandon(turn.ID, err)
	}
	if measured != turn.BaselineDigest {
		return productTurnReceipt{}, e.refuse(turn.ID, productsession.CodeStaleBaseline, errProductStaleBaseline)
	}
	worker, err := newProductTurnWorker(e.MaxWorkerSteps, func() bool { return e.cancelRequested(turn.ID) }, host.Crash)
	if err != nil {
		return productTurnReceipt{}, e.abandon(turn.ID, err)
	}
	prepared, err := host.PrepareTurn(ctx, turn, session.ProviderProfile, predecessor)
	if err != nil {
		if errors.Is(err, errProductCommandUnsettled) {
			return productTurnReceipt{}, err
		}
		if errors.Is(err, errProductStaleBaseline) {
			return productTurnReceipt{}, e.refuse(turn.ID, productsession.CodeStaleBaseline, err)
		}
		return productTurnReceipt{}, e.abandon(turn.ID, err)
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
			return productTurnReceipt{}, errors.Join(err, errProductCommandUnsettled, cleanupErr)
		}
		return productTurnReceipt{}, e.abandon(turn.ID, err)
	}
	if e.cancelRequested(turn.ID) {
		if err := prepared.Close(); err != nil {
			return productTurnReceipt{}, errors.Join(errProductCommandUnsettled, err)
		}
		return productTurnReceipt{}, e.refuse(turn.ID, productsession.CodeCancelled, errProductTurnCancelled)
	}
	host.Crash("before-link")
	if err := e.Catalog.LinkTurn(turn.ID, prepared.Link()); err != nil {
		if cleanupErr := prepared.Close(); cleanupErr != nil {
			return productTurnReceipt{}, errors.Join(err, errProductCommandUnsettled, cleanupErr)
		}
		if errors.Is(err, productsession.ErrCode(productsession.CodeCancelled)) {
			return productTurnReceipt{}, e.refuse(turn.ID, productsession.CodeCancelled, errProductTurnCancelled)
		}
		return productTurnReceipt{}, err
	}

	s := productTurnSettlement{env: e, turn: turn, prepared: prepared, worker: worker, predecessor: predecessor,
		resultBaseline: turn.BaselineDigest}
	s.work, s.runErr = prepared.Drive(ctx, turn, worker)
	releasePublication()
	if err := prepared.SettleCommands(); err != nil {
		return s.unknown("command ownership did not settle", err)
	}
	resolved, err := prepared.Evidence(productEvidenceResolved)
	if err != nil {
		return s.unknownEvidence(err)
	}
	sealed := resolved.Sealed
	switch {
	// A cancel request that arrives before the finalizer starts or the review is recorded still
	// prevents both; once the finalizer started, the turn reports its real disposition.
	case errors.Is(s.runErr, errProductTurnCancelled), s.runErr == nil && e.cancelRequested(turn.ID):
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
		e.mu.Lock()
		publicationStarted := !e.cancelRequested(turn.ID)
		e.mu.Unlock()
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
	// The review is recorded and held under e.mu, so a resolution or cancellation sees both or neither;
	// a cancel recorded before this section is honoured here, one after it finds the held review.
	e.mu.Lock()
	receipt, err := s.settle()
	var cancelled *productPendingReview
	if err == nil {
		heldForReview = true
		review := &productPendingReview{turn: prepared,
			deadline: time.Now().Add(time.Duration(e.Registration.Limits.ReviewTimeoutSeconds) * time.Second)}
		if e.cancelRequested(turn.ID) {
			cancelled = review
		} else {
			e.holdReviewLocked(turn.ID, review)
		}
	}
	e.mu.Unlock()
	if cancelled != nil {
		reviewed, _ := e.Catalog.Turn(turn.ID)
		return e.discardReview(reviewed, cancelled, productsession.ReasonCancelled)
	}
	if heldForReview {
		host.Crash("after-awaiting-review")
	}
	return receipt, err
}

// productTurnChildResolved requires the child workflow to have no unresolved work: every Store
// effect attempt succeeded without quarantine, and no provider call or effect reservation is open
// in the checkpoint pins.
func productTurnChildResolved(bundle sessionrepo.EvidenceBundle, pins workflowrecovery.Pins) error {
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil {
		return err
	}
	if disposition.Quarantined || disposition.Unknowns != 0 || disposition.Successes != disposition.Attempts {
		return errors.New("Store evidence is quarantined or has an unresolved effect")
	}
	return productTurnReservationsResolved(pins)
}

func productTurnReservationsResolved(pins workflowrecovery.Pins) error {
	if effect := pins.EffectReservation; effect != nil && effect.Disposition != workflowrecovery.EffectReservationTerminal &&
		effect.Disposition != workflowrecovery.EffectReservationRetired {
		return errors.New("an effect reservation is unresolved")
	}
	if call := pins.HostCallReservation; call != nil && call.Disposition != workflowrecovery.HostCallTerminal {
		return errors.New("a provider host call is unresolved")
	}
	if pins.PendingCallDisposition == workflowrecovery.PendingCallCaptured || pins.PendingCallDisposition == workflowrecovery.PendingCallUnknown {
		return errors.New("a captured provider call is unresolved")
	}
	return nil
}

// productTurnSettlement is a linked turn on its way to a catalog disposition.
type productTurnSettlement struct {
	env            *productTurnEnv
	turn           productsession.Turn
	prepared       productPreparedTurn
	worker         *productTurnWorker
	predecessor    providerbridge.PredecessorContext
	work           productTurnWork
	runErr         error
	outcome        string
	resultBaseline string
	transactionID  string
	candidate      string
}

// unknown records a linked turn whose disposition cannot be established. It never claims success.
func (s *productTurnSettlement) unknown(reason string, cause error) (productTurnReceipt, error) {
	return productTurnReceipt{}, errors.Join(fmt.Errorf("product turn %s is unknown: %s", s.turn.ID, reason), cause,
		s.runErr, s.env.Catalog.MarkTurnUnknown(s.turn.ID, reason))
}

// unknownEvidence records a turn whose host could not establish its evidence.
func (s *productTurnSettlement) unknownEvidence(err error) (productTurnReceipt, error) {
	var evidence *productTurnEvidenceError
	if errors.As(err, &evidence) {
		return s.unknown(evidence.Reason, evidence.Cause)
	}
	return s.unknown("the turn evidence could not be read", err)
}

// settle writes the receipt of the closed turn and records its disposition in the catalog.
func (s *productTurnSettlement) settle() (productTurnReceipt, error) {
	if err := s.prepared.SettleCommands(); err != nil {
		return s.unknown("command ownership did not settle", err)
	}
	ev, err := s.prepared.Evidence(productEvidenceSettlement)
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
	receipt := productTurnReceipt{SchemaVersion: productTurnReceiptSchema,
		SessionID: s.turn.SessionID, TurnID: s.turn.ID, Ordinal: s.turn.Ordinal,
		WorkflowID: link.WorkflowID, RunID: link.RunID, ConversationID: link.ConversationID,
		UnverifiedInputs: []string{"runtime_digest", "image_digest", "extension_digest"},
		WorkerKind:       productsession.WorkerSupervisorLoop, WorkerInstance: s.worker.instance,
		ProviderJournalHead: ev.ProviderHead, ProviderJournalCount: ev.ProviderCount,
		StoreJournalHead: ev.StoreHead, StoreJournalCount: ev.StoreCount,
		CommandJournalHead: ev.CommandHead, CommandJournalCount: ev.CommandCount,
		ImportedPrefixDigest: s.predecessor.Digest(), PredecessorReceiptDigest: s.predecessor.PredecessorReceiptDigest(),
		BaselineDigest: s.turn.BaselineDigest, ResultBaselineDigest: s.resultBaseline,
		Outcome: s.outcome, Settlement: productsession.SettlementSupervisorLoopClosed,
		PublicationTransactionID: s.transactionID, Usage: usage,
		ToolSequence: s.work.toolSequence, Denials: s.work.denials}
	if receipt.ToolSequence == nil {
		receipt.ToolSequence, receipt.Denials = []string{}, []productTurnDenial{}
	}
	encoded, err := encodeProductTurnReceipt(receipt)
	if err != nil {
		return s.unknown("the turn receipt could not be built", err)
	}
	if s.outcome == "awaiting_review" {
		err = s.env.Catalog.MarkAwaitingReview(s.turn.ID, productsession.TurnReview{Settlement: receipt.Settlement,
			Receipt: encoded, Usage: usage, CandidateDigest: s.candidate})
	} else {
		err = s.env.Catalog.CompleteTurn(s.turn.ID, productsession.TurnTerminal{Outcome: s.outcome, Settlement: receipt.Settlement,
			Receipt: encoded, Usage: usage, ResultBaselineDigest: s.resultBaseline, ContextEligible: s.work.finalAnswer})
	}
	if err != nil {
		return s.unknown("the catalog refused the turn settlement", err)
	}
	return receipt, s.runErr
}

// predecessorContext rebuilds the history a turn imports. It reopens the predecessor's retained
// journals and Store, checks them against its receipt, and replays a context-eligible predecessor
// into a context ending in the boundary message; a predecessor that is not context-eligible adds
// only its boundary to the context it imported itself. Any failure wraps errProductTurnEvidence.
func (e *productTurnEnv) predecessorContext(turn productsession.Turn, providerProfile string) (providerbridge.PredecessorContext, error) {
	var none providerbridge.PredecessorContext
	if turn.PredecessorTurnID == "" {
		return none, nil
	}
	evidence := func(err error) (providerbridge.PredecessorContext, error) {
		return none, fmt.Errorf("%w: predecessor %s: %w", errProductTurnEvidence, turn.PredecessorTurnID, err)
	}
	pred, ok := e.Catalog.Turn(turn.PredecessorTurnID)
	// A turn refused before its link has no journals or receipt; its own refusal blocked the session
	// or project, so a successor cannot carry verified history across it.
	if !ok || pred.SessionID != turn.SessionID || pred.State != productsession.StateTerminal || pred.Link == nil ||
		pred.ReceiptDigest == "" || pred.ReceiptDigest != turn.PredecessorReceiptDigest {
		return evidence(errors.New("no settled, linked receipt"))
	}
	receipt, err := decodeProductTurnReceipt(pred.Receipt)
	if err != nil {
		return evidence(err)
	}
	paths, err := productTurnPathsFromLink(*pred.Link)
	if err != nil {
		return evidence(err)
	}
	retained, err := openRetainedProductTurn(paths, e.storeOptions(pred), e.OpenCommandHistory)
	if err != nil {
		return evidence(err)
	}
	defer retained.Close()
	if err := verifyProductTurnReceiptJournals(receipt, retained.ProviderJournal, retained.StoreJournal, retained.CommandJournal); err != nil {
		return evidence(err)
	}
	boundary := providerbridge.TurnBoundary{PredecessorReceiptDigest: pred.ReceiptDigest, Outcome: pred.Outcome,
		ProjectBaselineDigest: delta.TreeDigestProfile + bareWorkflowDigest(turn.BaselineDigest)}
	if !pred.ContextEligible {
		previous, err := e.predecessorContext(pred, providerProfile)
		if err != nil {
			return none, err
		}
		if previous.Digest() != pred.Link.ImportedPrefixDigest {
			return evidence(errors.New("rebuilt context differs from the imported prefix it linked"))
		}
		return providerbridge.ExtendPredecessorContext(previous, boundary)
	}
	policy, err := e.gatePolicy(pred.ProjectID, pred.PolicyDigest)
	if err != nil {
		return evidence(err)
	}
	bundle, err := retained.Store.EvidenceBundle()
	if err != nil {
		return evidence(err)
	}
	if sha256DigestFromBarePin(bareStoreTreeDigest(bundle.Baseline.TreeDigest)) != receipt.BaselineDigest {
		return evidence(errors.New("retained Store baseline differs from the receipt"))
	}
	providerTrace, err := retained.ProviderJournal.Trace()
	if err != nil {
		return evidence(err)
	}
	config := providerbridge.Config{ProductCaptureRefusals: true, ProductSoftRefusals: true, WorkflowID: pred.Link.WorkflowID, ConversationID: pred.Link.ConversationID,
		ProfileID: providerProfile, InitialGenerationID: bundle.Baseline.ID, InitialTreeDigest: bundle.Baseline.TreeDigest,
		SystemPrompt: e.SystemPrompt, DeveloperPrompt: e.DeveloperPrompt}
	config.Route = config.Route.WithCommandOutput(true) // a product turn's executor releases bounded Bash output
	route := config.Route.Resolved()
	pins := GovernedReplayPins{
		Provider: providerbridge.RecoveryPins{Config: config, Model: route.Model, Policy: policy, RequireHostCallReservations: true,
			ImportedPrefixDigest: importedPrefixReplayPin(pred.Link.ImportedPrefixDigest, len(providerTrace.Records))},
		Profile: broker.Profile{ID: providerProfile, Model: route.Model, Route: config.Route,
			Messages:     providerbridge.InitialPromptMessages(route, e.SystemPrompt, e.DeveloperPrompt),
			ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
			ResponseIDIssuer: governedResponseIssuer, Generation: bundle.Baseline.ID},
		Baseline: bundle.Baseline, StorePolicyDigest: e.StorePolicyDigest, MetadataPolicyDigest: e.MetadataPolicyDigest,
		BashOutputPaths:    productTurnBashScope(pred, policy),
		CommandOutputBytes: productCommandOutputBytes,
	}
	var commands []governedRecoveryCommandVerifier
	if retained.Commands != nil {
		commands = append(commands, retained.Commands)
	}
	replayed, _, err := ReplayGovernedPredecessorContext(retained.ProviderJournal, retained.StoreJournal, retained.Store, pins, boundary, commands...)
	if err == nil && retained.Commands != nil {
		err = retained.Commands.VerifyUnchanged()
	}
	if err != nil {
		return evidence(err)
	}
	return replayed, nil
}
