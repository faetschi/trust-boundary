//go:build linux

package sessionrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tbound/supervisor/internal/delta"
)

// The registered delta-admission scope rules. The default rule admits a candidate whose changed
// paths are all in the operation's declared exact-path output scope. promote-all/v1 admits every
// structurally valid candidate: it is the registered admission relaxation of a targeted
// configuration delta (admit-all/v1) and nothing else selects it.
const (
	AdmissionScopeRuleExactPath  = "exact-path-output-scope/v1"
	AdmissionScopeRulePromoteAll = "promote-all/v1"
)

// Stable reason codes of a denied candidate. They are computed from the retained record, never
// stored in it.
const (
	// DenyReasonProtectedPath: a changed path is, or lies under, a manifest-pinned protected path.
	DenyReasonProtectedPath = "scope_protected_path"
	// DenyReasonUncoveredPath: a changed path is outside the declared exact-path output scope.
	DenyReasonUncoveredPath = "scope_path_not_covered"
)

// AdmissionScope selects the admission rule of a Store. The zero value is the rule that existed
// before scopes: exact-path-output-scope/v1 without protected paths. Its admission policy digest is
// unchanged, so existing records and pins stay valid. Any other scope has its own digest.
//
// ProtectedPaths are canonical relative paths, strictly sorted. Under the exact-path rule a change
// to a protected path (the path itself, or anything below it) is denied even if the output scope
// declares it. They are not allowed with promote-all/v1.
type AdmissionScope struct {
	Rule           string   `json:"rule,omitempty"`
	ProtectedPaths []string `json:"protected_paths,omitempty"`
}

func (s AdmissionScope) rule() string {
	if s.Rule == "" {
		return AdmissionScopeRuleExactPath
	}
	return s.Rule
}

// IsZero reports whether s is the default scope. It also lets encoding/json omit a default scope
// (omitzero) from an evidence bundle.
func (s AdmissionScope) IsZero() bool {
	return s.rule() == AdmissionScopeRuleExactPath && len(s.ProtectedPaths) == 0
}

// Validate refuses an unknown rule, protected paths with promote-all, and protected paths that are
// not canonical relative paths in strictly increasing order.
func (s AdmissionScope) Validate() error {
	switch s.rule() {
	case AdmissionScopeRuleExactPath:
	case AdmissionScopeRulePromoteAll:
		if len(s.ProtectedPaths) != 0 {
			return errors.New("promote-all admission has no protected paths")
		}
	default:
		return fmt.Errorf("unknown admission scope rule %q", s.Rule)
	}
	if len(s.ProtectedPaths) == 0 {
		return nil
	}
	return ValidateOutputScopePaths(s.ProtectedPaths)
}

// Decide applies the scope to the changes of a candidate whose operation declared requestScope. It
// returns "allow" with no reason, or "deny" with the reason code.
func (s AdmissionScope) Decide(requestScope []string, changes []delta.ObjectChange) (outcome, reason string) {
	if s.rule() == AdmissionScopeRulePromoteAll {
		return "allow", ""
	}
	for _, change := range changes {
		if s.protects(change.Path) {
			return "deny", DenyReasonProtectedPath
		}
	}
	if !OutputScopeCoversChanges(requestScope, changes) {
		return "deny", DenyReasonUncoveredPath
	}
	return "allow", ""
}

func (s AdmissionScope) protects(path string) bool {
	for _, protected := range s.ProtectedPaths {
		if path == protected || strings.HasPrefix(path, protected+"/") {
			return true
		}
	}
	return false
}

// AdmissionPolicyDigest is the admission_policy_digest of a Store: it commits to the schema, both
// pinned policy digests, the scope rule and the protected paths.
func AdmissionPolicyDigest(policyDigest, metadataPolicyDigest string, scope AdmissionScope) (string, error) {
	return computeAdmissionPolicyDigest(policyDigest, metadataPolicyDigest, scope)
}

func computeAdmissionPolicyDigest(policyDigest, metadataPolicyDigest string, scope AdmissionScope) (string, error) {
	if !validSHA256(policyDigest) || !validSHA256(metadataPolicyDigest) || scope.Validate() != nil {
		return "", ErrInvalidAdmission
	}
	payload := struct {
		SchemaVersion        string   `json:"schema_version"`
		PolicyDigest         string   `json:"policy_digest"`
		MetadataPolicyDigest string   `json:"metadata_policy_digest"`
		ScopeRule            string   `json:"scope_rule"`
		ProtectedPaths       []string `json:"protected_paths,omitempty"`
	}{
		SchemaVersion:        AdmissionSchemaVersion,
		PolicyDigest:         policyDigest,
		MetadataPolicyDigest: metadataPolicyDigest,
		ScopeRule:            scope.rule(),
		ProtectedPaths:       scope.ProtectedPaths,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}
