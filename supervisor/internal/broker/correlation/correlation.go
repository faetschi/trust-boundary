// Package correlation matches untrusted adapter proposals to call envelopes
// already captured by a trusted provider broker. It does not construct provider
// requests, parse provider payloads, authorize effects, or establish that a
// captured envelope came from a real provider exchange.
package correlation

import (
	"strings"
	"sync"
)

// Identifier retains the issuer namespace and opaque provider value verbatim.
// Opaque is deliberately not parsed, case-folded, or used as authority.
type Identifier struct {
	Issuer string `json:"issuer"`
	Opaque string `json:"opaque"`
}

// BrokerCall is the trusted broker's captured correlation evidence for one
// provider tool call. The digest is opaque to this package.
// The broker profile owns derivation, encoding, strict JSON decoding, canonicalization, and all input bounds.
type BrokerCall struct {
	ResponseID               Identifier `json:"response_id"`
	ToolCallID               Identifier `json:"tool_call_id"`
	ToolName                 string     `json:"tool_name"`
	CanonicalArgumentsDigest string     `json:"canonical_arguments_digest"`
	Sequence                 uint64     `json:"sequence"`
	Generation               string     `json:"generation"`
}

// Proposal is the adapter's untrusted claim that it is handling a captured
// provider call. All correlation fields must exactly match BrokerCall.
type Proposal struct {
	ResponseID               Identifier `json:"response_id"`
	ToolCallID               Identifier `json:"tool_call_id"`
	ToolName                 string     `json:"tool_name"`
	CanonicalArgumentsDigest string     `json:"canonical_arguments_digest"`
	Sequence                 uint64     `json:"sequence"`
	Generation               string     `json:"generation"`
}

// Phase identifies which input was processed by a Decision.
type Phase string

const (
	PhaseCapture  Phase = "capture"
	PhaseProposal Phase = "proposal"
)

// ReasonCode is stable evidence vocabulary for deterministic correlation
// outcomes. Denials close the in-memory stream.
type ReasonCode string

const (
	ReasonCallCaptured           ReasonCode = "call_captured"
	ReasonMatched                ReasonCode = "matched"
	ReasonSessionClosed          ReasonCode = "session_closed"
	ReasonMalformedCapture       ReasonCode = "malformed_capture"
	ReasonMalformedProposal      ReasonCode = "malformed_proposal"
	ReasonUnregisteredTool       ReasonCode = "unregistered_tool"
	ReasonDuplicateCallID        ReasonCode = "duplicate_call_id"
	ReasonNonIncreasingSequence  ReasonCode = "non_increasing_sequence"
	ReasonReplay                 ReasonCode = "replay"
	ReasonUnknownCall            ReasonCode = "unknown_call"
	ReasonCallIDMismatch         ReasonCode = "call_id_mismatch"
	ReasonOutOfOrder             ReasonCode = "out_of_order"
	ReasonResponseIDMismatch     ReasonCode = "response_id_mismatch"
	ReasonToolNameMismatch       ReasonCode = "tool_name_mismatch"
	ReasonArgumentDigestMismatch ReasonCode = "argument_digest_mismatch"
	ReasonSequenceMismatch       ReasonCode = "sequence_mismatch"
	ReasonGenerationMismatch     ReasonCode = "generation_mismatch"
)

// Decision preserves the raw input evidence used for a decision. Accepted
// proposal correlation is only a match receipt; it is not an effect lease or
// permission to execute a tool.
type Decision struct {
	Phase          Phase       `json:"phase"`
	Accepted       bool        `json:"accepted"`
	ReasonCode     ReasonCode  `json:"reason_code"`
	StreamClosed   bool        `json:"stream_closed"`
	CaptureOrdinal uint64      `json:"capture_ordinal,omitempty"`
	BrokerCapture  *BrokerCall `json:"broker_capture,omitempty"`
	Proposal       *Proposal   `json:"proposal,omitempty"`
}

