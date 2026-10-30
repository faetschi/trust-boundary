// Package gate implements the versioned, deterministic policy decision layer.
// Correlation receipts are mandatory and are checked before policy rules.
package gate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
)

const (
	ProfileVersion = "tbound-policy/v1"
	MaxPolicyBytes = 64 << 10
)

type Verdict string

const (
	Allow Verdict = "ALLOW"
	Deny  Verdict = "DENY"
)

type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

var ErrInvalidPolicy = errors.New("invalid or unsupported policy profile")

// Profile is the complete versioned input to policy compilation. Rules not
// listed have no authority and are denied by default.
type Profile struct {
	Version string `json:"version"`
	Rules   []Rule `json:"rules"`
}

type Rule struct {
	Tool   string `json:"tool"`
	Effect Effect `json:"effect"`
}

// Policy is an immutable compiled snapshot. Its rule map and canonical profile
// are private so callers cannot mutate a policy after its digest is computed.
type Policy struct {
	version   string
	rules     map[string]Effect
	canonical Profile
	digest    string
}

// Decision is a deterministic result for one proposal and compiled policy.
// The policy digest is present in every decision made from a valid snapshot.
type Decision struct {
	Verdict                  Verdict                 `json:"verdict"`
	ReasonCode               string                  `json:"reason_code"`
	PolicyDigest             string                  `json:"policy_digest"`
	CanonicalArgumentsDigest string                  `json:"canonical_arguments_digest,omitempty"`
	ResponseID               *correlation.Identifier `json:"response_id,omitempty"`
	ToolCallID               string                  `json:"tool_call_id"`
	Tool                     string                  `json:"tool"`
	Sequence                 uint64                  `json:"sequence"`
}

