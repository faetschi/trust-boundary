//go:build linux

// Package productdriver owns the state and disposition of interactive product turns: which turns
// run, which review turns are held for a decision, the receipt of each settled turn, and the order
// of lifecycle steps around a turn. It never touches a Store, executor, publication runtime,
// command runner or provider worker; the Host does, and the driver reaches them only through Host
// and PreparedTurn.
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

var (
	// ErrStaleBaseline marks a managed project whose tree differs from the admitted baseline.
	ErrStaleBaseline = errors.New("managed project tree differs from the admitted baseline")
	// ErrCommandsUnsettled marks command ownership that is unresolved. The workflow stays retained
	// and the turn is not withdrawn.
	ErrCommandsUnsettled = errors.New("product command ownership is unresolved")
	// ErrTurnCancelled marks a turn that stopped at a step boundary because a cancellation was
	// requested.
	ErrTurnCancelled = errors.New("product turn cancelled at a step boundary")
	// ErrReviewNotRetained marks a review turn whose workflow and finalizer are not held by this
	// process: restart reconciliation, not this process, resolves it.
	ErrReviewNotRetained = errors.New("the review turn is not held by this process; restart reconciliation resolves it")
)

// Host is everything the driver needs from the governed composition. The driver owns turn, review
// and catalog state; the host owns Store, executor, publication, command runner and provider
// handles, the retained-command set and the live provider-journal map.
type Host interface {
	MeasureProjectBaseline(projectID string) (string, error)
	PredecessorContext(turn productsession.Turn, providerProfile string) (Predecessor, error)
	// PrepareTurn validates the turn worker, then composes the turn workflow and holds its provider
	// journal for readers; the caller owns the returned turn and must close it. cancelled reports a
	// durable cancel request, which the worker reads before every provider exchange.
	PrepareTurn(ctx context.Context, turn productsession.Turn, providerProfile string,
		predecessor Predecessor, cancelled func() bool) (PreparedTurn, error)
	SettleRetainedCommands(turnID string) error
	CloseRetainedCommands() error
	// ReconcileLinkedTurn reads the retained evidence of a linked turn whose process died and proves
	// it resolved; a failure comes from Unestablished.
	ReconcileLinkedTurn(ctx context.Context, turn productsession.Turn) (Evidence, error)
	WorkflowPublication(projectID, workflowID string) (records bool, succeeded string, err error)
	// Crash is the named test hook called at driver boundaries.
	Crash(point string)
}

// Predecessor is the imported history a turn builds on: opaque to the driver, which records its
// digests in the receipt and hands it back to the host.
type Predecessor interface {
	Digest() string
	PredecessorReceiptDigest() string
}

// PreparedTurn is one composed turn: workflow, publication finalizer and handles.
type PreparedTurn interface {
	Link() productsession.TurnLink
	// BeginPublication composes the finalizer and holds session admission until the returned release.
	BeginPublication(ctx context.Context, projectID string) (release func(), err error)
	Drive(ctx context.Context, turn productsession.Turn) (Work, error)
	// WorkerInstance is the identity of the worker PrepareTurn validated, which the receipt records.
	WorkerInstance() string
	Evidence(stage EvidenceStage) (Evidence, error)
	SettleCommands() error
	Finalize(ctx context.Context) (publication.Result, error)
	Close() error
	CloseUnlessRetained() error
	// Candidate is the read-only view of the sealed candidate; it is valid until the turn closes.
	Candidate(turnID, candidateDigest string) (productprotocol.CandidateView, error)
}

// Work is what the worker observed: the tools it proposed in order, the denials released to the
// provider, and whether the transcript ended at a final provider answer.
type Work struct {
	ToolSequence []string
	Denials      []productsession.ReceiptDenial
	FinalAnswer  bool
}

type EvidenceStage int

const (
	// EvidenceResolved reads what decides the outcome: that nothing is unresolved, the sealed tree
	// and whether the turn changed anything.
	EvidenceResolved EvidenceStage = iota
	// EvidenceSettlement reads what the receipt commits: journal heads, usage and identity.
	EvidenceSettlement
)

// Evidence is what the driver reads to decide and record a turn; only the fields of the requested
// stage are set.
type Evidence struct {
	Sealed   string
	NoChange bool

	ProviderHead, StoreHead, CommandHead    string
	ProviderCount, StoreCount, CommandCount uint64
	HostCalls, Tokens                       int64
	Started                                 time.Time
}

// evidenceError is evidence the host could not establish: the driver records the turn unknown with
// Reason.
type evidenceError struct {
	Reason string
	Cause  error
}

func (e *evidenceError) Error() string {
	return fmt.Sprintf("%s: %v", e.Reason, e.Cause)
}

func (e *evidenceError) Unwrap() error { return e.Cause }

// Unestablished is the error a host returns for evidence it cannot establish.
func Unestablished(reason string, cause error) error {
	return &evidenceError{Reason: reason, Cause: cause}
}

// Refusal is a launch the driver refused before linking the turn: the catalog recorded the turn as
// failed with a full refund. errors.Is matches productsession.ErrCode(Code).
type Refusal struct {
	Code  productsession.Code
	Cause error
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("product turn launch refused (%s): %v", r.Code, r.Cause)
}

func (r *Refusal) Unwrap() []error {
	return []error{productsession.ErrCode(r.Code), r.Cause}
}