type callIdentity struct {
	issuer string
	opaque string
}

type capturedCall struct {
	evidence BrokerCall
}

// Correlator serializes broker capture and proposal validation. Calls are
// consumed in increasing broker sequence order, once per tool-call identity.
type Correlator struct {
	mu              sync.Mutex
	registeredTools map[string]struct{}
	calls           []capturedCall
	byCallID        map[callIdentity]int
	consumed        map[callIdentity]struct{}
	next            int
	lastSequence    uint64
	closed          bool
}

var fixedTools = [...]string{"read", "write", "edit", "bash"}

// New creates a correlation stream with the fixed proxy surface from the
// current thesis MVP. The registry cannot be expanded at runtime.
func New() *Correlator {
	registered := make(map[string]struct{}, len(fixedTools))
	for _, name := range fixedTools {
		registered[name] = struct{}{}
	}
	return &Correlator{
		registeredTools: registered,
		byCallID:        make(map[callIdentity]int),
		consumed:        make(map[callIdentity]struct{}),
	}
}

// Capture admits one trusted broker envelope. Capture order and Sequence must
// increase strictly; identifiers, tool name, digest, and generation are retained
// exactly as received. Any invalid capture closes the stream.
func (c *Correlator) Capture(envelope BrokerCall) Decision {
	c.mu.Lock()
	defer c.mu.Unlock()

	evidence := copyCall(envelope)
	decision := Decision{Phase: PhaseCapture, BrokerCapture: &evidence}
	if c.closed {
		return c.deny(decision, ReasonSessionClosed)
	}
	if !validIdentifier(envelope.ResponseID) || !validIdentifier(envelope.ToolCallID) ||
		envelope.Sequence == 0 || strings.TrimSpace(envelope.Generation) == "" ||
		!hasDigestValue(envelope.CanonicalArgumentsDigest) {
		return c.deny(decision, ReasonMalformedCapture)
	}
	if _, ok := c.registeredTools[envelope.ToolName]; !ok {
		return c.deny(decision, ReasonUnregisteredTool)
	}
	id := callIdentity{issuer: envelope.ToolCallID.Issuer, opaque: envelope.ToolCallID.Opaque}
	if index, exists := c.byCallID[id]; exists {
		decision.CaptureOrdinal = uint64(index + 1)
		return c.deny(decision, ReasonDuplicateCallID)
	}
	if envelope.Sequence <= c.lastSequence {
		return c.deny(decision, ReasonNonIncreasingSequence)
	}

	ordinal := uint64(len(c.calls) + 1)
	c.byCallID[id] = len(c.calls)
	c.calls = append(c.calls, capturedCall{evidence: copyCall(envelope)})
	c.lastSequence = envelope.Sequence
	decision.Accepted = true
	decision.ReasonCode = ReasonCallCaptured
	decision.CaptureOrdinal = ordinal
	return decision
}

