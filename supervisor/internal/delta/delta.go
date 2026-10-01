// Package delta validates and composes an ordered chain of generation transitions.
//
// This is a pure validation slice. It does not authenticate manifests or decision
// verifiers, discover filesystem changes, seal generations, persist decisions,
// filter paths, select metadata policy, or apply changes to a live tree.
package delta

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
)

const (
	TreeDigestProfile = "tbound-tree-jcs-rfc8785/v1:sha256:"
	manifestSchema    = "tbound-tree-manifest/v1"

	MaxTransitions          = 2048
	MaxObjectsPerManifest   = 20000
	MaxChangesPerTransition = 10000
	MaxTotalChanges         = 25000
	MaxPathBytes            = 1024
	MaxIdentityBytes        = 512
	MaxInputBytes           = 96 << 20
	MaxAggregateTreeWork    = 2 << 30
)

var ErrInvalidChain = errors.New("invalid approved-delta chain")

type ObjectType string

const (
	ObjectRegular     ObjectType = "regular"
	ObjectDirectory   ObjectType = "directory"
	ObjectSymlink     ObjectType = "symlink"
	ObjectFIFO        ObjectType = "fifo"
	ObjectSocket      ObjectType = "socket"
	ObjectCharDevice  ObjectType = "char_device"
	ObjectBlockDevice ObjectType = "block_device"
)

// ObjectState binds logical type, content, and policy-selected metadata identity.
// ContentDigest hashes regular-file bytes or the raw symlink target. Metadata
// identity must hash the caller's selected metadata representation.
type ObjectState struct {
	Exists           bool       `json:"exists"`
	Type             ObjectType `json:"type,omitempty"`
	ContentDigest    string     `json:"content_digest,omitempty"`
	MetadataIdentity string     `json:"metadata_identity,omitempty"`
}

type ManifestObject struct {
	Path  string      `json:"path"`
	State ObjectState `json:"state"`
}

// TreeManifest is a caller-supplied complete logical tree. Objects must be
// strictly sorted by Path in ascending UTF-8 byte order. Generation is a label,
// not proof of origin or immutability.
type TreeManifest struct {
	Generation           string           `json:"generation"`
	MetadataPolicyDigest string           `json:"metadata_policy_digest"`
	Objects              []ManifestObject `json:"objects"`
}

// ChainSpec explicitly pins both complete endpoint manifests and policy
// commitments. The caller remains responsible for authenticating those inputs.
type ChainSpec struct {
	Baseline                   TreeManifest
	Sealed                     TreeManifest
	ExpectedBaselineTreeDigest string
	ExpectedSealedTreeDigest   string
	PolicyDigest               string
	MetadataPolicyDigest       string
}

type OperationKind string

const (
	OperationProposalCall OperationKind = "proposal_call"
	OperationLease        OperationKind = "lease"
)

// OperationIdentity binds a transition to either a proposal and captured call,
// or to a command lease. Exactly one form must be populated.
type OperationIdentity struct {
	Kind       OperationKind `json:"kind"`
	ProposalID string        `json:"proposal_id,omitempty"`
	CallIssuer string        `json:"call_issuer,omitempty"`
	CallID     string        `json:"call_id,omitempty"`
	LeaseID    string        `json:"lease_id,omitempty"`
}

type ObjectChange struct {
	Path   string      `json:"path"`
	Before ObjectState `json:"before"`
	After  ObjectState `json:"after"`
}

type PolicyOutcome string

const (
	PolicyAllow PolicyOutcome = "allow"
	PolicyDeny  PolicyOutcome = "deny"
)

// PolicyDecision identifies a caller's policy receipt. These fields alone are
// not authorization: a required DecisionVerifier must authenticate the receipt
// and confirm that it covers the exact transition binding.
type PolicyDecision struct {
	ID                   string        `json:"id"`
	Outcome              PolicyOutcome `json:"outcome"`
	PolicyDigest         string        `json:"policy_digest"`
	MetadataPolicyDigest string        `json:"metadata_policy_digest"`
}

type Transition struct {
	ID               string            `json:"id"`
	Sequence         uint64            `json:"sequence"`
	InputGeneration  string            `json:"input_generation"`
	OutputGeneration string            `json:"output_generation"`
	InputTreeDigest  string            `json:"input_tree_digest"`
	OutputTreeDigest string            `json:"output_tree_digest"`
	Operation        OperationIdentity `json:"operation"`
	Changes          []ObjectChange    `json:"changes"`
	Decision         PolicyDecision    `json:"decision"`
}

