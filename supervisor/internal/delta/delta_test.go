package delta

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestValidateChainComposesEditsAndReverts(t *testing.T) {
	spec, entries := fixture(t)
	verified := make([]string, 0, len(entries))
	result, err := ValidateChain(spec, entries, func(decision PolicyDecision, binding TransitionBinding) error {
		if decision.Outcome != PolicyAllow || decision.PolicyDigest != spec.PolicyDigest ||
			decision.MetadataPolicyDigest != spec.MetadataPolicyDigest {
			return fmt.Errorf("untrusted decision context")
		}
		if binding.ID == "" || binding.Sequence == 0 || len(binding.Changes) == 0 {
			return fmt.Errorf("incomplete transition binding")
		}
		verified = append(verified, decision.ID)
		binding.Changes[0].After = ObjectState{}
		return nil
	})
	if err != nil {
		t.Fatalf("ValidateChain: %v", err)
	}
	if result.BaselineGeneration != "g0" || result.SealedGeneration != "g2" ||
		result.TransitionCount != 2 || len(verified) != 2 {
		t.Fatalf("unexpected result endpoints/count/verifications: %+v, %v", result, verified)
	}
	if len(result.ComposedChanges) != 1 || result.ComposedChanges[0].Path != "docs/b.txt" {
		t.Fatalf("reverted path should be omitted from net delta: %+v", result.ComposedChanges)
	}
	if len(result.TouchedPaths) != 2 || result.TouchedPaths[0] != "docs/a.txt" ||
		result.TouchedPaths[1] != "docs/b.txt" {
		t.Fatalf("unexpected touched paths: %v", result.TouchedPaths)
	}

	// Returned data and verifier inputs must not alias caller-owned transition data.
	result.FinalManifest.Objects[0].Path = "mutated"
	if spec.Sealed.Objects[0].Path != "docs" || entries[0].Changes[0].Path != "docs/a.txt" {
		t.Fatal("result or verifier path leaked mutation into caller input")
	}
}

func TestValidateChainRejectsBrokenOrderingAndLinks(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]Transition) []Transition
	}{
		{name: "missing predecessor", mutate: func(in []Transition) []Transition { return in[1:] }},
		{name: "out of order", mutate: func(in []Transition) []Transition { return []Transition{in[1], in[0]} }},
		{name: "branched generation", mutate: func(in []Transition) []Transition {
			in[1].InputGeneration = "other-g1"
			return in
		}},
		{name: "reused generation label", mutate: func(in []Transition) []Transition {
			in[1].OutputGeneration = "g0"
			return in
		}},
		{name: "input digest mismatch", mutate: func(in []Transition) []Transition {
			in[1].InputTreeDigest = wrongTreeDigest()
			return in
		}},
		{name: "output digest mismatch", mutate: func(in []Transition) []Transition {
			in[0].OutputTreeDigest = wrongTreeDigest()
			return in
		}},
		{name: "omitted changed object", mutate: func(in []Transition) []Transition {
			in[1].Changes = in[1].Changes[:1]
			return in
		}},
		{name: "duplicate changed path", mutate: func(in []Transition) []Transition {
			in[0].Changes = append(in[0].Changes, in[0].Changes[0])
			return in
		}},
		{name: "preimage metadata mismatch", mutate: func(in []Transition) []Transition {
			in[0].Changes[0].Before.MetadataIdentity = testDigest("different-metadata")
			return in
		}},
		{name: "unchanged object row", mutate: func(in []Transition) []Transition {
			in[0].Changes[0].After = in[0].Changes[0].Before
			return in
		}},
		{name: "duplicate operation", mutate: func(in []Transition) []Transition {
			in[1].Operation = in[0].Operation
			return in
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			spec, entries := fixture(t)
			entries = test.mutate(entries)
			if _, err := ValidateChain(spec, entries, acceptingVerifier); err == nil {
				t.Fatal("expected invalid chain to be rejected")
			}
		})
	}
}