// Validate checks an adapter proposal against the earliest unconsumed broker
// call. A mismatch, unknown/replayed call, out-of-order attempt, or unregistered
// tool closes the stream. The mutex gives each attempt a single linearized
// decision; a call can be accepted at most once.
func (c *Correlator) Validate(proposal Proposal) Decision {
	c.mu.Lock()
	defer c.mu.Unlock()

	evidence := copyProposal(proposal)
	decision := Decision{Phase: PhaseProposal, Proposal: &evidence}
	// Attach the known broker record before rejecting so the decision preserves
	// both sides of a malformed or unregistered proposal attempt.
	if validIdentifier(proposal.ToolCallID) {
		id := callIdentity{issuer: proposal.ToolCallID.Issuer, opaque: proposal.ToolCallID.Opaque}
		if index, exists := c.byCallID[id]; exists {
			c.attachCapture(&decision, index)
		} else if index, exists := c.indexByOpaque(proposal.ToolCallID.Opaque); exists {
			c.attachCapture(&decision, index)
		}
	}
	if c.closed {
		return c.deny(decision, ReasonSessionClosed)
	}

	if _, ok := c.registeredTools[proposal.ToolName]; !ok {
		return c.deny(decision, ReasonUnregisteredTool)
	}
	if !validIdentifier(proposal.ResponseID) || !validIdentifier(proposal.ToolCallID) ||
		proposal.Sequence == 0 || strings.TrimSpace(proposal.Generation) == "" ||
		!hasDigestValue(proposal.CanonicalArgumentsDigest) {
		return c.deny(decision, ReasonMalformedProposal)
	}

	id := callIdentity{issuer: proposal.ToolCallID.Issuer, opaque: proposal.ToolCallID.Opaque}
	index, exists := c.byCallID[id]
	if !exists {
		if c.hasOpaqueCallID(proposal.ToolCallID.Opaque) {
			return c.deny(decision, ReasonCallIDMismatch)
		}
		return c.deny(decision, ReasonUnknownCall)
	}
	c.attachCapture(&decision, index)
	if _, wasConsumed := c.consumed[id]; wasConsumed {
		return c.deny(decision, ReasonReplay)
	}
	if index != c.next {
		return c.deny(decision, ReasonOutOfOrder)
	}

	expectedEvidence := c.calls[index].evidence
	switch {
	case !sameIdentifier(proposal.ResponseID, expectedEvidence.ResponseID):
		return c.deny(decision, ReasonResponseIDMismatch)
	case !sameIdentifier(proposal.ToolCallID, expectedEvidence.ToolCallID):
		return c.deny(decision, ReasonCallIDMismatch)
	case proposal.ToolName != expectedEvidence.ToolName:
		return c.deny(decision, ReasonToolNameMismatch)
	case proposal.CanonicalArgumentsDigest != expectedEvidence.CanonicalArgumentsDigest:
		return c.deny(decision, ReasonArgumentDigestMismatch)
	case proposal.Sequence != expectedEvidence.Sequence:
		return c.deny(decision, ReasonSequenceMismatch)
	case proposal.Generation != expectedEvidence.Generation:
		return c.deny(decision, ReasonGenerationMismatch)
	}

	c.consumed[id] = struct{}{}
	c.next++
	decision.Accepted = true
	decision.ReasonCode = ReasonMatched
	return decision
}

func (c *Correlator) deny(decision Decision, reason ReasonCode) Decision {
	c.closed = true
	decision.Accepted = false
	decision.ReasonCode = reason
	decision.StreamClosed = true
	return decision
}

func (c *Correlator) indexByOpaque(opaque string) (int, bool) {
	match := -1
	for index, call := range c.calls {
		if call.evidence.ToolCallID.Opaque != opaque {
			continue
		}
		if match >= 0 {
			return 0, false
		}
		match = index
	}
	if match < 0 {
		return 0, false
	}
	return match, true
}

func (c *Correlator) hasOpaqueCallID(opaque string) bool {
	for _, call := range c.calls {
		if call.evidence.ToolCallID.Opaque == opaque {
			return true
		}
	}
	return false
}
func (c *Correlator) attachCapture(decision *Decision, index int) {
	evidence := copyCall(c.calls[index].evidence)
	decision.BrokerCapture = &evidence
	decision.CaptureOrdinal = uint64(index + 1)
}

func validIdentifier(id Identifier) bool {
	return strings.TrimSpace(id.Issuer) != "" && strings.TrimSpace(id.Opaque) != ""
}

func sameIdentifier(left, right Identifier) bool {
	return left.Issuer == right.Issuer && left.Opaque == right.Opaque
}

func hasDigestValue(digest string) bool {
	// Digest algorithm and wire encoding belong to the future broker profile.
	// This core checks presence, then preserves and compares the value exactly.
	return strings.TrimSpace(digest) != ""
}

func copyCall(call BrokerCall) BrokerCall {
	return call
}

func copyProposal(proposal Proposal) Proposal {
	return proposal
}
