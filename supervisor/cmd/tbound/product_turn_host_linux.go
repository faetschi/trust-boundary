//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productdriver"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

// productTurnComposition is the host over the env's concrete composition.
type productTurnComposition struct{ env *productTurnEnv }

// driver is the process's product turn driver over this env's composition. It is built on first use,
// after the host profile has filled the env.
func (e *productTurnEnv) driver() *productdriver.Driver {
	e.driverOnce.Do(func() {
		e.drv = productdriver.New(productdriver.Config{Catalog: e.Catalog, Host: productTurnComposition{e},
			ReviewTimeoutSeconds: e.Registration.Limits.ReviewTimeoutSeconds, Now: e.reviewNow})
	})
	return e.drv
}

// reviewNow is the clock of review deadlines; reviewClock is a trusted-host test seam.
func (e *productTurnEnv) reviewNow() time.Time {
	if e.reviewClock != nil {
		return e.reviewClock()
	}
	return time.Now()
}

func (c productTurnComposition) MeasureProjectBaseline(projectID string) (string, error) {
	return c.env.MeasureProjectBaseline(projectID)
}

func (c productTurnComposition) PredecessorContext(turn productsession.Turn, providerProfile string) (productdriver.Predecessor, error) {
	return c.env.predecessorContext(turn, providerProfile)
}

func (c productTurnComposition) PrepareTurn(ctx context.Context, turn productsession.Turn, providerProfile string,
	predecessorValue productdriver.Predecessor, cancelled func() bool) (productdriver.PreparedTurn, error) {
	predecessor, ok := predecessorValue.(providerbridge.PredecessorContext)
	if !ok {
		return nil, errors.New("product turn predecessor is not a provider predecessor context")
	}
	worker, err := newProductTurnWorker(c.env.MaxWorkerSteps, cancelled, c.env.crash)
	if err != nil {
		return nil, err
	}
	w, err := c.env.composeProductTurnWorkflow(ctx, turn, providerProfile, predecessor)
	if err != nil {
		return nil, err
	}
	// The release goes first in the list, so it runs after the journals are closed.
	w.closers = append([]func() error{c.env.holdLiveProviderJournal(turn.ID, w.providerJournal)}, w.closers...)
	composed := &productComposedTurn{env: c.env, w: w, predecessor: predecessor, worker: worker}
	if c.env.onPrepared != nil {
		c.env.onPrepared(turn.ID, composed)
	}
	return composed, nil
}

func (c productTurnComposition) SettleRetainedCommands(turnID string) error {
	return c.env.settleRetainedProductCommands(turnID)
}

func (c productTurnComposition) CloseRetainedCommands() error {
	e := c.env
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
	return errors.Join(errs...)
}

func (c productTurnComposition) WorkflowPublication(projectID, workflowID string) (bool, string, error) {
	return c.env.workflowPublication(projectID, workflowID)
}

func (c productTurnComposition) Crash(point string) { c.env.crash(point) }

