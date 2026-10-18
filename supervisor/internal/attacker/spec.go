package attacker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
)

// SpecSchema names the pinned attacker artifact: the pre-registered pivot library, the seed, the
// cumulative caps and the feedback policy of the claim-bearing adaptive attacker (D32). A corpus
// manifest pins it by the SHA-256 of the artifact bytes (Pins.AttackerDigest).
const SpecSchema = "tbound-attacker/v1"

// MaxSpecBytes bounds an attacker artifact.
const MaxSpecBytes = 64 << 10

// Spec is the parsed attacker artifact. It carries data only: Machine validates it and builds the
// deterministic transition function.
type Spec struct {
	Schema   string       `json:"schema"`
	Seed     string       `json:"seed"`
	Library  SpecLibrary  `json:"library"`
	Caps     SpecCaps     `json:"caps"`
	Feedback SpecFeedback `json:"feedback_policy"`
}

// SpecLibrary is the JSON form of Library.
type SpecLibrary struct {
	Classes   []SpecClass    `json:"classes"`
	Fallbacks []SpecFallback `json:"fallbacks,omitempty"`
}

// SpecClass is the JSON form of Class.
type SpecClass struct {
	ID    string   `json:"id"`
	Steps []string `json:"steps"`
}

// SpecFallback is the JSON form of Fallback.
type SpecFallback struct {
	From     string   `json:"from"`
	OnReason string   `json:"on_reason,omitempty"`
	To       []string `json:"to"`
}

// SpecCaps is the JSON form of Caps.
type SpecCaps struct {
	Iterations int64 `json:"iterations"`
	ToolCalls  int64 `json:"tool_calls"`
	WallMS     int64 `json:"wall_ms"`
	InTokens   int64 `json:"in_tokens"`
	OutTokens  int64 `json:"out_tokens"`
	MicroUSD   int64 `json:"micro_usd"`
}

// SpecFeedback is the JSON form of FeedbackPolicy.
type SpecFeedback struct {
	ReasonCodes        []string `json:"reason_codes"`
	CapabilityTexts    []string `json:"capability_texts"`
	ManifestedReason   string   `json:"manifested_reason"`
	MaxCapabilityBytes int      `json:"max_capability_bytes"`
}

// ParseSpec reads exactly one attacker artifact: no unknown member, no trailing data, the registered
// schema. It does not validate the library, caps or policy; Spec.Validate does.
func ParseSpec(raw []byte) (Spec, error) {
	if len(raw) == 0 || len(raw) > MaxSpecBytes {
		return Spec{}, fmt.Errorf("attacker: artifact is empty or exceeds %d bytes", MaxSpecBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var spec Spec
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("attacker: artifact: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Spec{}, errors.New("attacker: artifact has trailing data")
	}
	if spec.Schema != SpecSchema {
		return Spec{}, fmt.Errorf("attacker: artifact schema is %q; want %q", spec.Schema, SpecSchema)
	}
	return spec, nil
}

// LibraryValue returns the pivot library of the artifact.
func (s Spec) LibraryValue() Library {
	lib := Library{}
	for _, c := range s.Library.Classes {
		lib.Classes = append(lib.Classes, Class{ID: c.ID, Steps: slices.Clone(c.Steps)})
	}
	for _, f := range s.Library.Fallbacks {
		lib.Fallbacks = append(lib.Fallbacks, Fallback{From: f.From, OnReason: f.OnReason, To: slices.Clone(f.To)})
	}
	return lib
}

// CapsValue returns the cumulative caps of the artifact.
func (s Spec) CapsValue() Caps {
	return Caps{Iterations: s.Caps.Iterations, ToolCalls: s.Caps.ToolCalls, WallMS: s.Caps.WallMS,
		InTokens: s.Caps.InTokens, OutTokens: s.Caps.OutTokens, MicroUSD: s.Caps.MicroUSD}
}

// Policy returns the feedback policy of the artifact.
func (s Spec) Policy() FeedbackPolicy {
	return FeedbackPolicy{ReasonCodes: slices.Clone(s.Feedback.ReasonCodes), CapabilityTexts: slices.Clone(s.Feedback.CapabilityTexts),
		ManifestedReason: s.Feedback.ManifestedReason, MaxCapabilityBytes: s.Feedback.MaxCapabilityBytes}
}

// Validate checks that the artifact builds a Machine and that its feedback policy covers the whole
// code-defined public projection (PublicDenialReasons): every denial the enforcement surface can show
// must be valid feedback, so that a denial never ends the attacker as invalid.
func (s Spec) Validate() error {
	if _, err := s.Machine(); err != nil {
		return err
	}
	policy := s.Policy()
	for _, reason := range PublicDenialReasons() {
		text, _ := PublicCapabilityText(reason)
		if !slices.Contains(policy.ReasonCodes, reason) || !slices.Contains(policy.CapabilityTexts, text) {
			return fmt.Errorf("attacker: feedback policy lacks the public projection of reason %q", reason)
		}
		if len(text) > policy.MaxCapabilityBytes {
			return fmt.Errorf("attacker: max_capability_bytes %d is below the projection text of reason %q", policy.MaxCapabilityBytes, reason)
		}
	}
	if slices.Contains(PublicDenialReasons(), policy.ManifestedReason) {
		return errors.New("attacker: the manifested reason is a public denial reason")
	}
	return nil
}

// StepIDs returns the sorted step IDs of the library.
func (s Spec) StepIDs() []string {
	var ids []string
	for _, c := range s.Library.Classes {
		ids = append(ids, c.Steps...)
	}
	sort.Strings(ids)
	return ids
}

// Machine builds the deterministic transition function from the artifact.
func (s Spec) Machine() (Machine, error) {
	return NewMachine(s.LibraryValue(), []byte(s.Seed), s.CapsValue(), s.Policy())
}

// publicProjection is the code-defined mapping from a stable public denial or failure reason code to its generic
// capability description (D26). It is part of the signed binary, not of the manifest: the description of
// a denial never depends on the case, the command or any tool output. The texts say what class of action
// is not available and never why, where, or by which rule.
var publicProjection = map[string]string{
	"policy_rule_deny":         "the policy does not allow this action",
	"policy_default_deny":      "the policy does not allow this action",
	"invalid_policy":           "the policy does not allow this action",
	"unknown_tool":             "that tool is not available",
	"invalid_arguments":        "the tool arguments are not valid",
	"argument_digest_mismatch": "the tool arguments do not match the registered call",
	"correlation_required":     "the call is not a registered provider call",
	"scope_protected_path":     "the change touches a protected path",
	"scope_path_not_covered":   "the change is outside the permitted output scope",
	ReasonCommandFailed:        "the command did not complete successfully",
}

// ReasonCommandFailed is the public reason of an ALLOWed bash command that settled and failed (nonzero or
// unobserved exit). The gate allowed it, so it is not a denial, but it is not an executed pivot either: the
// adaptive stop is the first non-denied, non-failed pivot (D32), so the machine is shown this reason and
// pivots on. The reason says that the command failed and never why, where or what it printed.
const ReasonCommandFailed = "command_failed"

// PublicCapabilityText returns the generic capability description of a public denial reason code.
func PublicCapabilityText(reasonCode string) (string, bool) {
	text, ok := publicProjection[reasonCode]
	return text, ok
}

// PublicDenialReasons lists the reason codes of the public projection (the denials and ReasonCommandFailed), sorted.
func PublicDenialReasons() []string {
	reasons := make([]string, 0, len(publicProjection))
	for reason := range publicProjection {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return reasons
}
