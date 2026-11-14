// recovery_ledger.go: Ledger operations: open, initialize, reserve, record, advance, observe and commit.

package workflowrecovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// Ledger is a validated snapshot. Mutations use durable compare-and-swap and
// replace this snapshot only after the Owner confirms the trusted root commit.
type Ledger struct {
	mu                     sync.Mutex
	owner                  Owner
	workflowID             string
	fencedEpoch            uint64
	incarnationMatches     bool
	restartReceiptVerified bool
	checkpoint             Checkpoint
	digest                 string
}

// Open reads and validates the exact retained checkpoint. A checkpoint from a
// different process incarnation, a newer owner epoch, or an unsettled provider
// state is inspection-only; it cannot reserve fresh authority before a verified
// clean restart or explicit in-session settlement.
func Open(ctx context.Context, owner Owner, options OpenOptions) (*Ledger, error) {
	if ctx == nil || owner == nil || !validID(options.WorkflowID) || options.OwnerEpoch == 0 ||
		options.RetainedPins.WorkflowID != options.WorkflowID || options.OwnerEpoch < options.RetainedPins.OwnerEpoch {
		return nil, fmt.Errorf("%w: invalid open options", ErrInvalid)
	}
	rootIdentity := owner.TrustedRootIdentityDigest()
	incarnation := owner.OwnerIncarnationDigest()
	if !validDigest(rootIdentity) || options.RetainedPins.RootIdentityDigest != rootIdentity ||
		!validDigest(incarnation) {
		return nil, fmt.Errorf("%w: retained root or owner-incarnation identity is invalid", ErrOwnerFence)
	}
	options.Limits = normalizeLimits(options.Limits)
	if err := validateLimits(options.Limits, time.Now()); err != nil {
		return nil, err
	}
	if err := validatePins(options.RetainedPins); err != nil {
		return nil, err
	}
	cp, digest, exists, err := owner.LoadCheckpoint(ctx, options.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("load workflow checkpoint: %w", err)
	}
	if !exists {
		return nil, ErrMissing
	}
	if err := validateCheckpoint(cp, digest); err != nil {
		return nil, err
	}
	if cp.Pins.WorkflowID != options.WorkflowID || !reflect.DeepEqual(cp.Pins, options.RetainedPins) ||
		!reflect.DeepEqual(cp.Limits, options.Limits) {
		return nil, fmt.Errorf("%w: retained pins or immutable limits differ", ErrRollback)
	}
	if options.OwnerEpoch < cp.Pins.OwnerEpoch {
		return nil, fmt.Errorf("%w: candidate owner epoch precedes retained checkpoint", ErrOwnerFence)
	}
	if options.OwnerEpoch > cp.Pins.OwnerEpoch {
		acquirer, ok := owner.(OwnerEpochAcquirer)
		if !ok {
			return nil, fmt.Errorf("%w: owner cannot acquire an externally fenced replacement epoch", ErrOwnerFence)
		}
		if err := acquirer.AcquireOwnerEpoch(ctx, options.WorkflowID, cp.Pins.OwnerEpoch, options.OwnerEpoch); err != nil {
			return nil, fmt.Errorf("%w: replacement epoch acquisition: %v", ErrOwnerFence, err)
		}
		drain, ok := owner.(DrainVerifier)
		if !ok {
			return nil, fmt.Errorf("%w: owner cannot verify drain of superseded owner epochs", ErrOwnerFence)
		}
		if err := drain.VerifyOwnersDrained(ctx, options.WorkflowID, cp.Pins.OwnerEpoch, options.OwnerEpoch); err != nil {
			return nil, fmt.Errorf("%w: superseded owner/cell drain: %v", ErrOwnerFence, err)
		}
	}
	if err := owner.VerifyFence(ctx, options.WorkflowID, options.OwnerEpoch); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOwnerFence, err)
	}
	if err := validateCheckpointAdmission(cp, time.Now()); err != nil && !errors.Is(err, ErrPendingUnknown) {
		return nil, err
	}
	incarnationMatches := options.OwnerEpoch == cp.Pins.OwnerEpoch && incarnation == cp.Pins.OwnerIncarnationDigest
	return &Ledger{owner: owner, workflowID: options.WorkflowID, fencedEpoch: options.OwnerEpoch,
		incarnationMatches: incarnationMatches, restartReceiptVerified: cp.Counters.CleanRestarts == 0,
		checkpoint: cp, digest: digest}, nil
}