// ReconcileLinkedTurn is the evidence half of restart reconciliation of a linked turn: failed with
// settlement reconciled-after-restart is justified when the retained Store reconstructs cleanly, the
// owner checkpoint has no open reservation, the project still holds the admitted baseline and the
// publication journal has no record for the workflow.
func (c productTurnComposition) ReconcileLinkedTurn(ctx context.Context, turn productsession.Turn) (productdriver.Evidence, error) {
	e := c.env
	none := productdriver.Evidence{}
	paths, err := productTurnPathsFromLink(*turn.Link)
	if err != nil {
		return none, productdriver.Unestablished("the retained evidence paths are invalid", err)
	}
	retained, err := openRetainedProductTurn(paths, e.storeOptions(turn), e.OpenCommandHistory)
	if err != nil {
		if errors.Is(err, errHistoricalBashContext) {
			if interrupted, _, proofErr := openInterruptedProductBash(ctx, paths, e.Registration.Digest, e.storeOptions(turn)); proofErr == nil {
				return none, productdriver.Unestablished("the interrupted Bash scope is drained; run tbound product turns resolve to abandon the retained candidate", errors.Join(err, interrupted.Close()))
			}
			return none, productdriver.Unestablished("the retained Store has an unresolved Bash effect whose command scope is not proven drained", err)
		}
		return none, productdriver.Unestablished("the retained Store or journals cannot be reopened", err)
	}
	defer retained.Close()
	bundle, err := retained.Store.EvidenceBundle()
	if err != nil {
		return none, productdriver.Unestablished("the retained Store evidence cannot be read", err)
	}
	checkpoint, err := loadRetainedProductCheckpoint(ctx, filepath.Dir(paths.StoreJournalPath), turn.Link.WorkflowID)
	if err != nil {
		return none, productdriver.Unestablished("the retained owner checkpoint cannot be read", err)
	}
	if err := e.verifyRetainedSoftRefusals(turn, retained, checkpoint.Pins); err != nil {
		return none, productdriver.Unestablished("the retained soft refusal cannot be reproduced", err)
	}
	if err := productTurnChildResolved(bundle, checkpoint.Pins); err != nil {
		return none, productdriver.Unestablished("the retained turn has unresolved Store or workflow state", err)
	}
	if after, err := c.MeasureProjectBaseline(turn.ProjectID); err != nil || after != turn.BaselineDigest {
		return none, productdriver.Unestablished("the project is not the admitted baseline", err)
	}
	records, _, err := c.WorkflowPublication(turn.ProjectID, turn.Link.WorkflowID)
	if err != nil || records {
		return none, productdriver.Unestablished("the publication journal names the workflow or cannot be read", err)
	}
	providerTrace, err := retained.ProviderJournal.Trace()
	if err != nil {
		return none, productdriver.Unestablished("the retained provider journal cannot be read", err)
	}
	storeTrace, err := retained.StoreJournal.Trace()
	if err != nil {
		return none, productdriver.Unestablished("the retained Store journal cannot be read", err)
	}
	commandTrace, err := retained.CommandJournal.Trace()
	if err != nil {
		return none, productdriver.Unestablished("the retained command journal cannot be read", err)
	}
	return productdriver.Evidence{
		ProviderHead: auditHeadHash(providerTrace.Records), ProviderCount: uint64(len(providerTrace.Records)),
		StoreHead: auditHeadHash(storeTrace.Records), StoreCount: uint64(len(storeTrace.Records)),
		CommandHead: auditHeadHash(commandTrace.Records), CommandCount: uint64(len(commandTrace.Records)),
		HostCalls: int64(checkpoint.Counters.HostCalls), Tokens: int64(checkpoint.Counters.Tokens),
	}, nil
}

// productComposedTurn is a prepared turn over its composed workflow.
type productComposedTurn struct {
	env         *productTurnEnv
	w           *productTurnWorkflow
	predecessor providerbridge.PredecessorContext
	publish     *PublicationRuntime
	worker      *productTurnWorker
}

func (t *productComposedTurn) Link() productsession.TurnLink { return t.w.link(t.predecessor) }

func (t *productComposedTurn) BeginPublication(ctx context.Context, projectID string) (func(), error) {
	publish, release, err := t.env.beginProductPublication(ctx, projectID, t.w)
	if err != nil {
		return nil, err
	}
	t.publish = publish
	return release, nil
}

func (t *productComposedTurn) Drive(ctx context.Context, turn productsession.Turn) (productdriver.Work, error) {
	return t.env.driveProductTurn(ctx, turn, t.w, t.worker)
}

func (t *productComposedTurn) WorkerInstance() string { return t.worker.instance }

