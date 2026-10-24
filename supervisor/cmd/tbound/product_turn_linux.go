//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
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
// composes a fresh workflow, links the turn, drives the headless worker, and settles the outcome
// with a receipt. A launch refused before the link returns a *productTurnRefusal.
func (e *productTurnEnv) RunTurn(ctx context.Context, admission productsession.Admission) (_ productTurnReceipt, err error) {
	turn := admission.Turn
	if turn.State != productsession.StateAdmitted {
		return productTurnReceipt{}, fmt.Errorf("turn %s is %s, not admitted", turn.ID, turn.State)
	}
	session, err := e.Catalog.Session(turn.Principal, turn.SessionID)
	if err != nil {
		return productTurnReceipt{}, err
	}
	predecessor, err := e.predecessorContext(turn, session.ProviderProfile)
	if err != nil {
		return productTurnReceipt{}, e.refuse(turn.ID, productsession.CodePredecessorEvidenceInvalid, err)
	}
	measured, err := e.MeasureProjectBaseline(turn.ProjectID)
	if err != nil {
		return productTurnReceipt{}, e.abandon(turn.ID, err)
	}
	if measured != turn.BaselineDigest {
		return productTurnReceipt{}, e.refuse(turn.ID, productsession.CodeStaleBaseline, errProductStaleBaseline)
	}
	if len(turn.BashOutputPaths) != 0 {
		// The historical Store verifier cannot reopen a Bash turn as a predecessor yet.
		return productTurnReceipt{}, e.abandon(turn.ID, errors.New("product turns do not compose Bash yet"))
	}
	worker, err := newProductTurnWorker(e.MaxWorkerSteps)
	if err != nil {
		return productTurnReceipt{}, e.abandon(turn.ID, err)
	}
	w, err := e.composeProductTurnWorkflow(ctx, turn, session.ProviderProfile, predecessor)
	if err != nil {
		if errors.Is(err, errProductStaleBaseline) {
			return productTurnReceipt{}, e.refuse(turn.ID, productsession.CodeStaleBaseline, err)
		}
		return productTurnReceipt{}, e.abandon(turn.ID, err)
	}
	defer func() { err = errors.Join(err, w.close()) }()
	var publish *PublicationRuntime
	releasePublication := func() {}
	if turn.PublicationMode == productsession.PublicationAuto {
		if publish, releasePublication, err = e.beginProductPublication(ctx, turn.ProjectID, w); err != nil {
			return productTurnReceipt{}, e.abandon(turn.ID, err)
		}
	}
	if err := e.Catalog.LinkTurn(turn.ID, w.link(predecessor)); err != nil {
		return productTurnReceipt{}, err
	}

	s := productTurnSettlement{env: e, turn: turn, w: w, worker: worker, predecessor: predecessor,
		resultBaseline: turn.BaselineDigest}
	s.work, s.runErr = e.driveProductTurn(ctx, turn, w, worker)
	releasePublication()
	bundle, err := w.store.EvidenceBundle()
	switch {
	case err != nil:
		return s.unknown("the turn's Store evidence could not be read", err)
	case s.runErr != nil:
		s.outcome = productsession.OutcomeFailed
	case bundleHasNoChange(bundle):
		s.outcome = productsession.OutcomeNoDiff
	case turn.PublicationMode == productsession.PublicationReview:
		s.outcome = "awaiting_review"
		s.candidate = sha256DigestFromBarePin(bareStoreTreeDigest(bundle.ValidatedChain.SealedTreeDigest))
	default:
		result, finalizeErr := publish.Finalize(ctx)
		after, measureErr := e.MeasureProjectBaseline(turn.ProjectID)
		switch {
		case measureErr != nil:
			return s.unknown("the project could not be measured after publication", errors.Join(finalizeErr, measureErr))
		case finalizeErr == nil && result.Status == publication.StatusSucceeded && len(result.Objects) != 0:
			s.outcome, s.resultBaseline, s.transactionID = productsession.OutcomePublished, after, result.TransactionID
		case result.Status == publication.StatusFailed && after == turn.BaselineDigest:
			s.outcome, s.runErr = productsession.OutcomeFailed, errors.Join(errors.New("publication failed"), finalizeErr)
		default:
			return s.unknown("the publication outcome is not established", finalizeErr)
		}
	}
	return s.settle()
}