// TransitionBinding is a defensive copy passed to DecisionVerifier. The
// verifier should resolve the decision ID against its trusted policy/audit
// source and attest that it covers this exact transition.
type TransitionBinding struct {
	ID               string
	Sequence         uint64
	InputGeneration  string
	OutputGeneration string
	InputTreeDigest  string
	OutputTreeDigest string
	Operation        OperationIdentity
	Changes          []ObjectChange
}

// DecisionVerifier is supplied by the trusted caller. It must reject decisions
// without authentic supervisor provenance or whose policy receipt is not bound
// to this exact operation, generations, tree digests, and change set. This
// package provides no signature scheme or persistent audit source.
type DecisionVerifier func(PolicyDecision, TransitionBinding) error

// Result contains the validated endpoint commitment and composed baseline to
// final changes. Net-reverted paths are omitted from ComposedChanges but remain
// in TouchedPaths.
type Result struct {
	BaselineGeneration string
	SealedGeneration   string
	BaselineTreeDigest string
	SealedTreeDigest   string
	TransitionCount    uint64
	FinalManifest      TreeManifest
	ComposedChanges    []ObjectChange
	TouchedPaths       []string
}

type treePayload struct {
	SchemaVersion        string           `json:"schema_version"`
	MetadataPolicyDigest string           `json:"metadata_policy_digest"`
	Objects              []ManifestObject `json:"objects"`
}

// ComputeTreeDigest computes the JCS/SHA-256 commitment to a complete
// manifest. It checks canonical ordering and tree structure but cannot prove
// that the manifest describes a real or sealed filesystem.
func ComputeTreeDigest(manifest TreeManifest) (string, error) {
	if err := validateManifest(manifest); err != nil {
		return "", err
	}
	return digestValidatedManifest(manifest)
}