// InitializeFresh writes the first checkpoint for an explicitly authorized
// new workflow. This is never used as a fallback for missing recovery state.
func InitializeFresh(ctx context.Context, owner Owner, pins Pins, limits Limits) (Receipt, error) {
	if ctx == nil || owner == nil || pins.OwnerEpoch == 0 || !validID(pins.WorkflowID) {
		return Receipt{}, fmt.Errorf("%w: invalid fresh workflow inputs", ErrInvalid)
	}
	freshOwner, ok := owner.(FreshWorkflowAuthorizer)
	if !ok {
		return Receipt{}, ErrFreshAuthorization
	}
	rootIdentity := owner.TrustedRootIdentityDigest()
	incarnation := owner.OwnerIncarnationDigest()
	if !validDigest(rootIdentity) || pins.RootIdentityDigest != rootIdentity ||
		!validDigest(incarnation) || pins.OwnerIncarnationDigest != incarnation {
		return Receipt{}, fmt.Errorf("%w: fresh pins differ from the trusted root or owner-incarnation identity", ErrFreshAuthorization)
	}
	limits = normalizeLimits(limits)
	if err := validateLimits(limits, time.Now()); err != nil {
		return Receipt{}, err
	}
	if err := validatePins(pins); err != nil {
		return Receipt{}, err
	}
	if pins.RecoveryGeneration != 1 || pins.ProviderProjectionCount != 0 || len(pins.ProviderTranscripts) != 0 ||
		pins.ProviderDisposition != DispositionNone || pins.PendingCallDisposition != PendingCallNone ||
		pins.PreviousRecoveryLineageDigest != "" {
		return Receipt{}, fmt.Errorf("%w: a fresh workflow must start with an empty recovery lineage and provider history", ErrInvalid)
	}
	cp := Checkpoint{SchemaVersion: checkpointSchema, Pins: clonePins(pins), Limits: limits, Counters: Counters{}}
	cp.ComputedCheckpointDigest = computeCheckpointDigest(cp)
	newDigest, err := freshOwner.InitializeFreshCheckpoint(ctx, pins.WorkflowID, pins.OwnerEpoch, cp)
	if err != nil {
		return Receipt{}, errors.Join(ErrCAS, err)
	}
	if newDigest != cp.ComputedCheckpointDigest {
		return Receipt{}, fmt.Errorf("%w: owner returned a different fresh checkpoint digest", ErrRollback)
	}
	return receiptFor(cp, newDigest, receiptFresh), nil
}

// ReserveCleanRestart consumes one workflow-wide clean restart and seals the
// new host-generated session/recovery lineage before any fresh worker,
// provider conversation, or authority is opened.
func (l *Ledger) ReserveCleanRestart(ctx context.Context, observation VerifiedRecoveryObservation, nextPins Pins) (Receipt, error) {
	return l.reserveCleanRestart(ctx, observation, nextPins, false)
}

// ReserveCleanRestartWithPreEffectRetry atomically consumes one clean restart and
// one pre-effect retry for a verified retired-unexecuted provider call. Both
// allowances and the new lineage commit in one checkpoint; the returned receipt
// is bound to that final checkpoint, so no later reservation can stale it.
func (l *Ledger) ReserveCleanRestartWithPreEffectRetry(ctx context.Context, observation VerifiedRecoveryObservation, nextPins Pins) (Receipt, error) {
	return l.reserveCleanRestart(ctx, observation, nextPins, true)
}