// driveProductTurn constructs the provider conversation with the imported context, admits the
// turn's prompt, runs the worker and closes the conversation.
func (e *productTurnEnv) driveProductTurn(ctx context.Context, turn productsession.Turn, w *productTurnWorkflow,
	worker *productTurnWorker) (productTurnWork, error) {
	if e.NewConversation == nil {
		return productTurnWork{}, errors.New("product turn has no provider conversation factory")
	}
	conversation, err := w.authority.newProviderConversation(w.config, e.NewConversation)
	if err != nil {
		return productTurnWork{}, err
	}
	defer conversation.Close()
	if err := w.authority.AdmitPrompt(conversation, w.durable, w.taskID, turn.Prompt); err != nil {
		return productTurnWork{}, err
	}
	return worker.run(ctx, conversation, w.durable, w.policy)
}

// beginProductPublication composes the existing publication finalizer over the project's managed
// directory and lifecycle journal, recovers earlier publications of that root, and holds session
// admission until the returned release, which the driver calls once the worker has finished.
func (e *productTurnEnv) beginProductPublication(ctx context.Context, projectID string, w *productTurnWorkflow) (*PublicationRuntime, func(), error) {
	if e.PublicationTrust == nil {
		return nil, nil, errors.New("auto-publish turn has no publication trust")
	}
	trust := e.PublicationTrust(w.authority)
	if trust == nil {
		return nil, nil, errors.New("auto-publish turn has no publication trust")
	}
	projectKey := sha256.Sum256([]byte(projectID))
	projectDir := filepath.Join(e.StateRoot, "projects", hex.EncodeToString(projectKey[:]))
	statePath := filepath.Join(projectDir, "publication-state")
	if err := os.MkdirAll(statePath, 0o700); err != nil {
		return nil, nil, err
	}
	journal, err := audit.Open(filepath.Join(projectDir, "publication-audit.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	w.closers = append(w.closers, journal.Close)
	stateRoot, err := os.Open(statePath)
	if err != nil {
		return nil, nil, err
	}
	w.closers = append(w.closers, stateRoot.Close)
	ledger, err := workflow.NewRootLedger(journal)
	if err != nil {
		return nil, nil, err
	}
	publicationID, err := randomProductIdentity("ppub-")
	if err != nil {
		return nil, nil, err
	}
	open := func(workflowID string, ws workspace.Options) (*publication.Repository, error) {
		return publication.Acquire(publication.Options{WorkflowID: workflowID, OwnerEpoch: 1,
			StateRoot: stateRoot, Journal: journal, Workspace: ws})
	}
	ws := e.projectWorkspace()
	runtime, err := NewPublicationRuntime(w.durable, workflow.FinalizerOptions{
		WorkflowID: w.workflowID, OwnerEpoch: 1, PublicationID: publicationID,
		LiveRoot: w.projectRoot, Baseline: w.baseline, Workspace: ws, Journal: journal, Ledger: ledger,
		OpenPublication: func() (*publication.Repository, error) { return open(w.workflowID, ws) },
		OpenRecovery: func(workflowID string) (workflow.RecoveryRepository, error) {
			recovery, err := managedRecoveryWorkspace(journal, w.projectRoot, workflowID)
			if err != nil {
				return nil, err
			}
			return open(workflowID, recovery)
		},
		Trust: trust,
	})
	if err != nil {
		return nil, nil, err
	}
	w.closers = append(w.closers, runtime.Close)
	release, err := runtime.BeginSession(ctx)
	if err != nil {
		return nil, nil, err
	}
	return runtime, release, nil
}

// productTurnSettlement is a linked turn on its way to a catalog disposition.
type productTurnSettlement struct {
	env            *productTurnEnv
	turn           productsession.Turn
	w              *productTurnWorkflow
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

// settle writes the receipt of the closed turn and records its disposition in the catalog.
func (s *productTurnSettlement) settle() (productTurnReceipt, error) {
	providerTrace, err := s.w.providerJournal.Trace()
	if err != nil {
		return s.unknown("the provider journal could not be read", err)
	}
	storeTrace, err := s.w.storeJournal.Trace()
	if err != nil {
		return s.unknown("the Store journal could not be read", err)
	}
	checkpoint, _ := s.w.ledger.Snapshot()
	reservation := s.turn.Reservation
	// shortcut: elapsed time after the ledger deadline (settlement, publication) is not billed beyond
	// the reservation; bill it once the catalog can record an overrun.
	elapsed := min(int64(math.Ceil(time.Since(s.w.started).Seconds())), reservation.DurationSeconds)
	usage := productsession.Budget{HostCalls: int64(checkpoint.Counters.HostCalls), Tokens: int64(checkpoint.Counters.Tokens),
		DurationSeconds: elapsed}
	receipt := productTurnReceipt{SchemaVersion: productTurnReceiptSchema,
		SessionID: s.turn.SessionID, TurnID: s.turn.ID, Ordinal: s.turn.Ordinal,
		WorkflowID: s.w.workflowID, RunID: s.w.runID, ConversationID: s.w.conversationID,
		WorkerKind: productsession.WorkerHeadlessFixture, WorkerInstance: s.worker.instance,
		ProviderJournalHead: auditHeadHash(providerTrace.Records), ProviderJournalCount: uint64(len(providerTrace.Records)),
		StoreJournalHead: auditHeadHash(storeTrace.Records), StoreJournalCount: uint64(len(storeTrace.Records)),
		ImportedPrefixDigest: s.predecessor.Digest(), PredecessorReceiptDigest: s.predecessor.PredecessorReceiptDigest(),
		BaselineDigest: s.turn.BaselineDigest, ResultBaselineDigest: s.resultBaseline,
		Outcome: s.outcome, Settlement: productsession.SettlementHeadlessClosed,
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
	retained, err := openRetainedProductTurn(paths, e.storeOptions())
	if err != nil {
		return evidence(err)
	}
	defer retained.Close()
	if err := verifyProductTurnReceiptJournals(receipt, retained.ProviderJournal, retained.StoreJournal); err != nil {
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
	config := providerbridge.Config{WorkflowID: pred.Link.WorkflowID, ConversationID: pred.Link.ConversationID,
		ProfileID: providerProfile, InitialGenerationID: bundle.Baseline.ID, InitialTreeDigest: bundle.Baseline.TreeDigest,
		SystemPrompt: e.SystemPrompt, DeveloperPrompt: e.DeveloperPrompt}
	route := config.Route.Resolved()
	pins := GovernedReplayPins{
		Provider: providerbridge.RecoveryPins{Config: config, Model: route.Model, Policy: policy, RequireHostCallReservations: true,
			ImportedPrefixDigest: importedPrefixReplayPin(pred.Link.ImportedPrefixDigest, len(providerTrace.Records))},
		Profile: broker.Profile{ID: providerProfile, Model: route.Model, Route: config.Route,
			Messages:     providerbridge.InitialPromptMessages(route, e.SystemPrompt, e.DeveloperPrompt),
			ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
			ResponseIDIssuer: governedResponseIssuer, Generation: bundle.Baseline.ID},
		Baseline: bundle.Baseline, StorePolicyDigest: e.StorePolicyDigest, MetadataPolicyDigest: e.MetadataPolicyDigest,
	}
	replayed, _, err := ReplayGovernedPredecessorContext(retained.ProviderJournal, retained.StoreJournal, retained.Store, pins, boundary)
	if err != nil {
		return evidence(err)
	}
	return replayed, nil
}