// NewPolicy validates a profile, sorts its rules, and computes the digest over
// the canonical typed representation. Ambiguous duplicate rules are rejected.
func NewPolicy(profile Profile) (Policy, error) {
	if profile.Version != ProfileVersion || len(profile.Rules) > 4 {
		return Policy{}, fmt.Errorf("%w: unsupported version or too many rules", ErrInvalidPolicy)
	}
	rules := make(map[string]Effect, len(profile.Rules))
	canonicalRules := append([]Rule{}, profile.Rules...)
	for _, rule := range canonicalRules {
		if !registeredTool(rule.Tool) || (rule.Effect != EffectAllow && rule.Effect != EffectDeny) {
			return Policy{}, fmt.Errorf("%w: unsupported tool or effect", ErrInvalidPolicy)
		}
		if _, exists := rules[rule.Tool]; exists {
			return Policy{}, fmt.Errorf("%w: duplicate rule for %q", ErrInvalidPolicy, rule.Tool)
		}
		rules[rule.Tool] = rule.Effect
	}
	sort.Slice(canonicalRules, func(i, j int) bool {
		return canonicalRules[i].Tool < canonicalRules[j].Tool
	})
	canonical := Profile{Version: profile.Version, Rules: canonicalRules}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return Policy{}, fmt.Errorf("%w: encode profile: %v", ErrInvalidPolicy, err)
	}
	digest := sha256.Sum256(encoded)
	return Policy{
		version: profile.Version, rules: rules, canonical: canonical,
		digest: profile.Version + ":sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

// NewPolicyFromJSON parses a bounded strict profile and rejects unknown or
// duplicate JSON fields before compiling it.
func NewPolicyFromJSON(raw []byte) (Policy, error) {
	if len(raw) == 0 || len(raw) > MaxPolicyBytes {
		return Policy{}, fmt.Errorf("%w: profile size out of bounds", ErrInvalidPolicy)
	}
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return Policy{}, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 2 || fields["version"] == nil || fields["rules"] == nil {
		return Policy{}, fmt.Errorf("%w: profile requires exactly version and rules", ErrInvalidPolicy)
	}
	if rules := bytes.TrimSpace(fields["rules"]); len(rules) == 0 || rules[0] != '[' {
		return Policy{}, fmt.Errorf("%w: rules must be an array", ErrInvalidPolicy)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Policy{}, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Policy{}, fmt.Errorf("%w: trailing JSON value", ErrInvalidPolicy)
	}
	return NewPolicy(profile)
}

func (p Policy) Digest() string { return p.digest }

// Profile returns a defensive copy of the compiled canonical profile.
func (p Policy) Profile() Profile {
	return Profile{Version: p.canonical.Version, Rules: append([]Rule{}, p.canonical.Rules...)}
}

// Evaluate requires a successful, exact broker-correlation receipt before it
// canonicalizes arguments or consults a policy rule. It never authorizes from
// IPC identity alone.
func Evaluate(proposal protocol.Proposal, matched correlation.Decision, policy Policy) Decision {
	decision := Decision{
		Verdict: Deny, ReasonCode: "correlation_required", PolicyDigest: policy.Digest(),
		ToolCallID: proposal.ToolCallID, Tool: proposal.Tool,
	}
	if !validCorrelation(proposal, matched) {
		return decision
	}
	decision.ReasonCode = "invalid_policy"
	if policy.digest == "" || policy.version != ProfileVersion || policy.rules == nil {
		return decision
	}
	if proposal.Tool != "read" && proposal.Tool != "write" && proposal.Tool != "edit" && proposal.Tool != "bash" {
		decision.ReasonCode = "unknown_tool"
		return decision
	}
	digest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
	if err != nil {
		decision.ReasonCode = "invalid_arguments"
		return decision
	}
	decision.CanonicalArgumentsDigest = digest
	decision.ResponseID = identifierCopy(matched.Proposal.ResponseID)
	decision.Sequence = matched.Proposal.Sequence
	if digest != matched.Proposal.CanonicalArgumentsDigest {
		decision.ReasonCode = "argument_digest_mismatch"
		return decision
	}
	effect, exists := policy.rules[proposal.Tool]
	if !exists {
		decision.ReasonCode = "policy_default_deny"
		return decision
	}
	if effect == EffectDeny {
		decision.ReasonCode = "policy_rule_deny"
		return decision
	}
	decision.Verdict = Allow
	decision.ReasonCode = "policy_rule_allow"
	return decision
}

func validCorrelation(proposal protocol.Proposal, matched correlation.Decision) bool {
	if !matched.Accepted || matched.StreamClosed || matched.Phase != correlation.PhaseProposal ||
		matched.ReasonCode != correlation.ReasonMatched || matched.Proposal == nil || matched.BrokerCapture == nil ||
		proposal.SchemaVersion != protocol.ProposalSchemaVersion || strings.TrimSpace(proposal.ToolCallID) == "" ||
		strings.TrimSpace(proposal.Tool) != proposal.Tool {
		return false
	}
	correlated := matched.Proposal
	capture := matched.BrokerCapture
	if correlated.ToolCallID.Opaque != proposal.ToolCallID || correlated.ToolName != proposal.Tool ||
		correlated.Sequence == 0 || correlated.Generation == "" ||
		!validIdentifier(correlated.ResponseID) || !validIdentifier(correlated.ToolCallID) ||
		capture.ResponseID != correlated.ResponseID || capture.ToolCallID != correlated.ToolCallID ||
		capture.ToolName != correlated.ToolName || capture.CanonicalArgumentsDigest != correlated.CanonicalArgumentsDigest ||
		capture.Sequence != correlated.Sequence || capture.Generation != correlated.Generation {
		return false
	}
	return true
}

func identifierCopy(id correlation.Identifier) *correlation.Identifier {
	copy := id
	return &copy
}

func validIdentifier(id correlation.Identifier) bool {
	return id.Issuer != "" && id.Opaque != "" &&
		strings.TrimSpace(id.Issuer) == id.Issuer && strings.TrimSpace(id.Opaque) == id.Opaque &&
		len(id.Issuer) <= 256 && len(id.Opaque) <= 256
}

func registeredTool(tool string) bool {
	switch tool {
	case "read", "write", "edit", "bash":
		return true
	default:
		return false
	}
}