// ValidateChain applies transitions in exact sequence to a private baseline
// copy. It checks all preimages against the unchanged input tree before
// atomically applying each complete change set.
func ValidateChain(spec ChainSpec, entries []Transition, verify DecisionVerifier) (Result, error) {
	if len(entries) > MaxTransitions {
		return Result{}, invalid("transition count exceeds limit %d", MaxTransitions)
	}
	if err := preflightInput(spec, entries); err != nil {
		return Result{}, err
	}
	if len(entries) > 0 && verify == nil {
		return Result{}, invalid("non-empty chain requires a trusted decision verifier")
	}

	baseline := cloneManifest(spec.Baseline)
	sealed := cloneManifest(spec.Sealed)
	transitions, totalChanges, err := cloneTransitions(entries)
	if err != nil {
		return Result{}, err
	}
	if totalChanges > MaxTotalChanges {
		return Result{}, invalid("total transition changes exceed limit %d", MaxTotalChanges)
	}
	if !validDigest(spec.PolicyDigest, "sha256:") ||
		!validDigest(spec.MetadataPolicyDigest, "sha256:") {
		return Result{}, invalid("policy commitments must be SHA-256 digests")
	}
	if !validLabel(baseline.Generation) || !validLabel(sealed.Generation) {
		return Result{}, invalid("baseline and sealed generation labels are required")
	}
	if baseline.Generation != sealed.Generation && len(transitions) == 0 {
		return Result{}, invalid("different endpoint generations require a transition chain")
	}
	if baseline.MetadataPolicyDigest != spec.MetadataPolicyDigest ||
		sealed.MetadataPolicyDigest != spec.MetadataPolicyDigest {
		return Result{}, invalid("endpoint metadata-policy digest does not match chain commitment")
	}

	baselineDigest, err := ComputeTreeDigest(baseline)
	if err != nil {
		return Result{}, invalid("baseline manifest: %v", err)
	}
	sealedDigest, err := ComputeTreeDigest(sealed)
	if err != nil {
		return Result{}, invalid("sealed manifest: %v", err)
	}
	if baselineDigest != spec.ExpectedBaselineTreeDigest {
		return Result{}, invalid("baseline tree digest does not match expected commitment")
	}
	if sealedDigest != spec.ExpectedSealedTreeDigest {
		return Result{}, invalid("sealed tree digest does not match expected commitment")
	}

	current := objectMap(baseline.Objects)
	currentGeneration, currentDigest := baseline.Generation, baselineDigest
	currentWeight := manifestWork(baseline.Objects)
	var aggregateWork int64
	generations := map[string]struct{}{baseline.Generation: {}}
	transitionIDs := make(map[string]struct{}, len(transitions))
	operationIDs := make(map[OperationIdentity]struct{}, len(transitions))
	decisionIDs := make(map[string]struct{}, len(transitions))
	type pendingDecision struct {
		sequence uint64
		decision PolicyDecision
		binding  TransitionBinding
	}
	pendingDecisions := make([]pendingDecision, 0, len(transitions))
	composed := make(map[string]ObjectChange)
	touched := make(map[string]struct{})

	for i, entry := range transitions {
		prefix := fmt.Sprintf("transition[%d]", i)
		if err := validateTransitionHeader(entry, spec); err != nil {
			return Result{}, invalid("%s: %v", prefix, err)
		}
		if entry.Sequence != uint64(i+1) {
			return Result{}, invalid("%s: sequence must be contiguous from 1", prefix)
		}
		if _, duplicate := transitionIDs[entry.ID]; duplicate {
			return Result{}, invalid("%s: duplicate transition ID", prefix)
		}
		if _, duplicate := operationIDs[entry.Operation]; duplicate {
			return Result{}, invalid("%s: duplicate operation identity", prefix)
		}
		if _, duplicate := decisionIDs[entry.Decision.ID]; duplicate {
			return Result{}, invalid("%s: duplicate policy decision ID", prefix)
		}
		transitionIDs[entry.ID] = struct{}{}
		operationIDs[entry.Operation] = struct{}{}
		decisionIDs[entry.Decision.ID] = struct{}{}

		if entry.InputGeneration != currentGeneration || entry.InputTreeDigest != currentDigest {
			return Result{}, invalid("%s: predecessor generation or tree digest mismatch", prefix)
		}
		if _, reused := generations[entry.OutputGeneration]; reused {
			return Result{}, invalid("%s: output generation label is reused or branched", prefix)
		}

		for j, change := range entry.Changes {
			if err := validatePath(change.Path); err != nil {
				return Result{}, invalid("%s change[%d]: %v", prefix, j, err)
			}
			if j > 0 && entry.Changes[j-1].Path >= change.Path {
				return Result{}, invalid("%s: changes must be strictly path-sorted with no duplicates", prefix)
			}
			if err := validateObjectState(change.Before); err != nil {
				return Result{}, invalid("%s change[%d] before: %v", prefix, j, err)
			}
			if err := validateObjectState(change.After); err != nil {
				return Result{}, invalid("%s change[%d] after: %v", prefix, j, err)
			}
			actual, exists := current[change.Path]
			if !exists {
				actual = ObjectState{}
			}
			if actual != change.Before {
				return Result{}, invalid("%s change[%d]: preimage mismatch for %q", prefix, j, change.Path)
			}
			if change.Before == change.After {
				return Result{}, invalid("%s change[%d]: before and after states are identical", prefix, j)
			}
		}
		nextWeight := currentWeight
		nextObjectCount := len(current)
		for _, change := range entry.Changes {
			if change.Before.Exists {
				nextWeight -= objectWork(change.Path)
				nextObjectCount--
			}
			if change.After.Exists {
				nextWeight += objectWork(change.Path)
				nextObjectCount++
			}
		}
		if nextObjectCount < 0 || nextWeight < 0 {
			return Result{}, invalid("%s: inconsistent changed-object work estimate", prefix)
		}
		stepWork := transitionWork(currentWeight, nextWeight, len(current), nextObjectCount)
		if stepWork > MaxAggregateTreeWork-aggregateWork {
			return Result{}, invalid("aggregate tree-validation work exceeds limit %d", MaxAggregateTreeWork)
		}
		aggregateWork += stepWork

		next := cloneObjectMap(current)
		for _, change := range entry.Changes {
			if change.After.Exists {
				next[change.Path] = change.After
			} else {
				delete(next, change.Path)
			}
			touched[change.Path] = struct{}{}
			if original, exists := composed[change.Path]; exists {
				original.After = change.After
				composed[change.Path] = original
			} else {
				composed[change.Path] = ObjectChange{Path: change.Path, Before: change.Before, After: change.After}
			}
		}
		if len(next) > MaxObjectsPerManifest {
			return Result{}, invalid("%s: output tree exceeds object limit %d", prefix, MaxObjectsPerManifest)
		}
		if err := validateObjectHierarchy(next); err != nil {
			return Result{}, invalid("%s output tree: %v", prefix, err)
		}
		nextDigest, err := computeTreeDigestFromMap(next, spec.MetadataPolicyDigest)
		if err != nil {
			return Result{}, invalid("%s output tree: %v", prefix, err)
		}
		if entry.OutputTreeDigest != nextDigest {
			return Result{}, invalid("%s: output tree digest mismatch", prefix)
		}

		pendingDecisions = append(pendingDecisions, pendingDecision{
			sequence: entry.Sequence, decision: entry.Decision, binding: transitionBinding(entry),
		})
		current, currentGeneration, currentDigest = next, entry.OutputGeneration, nextDigest
		currentWeight = nextWeight
		generations[currentGeneration] = struct{}{}
	}

	if currentGeneration != sealed.Generation {
		return Result{}, invalid("chain does not end at the sealed generation")
	}
	if currentDigest != sealedDigest || !sameObjectMaps(current, objectMap(sealed.Objects)) {
		return Result{}, invalid("composed chain does not reproduce the complete sealed manifest")
	}
	for _, pending := range pendingDecisions {
		if err := verify(pending.decision, pending.binding); err != nil {
			return Result{}, invalid("transition sequence %d: policy decision verification failed: %v", pending.sequence, err)
		}
	}

	paths := make([]string, 0, len(composed))
	for path := range composed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	composedChanges := make([]ObjectChange, 0, len(paths))
	for _, path := range paths {
		change := composed[path]
		if change.Before != change.After {
			composedChanges = append(composedChanges, change)
		}
	}
	touchedPaths := make([]string, 0, len(touched))
	for path := range touched {
		touchedPaths = append(touchedPaths, path)
	}
	sort.Strings(touchedPaths)

	return Result{
		BaselineGeneration: baseline.Generation,
		SealedGeneration:   sealed.Generation,
		BaselineTreeDigest: baselineDigest,
		SealedTreeDigest:   sealedDigest,
		TransitionCount:    uint64(len(transitions)),
		FinalManifest:      cloneManifest(sealed),
		ComposedChanges:    cloneChanges(composedChanges),
		TouchedPaths:       append([]string(nil), touchedPaths...),
	}, nil
}

