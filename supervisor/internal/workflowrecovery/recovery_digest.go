// recovery_digest.go: Digest, projection and identity helpers shared by the ledger and validators.

package workflowrecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

func digestObservation(kind transitionKind, previousDigest string, previous, observed Pins) string {
	record := struct {
		Schema         string         `json:"schema"`
		Kind           transitionKind `json:"kind"`
		PreviousDigest string         `json:"previous_digest"`
		PreviousPins   Pins           `json:"previous_pins"`
		ObservedPins   Pins           `json:"observed_pins"`
	}{checkpointSchema, kind, previousDigest, clonePins(previous), clonePins(observed)}
	encoded, _ := json.Marshal(record)
	sum := sha256.Sum256(append([]byte("tbound-verified-observation/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func digestTerminalEffect(previousDigest string, previous, terminal Pins, counters Counters) string {
	record := struct {
		Schema         string          `json:"schema"`
		Kind           transitionKind  `json:"kind"`
		Reservation    reservationKind `json:"reservation_kind"`
		PreviousDigest string          `json:"previous_digest"`
		PreviousPins   Pins            `json:"previous_pins"`
		TerminalPins   Pins            `json:"terminal_pins"`
		Counters       Counters        `json:"counters"`
	}{checkpointSchema, transitionTerminalEffect, reservationEffect, previousDigest,
		clonePins(previous), clonePins(terminal), counters}
	encoded, _ := json.Marshal(record)
	sum := sha256.Sum256(append([]byte("tbound-terminal-effect-accounting/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func makeReservationProof(kind reservationKind, previousDigest string, previous, next Checkpoint) TransitionProof {
	proof := TransitionProof{
		kind: transitionReservation, reservation: kind, previousDigest: previousDigest,
		previousPins: clonePins(previous.Pins), targetPins: clonePins(next.Pins),
		previousCounts: previous.Counters, targetCounts: next.Counters,
	}
	proof.proofDigest = digestReservationProof(proof)
	return proof
}

func digestReservationProof(proof TransitionProof) string {
	record := struct {
		Schema           string          `json:"schema"`
		Kind             transitionKind  `json:"kind"`
		Reservation      reservationKind `json:"reservation_kind"`
		PreviousDigest   string          `json:"previous_digest"`
		PreviousPins     Pins            `json:"previous_pins"`
		TargetPins       Pins            `json:"target_pins"`
		PreviousCounters Counters        `json:"previous_counters"`
		TargetCounters   Counters        `json:"target_counters"`
	}{checkpointSchema, proof.kind, proof.reservation, proof.previousDigest,
		clonePins(proof.previousPins), clonePins(proof.targetPins), proof.previousCounts, proof.targetCounts}
	encoded, _ := json.Marshal(record)
	sum := sha256.Sum256(append([]byte("tbound-workflow-reservation-proof/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func digestTranscriptPins(transcripts []TranscriptPin) string {
	encoded, _ := json.Marshal(transcripts)
	sum := sha256.Sum256(append([]byte("tbound-provider-transcript-pins/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func computeCheckpointDigest(cp Checkpoint) string {
	cp.ComputedCheckpointDigest = ""
	encoded, _ := json.Marshal(cp)
	sum := sha256.Sum256(append([]byte("tbound-workflow-checkpoint/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func validDisposition(disposition string) bool {
	switch disposition {
	case DispositionNone, DispositionAwaitingHostPrompt, DispositionAwaitingProviderExchange,
		DispositionAwaitingGateDecision, DispositionPendingProviderResult,
		DispositionTerminal, DispositionRetired, DispositionUnknown:
		return true
	default:
		return false
	}
}

func validPendingCallDisposition(disposition string) bool {
	switch disposition {
	case PendingCallNone, PendingCallInFlight, PendingCallCaptured, PendingCallTerminal, PendingCallRetired, PendingCallUnknown:
		return true
	default:
		return false
	}
}

func isUnsettledDisposition(disposition string) bool {
	switch disposition {
	case DispositionAwaitingProviderExchange, DispositionAwaitingGateDecision,
		DispositionPendingProviderResult, DispositionUnknown:
		return true
	default:
		return false
	}
}

func totalProjectionCount(transcripts []TranscriptPin) uint64 {
	var count uint64
	for _, transcript := range transcripts {
		if ^uint64(0)-count < transcript.ProjectionCount {
			return ^uint64(0)
		}
		count += transcript.ProjectionCount
	}
	return count
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validEffectBinding(binding EffectBinding) bool {
	if !validID(binding.OperationID) || !validDigest(binding.SpecDigest) {
		return false
	}
	if !hasCapturedEffectBinding(binding) {
		return true
	}
	return validID(binding.WorkflowID) && validID(binding.ConversationID) && validID(binding.HostCallID) &&
		validID(binding.ToolCallID) && validDigest(binding.HostCallOutcomeDigest) &&
		binding.RecoveryGeneration > 0 && binding.OwnerEpoch > 0
}

func validHostCallBinding(binding HostCallBinding) bool {
	return validID(binding.CallID) && validDigest(binding.RequestDigest) && binding.TokenUpperBound > 0 &&
		binding.TokenUpperBound <= maxSQLiteInteger
}

func validID(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func clonePins(p Pins) Pins {
	p.ProviderTranscripts = append([]TranscriptPin(nil), p.ProviderTranscripts...)
	if p.EffectReservation != nil {
		copy := *p.EffectReservation
		p.EffectReservation = &copy
	}
	if p.HostCallReservation != nil {
		copy := *p.HostCallReservation
		if copy.CapturedCall != nil {
			capturedCopy := *copy.CapturedCall
			copy.CapturedCall = &capturedCopy
		}
		p.HostCallReservation = &copy
	}
	return p
}

// TranscriptPinsDigest lets trusted callers construct the aggregate list
// commitment without exposing transcript contents.
func TranscriptPinsDigest(transcripts []TranscriptPin) string {
	return digestTranscriptPins(transcripts)
}

// CheckpointDigest returns the digest that the trusted external root must
// anchor for this checkpoint. It is a commitment only; it does not authenticate
// the checkpoint by itself.
func CheckpointDigest(cp Checkpoint) string {
	return computeCheckpointDigest(cp)
}

// ComputeRecoveryLineageDigest binds the host-generated restart envelope to
// the exact predecessor checkpoint and immutable ordered continuation pins.
// The caller may compute it before assigning Pins.RecoveryLineageDigest; that
// field is cleared while hashing to avoid a self-reference.
func ComputeRecoveryLineageDigest(previousCheckpointDigest string, previous, next Pins) string {
	copy := clonePins(next)
	copy.RecoveryLineageDigest = ""
	envelope := struct {
		Schema                   string `json:"schema"`
		PreviousCheckpointDigest string `json:"previous_checkpoint_digest"`
		PreviousRecoveryLineage  string `json:"previous_recovery_lineage_digest"`
		PreviousSessionID        string `json:"previous_session_id"`
		PreviousGeneration       uint64 `json:"previous_generation"`
		PreviousOwnerEpoch       uint64 `json:"previous_owner_epoch"`
		NextPins                 Pins   `json:"next_pins"`
	}{checkpointSchema, previousCheckpointDigest, previous.RecoveryLineageDigest,
		previous.SessionID, previous.RecoveryGeneration, previous.OwnerEpoch, copy}
	encoded, _ := json.Marshal(envelope)
	sum := sha256.Sum256(append([]byte("tbound-recovery-lineage/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}