func TestValidateChainAllowsEmptyGenerationTransition(t *testing.T) {
	spec, _ := fixture(t)
	spec.Sealed = cloneManifest(spec.Baseline)
	spec.Sealed.Generation = "g1-empty"
	spec.ExpectedSealedTreeDigest = spec.ExpectedBaselineTreeDigest
	entry := Transition{
		ID: "empty-transition", Sequence: 1, InputGeneration: "g0", OutputGeneration: "g1-empty",
		InputTreeDigest: spec.ExpectedBaselineTreeDigest, OutputTreeDigest: spec.ExpectedBaselineTreeDigest,
		Operation: OperationIdentity{Kind: OperationLease, LeaseID: "empty-lease"},
		Changes: []ObjectChange{},
		Decision: PolicyDecision{ID: "empty-decision", Outcome: PolicyAllow,
			PolicyDigest: spec.PolicyDigest, MetadataPolicyDigest: spec.MetadataPolicyDigest},
	}
	result, err := ValidateChain(spec, []Transition{entry}, acceptingVerifier)
	if err != nil {
		t.Fatalf("valid empty-change transition rejected: %v", err)
	}
	if len(result.ComposedChanges) != 0 || len(result.TouchedPaths) != 0 {
		t.Fatalf("empty-change transition produced object delta: %+v", result)
	}
}

func TestValidateChainAppliesChangeSetFromOneInputSnapshot(t *testing.T) {
	policy, metadataPolicy := testDigest("policy"), testDigest("metadata")
	directory := object(ObjectDirectory, "", "directory")
	left := object(ObjectRegular, "left", "file")
	right := object(ObjectRegular, "right", "file")
	baseline := TreeManifest{Generation: "g0", MetadataPolicyDigest: metadataPolicy, Objects: []ManifestObject{
		{Path: "docs", State: directory}, {Path: "docs/a", State: left}, {Path: "docs/b", State: right},
	}}
	sealed := TreeManifest{Generation: "g1", MetadataPolicyDigest: metadataPolicy, Objects: []ManifestObject{
		{Path: "docs", State: directory}, {Path: "docs/a", State: right}, {Path: "docs/b", State: left},
	}}
	baselineDigest, sealedDigest := digestManifest(t, baseline), digestManifest(t, sealed)
	spec := ChainSpec{Baseline: baseline, Sealed: sealed, ExpectedBaselineTreeDigest: baselineDigest,
		ExpectedSealedTreeDigest: sealedDigest, PolicyDigest: policy, MetadataPolicyDigest: metadataPolicy}
	entry := Transition{ID: "swap", Sequence: 1, InputGeneration: "g0", OutputGeneration: "g1",
		InputTreeDigest: baselineDigest, OutputTreeDigest: sealedDigest,
		Operation: OperationIdentity{Kind: OperationProposalCall, ProposalID: "p", CallIssuer: "broker", CallID: "c"},
		Changes: []ObjectChange{
			{Path: "docs/a", Before: left, After: right},
			{Path: "docs/b", Before: right, After: left},
		},
		Decision: PolicyDecision{ID: "d", Outcome: PolicyAllow, PolicyDigest: policy, MetadataPolicyDigest: metadataPolicy},
	}
	if _, err := ValidateChain(spec, []Transition{entry}, acceptingVerifier); err != nil {
		t.Fatalf("atomic two-object exchange rejected: %v", err)
	}
}

func TestValidateChainRejectsExcessAggregateTreeWorkBeforeStepCopy(t *testing.T) {
	metadataPolicy, policy := testDigest("metadata"), testDigest("policy")
	objects := make([]ManifestObject, 12000)
	for i := range objects {
		path := fmt.Sprintf("%05d", i) + strings.Repeat("x", MaxPathBytes-5)
		objects[i] = ManifestObject{Path: path, State: object(ObjectRegular, "bytes", "metadata")}
	}
	baseline := TreeManifest{Generation: "g0", MetadataPolicyDigest: metadataPolicy, Objects: objects}
	sealed := TreeManifest{Generation: "g1", MetadataPolicyDigest: metadataPolicy, Objects: append([]ManifestObject(nil), objects...)}
	baselineDigest, sealedDigest := digestManifest(t, baseline), digestManifest(t, sealed)
	spec := ChainSpec{Baseline: baseline, Sealed: sealed, ExpectedBaselineTreeDigest: baselineDigest,
		ExpectedSealedTreeDigest: sealedDigest, PolicyDigest: policy, MetadataPolicyDigest: metadataPolicy}
	entry := Transition{ID: "empty", Sequence: 1, InputGeneration: "g0", OutputGeneration: "g1",
		InputTreeDigest: baselineDigest, OutputTreeDigest: sealedDigest,
		Operation: OperationIdentity{Kind: OperationLease, LeaseID: "lease"},
		Decision: PolicyDecision{ID: "decision", Outcome: PolicyAllow, PolicyDigest: policy, MetadataPolicyDigest: metadataPolicy},
	}
	if _, err := ValidateChain(spec, []Transition{entry}, acceptingVerifier); err == nil || !strings.Contains(err.Error(), "aggregate tree-validation work") {
		t.Fatalf("aggregate tree work limit not enforced: %v", err)
	}
}