// preflightInput bounds caller-controlled slices and strings before copying
// them or constructing canonical JSON. The estimates include path, digest,
// metadata-identity, and transition-identity bytes plus per-record overhead.
func preflightInput(spec ChainSpec, entries []Transition) error {
	budget := inputBudget{}
	if err := budget.reserve(2048); err != nil {
		return err
	}
	for _, endpoint := range []struct {
		name     string
		manifest TreeManifest
	}{{"baseline", spec.Baseline}, {"sealed", spec.Sealed}} {
		name, manifest := endpoint.name, endpoint.manifest
		if len(manifest.Objects) > MaxObjectsPerManifest {
			return invalid("%s manifest exceeds object limit %d", name, MaxObjectsPerManifest)
		}
		if err := preflightManifest(&budget, manifest, name); err != nil {
			return err
		}
	}
	for _, value := range []struct {
		name  string
		value string
		limit int
	}{
		{"expected baseline digest", spec.ExpectedBaselineTreeDigest, len(TreeDigestProfile) + 64},
		{"expected sealed digest", spec.ExpectedSealedTreeDigest, len(TreeDigestProfile) + 64},
		{"policy digest", spec.PolicyDigest, 71},
		{"metadata-policy digest", spec.MetadataPolicyDigest, 71},
	} {
		if err := budget.add(value.value, value.limit, value.name); err != nil {
			return err
		}
	}
	totalChanges := 0
	for i, entry := range entries {
		if len(entry.Changes) > MaxChangesPerTransition {
			return invalid("transition[%d] exceeds change limit %d", i, MaxChangesPerTransition)
		}
		if totalChanges > MaxTotalChanges-len(entry.Changes) {
			return invalid("total transition changes exceed limit %d", MaxTotalChanges)
		}
		totalChanges += len(entry.Changes)
		if err := budget.reserve(512); err != nil {
			return err
		}
		for _, value := range []struct {
			name  string
			value string
			limit int
		}{
			{"transition ID", entry.ID, MaxIdentityBytes},
			{"input generation", entry.InputGeneration, MaxIdentityBytes},
			{"output generation", entry.OutputGeneration, MaxIdentityBytes},
			{"input tree digest", entry.InputTreeDigest, len(TreeDigestProfile) + 64},
			{"output tree digest", entry.OutputTreeDigest, len(TreeDigestProfile) + 64},
			{"operation kind", string(entry.Operation.Kind), 32},
			{"proposal ID", entry.Operation.ProposalID, MaxIdentityBytes},
			{"call issuer", entry.Operation.CallIssuer, MaxIdentityBytes},
			{"call ID", entry.Operation.CallID, MaxIdentityBytes},
			{"lease ID", entry.Operation.LeaseID, MaxIdentityBytes},
			{"decision ID", entry.Decision.ID, MaxIdentityBytes},
			{"decision outcome", string(entry.Decision.Outcome), 16},
			{"decision policy digest", entry.Decision.PolicyDigest, 71},
			{"decision metadata-policy digest", entry.Decision.MetadataPolicyDigest, 71},
		} {
			if err := budget.add(value.value, value.limit, fmt.Sprintf("transition[%d] %s", i, value.name)); err != nil {
				return err
			}
		}
		for j, change := range entry.Changes {
			if err := budget.reserve(512); err != nil {
				return err
			}
			if err := budget.add(change.Path, MaxPathBytes, fmt.Sprintf("transition[%d] change[%d] path", i, j)); err != nil {
				return err
			}
			if err := preflightState(&budget, change.Before, fmt.Sprintf("transition[%d] change[%d] before", i, j)); err != nil {
				return err
			}
			if err := preflightState(&budget, change.After, fmt.Sprintf("transition[%d] change[%d] after", i, j)); err != nil {
				return err
			}
		}
	}
	return nil
}

