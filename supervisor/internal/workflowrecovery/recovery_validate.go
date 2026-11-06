// recovery_validate.go: Validation: limits, pins, reservations and transitions admission checks.

package workflowrecovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
)

func normalizeLimits(limits Limits) Limits {
	if !limits.Deadline.IsZero() {
		limits.Deadline = limits.Deadline.Round(0).UTC()
	}
	return limits
}

func validateLimits(limits Limits, now time.Time) error {
	if limits.MaxPreEffectRetries > 2 || limits.MaxCleanRestarts > 2 ||
		limits.MaxHostCalls == 0 || limits.MaxTokens == 0 || limits.MaxHostCalls > maxSQLiteInteger ||
		limits.MaxTokens > maxSQLiteInteger || limits.Deadline.IsZero() {
		return fmt.Errorf("%w: limits must be finite, deadline-bearing, and within D20 totals", ErrInvalid)
	}
	if !now.Before(limits.Deadline) {
		return ErrDeadline
	}
	if limits.Deadline.Location() != time.UTC && limits.Deadline.Location().String() != "UTC" {
		return fmt.Errorf("%w: deadline must be normalized to UTC", ErrInvalid)
	}
	return nil
}

func validateCheckpointAdmission(cp Checkpoint, now time.Time) error {
	if err := validateCheckpoint(cp, cp.ComputedCheckpointDigest); err != nil {
		return err
	}
	if err := validateLimits(cp.Limits, now); err != nil {
		return err
	}
	if cp.Pins.PendingCallDisposition == PendingCallInFlight || cp.Pins.PendingCallDisposition == PendingCallCaptured ||
		cp.Pins.PendingCallDisposition == PendingCallUnknown ||
		isUnsettledDisposition(cp.Pins.ProviderDisposition) ||
		cp.Pins.EffectReservation != nil && (cp.Pins.EffectReservation.Disposition == EffectReservationReserved ||
			cp.Pins.EffectReservation.Disposition == EffectReservationUnknown) ||
		cp.Pins.HostCallReservation != nil && (cp.Pins.HostCallReservation.Disposition == HostCallReserved ||
			cp.Pins.HostCallReservation.Disposition == HostCallUnknown) {
		return ErrPendingUnknown
	}
	for _, transcript := range cp.Pins.ProviderTranscripts {
		if isUnsettledDisposition(transcript.Disposition) {
			return ErrPendingUnknown
		}
	}
	return nil
}

// validateHostCallReservationAdmission permits one additional provider
// reservation after a successful tool result has been durably checkpointed.
// The ordinary admission gate remains closed to AwaitingProviderExchange; the
// exception is local to ReserveHostCall and requires the exact preceding
// captured effect to be terminal in the current conversation/fence.
func validateHostCallReservationAdmission(cp Checkpoint, now time.Time) error {
	if err := validateCheckpointAdmission(cp, now); err == nil {
		return nil
	} else if !errors.Is(err, ErrPendingUnknown) {
		return err
	}
	if !isPostResultProviderExchangeReady(cp) && !isRetryableFailureProviderExchangeReady(cp) &&
		!isDeniedResultProviderExchangeReady(cp) {
		return ErrPendingUnknown
	}
	return nil
}

// deniedCallAwaitsProviderAnswer is the state a released DENY leaves: the active conversation awaits
// the model's answer to the DENY result, and the denied call, captured in that conversation under the
// current fence, is retired with a commitment to its journaled DENY decision and result. A retired
// call can never be consumed: effect admission requires an available capture.
func deniedCallAwaitsProviderAnswer(p Pins) bool {
	if p.ProviderDisposition != DispositionAwaitingProviderExchange || p.PendingCallDisposition != PendingCallRetired ||
		len(p.ProviderTranscripts) == 0 || p.HostCallReservation == nil ||
		p.HostCallReservation.Disposition != HostCallTerminal || p.HostCallReservation.CapturedCall == nil {
		return false
	}
	active := p.ProviderTranscripts[len(p.ProviderTranscripts)-1]
	captured := p.HostCallReservation.CapturedCall
	return active.Disposition == DispositionAwaitingProviderExchange && captured.Disposition == CapturedCallRetired &&
		validDigest(captured.RetirementDigest) && captured.ConversationID == active.ConversationID &&
		captured.RecoveryGeneration == p.RecoveryGeneration && captured.OwnerEpoch == p.OwnerEpoch
}

// isDeniedResultProviderExchangeReady admits the provider reservation that answers a released DENY.
// The new reservation replaces the retired capture, so its call identity cannot be charged again.
func isDeniedResultProviderExchangeReady(cp Checkpoint) bool {
	pins := cp.Pins
	if !deniedCallAwaitsProviderAnswer(pins) {
		return false
	}
	for _, transcript := range pins.ProviderTranscripts[:len(pins.ProviderTranscripts)-1] {
		if transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return false
		}
	}
	effect := pins.EffectReservation
	return effect == nil || effect.Disposition == EffectReservationTerminal || effect.Disposition == EffectReservationRetired
}

// isRetryableFailureProviderExchangeReady admits one further provider reservation after a
// failed, retryable provider attempt was journaled and its reservation settled: the exchange is
// still awaited, the failed attempt's host call is terminal with an outcome digest and captured
// nothing, and no effect or captured call is open. Whether the retry bound holds is enforced by the
// provider conversation and its replay; the host-call and token ceilings charged by every
// reservation bound the total here. The prior call may be settled by its effect or retired by a DENY.
func isRetryableFailureProviderExchangeReady(cp Checkpoint) bool {
	pins := cp.Pins
	if pins.ProviderDisposition != DispositionAwaitingProviderExchange || len(pins.ProviderTranscripts) == 0 ||
		pins.PendingCallDisposition != PendingCallNone && pins.PendingCallDisposition != PendingCallTerminal &&
			pins.PendingCallDisposition != PendingCallRetired {
		return false
	}
	activeIndex := len(pins.ProviderTranscripts) - 1
	if pins.ProviderTranscripts[activeIndex].Disposition != DispositionAwaitingProviderExchange {
		return false
	}
	for index, transcript := range pins.ProviderTranscripts {
		if index != activeIndex && transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return false
		}
	}
	host, effect := pins.HostCallReservation, pins.EffectReservation
	if host == nil || host.Disposition != HostCallTerminal || !host.RetryableFailure || host.CapturedCall != nil ||
		!validDigest(host.OutcomeDigest) || host.RetirementDigest != "" {
		return false
	}
	return effect == nil || effect.Disposition == EffectReservationTerminal || effect.Disposition == EffectReservationRetired
}

func isPostResultProviderExchangeReady(cp Checkpoint) bool {
	pins := cp.Pins
	if pins.ProviderDisposition != DispositionAwaitingProviderExchange ||
		pins.PendingCallDisposition != PendingCallTerminal || len(pins.ProviderTranscripts) == 0 {
		return false
	}
	activeIndex := len(pins.ProviderTranscripts) - 1
	active := pins.ProviderTranscripts[activeIndex]
	if active.Disposition != DispositionAwaitingProviderExchange {
		return false
	}
	for index, transcript := range pins.ProviderTranscripts {
		if index == activeIndex {
			continue
		}
		if transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return false
		}
	}
	host := pins.HostCallReservation
	effect := pins.EffectReservation
	if host == nil || host.Disposition != HostCallTerminal || host.CapturedCall != nil ||
		!validDigest(host.OutcomeDigest) || host.RetirementDigest != "" ||
		effect == nil || effect.Disposition != EffectReservationTerminal ||
		!validDigest(effect.OutcomeDigest) || effect.RetirementDigest != "" {
		return false
	}
	binding := effect.Binding
	return binding.WorkflowID == pins.WorkflowID && binding.ConversationID == active.ConversationID &&
		binding.HostCallID == host.Binding.CallID && binding.HostCallOutcomeDigest == host.OutcomeDigest &&
		binding.RecoveryGeneration == pins.RecoveryGeneration && binding.OwnerEpoch == pins.OwnerEpoch
}