func TestValidateChainPreflightsOversizedManifestStrings(t *testing.T) {
	spec, entries := fixture(t)
	spec.Baseline.Objects[0].Path = strings.Repeat("x", MaxPathBytes+1)
	if _, err := ValidateChain(spec, entries, acceptingVerifier); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("oversized caller path was not rejected by preflight: %v", err)
	}
}

func TestValidateChainBoundsAggregateInputBytes(t *testing.T) {
	longPath := strings.Repeat("p", MaxPathBytes)
	objects := make([]ManifestObject, MaxObjectsPerManifest)
	for i := range objects {
		objects[i] = ManifestObject{Path: longPath, State: object(ObjectRegular, "bytes", "metadata")}
	}
	metadataPolicy := testDigest("metadata")
	baseline := TreeManifest{Generation: "g0", MetadataPolicyDigest: metadataPolicy, Objects: objects}
	sealed := TreeManifest{Generation: "g1", MetadataPolicyDigest: metadataPolicy, Objects: objects}
	changes := make([]ObjectChange, MaxChangesPerTransition)
	for i := range changes {
		changes[i] = ObjectChange{Path: longPath}
	}
	entries := make([]Transition, 3)
	for i := range entries {
		entries[i].Changes = changes
	}
	entries[2].Changes = make([]ObjectChange, MaxTotalChanges-2*MaxChangesPerTransition)
	for i := range entries[2].Changes {
		entries[2].Changes[i] = ObjectChange{Path: longPath}
	}
	spec := ChainSpec{Baseline: baseline, Sealed: sealed}
	if _, err := ValidateChain(spec, entries, nil); err == nil || !strings.Contains(err.Error(), "input-byte limit") {
		t.Fatalf("aggregate caller-controlled bytes were not rejected during preflight: %v", err)
	}
}

func TestValidateChainRequiresDecisionAttestationAndPolicyMatch(t *testing.T) {
	spec, entries := fixture(t)
	if _, err := ValidateChain(spec, entries, nil); err == nil {
		t.Fatal("non-empty chain accepted without caller-supplied decision verifier")
	}

	spec, entries = fixture(t)
	entries[0].Decision.Outcome = PolicyDeny
	if _, err := ValidateChain(spec, entries, acceptingVerifier); err == nil {
		t.Fatal("denied decision accepted")
	}

	spec, entries = fixture(t)
	entries[0].Decision.MetadataPolicyDigest = testDigest("different-metadata-policy")
	called := false
	verifier := func(PolicyDecision, TransitionBinding) error { called = true; return nil }
	if _, err := ValidateChain(spec, entries, verifier); err == nil || called {
		t.Fatal("decision with mismatched metadata policy was accepted or attested")
	}

	spec, entries = fixture(t)
	if _, err := ValidateChain(spec, entries, func(decision PolicyDecision, binding TransitionBinding) error {
		if decision.ID != "decision-1" || binding.ID != "transition-1" {
			return fmt.Errorf("decision receipt is not bound to this transition")
		}
		return fmt.Errorf("decision receipt is not supervisor-attested")
	}); err == nil || !strings.Contains(err.Error(), "policy decision verification failed") {
		t.Fatalf("untrusted declared allow decision was accepted: %v", err)
	}
}

func TestValidateChainRequiresCompleteMatchingEndpointCommitments(t *testing.T) {
	spec, entries := fixture(t)
	spec.ExpectedSealedTreeDigest = wrongTreeDigest()
	if _, err := ValidateChain(spec, entries, acceptingVerifier); err == nil {
		t.Fatal("mismatched sealed manifest commitment accepted")
	}

	spec, entries = fixture(t)
	spec.MetadataPolicyDigest = testDigest("different-policy")
	if _, err := ValidateChain(spec, entries, acceptingVerifier); err == nil {
		t.Fatal("omitted/chosen metadata policy commitment accepted")
	}

	spec, _ = fixture(t)
	spec.Sealed.Objects = spec.Sealed.Objects[:1]
	if _, err := ValidateChain(spec, nil, nil); err == nil {
		t.Fatal("incomplete final manifest accepted")
	}
}