type inputBudget struct{ used int64 }

func (budget *inputBudget) reserve(size int64) error {
	if size < 0 || size > MaxInputBytes-budget.used {
		return invalid("caller-supplied manifests and transition data exceed input-byte limit %d", MaxInputBytes)
	}
	budget.used += size
	return nil
}

func (budget *inputBudget) add(value string, limit int, field string) error {
	if len(value) > limit {
		return invalid("%s exceeds byte limit %d", field, limit)
	}
	return budget.reserve(int64(len(value)))
}

func preflightManifest(budget *inputBudget, manifest TreeManifest, name string) error {
	if err := budget.reserve(256); err != nil {
		return err
	}
	if err := budget.add(manifest.Generation, MaxIdentityBytes, name+" generation"); err != nil {
		return err
	}
	if err := budget.add(manifest.MetadataPolicyDigest, 71, name+" metadata-policy digest"); err != nil {
		return err
	}
	for i, object := range manifest.Objects {
		if err := budget.reserve(256); err != nil {
			return err
		}
		if err := budget.add(object.Path, MaxPathBytes, fmt.Sprintf("%s object[%d] path", name, i)); err != nil {
			return err
		}
		if err := preflightState(budget, object.State, fmt.Sprintf("%s object[%d]", name, i)); err != nil {
			return err
		}
	}
	return nil
}

func preflightState(budget *inputBudget, state ObjectState, name string) error {
	if err := budget.reserve(128); err != nil {
		return err
	}
	if err := budget.add(string(state.Type), 32, name+" type"); err != nil {
		return err
	}
	if err := budget.add(state.ContentDigest, 71, name+" content digest"); err != nil {
		return err
	}
	return budget.add(state.MetadataIdentity, 71, name+" metadata identity")
}

// objectWork gives a conservative weight to one manifest entry. The path
// multiplier accounts for JSON escaping; fixed overhead covers type, content,
// and metadata digests and map/record work.
func objectWork(path string) int64 { return 6*int64(len(path)) + 256 }

func manifestWork(objects []ManifestObject) int64 {
	var total int64
	for _, object := range objects {
		total += objectWork(object.Path)
	}
	return total
}