// validateEffectReservationAdmission keeps the ordinary admission gate intact.
// The only exception is one captured provider call that has an exact terminal
// host-call reservation and current-fence capture pin. The verifier must join
// that capture to the protected ALLOW decision and typed host operation.
func validateEffectReservationAdmission(ctx context.Context, cp Checkpoint, binding EffectBinding,
	verifier CapturedOperationVerifier, now time.Time) error {
	if err := validateCheckpoint(cp, cp.ComputedCheckpointDigest); err != nil {
		return err
	}
	if err := validateLimits(cp.Limits, now); err != nil {
		return err
	}
	if cp.Pins.ProviderDisposition != DispositionAwaitingGateDecision {
		if hasCapturedEffectBinding(binding) {
			return ErrObservation
		}
		return validateCheckpointAdmission(cp, now)
	}
	if cp.Pins.PendingCallDisposition != PendingCallCaptured || len(cp.Pins.ProviderTranscripts) == 0 {
		return ErrPendingUnknown
	}
	if cp.Pins.EffectReservation != nil && (cp.Pins.EffectReservation.Disposition == EffectReservationReserved ||
		cp.Pins.EffectReservation.Disposition == EffectReservationUnknown) {
		return ErrPendingUnknown
	}
	reservation := cp.Pins.HostCallReservation
	if reservation == nil || reservation.Disposition != HostCallTerminal || !validDigest(reservation.OutcomeDigest) ||
		reservation.CapturedCall == nil || reservation.CapturedCall.Disposition != CapturedCallAvailable {
		return ErrPendingUnknown
	}
	if verifier == nil || !matchesCapturedEffectBinding(*reservation.CapturedCall, binding) {
		return ErrObservation
	}
	for i, transcript := range cp.Pins.ProviderTranscripts {
		if i == len(cp.Pins.ProviderTranscripts)-1 {
			if transcript.Disposition != DispositionAwaitingGateDecision {
				return ErrPendingUnknown
			}
		} else if transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return ErrPendingUnknown
		}
	}
	verifiedCheckpoint := cp
	verifiedCheckpoint.Pins = clonePins(cp.Pins)
	if err := verifier.VerifyCapturedOperation(ctx, verifiedCheckpoint, binding); err != nil {
		return fmt.Errorf("%w: captured operation verifier: %v", ErrObservation, err)
	}
	return nil
}

func hasCapturedEffectBinding(binding EffectBinding) bool {
	return binding.WorkflowID != "" || binding.ConversationID != "" || binding.HostCallID != "" ||
		binding.ToolCallID != "" || binding.HostCallOutcomeDigest != "" || binding.RecoveryGeneration != 0 ||
		binding.OwnerEpoch != 0
}

func matchesCapturedEffectBinding(captured CapturedCallPin, binding EffectBinding) bool {
	return binding.WorkflowID == captured.WorkflowID && binding.ConversationID == captured.ConversationID &&
		binding.HostCallID == captured.HostCallID && binding.ToolCallID == captured.ToolCallID &&
		binding.HostCallOutcomeDigest == captured.OutcomeDigest &&
		binding.RecoveryGeneration == captured.RecoveryGeneration && binding.OwnerEpoch == captured.OwnerEpoch
}

func validateCheckpoint(cp Checkpoint, digest string) error {
	if cp.SchemaVersion != checkpointSchema || cp.Pins.WorkflowID == "" || cp.Pins.OwnerEpoch == 0 ||
		!validDigest(digest) || cp.ComputedCheckpointDigest != digest || computeCheckpointDigest(cp) != digest {
		return fmt.Errorf("%w: checkpoint schema or digest mismatch", ErrRollback)
	}
	if cp.PreviousCheckpointDigest != "" && !validDigest(cp.PreviousCheckpointDigest) {
		return fmt.Errorf("%w: invalid predecessor digest", ErrRollback)
	}
	if err := validatePins(cp.Pins); err != nil {
		return err
	}
	if err := validateLimitsStructure(cp.Limits); err != nil {
		return err
	}
	if cp.Counters.PreEffectRetries > cp.Limits.MaxPreEffectRetries ||
		cp.Counters.CleanRestarts > cp.Limits.MaxCleanRestarts ||
		cp.Counters.HostCalls > cp.Limits.MaxHostCalls || cp.Counters.Tokens > cp.Limits.MaxTokens {
		return ErrExhausted
	}
	return nil
}

func validateLimitsStructure(limits Limits) error {
	if limits.MaxPreEffectRetries > 2 || limits.MaxCleanRestarts > 2 ||
		limits.MaxHostCalls == 0 || limits.MaxTokens == 0 || limits.MaxHostCalls > maxSQLiteInteger ||
		limits.MaxTokens > maxSQLiteInteger || limits.Deadline.IsZero() {
		return fmt.Errorf("%w: limits must be finite, deadline-bearing, and within D20 totals", ErrInvalid)
	}
	if limits.Deadline.Location() != time.UTC && limits.Deadline.Location().String() != "UTC" {
		return fmt.Errorf("%w: deadline must be normalized to UTC", ErrInvalid)
	}
	return nil
}

