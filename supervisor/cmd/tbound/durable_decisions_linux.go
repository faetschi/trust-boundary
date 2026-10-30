//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

// durableDecisionEventKind is a benign, non-effect audit event kind. It is safe
// to append to a journal dedicated to a session repository: sessionrepo only
// reserves the effect_intent/effect_outcome kinds and reconciles
// operation_denied records, so gate_decision frames neither advance nor
// quarantine the repository.
const durableDecisionEventKind = "gate_decision"

// AuditDecisionRecorder durably records every gate decision (ALLOW and DENY)
// before Serve reaches the executor. It appends one benign event per decision
// and returns an error unless the frame is written and synced.
type AuditDecisionRecorder struct {
	journal *audit.Journal
}

// NewAuditDecisionRecorder requires a dedicated, already-open journal. The
// caller retains ownership and closes it separately.
func NewAuditDecisionRecorder(journal *audit.Journal) (*AuditDecisionRecorder, error) {
	if journal == nil {
		return nil, errors.New("audit decision recorder requires a journal")
	}
	return &AuditDecisionRecorder{journal: journal}, nil
}

// RecordDecision appends one durable gate_decision event. A nil receiver or a
// journal failure is fail-closed: Serve withholds the result.
func (r *AuditDecisionRecorder) RecordDecision(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) error {
	if r == nil || r.journal == nil {
		return errors.New("audit decision recorder is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	eventID, err := durableDecisionEventID(proposal, decision)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(durableDecisionEvent{
		Proposal: proposal,
		Decision: decision,
	})
	if err != nil {
		return fmt.Errorf("encode gate decision event: %w", err)
	}
	if _, err := r.journal.Append(audit.Event{Kind: durableDecisionEventKind, ID: eventID, Data: payload}); err != nil {
		return fmt.Errorf("append gate decision event: %w", err)
	}
	return nil
}

// durableDecisionEvent is the faithful durable copy of one proposal and its
// gate decision. It asserts no effect, containment, or provider fact.
type durableDecisionEvent struct {
	Proposal protocol.Proposal `json:"proposal"`
	Decision gate.Decision     `json:"decision"`
}

// durableDecisionEventID derives a deterministic, domain-separated event ID
// from the proposal and decision. It tolerates a denied decision with no
// trusted response identity.
func durableDecisionEventID(proposal protocol.Proposal, decision gate.Decision) (string, error) {
	if !validIdentityText(decision.ToolCallID) && !validIdentityText(proposal.ToolCallID) {
		return "", errors.New("gate decision event has no call identity")
	}
	digest := sha256.New()
	writeHashField(digest, "gate_decision")
	writeHashField(digest, proposal.SchemaVersion)
	writeHashField(digest, proposal.ToolCallID)
	writeHashField(digest, proposal.Tool)
	writeHashField(digest, string(proposal.Arguments))
	writeHashField(digest, string(decision.Verdict))
	writeHashField(digest, decision.ReasonCode)
	writeHashField(digest, decision.PolicyDigest)
	writeHashField(digest, decision.CanonicalArgumentsDigest)
	writeHashField(digest, decision.ToolCallID)
	writeHashField(digest, decision.Tool)
	writeHashField(digest, strconv.FormatUint(decision.Sequence, 10))
	if decision.ResponseID != nil {
		writeHashField(digest, decision.ResponseID.Issuer)
		writeHashField(digest, decision.ResponseID.Opaque)
	}
	return "gate-decision-" + hex.EncodeToString(digest.Sum(nil)), nil
}