func transitionWork(before, after int64, beforeCount, afterCount int) int64 {
	count := beforeCount
	if afterCount > count {
		count = afterCount
	}
	// Each step clones/maps, checks hierarchy, builds/sorts a complete manifest,
	// and marshals/canonicalizes it. The logarithmic term bounds sorting work.
	factor := int64(6 + bits.Len(uint(count+1)))
	return (before + after) * factor
}

func validateTransitionHeader(entry Transition, spec ChainSpec) error {
	if !validLabel(entry.ID) || !validLabel(entry.InputGeneration) || !validLabel(entry.OutputGeneration) ||
		entry.InputGeneration == entry.OutputGeneration {
		return errors.New("transition and distinct generation labels are required")
	}
	if !validDigest(entry.InputTreeDigest, TreeDigestProfile) ||
		!validDigest(entry.OutputTreeDigest, TreeDigestProfile) {
		return errors.New("input or output tree digest is malformed")
	}
	if err := validateOperation(entry.Operation); err != nil {
		return err
	}
	if entry.Decision.Outcome != PolicyAllow {
		return errors.New("transition policy decision is not allow")
	}
	if !validLabel(entry.Decision.ID) {
		return errors.New("policy decision ID is required")
	}
	if entry.Decision.PolicyDigest != spec.PolicyDigest ||
		entry.Decision.MetadataPolicyDigest != spec.MetadataPolicyDigest {
		return errors.New("policy decision commitment mismatch")
	}
	return nil
}

func validateOperation(operation OperationIdentity) error {
	switch operation.Kind {
	case OperationProposalCall:
		if !validLabel(operation.ProposalID) || !validLabel(operation.CallIssuer) ||
			!validLabel(operation.CallID) || operation.LeaseID != "" {
			return errors.New("proposal/call identity must contain proposal ID, call issuer, and call ID only")
		}
	case OperationLease:
		if !validLabel(operation.LeaseID) || operation.ProposalID != "" ||
			operation.CallIssuer != "" || operation.CallID != "" {
			return errors.New("lease identity must contain lease ID only")
		}
	default:
		return errors.New("unknown operation identity kind")
	}
	return nil
}

func validateManifest(manifest TreeManifest) error {
	if !validLabel(manifest.Generation) {
		return errors.New("generation label is required")
	}
	if !validDigest(manifest.MetadataPolicyDigest, "sha256:") {
		return errors.New("metadata-policy digest must be a SHA-256 commitment")
	}
	if len(manifest.Objects) > MaxObjectsPerManifest {
		return fmt.Errorf("manifest exceeds object limit %d", MaxObjectsPerManifest)
	}
	objects := make(map[string]ObjectState, len(manifest.Objects))
	previous := ""
	for i, object := range manifest.Objects {
		if err := validatePath(object.Path); err != nil {
			return fmt.Errorf("object[%d]: %w", i, err)
		}
		if i > 0 && previous >= object.Path {
			return errors.New("manifest objects must be strictly path-sorted with no duplicates")
		}
		if err := validateObjectState(object.State); err != nil {
			return fmt.Errorf("object[%d] state: %w", i, err)
		}
		if !object.State.Exists {
			return fmt.Errorf("manifest object[%d] cannot represent an absent path", i)
		}
		objects[object.Path] = object.State
		previous = object.Path
	}
	return validateObjectHierarchy(objects)
}

func validateObjectState(state ObjectState) error {
	if !state.Exists {
		if state.Type != "" || state.ContentDigest != "" || state.MetadataIdentity != "" {
			return errors.New("absent state must not contain type or digests")
		}
		return nil
	}
	switch state.Type {
	case ObjectRegular, ObjectSymlink:
		if !validDigest(state.ContentDigest, "sha256:") {
			return errors.New("regular file and symlink require a SHA-256 content digest")
		}
	case ObjectDirectory, ObjectFIFO, ObjectSocket, ObjectCharDevice, ObjectBlockDevice:
		if state.ContentDigest != "" {
			return errors.New("non-content object type must not contain a content digest")
		}
	default:
		return errors.New("unsupported object type")
	}
	if !validDigest(state.MetadataIdentity, "sha256:") {
		return errors.New("present state requires a SHA-256 metadata identity")
	}
	return nil
}