func (t *productComposedTurn) Evidence(stage productdriver.EvidenceStage) (productdriver.Evidence, error) {
	w := t.w
	if stage == productdriver.EvidenceResolved {
		bundle, err := w.store.EvidenceBundle()
		if err != nil {
			return productdriver.Evidence{}, productdriver.Unestablished("the turn's Store evidence could not be read", err)
		}
		checkpoint, _ := w.ledger.Snapshot()
		if err := productTurnChildResolved(bundle, checkpoint.Pins); err != nil {
			return productdriver.Evidence{}, productdriver.Unestablished("the turn left unresolved Store or workflow state", err)
		}
		return productdriver.Evidence{
			Sealed:   sha256DigestFromBarePin(bareStoreTreeDigest(bundle.ValidatedChain.SealedTreeDigest)),
			NoChange: bundleHasNoChange(bundle),
		}, nil
	}
	providerTrace, err := w.providerJournal.Trace()
	if err != nil {
		return productdriver.Evidence{}, productdriver.Unestablished("the provider journal could not be read", err)
	}
	storeTrace, err := w.storeJournal.Trace()
	if err != nil {
		return productdriver.Evidence{}, productdriver.Unestablished("the Store journal could not be read", err)
	}
	commandTrace, err := w.commandJournal.Trace()
	if err != nil {
		return productdriver.Evidence{}, productdriver.Unestablished("the command journal could not be read", err)
	}
	checkpoint, _ := w.ledger.Snapshot()
	return productdriver.Evidence{
		ProviderHead: auditHeadHash(providerTrace.Records), ProviderCount: uint64(len(providerTrace.Records)),
		StoreHead: auditHeadHash(storeTrace.Records), StoreCount: uint64(len(storeTrace.Records)),
		CommandHead: auditHeadHash(commandTrace.Records), CommandCount: uint64(len(commandTrace.Records)),
		HostCalls: int64(checkpoint.Counters.HostCalls), Tokens: int64(checkpoint.Counters.Tokens), Started: w.started,
	}, nil
}

func (t *productComposedTurn) SettleCommands() error { return t.w.settleCommands() }

func (t *productComposedTurn) Finalize(ctx context.Context) (publication.Result, error) {
	return t.publish.Finalize(ctx)
}

func (t *productComposedTurn) Close() error { return t.w.close() }

func (t *productComposedTurn) CloseUnlessRetained() error { return t.w.closeUnlessRetained() }

func (t *productComposedTurn) Candidate(turnID, candidateDigest string) (productprotocol.CandidateView, error) {
	return buildProductCandidate(turnID, t.w.store, candidateDigest)
}

// driveProductTurn constructs the provider conversation with the imported context, admits the
// turn's prompt, runs the worker and closes the conversation.
func (e *productTurnEnv) driveProductTurn(ctx context.Context, turn productsession.Turn, w *productTurnWorkflow,
	worker *productTurnWorker) (productdriver.Work, error) {
	if e.NewConversation == nil {
		return productdriver.Work{}, errors.New("product turn has no provider conversation factory")
	}
	conversation, err := w.authority.newProviderConversation(w.config, e.NewConversation)
	if err != nil {
		return productdriver.Work{}, err
	}
	defer conversation.Close()
	if err := w.authority.AdmitPrompt(conversation, w.durable, w.taskID, turn.Prompt); err != nil {
		return productdriver.Work{}, err
	}
	e.crash("before-first-exchange")
	return worker.run(ctx, conversation, w.durable, w.policy)
}

// beginProductPublication composes the existing publication finalizer over the project's managed
// directory and lifecycle journal, recovers earlier publications of that root, and holds session
// admission until the returned release, which the driver calls once the worker has finished.
func (e *productTurnEnv) beginProductPublication(ctx context.Context, projectID string, w *productTurnWorkflow) (*PublicationRuntime, func(), error) {
	if e.PublicationTrust == nil {
		return nil, nil, errors.New("product turn has no publication trust")
	}
	trust := e.PublicationTrust(w.authority)
	if trust == nil {
		return nil, nil, errors.New("product turn has no publication trust")
	}
	if _, ok := trust.(workflow.TerminalOriginResolver); w.bash && !ok {
		return nil, nil, errors.New("product turn publication trust cannot resolve the origin of a Bash lease")
	}
	projectDir := e.projectPublicationDir(projectID)
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
		Trust: trust, FaultInjector: e.publicationFaults,
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
