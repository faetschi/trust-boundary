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
// configuration delta (admit-all/v1) and nothing else selects it. product-scope/v1 is the product
// rule: the Store's own Writable, ReadOnly and Protected lists decide, never the operation's
// declared output paths.
const (
	AdmissionScopeRuleExactPath  = "exact-path-output-scope/v1"
	AdmissionScopeRulePromoteAll = "promote-all/v1"
	AdmissionScopeRuleProduct    = "product-scope/v1"
)

// Stable reason codes of a denied candidate. They are computed from the retained record, never
// stored in it.
const (
	// DenyReasonProtectedPath: a changed path is, or lies under, a protected path (or, under product-scope/v1, a read-only one).
	DenyReasonProtectedPath = "scope_protected_path"
	// DenyReasonUncoveredPath: a changed path is outside the declared exact-path output scope, or outside every writable entry of a product scope.
	DenyReasonUncoveredPath = "scope_path_not_covered"
)

// AdmissionScope selects the admission rule of a Store. The zero value is the rule that existed
// before scopes: exact-path-output-scope/v1 without protected paths. Its admission policy digest is
// unchanged, so existing records and pins stay valid. Any other scope has its own digest.
//
// ProtectedPaths are canonical relative paths, strictly sorted. Under the exact-path rule a change
// to a protected path (the path itself, or anything below it) is denied even if the output scope
// declares it. They are not allowed with promote-all/v1 or product-scope/v1.
//
// Writable, ReadOnly and Protected belong to product-scope/v1 only, as does NoDelete. Each is a list of canonical
// relative paths, strictly sorted, at most 128, and no path is in two lists. Matching is by whole
// path components. A change is admitted only at or below a Writable entry, and a change at or
// below a Protected or ReadOnly entry is denied first, whatever the nesting.
//
// NoDelete is an overlay on Writable, not a fourth partition: an entry may equal or lie inside a
// Writable entry and is exempt from the no-two-lists rule. A change that removes an object at or
// below a NoDelete entry is denied with DenyReasonProtectedPath. Creates, modifications and
// metadata changes stay allowed. A rename out of the tree is a removal. An overwrite or truncation
// keeps the path existing, so NoDelete does not see it.
type AdmissionScope struct {
	Rule           string   `json:"rule,omitempty"`
	ProtectedPaths []string `json:"protected_paths,omitempty"`
	Writable       []string `json:"writable,omitempty"`
	ReadOnly       []string `json:"read_only,omitempty"`
	Protected      []string `json:"protected,omitempty"`
	NoDelete       []string `json:"no_delete,omitempty"`
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
	return s.rule() == AdmissionScopeRuleExactPath && len(s.ProtectedPaths)+len(s.Writable)+len(s.ReadOnly)+len(s.Protected)+len(s.NoDelete) == 0
}

// cloneAdmissionScope copies all four path lists, so a bundle or its clone cannot change the
// Store's admission policy. Nil and empty lists stay distinct so the copy DeepEquals the original.
func cloneAdmissionScope(scope AdmissionScope) AdmissionScope {
	scope.ProtectedPaths = cloneSliceKeepingNil(scope.ProtectedPaths)
	scope.Writable = cloneSliceKeepingNil(scope.Writable)
	scope.ReadOnly = cloneSliceKeepingNil(scope.ReadOnly)
	scope.Protected = cloneSliceKeepingNil(scope.Protected)
	scope.NoDelete = cloneSliceKeepingNil(scope.NoDelete)
	return scope
}

// Validate refuses an unknown rule, protected paths with promote-all, and protected paths that are
// not canonical relative paths in strictly increasing order. Under product-scope/v1 it also refuses
// ProtectedPaths and any path that appears in two of the three lists.
func (s AdmissionScope) Validate() error {
	switch s.rule() {
	case AdmissionScopeRuleExactPath:
	case AdmissionScopeRulePromoteAll:
		if len(s.ProtectedPaths) != 0 {
			return errors.New("promote-all admission has no protected paths")
		}
	case AdmissionScopeRuleProduct:
		return s.validateProduct()
	default:
		return fmt.Errorf("unknown admission scope rule %q", s.Rule)
	}
	if len(s.Writable)+len(s.ReadOnly)+len(s.Protected)+len(s.NoDelete) != 0 {
		return errors.New("writable, read_only, protected and no_delete belong to product-scope/v1")
	}
	if len(s.ProtectedPaths) == 0 {
		return nil
	}
	return ValidateOutputScopePaths(s.ProtectedPaths)
}

func (s AdmissionScope) validateProduct() error {
	if len(s.ProtectedPaths) != 0 {
		return errors.New("product-scope admission lists its protected paths in protected")
	}
	listed := map[string]bool{}
	for _, list := range [][]string{s.Writable, s.ReadOnly, s.Protected} {
		if len(list) != 0 {
			if err := ValidateOutputScopePaths(list); err != nil {
				return err
			}
		}
		for _, path := range list {
			if listed[path] {
				return fmt.Errorf("product scope lists %q twice", path)
			}
			listed[path] = true
		}
	}
	if len(s.NoDelete) != 0 {
		return ValidateOutputScopePaths(s.NoDelete)
	}
	return nil
}

// Decide applies the scope to the changes of a candidate whose operation declared requestScope. It
// returns "allow" with no reason, or "deny" with the reason code.
func (s AdmissionScope) Decide(requestScope []string, changes []delta.ObjectChange) (outcome, reason string) {
	switch s.rule() {
	case AdmissionScopeRulePromoteAll:
		return "allow", ""
	case AdmissionScopeRuleProduct:
		// A change list holds every path whose state differs, so a rename is two entries (the old
		// path removed, the new one created) and each is checked like any other change.
		for _, change := range changes {
			if pathAtOrBelow(s.Protected, change.Path) || pathAtOrBelow(s.ReadOnly, change.Path) {
				return "deny", DenyReasonProtectedPath
			}
		}
		for _, change := range changes {
			if !pathAtOrBelow(s.Writable, change.Path) {
				return "deny", DenyReasonUncoveredPath
			}
		}
		for _, change := range changes {
			if change.Before.Exists && !change.After.Exists && pathAtOrBelow(s.NoDelete, change.Path) {
				return "deny", DenyReasonProtectedPath
			}
		}
		return "allow", ""
	}
	for _, change := range changes {
		if pathAtOrBelow(s.ProtectedPaths, change.Path) {
			return "deny", DenyReasonProtectedPath
		}
	}
	if !OutputScopeCoversChanges(requestScope, changes) {
		return "deny", DenyReasonUncoveredPath
	}
	return "allow", ""
}

// pathAtOrBelow reports whether path is one of entries or lies below one, by whole components.
func pathAtOrBelow(entries []string, path string) bool {
	for _, entry := range entries {
		if path == entry || strings.HasPrefix(path, entry+"/") {
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
		Writable             []string `json:"writable,omitempty"`
		ReadOnly             []string `json:"read_only,omitempty"`
		Protected            []string `json:"protected,omitempty"`
		NoDelete             []string `json:"no_delete,omitempty"`
	}{
		SchemaVersion:        AdmissionSchemaVersion,
		PolicyDigest:         policyDigest,
		MetadataPolicyDigest: metadataPolicyDigest,
		ScopeRule:            scope.rule(),
		ProtectedPaths:       scope.ProtectedPaths,
		Writable:             scope.Writable,
		ReadOnly:             scope.ReadOnly,
		Protected:            scope.Protected,
		NoDelete:             scope.NoDelete,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}