func validateObjectHierarchy(objects map[string]ObjectState) error {
	for name := range objects {
		slash := strings.IndexByte(name, '/')
		for slash >= 0 {
			parent := name[:slash]
			state, exists := objects[parent]
			if !exists || state.Type != ObjectDirectory {
				return fmt.Errorf("path %q has a missing or non-directory parent %q", name, parent)
			}
			next := strings.IndexByte(name[slash+1:], '/')
			if next < 0 {
				break
			}
			slash += next + 1
		}
	}
	return nil
}

func validatePath(value string) error {
	if value == "" || len(value) > MaxPathBytes || !utf8.ValidString(value) ||
		strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\\') {
		return errors.New("path must be a bounded, valid UTF-8 relative slash path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("path contains an empty, dot, or parent component")
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return errors.New("path contains a control character")
			}
		}
	}
	return nil
}

func validLabel(value string) bool {
	if value == "" || len(value) > MaxIdentityBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validDigest(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	encoded := value[len(prefix):]
	if strings.ToLower(encoded) != encoded {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func computeTreeDigestFromMap(objects map[string]ObjectState, metadataPolicyDigest string) (string, error) {
	manifestObjects := make([]ManifestObject, 0, len(objects))
	for name, state := range objects {
		manifestObjects = append(manifestObjects, ManifestObject{Path: name, State: state})
	}
	sort.Slice(manifestObjects, func(i, j int) bool {
		return manifestObjects[i].Path < manifestObjects[j].Path
	})
	manifest := TreeManifest{
		Generation: "intermediate", MetadataPolicyDigest: metadataPolicyDigest,
		Objects: manifestObjects,
	}
	if err := validateManifest(manifest); err != nil {
		return "", err
	}
	return digestValidatedManifest(manifest)
}

func digestValidatedManifest(manifest TreeManifest) (string, error) {
	payload := treePayload{
		SchemaVersion: manifestSchema, MetadataPolicyDigest: manifest.MetadataPolicyDigest,
		Objects: cloneManifestObjects(manifest.Objects),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode tree manifest: %w", err)
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return "", fmt.Errorf("canonicalize tree manifest: %w", err)
	}
	hash := sha256.Sum256(canonical)
	return TreeDigestProfile + hex.EncodeToString(hash[:]), nil
}

func cloneTransitions(entries []Transition) ([]Transition, int, error) {
	copied := make([]Transition, len(entries))
	total := 0
	for i, entry := range entries {
		if len(entry.Changes) > MaxChangesPerTransition {
			return nil, 0, invalid("transition[%d] exceeds change limit %d", i, MaxChangesPerTransition)
		}
		if total > MaxTotalChanges-len(entry.Changes) {
			return nil, 0, invalid("total transition changes exceed limit %d", MaxTotalChanges)
		}
		total += len(entry.Changes)
		copied[i] = entry
		copied[i].Changes = cloneChanges(entry.Changes)
	}
	return copied, total, nil
}

func cloneManifest(manifest TreeManifest) TreeManifest {
	manifest.Objects = cloneManifestObjects(manifest.Objects)
	return manifest
}

func cloneManifestObjects(objects []ManifestObject) []ManifestObject {
	if objects == nil {
		return nil
	}
	return append([]ManifestObject(nil), objects...)
}

func cloneChanges(changes []ObjectChange) []ObjectChange {
	if changes == nil {
		return nil
	}
	return append([]ObjectChange(nil), changes...)
}

func transitionBinding(entry Transition) TransitionBinding {
	return TransitionBinding{
		ID: entry.ID, Sequence: entry.Sequence, InputGeneration: entry.InputGeneration,
		OutputGeneration: entry.OutputGeneration, InputTreeDigest: entry.InputTreeDigest,
		OutputTreeDigest: entry.OutputTreeDigest, Operation: entry.Operation,
		Changes: cloneChanges(entry.Changes),
	}
}

func objectMap(objects []ManifestObject) map[string]ObjectState {
	result := make(map[string]ObjectState, len(objects))
	for _, object := range objects {
		result[object.Path] = object.State
	}
	return result
}

func cloneObjectMap(objects map[string]ObjectState) map[string]ObjectState {
	result := make(map[string]ObjectState, len(objects)+1)
	for name, state := range objects {
		result[name] = state
	}
	return result
}

func sameObjectMaps(left, right map[string]ObjectState) bool {
	if len(left) != len(right) {
		return false
	}
	for name, state := range left {
		if right[name] != state {
			return false
		}
	}
	return true
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidChain, fmt.Sprintf(format, args...))
}