func TestComputeTreeDigestRequiresCanonicalCompleteManifest(t *testing.T) {
	spec, _ := fixture(t)
	manifest := spec.Baseline
	manifest.Objects = []ManifestObject{manifest.Objects[1], manifest.Objects[0]}
	if _, err := ComputeTreeDigest(manifest); err == nil {
		t.Fatal("unsorted manifest accepted")
	}
	manifest = spec.Baseline
	manifest.Objects[1].State.MetadataIdentity = "sha256:bad"
	if _, err := ComputeTreeDigest(manifest); err == nil {
		t.Fatal("malformed metadata identity accepted")
	}
}

func acceptingVerifier(PolicyDecision, TransitionBinding) error { return nil }

func fixture(t *testing.T) (ChainSpec, []Transition) {
	t.Helper()
	policy := testDigest("policy-v1")
	metadataPolicy := testDigest("metadata-policy-v1")
	directory := object(ObjectDirectory, "", "directory-metadata")
	oldFile := object(ObjectRegular, "old", "file-metadata")
	middleFile := object(ObjectRegular, "middle", "file-metadata")
	createdFile := object(ObjectRegular, "created", "file-metadata")

	baseline := TreeManifest{
		Generation: "g0", MetadataPolicyDigest: metadataPolicy,
		Objects: []ManifestObject{{Path: "docs", State: directory}, {Path: "docs/a.txt", State: oldFile}},
	}
	middle := TreeManifest{
		Generation: "g1", MetadataPolicyDigest: metadataPolicy,
		Objects: []ManifestObject{{Path: "docs", State: directory}, {Path: "docs/a.txt", State: middleFile}},
	}
	sealed := TreeManifest{
		Generation: "g2", MetadataPolicyDigest: metadataPolicy,
		Objects: []ManifestObject{
			{Path: "docs", State: directory},
			{Path: "docs/a.txt", State: oldFile},
			{Path: "docs/b.txt", State: createdFile},
		},
	}
	baselineDigest := digestManifest(t, baseline)
	middleDigest := digestManifest(t, middle)
	sealedDigest := digestManifest(t, sealed)
	spec := ChainSpec{
		Baseline: baseline, Sealed: sealed,
		ExpectedBaselineTreeDigest: baselineDigest, ExpectedSealedTreeDigest: sealedDigest,
		PolicyDigest: policy, MetadataPolicyDigest: metadataPolicy,
	}
	entries := []Transition{
		{
			ID: "transition-1", Sequence: 1, InputGeneration: "g0", OutputGeneration: "g1",
			InputTreeDigest: baselineDigest, OutputTreeDigest: middleDigest,
			Operation: OperationIdentity{Kind: OperationProposalCall, ProposalID: "proposal-1", CallIssuer: "broker", CallID: "call-1"},
			Changes: []ObjectChange{{Path: "docs/a.txt", Before: oldFile, After: middleFile}},
			Decision: PolicyDecision{ID: "decision-1", Outcome: PolicyAllow, PolicyDigest: policy, MetadataPolicyDigest: metadataPolicy},
		},
		{
			ID: "transition-2", Sequence: 2, InputGeneration: "g1", OutputGeneration: "g2",
			InputTreeDigest: middleDigest, OutputTreeDigest: sealedDigest,
			Operation: OperationIdentity{Kind: OperationLease, LeaseID: "lease-2"},
			Changes: []ObjectChange{
				{Path: "docs/a.txt", Before: middleFile, After: oldFile},
				{Path: "docs/b.txt", Before: ObjectState{}, After: createdFile},
			},
			Decision: PolicyDecision{ID: "decision-2", Outcome: PolicyAllow, PolicyDigest: policy, MetadataPolicyDigest: metadataPolicy},
		},
	}
	return spec, entries
}

func object(kind ObjectType, content, metadata string) ObjectState {
	state := ObjectState{Exists: true, Type: kind, MetadataIdentity: testDigest(metadata)}
	if kind == ObjectRegular || kind == ObjectSymlink {
		state.ContentDigest = testDigest(content)
	}
	return state
}

func digestManifest(t *testing.T, manifest TreeManifest) string {
	t.Helper()
	digest, err := ComputeTreeDigest(manifest)
	if err != nil {
		t.Fatalf("ComputeTreeDigest: %v", err)
	}
	return digest
}

func testDigest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func wrongTreeDigest() string { return TreeDigestProfile + strings.Repeat("0", 64) }