func (l *Ledger) reserveCleanRestart(ctx context.Context, observation VerifiedRecoveryObservation, nextPins Pins, chargeRetry bool) (Receipt, error) {
	if l == nil || ctx == nil {
		return Receipt{}, fmt.Errorf("%w: nil ledger or context", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if l.checkpoint.Counters.CleanRestarts >= l.checkpoint.Limits.MaxCleanRestarts {
		return Receipt{}, ErrExhausted
	}
	if chargeRetry && l.checkpoint.Counters.PreEffectRetries >= l.checkpoint.Limits.MaxPreEffectRetries {
		return Receipt{}, ErrExhausted
	}
	if err := validatePins(nextPins); err != nil {
		return Receipt{}, err
	}
	if nextPins.WorkflowID != l.workflowID || nextPins.OwnerEpoch != l.fencedEpoch ||
		nextPins.OwnerEpoch <= l.checkpoint.Pins.OwnerEpoch ||
		nextPins.RecoveryGeneration != l.checkpoint.Pins.RecoveryGeneration+1 ||
		nextPins.SessionID == l.checkpoint.Pins.SessionID ||
		nextPins.OwnerIncarnationDigest != l.owner.OwnerIncarnationDigest() {
		return Receipt{}, fmt.Errorf("%w: clean restart must bind a newer fenced epoch, session, and recovery generation", ErrInvalid)
	}
	proof := observation.proof
	if proof.kind != transitionRecovery || proof.reservation != reservationNone || proof.previousDigest != l.digest ||
		!reflect.DeepEqual(proof.previousPins, l.checkpoint.Pins) || !validDigest(proof.proofDigest) {
		return Receipt{}, ErrObservation
	}
	if err := validPinsMonotonic(proof.previousPins, proof.observedPins); err != nil {
		return Receipt{}, fmt.Errorf("%w: recovery observation is not monotonic: %v", ErrObservation, err)
	}
	if err := validateRestartPins(proof.observedPins, nextPins, l.digest); err != nil {
		return Receipt{}, err
	}
	if chargeRetry {
		if !retiredUnleasedCallObserved(proof.observedPins) {
			return Receipt{}, fmt.Errorf("%w: pre-effect retry requires an observed retired unleased call", ErrObservation)
		}
		proof.reservation = reservationCleanRestartPreEffectRetry
	}
	proof.targetPins = clonePins(nextPins)
	next := l.checkpoint
	next.Pins = clonePins(nextPins)
	next.Counters.CleanRestarts++
	if chargeRetry {
		next.Counters.PreEffectRetries++
	}
	next.PreviousCheckpointDigest = l.digest
	next.ObservationDigest = proof.proofDigest
	next.ComputedCheckpointDigest = ""
	return l.commit(ctx, next, proof, receiptCleanRestart)
}

// ReservePreEffectRetry durably charges one pre-effect retry in the current
// active session/owner epoch. Replacement supervisors must reserve a clean
// restart first; retries do not change or replenish workflow identity/budgets.
func (l *Ledger) ReservePreEffectRetry(ctx context.Context) (Receipt, error) {
	if l == nil || ctx == nil {
		return Receipt{}, fmt.Errorf("%w: nil ledger or context", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if err := validateCheckpointAdmission(l.checkpoint, time.Now()); err != nil {
		return Receipt{}, err
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrRestartRequired
	}
	if l.checkpoint.Counters.PreEffectRetries >= l.checkpoint.Limits.MaxPreEffectRetries {
		return Receipt{}, ErrExhausted
	}
	next := l.checkpoint
	next.Pins = clonePins(l.checkpoint.Pins)
	next.Counters.PreEffectRetries++
	next.PreviousCheckpointDigest = l.digest
	next.ComputedCheckpointDigest = ""
	proof := makeReservationProof(reservationPreEffectRetry, l.digest, l.checkpoint, next)
	return l.commit(ctx, next, proof, receiptPreEffectRetry)
}

// ReserveHostCall consumes a provider/host call and a conservative token
// upper bound before dispatch. Ambiguous usage, cancellation, and crashes keep
// the reservation charged; there is intentionally no refund API.
func (l *Ledger) ReserveHostCall(ctx context.Context, binding HostCallBinding) (Receipt, error) {
	if l == nil || ctx == nil || !validHostCallBinding(binding) {
		return Receipt{}, fmt.Errorf("%w: invalid host call reservation", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if err := validateHostCallReservationAdmission(l.checkpoint, time.Now()); err != nil {
		return Receipt{}, err
	}
	if len(l.checkpoint.Pins.ProviderTranscripts) == 0 {
		return Receipt{}, ErrObservation
	}
	if previous := l.checkpoint.Pins.HostCallReservation; previous != nil && previous.Binding.CallID == binding.CallID {
		return Receipt{}, fmt.Errorf("%w: provider call identity was already consumed", ErrObservation)
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrRestartRequired
	}
	if l.checkpoint.Counters.HostCalls >= l.checkpoint.Limits.MaxHostCalls ||
		binding.TokenUpperBound > l.checkpoint.Limits.MaxTokens-l.checkpoint.Counters.Tokens {
		return Receipt{}, ErrExhausted
	}
	next := l.checkpoint
	next.Pins = clonePins(l.checkpoint.Pins)
	next.Counters.HostCalls++
	next.Counters.Tokens += binding.TokenUpperBound
	next.Pins.HostCallReservation = &HostCallReservationPin{Binding: binding, Disposition: HostCallReserved}
	next.PreviousCheckpointDigest = l.digest
	next.ComputedCheckpointDigest = ""
	proof := makeReservationProof(reservationHostCall, l.digest, l.checkpoint, next)
	return l.commit(ctx, next, proof, receiptHostCall)
}

// HostCallsLeft is the number of host calls ReserveHostCall and ReserveEffect can still charge. The
// provider bridge asks it so that it never captures a tool call, or retries an exchange, that the
// remaining allowance cannot carry to a settleable end.
func (l *Ledger) HostCallsLeft() uint64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.checkpoint.Counters.HostCalls >= l.checkpoint.Limits.MaxHostCalls {
		return 0
	}
	return l.checkpoint.Limits.MaxHostCalls - l.checkpoint.Counters.HostCalls
}

// ReserveEffect consumes one workflow-wide host-call allowance before an
// irreversible governed effect. It charges no provider tokens and has no
// refund path if the effect callback or process later fails.
func (l *Ledger) ReserveEffect(ctx context.Context, binding EffectBinding, verifier CapturedOperationVerifier) (Receipt, error) {
	if l == nil || ctx == nil || !validEffectBinding(binding) {
		return Receipt{}, fmt.Errorf("%w: invalid effect reservation", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if err := validateEffectReservationAdmission(ctx, l.checkpoint, binding, verifier, time.Now()); err != nil {
		return Receipt{}, err
	}
	if len(l.checkpoint.Pins.ProviderTranscripts) == 0 {
		return Receipt{}, ErrObservation
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrRestartRequired
	}
	if l.checkpoint.Counters.HostCalls >= l.checkpoint.Limits.MaxHostCalls {
		return Receipt{}, ErrExhausted
	}
	next := l.checkpoint
	next.Pins = clonePins(l.checkpoint.Pins)
	if next.Pins.EffectReservation != nil &&
		next.Pins.EffectReservation.Disposition != EffectReservationTerminal &&
		next.Pins.EffectReservation.Disposition != EffectReservationRetired {
		return Receipt{}, ErrPendingUnknown
	}
	if next.Pins.ProviderDisposition == DispositionAwaitingGateDecision {
		captured := next.Pins.HostCallReservation.CapturedCall
		captured.Disposition = CapturedCallConsumed
		captured.RetirementDigest = ""
	}
	next.Pins.EffectReservation = &EffectReservationPin{Binding: binding, Disposition: EffectReservationReserved}
	next.Counters.HostCalls++
	next.PreviousCheckpointDigest = l.digest
	next.ComputedCheckpointDigest = ""
	proof := makeReservationProof(reservationEffect, l.digest, l.checkpoint, next)
	return l.commit(ctx, next, proof, receiptEffect)
}

// RecordTerminalEffectOutcome accounts for a known, independently verified
// Store outcome from an effect whose authority was already durably consumed.
// It can advance only terminal Store pins and the disposition of that exact
// reservation. It never restores counters or permits another effect.
func (l *Ledger) RecordTerminalEffectOutcome(ctx context.Context, reservation Receipt,
	verifier TerminalEffectVerifier, nextPins Pins) (Receipt, error) {
	if l == nil || ctx == nil || verifier == nil || reservation.kind != receiptEffect {
		return Receipt{}, ErrObservation
	}
	l.mu.Lock()
	if l.owner == nil || reservation.workflowID != l.workflowID ||
		reservation.sessionID != l.checkpoint.Pins.SessionID ||
		reservation.recoveryGeneration != l.checkpoint.Pins.RecoveryGeneration ||
		reservation.ownerEpoch != l.fencedEpoch || reservation.ownerEpoch != l.checkpoint.Pins.OwnerEpoch ||
		reservation.checkpointDigest != l.digest || reservation.previousDigest != l.checkpoint.PreviousCheckpointDigest ||
		reservation.counters != l.checkpoint.Counters ||
		l.checkpoint.Pins.EffectReservation == nil ||
		l.checkpoint.Pins.EffectReservation.Disposition != EffectReservationReserved ||
		reservation.effectBinding != l.checkpoint.Pins.EffectReservation.Binding {
		l.mu.Unlock()
		return Receipt{}, ErrObservation
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		l.mu.Unlock()
		return Receipt{}, ErrOwnerFence
	}
	previous := l.checkpoint
	previous.Pins = clonePins(previous.Pins)
	previousDigest := l.digest
	fencedEpoch := l.fencedEpoch
	owner := l.owner
	workflowID := l.workflowID
	deadline := l.checkpoint.Limits.Deadline
	binding := l.checkpoint.Pins.EffectReservation.Binding
	l.mu.Unlock()

	terminalOwner, ok := owner.(TerminalEffectOwner)
	if !ok {
		return Receipt{}, fmt.Errorf("%w: owner cannot verify same-incarnation terminal accounting", ErrOwnerFence)
	}
	if err := terminalOwner.VerifyTerminalEffectOwner(ctx, workflowID, fencedEpoch, previousDigest, deadline); err != nil {
		return Receipt{}, fmt.Errorf("%w: live owner/fence check for terminal accounting: %v", ErrOwnerFence, err)
	}
	if err := validateTerminalEffectPins(previous.Pins, nextPins); err != nil {
		return Receipt{}, err
	}
	if err := verifier.VerifyTerminalEffect(ctx, previous, binding, clonePins(nextPins)); err != nil {
		return Receipt{}, fmt.Errorf("%w: terminal Store outcome verifier: %v", ErrObservation, err)
	}

	next := previous
	next.Pins = clonePins(nextPins)
	next.PreviousCheckpointDigest = previousDigest
	next.ObservationDigest = digestTerminalEffect(previousDigest, previous.Pins, next.Pins, previous.Counters)
	next.ComputedCheckpointDigest = ""
	proof := TransitionProof{kind: transitionTerminalEffect, reservation: reservationEffect,
		previousDigest: previousDigest, previousPins: clonePins(previous.Pins),
		observedPins: clonePins(next.Pins), targetPins: clonePins(next.Pins),
		previousCounts: previous.Counters, targetCounts: next.Counters,
		proofDigest: next.ObservationDigest}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.digest != previousDigest || l.fencedEpoch != fencedEpoch || !reflect.DeepEqual(l.checkpoint, previous) {
		return Receipt{}, ErrCAS
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrOwnerFence
	}
	return l.commit(ctx, next, proof, receiptTerminalEffect)
}

// AdvanceCheckpoint records newly verified monotonic host observations. It
// cannot change identity, deadline, limits, or counters and never grants fresh
// authority. Prior transcript pins are immutable; only the active transcript
// may extend its protected journal/projection head.
func (l *Ledger) AdvanceCheckpoint(ctx context.Context, verifier ProgressVerifier, nextPins Pins) (Receipt, error) {
	if l == nil || ctx == nil {
		return Receipt{}, fmt.Errorf("%w: nil ledger or context", ErrInvalid)
	}
	if verifier == nil {
		return Receipt{}, ErrObservation
	}
	l.mu.Lock()
	if l.checkpoint.Pins.EffectReservation != nil &&
		l.checkpoint.Pins.EffectReservation.Disposition == EffectReservationReserved {
		l.mu.Unlock()
		return Receipt{}, ErrPendingUnknown
	}
	if err := l.checkDeadline(ctx); err != nil {
		l.mu.Unlock()
		return Receipt{}, err
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		l.mu.Unlock()
		return Receipt{}, ErrRestartRequired
	}
	if err := validatePins(nextPins); err != nil {
		l.mu.Unlock()
		return Receipt{}, err
	}
	if err := validateObservationAdvance(l.checkpoint.Pins, nextPins); err != nil {
		l.mu.Unlock()
		return Receipt{}, err
	}
	previous := l.checkpoint
	previous.Pins = clonePins(previous.Pins)
	previousDigest := l.digest
	fencedEpoch := l.fencedEpoch
	l.mu.Unlock()
	if err := verifier.VerifyProgress(ctx, previous, clonePins(nextPins)); err != nil {
		return Receipt{}, fmt.Errorf("%w: progress verifier: %v", ErrObservation, err)
	}
	proof := TransitionProof{kind: transitionProgress, previousDigest: previousDigest,
		previousPins: clonePins(previous.Pins), observedPins: clonePins(nextPins),
		targetPins: clonePins(nextPins), proofDigest: digestObservation(transitionProgress, previousDigest, previous.Pins, nextPins)}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.digest != previousDigest || l.fencedEpoch != fencedEpoch || !reflect.DeepEqual(l.checkpoint, previous) {
		return Receipt{}, ErrCAS
	}
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrRestartRequired
	}
	next := previous
	next.Pins = clonePins(nextPins)
	next.PreviousCheckpointDigest = previousDigest
	next.ObservationDigest = proof.proofDigest
	next.ComputedCheckpointDigest = ""
	return l.commit(ctx, next, proof, receiptProgress)
}

// RegisterInitialTranscript durably binds the first empty provider conversation
// to the workflow before ReserveHostCall can charge or dispatch an exchange.
// Clean restarts append their next conversation through ReserveCleanRestart and
// are not eligible for this path.
func (l *Ledger) RegisterInitialTranscript(ctx context.Context, verifier ProgressVerifier, transcript TranscriptPin) (Receipt, error) {
	if l == nil || ctx == nil || verifier == nil {
		return Receipt{}, ErrObservation
	}
	l.mu.Lock()
	if l.checkpoint.Pins.RecoveryGeneration != 1 || len(l.checkpoint.Pins.ProviderTranscripts) != 0 ||
		l.checkpoint.Pins.ProviderDisposition != DispositionNone || l.checkpoint.Pins.PendingCallDisposition != PendingCallNone {
		l.mu.Unlock()
		return Receipt{}, fmt.Errorf("%w: initial transcript can only be registered once before provider use", ErrInvalid)
	}
	nextPins := clonePins(l.checkpoint.Pins)
	nextPins.ProviderTranscripts = []TranscriptPin{transcript}
	nextPins.ProviderDisposition = DispositionAwaitingHostPrompt
	nextPins.ProviderProjectionCount = 0
	nextPins.ProviderProjectionHead = ""
	nextPins.ProviderProjectionDigest = digestTranscriptPins(nextPins.ProviderTranscripts)
	// AdvanceCheckpoint owns the mutex and executes the verifier outside this
	// registration-specific state check.
	l.mu.Unlock()
	return l.AdvanceCheckpoint(ctx, verifier, nextPins)
}

// ObserveRecovery verifies every retained journal/transcript join and returns
// an opaque candidate tail. It performs no write, owner transition, or fresh
// authority operation.
func (l *Ledger) ObserveRecovery(ctx context.Context, verifier RecoveryVerifier, observed Pins) (VerifiedRecoveryObservation, error) {
	if l == nil || ctx == nil || verifier == nil {
		return VerifiedRecoveryObservation{}, ErrObservation
	}
	l.mu.Lock()
	previous := l.checkpoint
	previous.Pins = clonePins(previous.Pins)
	previousDigest := l.digest
	fencedEpoch := l.fencedEpoch
	l.mu.Unlock()
	if fencedEpoch <= previous.Pins.OwnerEpoch {
		return VerifiedRecoveryObservation{}, ErrRestartRequired
	}
	if err := validatePins(observed); err != nil {
		return VerifiedRecoveryObservation{}, err
	}
	if err := validPinsMonotonic(previous.Pins, observed); err != nil {
		return VerifiedRecoveryObservation{}, err
	}
	if err := validateObservedDisposition(observed); err != nil {
		return VerifiedRecoveryObservation{}, err
	}
	if err := verifier.VerifyRecoveryObservation(ctx, previous, clonePins(observed)); err != nil {
		return VerifiedRecoveryObservation{}, fmt.Errorf("%w: recovery verifier: %v", ErrObservation, err)
	}
	proof := TransitionProof{kind: transitionRecovery, previousDigest: previousDigest,
		previousPins: clonePins(previous.Pins), observedPins: clonePins(observed),
		proofDigest: digestObservation(transitionRecovery, previousDigest, previous.Pins, observed)}
	return VerifiedRecoveryObservation{proof: proof}, nil
}

func (l *Ledger) commit(ctx context.Context, next Checkpoint, proof TransitionProof, kind receiptKind) (Receipt, error) {
	next.ComputedCheckpointDigest = computeCheckpointDigest(next)
	var validationErr error
	if kind == receiptProgress || kind == receiptTerminalEffect || kind == receiptHostCall || kind == receiptEffect {
		validationErr = validateCheckpoint(next, next.ComputedCheckpointDigest)
	} else {
		validationErr = validateCheckpointAdmission(next, time.Now())
	}
	if validationErr != nil {
		return Receipt{}, validationErr
	}
	newDigest, err := l.owner.CompareAndSwapCheckpoint(ctx, l.workflowID, next.Pins.OwnerEpoch, l.digest, next, proof)
	if err != nil {
		if errors.Is(err, ErrExhausted) {
			// The owner returns ErrExhausted before its transaction commits: either
			// the effect headroom gate refused, or a counter is over its limit. Neither
			// wrote anything, so neither is a compare-and-swap conflict.
			return Receipt{}, err
		}
		return Receipt{}, errors.Join(ErrCAS, err)
	}
	if newDigest != next.ComputedCheckpointDigest {
		return Receipt{}, fmt.Errorf("%w: owner returned a different checkpoint digest", ErrRollback)
	}
	l.checkpoint = next
	l.digest = newDigest
	if kind == receiptCleanRestart {
		l.incarnationMatches = true
		l.restartReceiptVerified = false
	} else if kind == receiptFresh {
		l.restartReceiptVerified = true
	}
	return receiptFor(next, newDigest, kind), nil
}

func (l *Ledger) checkDeadline(ctx context.Context) error {
	if l == nil {
		return ErrMissing
	}
	if verifier, ok := l.owner.(DeadlineVerifier); ok {
		if err := verifier.VerifyWorkflowDeadline(ctx, l.workflowID, l.checkpoint.Limits.Deadline); err != nil {
			return err
		}
	}
	if !time.Now().Before(l.checkpoint.Limits.Deadline) {
		return ErrDeadline
	}
	return nil
}

func (l *Ledger) requireCurrentIncarnation() error {
	if l == nil || l.owner == nil || !l.incarnationMatches || !l.restartReceiptVerified ||
		l.fencedEpoch != l.checkpoint.Pins.OwnerEpoch ||
		l.owner.OwnerIncarnationDigest() != l.checkpoint.Pins.OwnerIncarnationDigest {
		return ErrRestartRequired
	}
	return nil
}

func (l *Ledger) Snapshot() (Checkpoint, string) {
	if l == nil {
		return Checkpoint{}, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	copy := l.checkpoint
	copy.Pins = clonePins(copy.Pins)
	return copy, l.digest
}

// VerifyCleanRestartReceipt confirms that receipt is the exact latest,
// externally authenticated clean-restart checkpoint for expectedPins.
func (l *Ledger) VerifyCleanRestartReceipt(ctx context.Context, receipt Receipt, expectedPins Pins) error {
	if l == nil || ctx == nil || receipt.kind != receiptCleanRestart {
		return ErrObservation
	}
	l.mu.Lock()
	if !l.incarnationMatches || l.owner == nil ||
		l.owner.OwnerIncarnationDigest() != l.checkpoint.Pins.OwnerIncarnationDigest {
		l.mu.Unlock()
		return ErrOwnerFence
	}
	if receipt.workflowID != l.workflowID || receipt.ownerEpoch != l.checkpoint.Pins.OwnerEpoch ||
		receipt.sessionID != l.checkpoint.Pins.SessionID || receipt.recoveryGeneration != l.checkpoint.Pins.RecoveryGeneration ||
		receipt.checkpointDigest != l.digest || receipt.previousDigest != l.checkpoint.PreviousCheckpointDigest ||
		!reflect.DeepEqual(expectedPins, l.checkpoint.Pins) || receipt.counters != l.checkpoint.Counters {
		l.mu.Unlock()
		return ErrObservation
	}
	owner := l.owner
	workflowID := l.workflowID
	ownerEpoch := receipt.ownerEpoch
	expected := l.checkpoint
	expected.Pins = clonePins(expected.Pins)
	l.mu.Unlock()
	cp, digest, exists, err := owner.LoadCheckpoint(ctx, workflowID)
	if err != nil || !exists || digest != receipt.checkpointDigest || !reflect.DeepEqual(cp, expected) {
		return errors.Join(ErrRollback, err)
	}
	if err := owner.VerifyFence(ctx, workflowID, ownerEpoch); err != nil {
		return fmt.Errorf("%w: receipt owner fence: %v", ErrOwnerFence, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.digest != receipt.checkpointDigest || !reflect.DeepEqual(l.checkpoint, expected) ||
		l.owner.OwnerIncarnationDigest() != l.checkpoint.Pins.OwnerIncarnationDigest {
		return ErrObservation
	}
	l.restartReceiptVerified = true
	return nil
}

func receiptFor(cp Checkpoint, digest string, kind receiptKind) Receipt {
	receipt := Receipt{
		kind: kind, workflowID: cp.Pins.WorkflowID, sessionID: cp.Pins.SessionID,
		recoveryGeneration: cp.Pins.RecoveryGeneration, ownerEpoch: cp.Pins.OwnerEpoch,
		checkpointDigest: digest, previousDigest: cp.PreviousCheckpointDigest,
		counters: cp.Counters, remaining: Remaining{
			PreEffectRetries: subtract(cp.Limits.MaxPreEffectRetries, cp.Counters.PreEffectRetries),
			CleanRestarts:    subtract(cp.Limits.MaxCleanRestarts, cp.Counters.CleanRestarts),
			HostCalls:        subtract(cp.Limits.MaxHostCalls, cp.Counters.HostCalls),
			Tokens:           subtract(cp.Limits.MaxTokens, cp.Counters.Tokens),
		},
	}
	if (kind == receiptEffect || kind == receiptTerminalEffect) && cp.Pins.EffectReservation != nil {
		receipt.effectBinding = cp.Pins.EffectReservation.Binding
	}
	if kind == receiptHostCall && cp.Pins.HostCallReservation != nil {
		receipt.hostCallBinding = cp.Pins.HostCallReservation.Binding
	}
	return receipt
}

func subtract(maximum, used uint64) uint64 {
	if used >= maximum {
		return 0
	}
	return maximum - used
}