func validatePins(p Pins) error {
	if !validID(p.WorkflowID) || !validID(p.SessionID) || p.RecoveryGeneration == 0 || p.OwnerEpoch == 0 ||
		!validDigest(p.OwnerIncarnationDigest) || !validDigest(p.RootIdentityDigest) || !validDigest(p.StoreIdentityDigest) ||
		!validDigest(p.AdmissionIdentityDigest) || !validDigest(p.CommandIdentityDigest) || !validDigest(p.AdminIdentityDigest) ||
		!validDigest(p.TaskDigest) || !validDigest(p.PromptDigest) || !validDigest(p.PolicyDigest) ||
		!validDigest(p.ProfileDigest) || !validDigest(p.ImageDigest) || !validDigest(p.RuntimeDigest) ||
		!validDigest(p.D06ProvenanceDigest) || !validDigest(p.PrivateGitIdentityDigest) ||
		!validID(p.StoreBaselineGenerationID) || !validDigest(p.StoreBaselineTreeDigest) ||
		!validID(p.StoreTipGenerationID) || !validDigest(p.StoreTipTreeDigest) ||
		!validDigest(p.ProviderProjectionDigest) || !validDigest(p.RecoveryLineageDigest) ||
		!validDisposition(p.ProviderDisposition) || !validPendingCallDisposition(p.PendingCallDisposition) ||
		len(p.ProviderTranscripts) > 10000 ||
		p.ImportedPrefixDigest != "" && !validDigest(p.ImportedPrefixDigest) ||
		p.PredecessorTurnReceiptDigest != "" && !validDigest(p.PredecessorTurnReceiptDigest) {
		return fmt.Errorf("%w: invalid checkpoint pins", ErrInvalid)
	}
	if p.StoreJournalCount == 0 && p.StoreJournalHead != "" || p.StoreJournalCount > 0 && !validDigest(p.StoreJournalHead) ||
		p.AdmissionJournalCount == 0 && p.AdmissionJournalHead != "" || p.AdmissionJournalCount > 0 && !validDigest(p.AdmissionJournalHead) {
		return fmt.Errorf("%w: Store/admission head and count disagree", ErrInvalid)
	}
	if p.EffectReservation != nil {
		reservation := p.EffectReservation
		if !validEffectBinding(reservation.Binding) {
			return fmt.Errorf("%w: invalid effect reservation binding", ErrInvalid)
		}
		switch reservation.Disposition {
		case EffectReservationReserved:
			if reservation.OutcomeDigest != "" || reservation.RetirementDigest != "" {
				return fmt.Errorf("%w: reserved effect already has an outcome", ErrRollback)
			}
		case EffectReservationTerminal:
			if !validDigest(reservation.OutcomeDigest) || reservation.RetirementDigest != "" {
				return fmt.Errorf("%w: terminal effect has no valid outcome digest", ErrInvalid)
			}
		case EffectReservationRetired:
			if !validDigest(reservation.RetirementDigest) || reservation.OutcomeDigest != "" {
				return fmt.Errorf("%w: retired effect has no valid retirement digest", ErrInvalid)
			}
		case EffectReservationUnknown:
			if reservation.OutcomeDigest != "" || reservation.RetirementDigest != "" {
				return fmt.Errorf("%w: unknown effect has fabricated settlement evidence", ErrRollback)
			}
		default:
			return fmt.Errorf("%w: invalid effect reservation disposition", ErrInvalid)
		}
	}
	if p.HostCallReservation != nil {
		reservation := p.HostCallReservation
		if !validHostCallBinding(reservation.Binding) {
			return fmt.Errorf("%w: invalid host-call reservation binding", ErrInvalid)
		}
		switch reservation.Disposition {
		case HostCallReserved, HostCallUnknown:
			if reservation.OutcomeDigest != "" || reservation.RetirementDigest != "" || reservation.RetryableFailure {
				return fmt.Errorf("%w: unsettled host call has fabricated terminal evidence", ErrRollback)
			}
		case HostCallTerminal:
			if !validDigest(reservation.OutcomeDigest) || reservation.RetirementDigest != "" {
				return fmt.Errorf("%w: terminal host call has no valid result digest", ErrInvalid)
			}
			if reservation.RetryableFailure && reservation.CapturedCall != nil {
				return fmt.Errorf("%w: a retryable failed attempt captured a call", ErrInvalid)
			}
		case HostCallRetired:
			if !validDigest(reservation.RetirementDigest) || reservation.OutcomeDigest != "" || reservation.RetryableFailure {
				return fmt.Errorf("%w: retired host call has no valid retirement digest", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: invalid host-call disposition", ErrInvalid)
		}
	}
	if p.ProviderProjectionCount != totalProjectionCount(p.ProviderTranscripts) ||
		p.ProviderProjectionDigest != digestTranscriptPins(p.ProviderTranscripts) {
		return fmt.Errorf("%w: ordered transcript list does not match its projection commitment", ErrRollback)
	}
	if len(p.ProviderTranscripts) == 0 {
		if p.ProviderProjectionHead != "" || p.ProviderDisposition != DispositionNone {
			return fmt.Errorf("%w: empty provider history has a non-empty tip", ErrInvalid)
		}
	} else {
		last := p.ProviderTranscripts[len(p.ProviderTranscripts)-1]
		if p.ProviderProjectionHead != last.ProjectionHead || p.ProviderDisposition != last.Disposition {
			return fmt.Errorf("%w: provider tip differs from last transcript", ErrRollback)
		}
	}
	seenConversation := make(map[string]struct{}, len(p.ProviderTranscripts))
	seenJournal := make(map[string]struct{}, len(p.ProviderTranscripts))
	for index, transcript := range p.ProviderTranscripts {
		if transcript.Ordinal != uint64(index+1) || !validID(transcript.ConversationID) ||
			!validDigest(transcript.JournalIdentityDigest) || !validDisposition(transcript.Disposition) ||
			transcript.Disposition == DispositionNone ||
			transcript.JournalCount == 0 && transcript.JournalHead != "" ||
			transcript.JournalCount > 0 && !validDigest(transcript.JournalHead) ||
			transcript.ProjectionCount == 0 && (transcript.ProjectionDigest != "" || transcript.ProjectionHead != "") ||
			transcript.ProjectionCount > 0 && (!validDigest(transcript.ProjectionDigest) || !validDigest(transcript.ProjectionHead)) {
			return fmt.Errorf("%w: invalid ordered provider transcript pin %d", ErrInvalid, index)
		}
		if _, ok := seenConversation[transcript.ConversationID]; ok {
			return fmt.Errorf("%w: duplicate conversation ID", ErrRollback)
		}
		if _, ok := seenJournal[transcript.JournalIdentityDigest]; ok {
			return fmt.Errorf("%w: duplicate provider journal identity", ErrRollback)
		}
		seenConversation[transcript.ConversationID] = struct{}{}
		seenJournal[transcript.JournalIdentityDigest] = struct{}{}
	}
	if err := validateCapturedCallPin(p); err != nil {
		return err
	}
	if p.PreviousRecoveryLineageDigest != "" && !validDigest(p.PreviousRecoveryLineageDigest) ||
		p.LastDecisionDigest != "" && !validDigest(p.LastDecisionDigest) ||
		p.LastResultDigest != "" && !validDigest(p.LastResultDigest) {
		return fmt.Errorf("%w: invalid recovery lineage or decision/result reference", ErrInvalid)
	}
	return nil
}

func validateCapturedCallPin(p Pins) error {
	reservation := p.HostCallReservation
	if reservation == nil || reservation.CapturedCall == nil {
		if p.ProviderDisposition == DispositionAwaitingGateDecision || p.PendingCallDisposition == PendingCallCaptured {
			return fmt.Errorf("%w: gate-decision state lacks a typed captured-call binding", ErrPendingUnknown)
		}
		return nil
	}
	captured := reservation.CapturedCall
	if reservation.Disposition != HostCallTerminal || !validID(captured.WorkflowID) ||
		!validID(captured.ConversationID) || !validID(captured.HostCallID) || !validID(captured.ToolCallID) ||
		!validDigest(captured.OutcomeDigest) || captured.WorkflowID != p.WorkflowID ||
		captured.HostCallID != reservation.Binding.CallID || captured.OutcomeDigest != reservation.OutcomeDigest ||
		len(p.ProviderTranscripts) == 0 {
		return fmt.Errorf("%w: captured call differs from its workflow, conversation, or terminal host-call reservation", ErrRollback)
	}
	if captured.Disposition != CapturedCallRetired &&
		(captured.RecoveryGeneration != p.RecoveryGeneration || captured.OwnerEpoch != p.OwnerEpoch) {
		return fmt.Errorf("%w: live captured call belongs to another workflow generation or owner fence", ErrOwnerFence)
	}
	if captured.Disposition == CapturedCallRetired {
		foundRetiredConversation := deniedCallAwaitsProviderAnswer(p)
		for index, transcript := range p.ProviderTranscripts {
			if transcript.ConversationID == captured.ConversationID && transcript.Disposition == DispositionRetired {
				foundRetiredConversation = true
				break
			}
			if index == len(p.ProviderTranscripts)-1 && transcript.ConversationID == captured.ConversationID &&
				p.ProviderDisposition == DispositionAwaitingHostPrompt &&
				(p.PendingCallDisposition == PendingCallNone || p.PendingCallDisposition == PendingCallTerminal ||
					p.PendingCallDisposition == PendingCallRetired) {
				foundRetiredConversation = true
				break
			}
		}
		if !foundRetiredConversation {
			return fmt.Errorf("%w: retired captured call is not retained in its retired conversation", ErrRollback)
		}
	} else if captured.ConversationID != p.ProviderTranscripts[len(p.ProviderTranscripts)-1].ConversationID {
		return fmt.Errorf("%w: live captured call differs from the active conversation", ErrRollback)
	}
	switch captured.Disposition {
	case CapturedCallAvailable:
		if captured.RetirementDigest != "" || p.ProviderDisposition != DispositionAwaitingGateDecision ||
			p.PendingCallDisposition != PendingCallCaptured || p.EffectReservation != nil &&
			(p.EffectReservation.Disposition == EffectReservationReserved || p.EffectReservation.Disposition == EffectReservationUnknown) {
			return fmt.Errorf("%w: available captured call is not awaiting its first bound gate operation", ErrPendingUnknown)
		}
	case CapturedCallConsumed:
		if captured.RetirementDigest != "" || p.ProviderDisposition != DispositionAwaitingGateDecision ||
			p.PendingCallDisposition != PendingCallCaptured || p.EffectReservation == nil ||
			!matchesCapturedEffectBinding(*captured, p.EffectReservation.Binding) {
			return fmt.Errorf("%w: consumed captured call lacks its exact effect reservation", ErrRollback)
		}
	case CapturedCallRetired:
		retiredState := p.ProviderDisposition == DispositionRetired && p.PendingCallDisposition == PendingCallRetired
		promptState := p.ProviderDisposition == DispositionAwaitingHostPrompt &&
			(p.PendingCallDisposition == PendingCallNone || p.PendingCallDisposition == PendingCallTerminal ||
				p.PendingCallDisposition == PendingCallRetired)
		if !validDigest(captured.RetirementDigest) || (!retiredState && !promptState && !deniedCallAwaitsProviderAnswer(p)) {
			return fmt.Errorf("%w: captured call retirement is not joined to a retired or restarted provider state", ErrRollback)
		}
	default:
		return fmt.Errorf("%w: invalid captured-call disposition", ErrInvalid)
	}
	return nil
}

// validateOwnerTransition replays the durable owner history. Reservations
// consume one allowance at a time, except that retirement of an unexecuted
// captured call may atomically consume one clean restart and one pre-effect retry.
// Progress commits may change verified pins without changing budgets; clean
// restarts advance exactly one generation and consume exactly one workflow-wide
// restart.
func validateOwnerTransition(previous Checkpoint, previousDigest string, priorExists bool, next Checkpoint, expected string, proof TransitionProof) error {
	if err := validateCheckpoint(next, next.ComputedCheckpointDigest); err != nil {
		return err
	}
	if !priorExists {
		if expected != "" || next.PreviousCheckpointDigest != "" || next.ObservationDigest != "" ||
			next.Pins.RecoveryGeneration != 1 || proof.kind != transitionNone || proof.reservation != reservationNone ||
			proof.proofDigest != "" || next.Counters != (Counters{}) ||
			next.Pins.EffectReservation != nil || next.Pins.HostCallReservation != nil ||
			len(next.Pins.ProviderTranscripts) != 0 || next.Pins.ProviderDisposition != DispositionNone ||
			next.Pins.PendingCallDisposition != PendingCallNone {
			return fmt.Errorf("%w: fresh checkpoint must begin an empty ledger", ErrRollback)
		}
		return nil
	}
	if expected == "" || expected != previousDigest || next.PreviousCheckpointDigest != previousDigest {
		return ErrCAS
	}
	if next.Limits != previous.Limits || next.Counters.PreEffectRetries < previous.Counters.PreEffectRetries ||
		next.Counters.CleanRestarts < previous.Counters.CleanRestarts ||
		next.Counters.HostCalls < previous.Counters.HostCalls || next.Counters.Tokens < previous.Counters.Tokens {
		return ErrRollback
	}
	if proof.reservation == reservationCleanRestartPreEffectRetry {
		return validateCleanRestartPreEffectRetryTransition(proof, previousDigest, previous, next)
	}
	if next.Pins.WorkflowID != previous.Pins.WorkflowID ||
		next.Counters.PreEffectRetries-previous.Counters.PreEffectRetries+
			next.Counters.CleanRestarts-previous.Counters.CleanRestarts+
			next.Counters.HostCalls-previous.Counters.HostCalls > 1 {
		return ErrRollback
	}
	sameCounters := next.Counters == previous.Counters
	if next.Pins.RecoveryGeneration == previous.Pins.RecoveryGeneration {
		if next.Pins.OwnerEpoch != previous.Pins.OwnerEpoch || next.Pins.SessionID != previous.Pins.SessionID ||
			next.Counters.CleanRestarts != previous.Counters.CleanRestarts {
			return ErrRollback
		}
		if sameCounters {
			if proof.kind == transitionTerminalEffect {
				return validateTerminalEffectTransition(proof, previousDigest, previous, next)
			}
			if proof.kind != transitionProgress || proof.reservation != reservationNone || proof.previousDigest != previousDigest ||
				!reflect.DeepEqual(proof.previousPins, previous.Pins) || !reflect.DeepEqual(proof.observedPins, next.Pins) ||
				!reflect.DeepEqual(proof.targetPins, next.Pins) || proof.proofDigest != next.ObservationDigest ||
				proof.proofDigest != digestObservation(transitionProgress, previousDigest, previous.Pins, next.Pins) {
				return ErrObservation
			}
			return validateObservationAdvance(previous.Pins, next.Pins)
		}
		if next.Counters.PreEffectRetries == previous.Counters.PreEffectRetries+1 &&
			next.Counters.CleanRestarts == previous.Counters.CleanRestarts &&
			next.Counters.HostCalls == previous.Counters.HostCalls && next.Counters.Tokens == previous.Counters.Tokens {
			if !reflect.DeepEqual(previous.Pins, next.Pins) {
				return ErrRollback
			}
			return validateReservationProof(proof, reservationPreEffectRetry, previousDigest, previous, next)
		}
		if next.Counters.HostCalls == previous.Counters.HostCalls+1 &&
			next.Counters.PreEffectRetries == previous.Counters.PreEffectRetries &&
			next.Counters.CleanRestarts == previous.Counters.CleanRestarts {
			if next.ObservationDigest != previous.ObservationDigest {
				return ErrRollback
			}
			if next.Counters.Tokens == previous.Counters.Tokens {
				if err := validateEffectReservationTransition(previous.Pins, next.Pins); err != nil {
					return err
				}
				return validateReservationProof(proof, reservationEffect, previousDigest, previous, next)
			}
			if next.Counters.Tokens > previous.Counters.Tokens {
				if err := validateHostCallReservationTransition(previous.Pins, next.Pins); err != nil {
					return err
				}
				return validateReservationProof(proof, reservationHostCall, previousDigest, previous, next)
			}
			return ErrRollback
		}
		return ErrRollback
	}
	if previous.Pins.RecoveryGeneration == ^uint64(0) || next.Pins.RecoveryGeneration != previous.Pins.RecoveryGeneration+1 ||
		next.Pins.OwnerEpoch <= previous.Pins.OwnerEpoch || next.Pins.SessionID == previous.Pins.SessionID ||
		next.Counters.CleanRestarts != previous.Counters.CleanRestarts+1 ||
		next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries ||
		next.Counters.HostCalls != previous.Counters.HostCalls || next.Counters.Tokens != previous.Counters.Tokens {
		return ErrRollback
	}
	if proof.kind != transitionRecovery || proof.reservation != reservationNone || proof.previousDigest != previousDigest ||
		!reflect.DeepEqual(proof.previousPins, previous.Pins) || !reflect.DeepEqual(proof.targetPins, next.Pins) ||
		proof.proofDigest != next.ObservationDigest ||
		proof.proofDigest != digestObservation(transitionRecovery, previousDigest, previous.Pins, proof.observedPins) {
		return ErrObservation
	}
	if err := validateObservationAdvance(previous.Pins, proof.observedPins); err != nil {
		return err
	}
	return validateRestartPins(proof.observedPins, next.Pins, previousDigest)
}

func validateReservationProof(proof TransitionProof, kind reservationKind, previousDigest string, previous, next Checkpoint) error {
	if proof.kind != transitionReservation || proof.reservation != kind || proof.previousDigest != previousDigest ||
		!reflect.DeepEqual(proof.previousPins, previous.Pins) || !reflect.DeepEqual(proof.targetPins, next.Pins) ||
		!reflect.DeepEqual(proof.previousCounts, previous.Counters) || !reflect.DeepEqual(proof.targetCounts, next.Counters) ||
		!reflect.DeepEqual(proof.observedPins, Pins{}) || proof.proofDigest != digestReservationProof(proof) {
		return ErrObservation
	}
	if next.ObservationDigest != previous.ObservationDigest {
		return ErrObservation
	}
	switch kind {
	case reservationPreEffectRetry:
		if next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries+1 ||
			next.Counters.CleanRestarts != previous.Counters.CleanRestarts ||
			next.Counters.HostCalls != previous.Counters.HostCalls || next.Counters.Tokens != previous.Counters.Tokens {
			return ErrRollback
		}
	case reservationHostCall:
		if next.Counters.HostCalls != previous.Counters.HostCalls+1 ||
			next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries ||
			next.Counters.CleanRestarts != previous.Counters.CleanRestarts ||
			next.Counters.Tokens <= previous.Counters.Tokens ||
			next.Counters.Tokens-previous.Counters.Tokens != next.Pins.HostCallReservation.Binding.TokenUpperBound {
			return ErrRollback
		}
	case reservationEffect:
		if next.Counters.HostCalls != previous.Counters.HostCalls+1 ||
			next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries ||
			next.Counters.CleanRestarts != previous.Counters.CleanRestarts || next.Counters.Tokens != previous.Counters.Tokens {
			return ErrRollback
		}
	default:
		return ErrObservation
	}
	return nil
}

// validateCleanRestartPreEffectRetryTransition accepts exactly one atomic
// recovery that consumes one clean restart and one pre-effect retry. The retry
// is justified only by an observed retirement of an unexecuted captured call;
// every other counter and the host-call/token budget must be unchanged.
func validateCleanRestartPreEffectRetryTransition(proof TransitionProof, previousDigest string, previous, next Checkpoint) error {
	if previous.Pins.RecoveryGeneration == ^uint64(0) || next.Pins.WorkflowID != previous.Pins.WorkflowID ||
		next.Pins.RecoveryGeneration != previous.Pins.RecoveryGeneration+1 ||
		next.Pins.OwnerEpoch <= previous.Pins.OwnerEpoch || next.Pins.SessionID == previous.Pins.SessionID ||
		next.Counters.CleanRestarts != previous.Counters.CleanRestarts+1 ||
		next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries+1 ||
		next.Counters.HostCalls != previous.Counters.HostCalls || next.Counters.Tokens != previous.Counters.Tokens {
		return ErrRollback
	}
	if proof.kind != transitionRecovery || proof.previousDigest != previousDigest ||
		!reflect.DeepEqual(proof.previousPins, previous.Pins) || !reflect.DeepEqual(proof.targetPins, next.Pins) ||
		proof.proofDigest != next.ObservationDigest ||
		proof.proofDigest != digestObservation(transitionRecovery, previousDigest, previous.Pins, proof.observedPins) ||
		!retiredUnleasedCallObserved(proof.observedPins) {
		return ErrObservation
	}
	if err := validateObservationAdvance(previous.Pins, proof.observedPins); err != nil {
		return err
	}
	return validateRestartPins(proof.observedPins, next.Pins, previousDigest)
}

// retiredUnleasedCallObserved is the only observation that can justify charging a
// pre-effect retry: the provider lineage is retired and its captured call was
// retired without execution, with a commitment to that retirement.
func retiredUnleasedCallObserved(p Pins) bool {
	return p.ProviderDisposition == DispositionRetired && p.PendingCallDisposition == PendingCallRetired &&
		p.HostCallReservation != nil && p.HostCallReservation.CapturedCall != nil &&
		p.HostCallReservation.CapturedCall.Disposition == CapturedCallRetired &&
		validDigest(p.HostCallReservation.CapturedCall.RetirementDigest)
}

func validateRestartPins(old, next Pins, previousCheckpointDigest string) error {
	if old.OwnerEpoch == ^uint64(0) || next.OwnerEpoch <= old.OwnerEpoch {
		return fmt.Errorf("%w: clean restart must advance the owner epoch", ErrOwnerFence)
	}
	if old.EffectReservation != nil && old.EffectReservation.Disposition != EffectReservationTerminal &&
		old.EffectReservation.Disposition != EffectReservationRetired {
		return ErrPendingUnknown
	}
	if old.HostCallReservation != nil && old.HostCallReservation.Disposition != HostCallTerminal &&
		old.HostCallReservation.Disposition != HostCallRetired {
		return ErrPendingUnknown
	}
	hostCallPreserved := reflect.DeepEqual(old.HostCallReservation, next.HostCallReservation) ||
		preservesRetiredCapturedCall(old.HostCallReservation, next.HostCallReservation)
	if old.WorkflowID != next.WorkflowID || old.RootIdentityDigest != next.RootIdentityDigest ||
		old.StoreIdentityDigest != next.StoreIdentityDigest || old.AdmissionIdentityDigest != next.AdmissionIdentityDigest ||
		old.CommandIdentityDigest != next.CommandIdentityDigest || old.AdminIdentityDigest != next.AdminIdentityDigest ||
		old.TaskDigest != next.TaskDigest || old.PromptDigest != next.PromptDigest ||
		old.ImportedPrefixDigest != next.ImportedPrefixDigest || old.PredecessorTurnReceiptDigest != next.PredecessorTurnReceiptDigest ||
		old.PolicyDigest != next.PolicyDigest || old.ProfileDigest != next.ProfileDigest || old.ImageDigest != next.ImageDigest ||
		old.RuntimeDigest != next.RuntimeDigest || old.D06ProvenanceDigest != next.D06ProvenanceDigest ||
		old.PrivateGitIdentityDigest != next.PrivateGitIdentityDigest ||
		old.StoreBaselineGenerationID != next.StoreBaselineGenerationID || old.StoreBaselineTreeDigest != next.StoreBaselineTreeDigest ||
		old.StoreTipGenerationID != next.StoreTipGenerationID || old.StoreTipTreeDigest != next.StoreTipTreeDigest ||
		old.StoreJournalHead != next.StoreJournalHead || old.StoreJournalCount != next.StoreJournalCount ||
		old.AdmissionJournalHead != next.AdmissionJournalHead || old.AdmissionJournalCount != next.AdmissionJournalCount ||
		!reflect.DeepEqual(old.EffectReservation, next.EffectReservation) ||
		!hostCallPreserved {
		return fmt.Errorf("%w: clean restart changed immutable workflow, D06, or retained Store pins", ErrRollback)
	}
	if next.PreviousRecoveryLineageDigest != old.RecoveryLineageDigest ||
		next.LastDecisionDigest != old.LastDecisionDigest || next.LastResultDigest != old.LastResultDigest ||
		next.PendingCallDisposition != PendingCallNone ||
		len(next.ProviderTranscripts) != len(old.ProviderTranscripts)+1 {
		return fmt.Errorf("%w: clean restart omitted recovery lineage, decision/result, or transcript history", ErrRollback)
	}
	for i := range old.ProviderTranscripts {
		if !reflect.DeepEqual(old.ProviderTranscripts[i], next.ProviderTranscripts[i]) {
			return fmt.Errorf("%w: clean restart changed historical transcript %d", ErrRollback, i)
		}
		if old.ProviderTranscripts[i].Disposition != DispositionTerminal && old.ProviderTranscripts[i].Disposition != DispositionRetired {
			return fmt.Errorf("%w: historical transcript %d is not terminal or retired", ErrPendingUnknown, i)
		}
	}
	last := next.ProviderTranscripts[len(next.ProviderTranscripts)-1]
	if last.Ordinal != uint64(len(next.ProviderTranscripts)) ||
		last.JournalCount != 0 || last.JournalHead != "" || last.ProjectionCount != 0 ||
		last.ProjectionDigest != "" || last.ProjectionHead != "" || last.Disposition != DispositionAwaitingHostPrompt {
		return fmt.Errorf("%w: new provider conversation must start with an empty retained transcript", ErrInvalid)
	}
	if next.RecoveryLineageDigest == old.RecoveryLineageDigest || !validDigest(next.RecoveryLineageDigest) {
		return fmt.Errorf("%w: clean restart requires a new host-generated recovery lineage", ErrInvalid)
	}
	if previousCheckpointDigest == "" || !validDigest(previousCheckpointDigest) {
		return fmt.Errorf("%w: missing predecessor checkpoint commitment", ErrRollback)
	}
	if next.RecoveryLineageDigest != ComputeRecoveryLineageDigest(previousCheckpointDigest, old, next) {
		return fmt.Errorf("%w: recovery lineage does not bind the predecessor checkpoint and new envelope", ErrRollback)
	}
	return nil
}

func preservesRetiredCapturedCall(old, next *HostCallReservationPin) bool {
	if old == nil || next == nil || old.CapturedCall == nil || next.CapturedCall == nil ||
		old.CapturedCall.Disposition != CapturedCallAvailable || next.CapturedCall.Disposition != CapturedCallRetired ||
		!validDigest(next.CapturedCall.RetirementDigest) {
		return false
	}
	oldWithoutCapture, nextWithoutCapture := *old, *next
	oldCapture, nextCapture := *old.CapturedCall, *next.CapturedCall
	oldWithoutCapture.CapturedCall, nextWithoutCapture.CapturedCall = nil, nil
	oldCapture.Disposition, oldCapture.RetirementDigest = CapturedCallRetired, nextCapture.RetirementDigest
	return reflect.DeepEqual(oldWithoutCapture, nextWithoutCapture) && reflect.DeepEqual(oldCapture, nextCapture)
}

func validateObservationAdvance(old, next Pins) error {
	if old.WorkflowID != next.WorkflowID || old.SessionID != next.SessionID ||
		old.RecoveryGeneration != next.RecoveryGeneration || old.OwnerEpoch != next.OwnerEpoch ||
		old.OwnerIncarnationDigest != next.OwnerIncarnationDigest || old.RootIdentityDigest != next.RootIdentityDigest || old.StoreIdentityDigest != next.StoreIdentityDigest ||
		old.AdmissionIdentityDigest != next.AdmissionIdentityDigest || old.CommandIdentityDigest != next.CommandIdentityDigest ||
		old.AdminIdentityDigest != next.AdminIdentityDigest ||
		old.TaskDigest != next.TaskDigest || old.PromptDigest != next.PromptDigest ||
		old.ImportedPrefixDigest != next.ImportedPrefixDigest || old.PredecessorTurnReceiptDigest != next.PredecessorTurnReceiptDigest ||
		old.PolicyDigest != next.PolicyDigest || old.ProfileDigest != next.ProfileDigest || old.ImageDigest != next.ImageDigest ||
		old.RuntimeDigest != next.RuntimeDigest || old.D06ProvenanceDigest != next.D06ProvenanceDigest ||
		old.PrivateGitIdentityDigest != next.PrivateGitIdentityDigest ||
		old.StoreBaselineGenerationID != next.StoreBaselineGenerationID || old.StoreBaselineTreeDigest != next.StoreBaselineTreeDigest ||
		old.RecoveryLineageDigest != next.RecoveryLineageDigest || old.PreviousRecoveryLineageDigest != next.PreviousRecoveryLineageDigest {
		return fmt.Errorf("%w: observation changed immutable workflow pins", ErrRollback)
	}
	if next.StoreJournalCount < old.StoreJournalCount || next.AdmissionJournalCount < old.AdmissionJournalCount {
		return ErrRollback
	}
	if next.StoreJournalCount == old.StoreJournalCount && (next.StoreJournalHead != old.StoreJournalHead ||
		next.StoreTipGenerationID != old.StoreTipGenerationID || next.StoreTipTreeDigest != old.StoreTipTreeDigest) {
		return ErrRollback
	}
	if next.AdmissionJournalCount == old.AdmissionJournalCount && next.AdmissionJournalHead != old.AdmissionJournalHead {
		return ErrRollback
	}
	if err := validateEffectReservationAdvance(old.EffectReservation, next.EffectReservation); err != nil {
		return err
	}
	if err := validateHostCallReservationAdvance(old.HostCallReservation, next.HostCallReservation); err != nil {
		return err
	}
	if err := validateCapturedCallObservation(old, next); err != nil {
		return err
	}
	initialTranscriptAppend := len(old.ProviderTranscripts) == 0 && len(next.ProviderTranscripts) == 1 &&
		old.RecoveryGeneration == 1 && old.ProviderDisposition == DispositionNone &&
		old.PendingCallDisposition == PendingCallNone && old.ProviderProjectionCount == 0 &&
		next.PendingCallDisposition == PendingCallNone && next.ProviderDisposition == DispositionAwaitingHostPrompt &&
		next.ProviderProjectionCount == 0 && next.ProviderProjectionHead == "" &&
		next.ProviderTranscripts[0].Ordinal == 1 && next.ProviderTranscripts[0].JournalCount == 0 &&
		next.ProviderTranscripts[0].JournalHead == "" && next.ProviderTranscripts[0].ProjectionCount == 0 &&
		next.ProviderTranscripts[0].ProjectionDigest == "" && next.ProviderTranscripts[0].ProjectionHead == "" &&
		next.ProviderTranscripts[0].Disposition == DispositionAwaitingHostPrompt
	if !initialTranscriptAppend && len(old.ProviderTranscripts) != len(next.ProviderTranscripts) {
		return fmt.Errorf("%w: observation cannot add or drop conversations", ErrRollback)
	}
	for i := range old.ProviderTranscripts {
		prior, current := old.ProviderTranscripts[i], next.ProviderTranscripts[i]
		if i != len(old.ProviderTranscripts)-1 {
			if !reflect.DeepEqual(prior, current) {
				return fmt.Errorf("%w: observation changed historical transcript %d", ErrRollback, i)
			}
			continue
		}
		if prior.Ordinal != current.Ordinal || prior.ConversationID != current.ConversationID ||
			prior.JournalIdentityDigest != current.JournalIdentityDigest || current.JournalCount < prior.JournalCount ||
			current.ProjectionCount < prior.ProjectionCount ||
			current.JournalCount == prior.JournalCount && current.JournalHead != prior.JournalHead ||
			current.ProjectionCount == prior.ProjectionCount && (current.ProjectionHead != prior.ProjectionHead || current.ProjectionDigest != prior.ProjectionDigest) {
			return fmt.Errorf("%w: active transcript regressed or changed identity", ErrRollback)
		}
	}
	if next.ProviderProjectionCount < old.ProviderProjectionCount ||
		(!initialTranscriptAppend && next.ProviderProjectionCount == old.ProviderProjectionCount &&
			len(old.ProviderTranscripts) == 0 && next.ProviderProjectionDigest != old.ProviderProjectionDigest) {
		return fmt.Errorf("%w: provider projection commitment regressed", ErrRollback)
	}
	if old.LastDecisionDigest != "" && old.LastDecisionDigest != next.LastDecisionDigest ||
		old.LastResultDigest != "" && old.LastResultDigest != next.LastResultDigest {
		return fmt.Errorf("%w: prior decision or result digest changed", ErrRollback)
	}
	if old.PendingCallDisposition == PendingCallTerminal || old.PendingCallDisposition == PendingCallRetired {
		if next.PendingCallDisposition != old.PendingCallDisposition && !isNextCapturedCallAfterSettledCall(old, next) {
			return fmt.Errorf("%w: terminal call disposition was rewritten", ErrRollback)
		}
	}
	return nil
}

// A terminal/retired provider tool call is immutable once settled, but a later
// host request may capture a different call in the same conversation. The
// owner first durably charges that new HostCallBinding, so this narrowly
// permits only its exact Reserved-to-Terminal capture transition.
func isNextCapturedCallAfterSettledCall(old, next Pins) bool {
	canCaptureAfterSettledResult := old.ProviderDisposition == DispositionAwaitingHostPrompt
	if old.ProviderDisposition == DispositionAwaitingProviderExchange && len(old.ProviderTranscripts) != 0 {
		active := old.ProviderTranscripts[len(old.ProviderTranscripts)-1]
		canCaptureAfterSettledResult = active.Disposition == DispositionAwaitingProviderExchange
	}
	if (old.PendingCallDisposition != PendingCallTerminal && old.PendingCallDisposition != PendingCallRetired) ||
		!canCaptureAfterSettledResult ||
		next.PendingCallDisposition != PendingCallCaptured || next.ProviderDisposition != DispositionAwaitingGateDecision ||
		old.HostCallReservation == nil || old.HostCallReservation.Disposition != HostCallReserved ||
		old.HostCallReservation.CapturedCall != nil || next.HostCallReservation == nil ||
		next.HostCallReservation.Disposition != HostCallTerminal || next.HostCallReservation.CapturedCall == nil ||
		next.HostCallReservation.CapturedCall.Disposition != CapturedCallAvailable ||
		old.HostCallReservation.Binding != next.HostCallReservation.Binding {
		return false
	}
	return true
}

func validateCapturedCallObservation(old, next Pins) error {
	var previous, current *CapturedCallPin
	if old.HostCallReservation != nil {
		previous = old.HostCallReservation.CapturedCall
	}
	if next.HostCallReservation != nil {
		current = next.HostCallReservation.CapturedCall
	}
	if reflect.DeepEqual(previous, current) {
		return nil
	}
	if previous == nil && current != nil && current.Disposition == CapturedCallAvailable {
		if old.EffectReservation != nil && old.EffectReservation.Binding.ToolCallID == current.ToolCallID {
			return fmt.Errorf("%w: provider tool-call identity already has a consumed effect reservation", ErrObservation)
		}
		if old.HostCallReservation != nil && old.HostCallReservation.Disposition == HostCallReserved &&
			next.HostCallReservation != nil && next.HostCallReservation.Disposition == HostCallTerminal &&
			old.ProviderDisposition != DispositionAwaitingGateDecision &&
			next.ProviderDisposition == DispositionAwaitingGateDecision &&
			next.PendingCallDisposition == PendingCallCaptured {
			return nil
		}
		return fmt.Errorf("%w: capture can appear only with the verified terminal provider exchange", ErrObservation)
	}
	if previous != nil && previous.Disposition == CapturedCallAvailable && current != nil &&
		current.Disposition == CapturedCallRetired && previous.WorkflowID == current.WorkflowID &&
		previous.ConversationID == current.ConversationID && previous.HostCallID == current.HostCallID &&
		previous.ToolCallID == current.ToolCallID && previous.OutcomeDigest == current.OutcomeDigest &&
		previous.RecoveryGeneration == current.RecoveryGeneration && previous.OwnerEpoch == current.OwnerEpoch &&
		current.RetirementDigest != "" && next.HostCallReservation != nil &&
		next.HostCallReservation.Disposition == HostCallTerminal &&
		(next.ProviderDisposition == DispositionRetired && next.PendingCallDisposition == PendingCallRetired ||
			next.ProviderDisposition == DispositionAwaitingHostPrompt &&
				(next.PendingCallDisposition == PendingCallNone || next.PendingCallDisposition == PendingCallRetired) ||
			deniedCallAwaitsProviderAnswer(next)) {
		return nil
	}
	if previous != nil && previous.Disposition == CapturedCallConsumed && current == nil &&
		old.ProviderDisposition == DispositionAwaitingGateDecision &&
		next.ProviderDisposition != DispositionAwaitingGateDecision &&
		old.EffectReservation != nil && old.EffectReservation.Disposition == EffectReservationTerminal &&
		reflect.DeepEqual(old.EffectReservation, next.EffectReservation) &&
		old.HostCallReservation != nil && next.HostCallReservation != nil &&
		old.HostCallReservation.Binding == next.HostCallReservation.Binding &&
		old.HostCallReservation.OutcomeDigest == next.HostCallReservation.OutcomeDigest &&
		next.HostCallReservation.Disposition == HostCallTerminal {
		return nil
	}
	return fmt.Errorf("%w: captured call binding was rewritten, duplicated, or dropped without terminal settlement", ErrRollback)
}

func validateTerminalEffectPins(old, next Pins) error {
	if old.EffectReservation == nil || old.EffectReservation.Disposition != EffectReservationReserved ||
		next.EffectReservation == nil || next.EffectReservation.Disposition != EffectReservationTerminal ||
		old.EffectReservation.Binding != next.EffectReservation.Binding ||
		!validDigest(next.EffectReservation.OutcomeDigest) || next.EffectReservation.RetirementDigest != "" {
		return ErrObservation
	}
	if next.StoreJournalCount <= old.StoreJournalCount || next.StoreJournalHead == old.StoreJournalHead ||
		!validDigest(next.StoreJournalHead) || next.AdmissionJournalCount < old.AdmissionJournalCount ||
		next.AdmissionJournalCount == old.AdmissionJournalCount && next.AdmissionJournalHead != old.AdmissionJournalHead ||
		next.AdmissionJournalCount > old.AdmissionJournalCount && next.AdmissionJournalHead == old.AdmissionJournalHead {
		return fmt.Errorf("%w: terminal Store outcome did not advance a durable journal head", ErrObservation)
	}
	expected := clonePins(old)
	expected.StoreJournalHead = next.StoreJournalHead
	expected.StoreJournalCount = next.StoreJournalCount
	expected.StoreTipGenerationID = next.StoreTipGenerationID
	expected.StoreTipTreeDigest = next.StoreTipTreeDigest
	expected.AdmissionJournalHead = next.AdmissionJournalHead
	expected.AdmissionJournalCount = next.AdmissionJournalCount
	expected.EffectReservation = next.EffectReservation
	if !reflect.DeepEqual(expected, next) {
		return fmt.Errorf("%w: terminal effect accounting changed unrelated workflow pins", ErrRollback)
	}
	return validatePins(next)
}

func validateTerminalEffectTransition(proof TransitionProof, previousDigest string, previous, next Checkpoint) error {
	if proof.reservation != reservationEffect || proof.previousDigest != previousDigest ||
		!reflect.DeepEqual(proof.previousPins, previous.Pins) ||
		!reflect.DeepEqual(proof.observedPins, next.Pins) || !reflect.DeepEqual(proof.targetPins, next.Pins) ||
		proof.previousCounts != previous.Counters || proof.targetCounts != next.Counters ||
		next.Counters != previous.Counters || next.Limits != previous.Limits ||
		proof.proofDigest != next.ObservationDigest ||
		proof.proofDigest != digestTerminalEffect(previousDigest, previous.Pins, next.Pins, previous.Counters) {
		return ErrObservation
	}
	return validateTerminalEffectPins(previous.Pins, next.Pins)
}

func validateEffectReservationTransition(old, next Pins) error {
	if next.EffectReservation == nil || next.EffectReservation.Disposition != EffectReservationReserved ||
		next.EffectReservation.OutcomeDigest != "" || next.EffectReservation.RetirementDigest != "" {
		return ErrObservation
	}
	if old.EffectReservation != nil {
		if old.EffectReservation.Disposition != EffectReservationTerminal && old.EffectReservation.Disposition != EffectReservationRetired {
			return ErrPendingUnknown
		}
		if old.EffectReservation.Binding.OperationID == next.EffectReservation.Binding.OperationID {
			return fmt.Errorf("%w: consumed effect operation identity cannot be reused", ErrObservation)
		}
	}
	expected := clonePins(old)
	if old.ProviderDisposition == DispositionAwaitingGateDecision {
		if old.HostCallReservation == nil || old.HostCallReservation.CapturedCall == nil ||
			old.HostCallReservation.CapturedCall.Disposition != CapturedCallAvailable ||
			next.HostCallReservation == nil || next.HostCallReservation.CapturedCall == nil ||
			next.HostCallReservation.CapturedCall.Disposition != CapturedCallConsumed ||
			!matchesCapturedEffectBinding(*old.HostCallReservation.CapturedCall, next.EffectReservation.Binding) {
			return ErrObservation
		}
		expected.HostCallReservation.CapturedCall.Disposition = CapturedCallConsumed
		expected.HostCallReservation.CapturedCall.RetirementDigest = ""
	}
	expected.EffectReservation = next.EffectReservation
	if !reflect.DeepEqual(expected, next) {
		return fmt.Errorf("%w: effect reservation changed unrelated pins", ErrRollback)
	}
	return nil
}

func validateHostCallReservationTransition(old, next Pins) error {
	if next.HostCallReservation == nil || next.HostCallReservation.Disposition != HostCallReserved ||
		next.HostCallReservation.OutcomeDigest != "" || next.HostCallReservation.RetirementDigest != "" {
		return ErrObservation
	}
	if old.HostCallReservation != nil {
		if old.HostCallReservation.Disposition != HostCallTerminal && old.HostCallReservation.Disposition != HostCallRetired {
			return ErrPendingUnknown
		}
		if old.HostCallReservation.Binding.CallID == next.HostCallReservation.Binding.CallID {
			return fmt.Errorf("%w: consumed provider call identity cannot be reused", ErrObservation)
		}
	}
	expected := clonePins(old)
	expected.HostCallReservation = next.HostCallReservation
	if !reflect.DeepEqual(expected, next) {
		return fmt.Errorf("%w: provider reservation changed unrelated pins", ErrRollback)
	}
	return nil
}

func validateEffectReservationAdvance(old, next *EffectReservationPin) error {
	if reflect.DeepEqual(old, next) {
		return nil
	}
	if old == nil || next == nil || old.Binding != next.Binding {
		return fmt.Errorf("%w: effect reservation identity was added, removed, or rewritten", ErrRollback)
	}
	switch old.Disposition {
	case EffectReservationReserved, EffectReservationUnknown:
		if next.Disposition == EffectReservationTerminal || next.Disposition == EffectReservationRetired ||
			next.Disposition == EffectReservationUnknown {
			return nil
		}
	}
	return fmt.Errorf("%w: effect reservation disposition regressed or was rewritten", ErrRollback)
}

func validateHostCallReservationAdvance(old, next *HostCallReservationPin) error {
	if reflect.DeepEqual(old, next) {
		return nil
	}
	if old == nil || next == nil || old.Binding != next.Binding {
		return fmt.Errorf("%w: provider request reservation identity was added, removed, or rewritten", ErrRollback)
	}
	if old.Disposition == HostCallTerminal && next.Disposition == HostCallTerminal {
		oldWithoutCapture, nextWithoutCapture := *old, *next
		oldWithoutCapture.CapturedCall, nextWithoutCapture.CapturedCall = nil, nil
		if reflect.DeepEqual(oldWithoutCapture, nextWithoutCapture) {
			return nil
		}
	}
	switch old.Disposition {
	case HostCallReserved, HostCallUnknown:
		if next.Disposition == HostCallTerminal || next.Disposition == HostCallRetired || next.Disposition == HostCallUnknown {
			return nil
		}
	}
	return fmt.Errorf("%w: provider request reservation disposition regressed or was rewritten", ErrRollback)
}

func validPinsMonotonic(old, next Pins) error { return validateObservationAdvance(old, next) }

func validateObservedDisposition(p Pins) error {
	if p.PendingCallDisposition == PendingCallInFlight || p.PendingCallDisposition == PendingCallCaptured ||
		p.PendingCallDisposition == PendingCallUnknown ||
		isUnsettledDisposition(p.ProviderDisposition) {
		return ErrPendingUnknown
	}
	if p.EffectReservation != nil && (p.EffectReservation.Disposition == EffectReservationReserved ||
		p.EffectReservation.Disposition == EffectReservationUnknown) {
		return ErrPendingUnknown
	}
	if p.HostCallReservation != nil && (p.HostCallReservation.Disposition == HostCallReserved ||
		p.HostCallReservation.Disposition == HostCallUnknown) {
		return ErrPendingUnknown
	}
	for i, transcript := range p.ProviderTranscripts {
		if transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return fmt.Errorf("%w: transcript %d lacks terminal lineage disposition", ErrPendingUnknown, i)
		}
	}
	return nil
}
