package sessionrepo

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
)

// newVerifyEvidenceFixture returns a store with baseline g0 and two authorized
// writes: g1 adds created.txt and g2 adds second.txt. The tip is g2.
func newVerifyEvidenceFixture(t *testing.T) (*Store, *Generation, *Generation) {
	t.Helper()
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	buildWriteFixtureTree(t, sourcePath, writeFixtureFiles())
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	firstFiles := append(cloneWriteFixtureFiles(writeFixtureFiles()), writeFixtureFileSpec{path: "created.txt", content: "created\n", mode: 0o644})
	g1 := writeAndAssertMode(t, store, g0, authority, "created", "created.txt", []byte("created\n"), expectedWriteSnapshot(t, "g1", firstFiles), 1, 0o644)
	secondFiles := append(cloneWriteFixtureFiles(firstFiles), writeFixtureFileSpec{path: "second.txt", content: "second\n", mode: 0o644})
	g2 := writeAndAssertMode(t, store, g1, authority, "second", "second.txt", []byte("second\n"), expectedWriteSnapshot(t, "g2", secondFiles), 2, 0o644)
	return store, g1, g2
}

func TestVerifyEvidenceMatchesVerifyEvidenceAndBundleOnOneState(t *testing.T) {
	store, _, _ := newVerifyEvidenceFixture(t)
	verified, err := store.VerifyEvidence()
	if err != nil {
		t.Fatalf("VerifyEvidence: %v", err)
	}
	chain, err := store.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	artifact, err := store.Evidence()
	if err != nil {
		t.Fatalf("Evidence: %v", err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatalf("EvidenceBundle: %v", err)
	}
	if chain.TransitionCount != 2 || len(artifact.Operations) == 0 {
		t.Fatalf("fixture must cover two transitions and operations, got %d transitions and %d operations", chain.TransitionCount, len(artifact.Operations))
	}
	if !reflect.DeepEqual(verified.Chain, chain) || !reflect.DeepEqual(verified.Artifact, artifact) || !reflect.DeepEqual(verified.Bundle, bundle) {
		t.Fatal("grouped reports differ from Verify, Evidence, and EvidenceBundle on one state")
	}
}

func TestVerifyEvidenceReportsShareNoMemoryWithEachOtherOrStore(t *testing.T) {
	store, _, _ := newVerifyEvidenceFixture(t)
	pristine, err := store.VerifyEvidence()
	if err != nil {
		t.Fatalf("pristine VerifyEvidence: %v", err)
	}

	bundleVictim, err := store.VerifyEvidence()
	if err != nil {
		t.Fatalf("bundle victim VerifyEvidence: %v", err)
	}
	scrambleVerifiedBundle(t, &bundleVictim.Bundle)
	if !reflect.DeepEqual(bundleVictim.Artifact, pristine.Artifact) || !reflect.DeepEqual(bundleVictim.Chain, pristine.Chain) {
		t.Fatal("mutating the bundle changed the artifact or chain report")
	}

	baselineVictim, err := store.VerifyEvidence()
	if err != nil {
		t.Fatalf("baseline victim VerifyEvidence: %v", err)
	}
	baselineVictim.Bundle.Baseline.Manifest.Objects[0].Path = "mutated"
	if baselineVictim.Bundle.Generations[0].Manifest.Objects[0].Path == "mutated" {
		t.Fatal("bundle baseline aliases its generation list")
	}

	artifactVictim, err := store.VerifyEvidence()
	if err != nil {
		t.Fatalf("artifact victim VerifyEvidence: %v", err)
	}
	scrambleVerifiedArtifact(t, &artifactVictim.Artifact)
	if !reflect.DeepEqual(artifactVictim.Bundle, pristine.Bundle) || !reflect.DeepEqual(artifactVictim.Chain, pristine.Chain) {
		t.Fatal("mutating the artifact changed the bundle or chain report")
	}

	chainVictim, err := store.VerifyEvidence()
	if err != nil {
		t.Fatalf("chain victim VerifyEvidence: %v", err)
	}
	scrambleVerifiedChain(t, &chainVictim.Chain)
	if !reflect.DeepEqual(chainVictim.Artifact, pristine.Artifact) || !reflect.DeepEqual(chainVictim.Bundle, pristine.Bundle) {
		t.Fatal("mutating the chain changed the artifact or bundle report")
	}

	clonedBundle := pristine.Bundle.Clone()
	if !reflect.DeepEqual(clonedBundle, pristine.Bundle) {
		t.Fatal("bundle clone differs from its original")
	}
	scrambleVerifiedBundle(t, &clonedBundle)

	fresh, err := store.VerifyEvidence()
	if err != nil {
		t.Fatalf("fresh VerifyEvidence: %v", err)
	}
	if !reflect.DeepEqual(fresh, pristine) {
		t.Fatal("mutating returned reports changed Store-derived evidence")
	}
}

func TestVerifyEvidenceRefusesQuarantinedStoreWhileForensicBundleStillReconstructs(t *testing.T) {
	store, _, _ := newVerifyEvidenceFixture(t)
	store.quarantined = true
	verified, err := store.VerifyEvidence()
	if !errors.Is(err, ErrQuarantined) {
		t.Fatalf("VerifyEvidence on a quarantined store error = %v, want ErrQuarantined", err)
	}
	if !reflect.DeepEqual(verified, VerifiedEvidence{}) {
		t.Fatal("VerifyEvidence returned reports after refusing a quarantined store")
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatalf("forensic EvidenceBundle on a quarantined store: %v", err)
	}
	if _, err := Reconstruct(bundle); err != nil {
		t.Fatalf("Reconstruct the quarantined forensic bundle: %v", err)
	}
}

func TestVerifyEvidenceCatchesContentChangedDuringDecisionVerification(t *testing.T) {
	store, g1, _ := newVerifyEvidenceFixture(t)
	changed := false
	verifyDecision := store.options.VerifyDecision
	store.options.VerifyDecision = func(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
		if !changed {
			changed = true
			if err := os.WriteFile(filepath.Join(g1.testPath(), "created.txt"), []byte("tampered"), 0o644); err != nil {
				t.Fatalf("tamper sealed content during verification: %v", err)
			}
		}
		return verifyDecision(decision, binding)
	}
	verified, err := store.VerifyEvidence()
	requireVerifyEvidenceRefused(t, store, verified, err, changed)
}

func TestVerifyEvidenceCatchesGenerationNameSwappedDuringDecisionVerification(t *testing.T) {
	store, g1, _ := newVerifyEvidenceFixture(t)
	swapped := false
	verifyDecision := store.options.VerifyDecision
	store.options.VerifyDecision = func(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
		if !swapped {
			swapped = true
			swapGenerationDirectory(t, g1.testPath())
		}
		return verifyDecision(decision, binding)
	}
	verified, err := store.VerifyEvidence()
	requireVerifyEvidenceRefused(t, store, verified, err, swapped)
}

func TestVerifyEvidenceRejectsMalformedAdmissionRecordLikeVerify(t *testing.T) {
	store, _, _ := newVerifyEvidenceFixture(t)
	if _, err := store.admissionJournal.Append(audit.Event{Kind: admissionEventKind, ID: "admission-malformed", Data: []byte(`{"not":"an admission record"}`)}); err != nil {
		t.Fatalf("append malformed admission record: %v", err)
	}
	if _, err := store.Verify(); err == nil {
		t.Fatal("Verify accepted the malformed admission record")
	}
	verified, err := store.VerifyEvidence()
	if err == nil {
		t.Fatal("VerifyEvidence accepted a malformed admission record")
	}
	if !reflect.DeepEqual(verified, VerifiedEvidence{}) {
		t.Fatal("VerifyEvidence returned reports after rejecting a malformed admission record")
	}
}

// A failing decision verifier must not skip the sweep: preexisting tampering is
// quarantined and reported as the filesystem error, not as the verifier's refusal.
func TestVerifyEvidenceFailingVerifierStillSweepsPreexistingTamper(t *testing.T) {
	store, g1, _ := newVerifyEvidenceFixture(t)
	if err := os.WriteFile(filepath.Join(g1.testPath(), "created.txt"), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper sealed content before verification: %v", err)
	}
	verifierRuns := 0
	store.options.VerifyDecision = func(delta.PolicyDecision, delta.TransitionBinding) error {
		verifierRuns++
		return errors.New("decision verifier refused")
	}
	verified, err := store.VerifyEvidence()
	if verifierRuns == 0 {
		t.Fatal("decision verifier never ran; the regression is vacuous")
	}
	requireVerifyEvidenceRefused(t, store, verified, err, true)
}

// A verifier that changes a sealed generation and then refuses still gets that
// change swept, so the Store is quarantined before the refusal is returned.
func TestVerifyEvidenceFailingVerifierStillSweepsCallbackCreatedTamper(t *testing.T) {
	store, g1, _ := newVerifyEvidenceFixture(t)
	tampered := false
	store.options.VerifyDecision = func(delta.PolicyDecision, delta.TransitionBinding) error {
		if !tampered {
			tampered = true
			if err := os.WriteFile(filepath.Join(g1.testPath(), "created.txt"), []byte("tampered"), 0o644); err != nil {
				t.Fatalf("tamper sealed content during verification: %v", err)
			}
		}
		return errors.New("decision verifier refused")
	}
	verified, err := store.VerifyEvidence()
	requireVerifyEvidenceRefused(t, store, verified, err, tampered)
}

// Malformed admission evidence is checked after the sweep, so tampering next to it
// still quarantines the Store and is reported as the filesystem error.
func TestVerifyEvidenceMalformedAdmissionWithTamperReportsFilesystemFirst(t *testing.T) {
	store, g1, _ := newVerifyEvidenceFixture(t)
	if _, err := store.admissionJournal.Append(audit.Event{Kind: admissionEventKind, ID: "admission-malformed", Data: []byte(`{"not":"an admission record"}`)}); err != nil {
		t.Fatalf("append malformed admission record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(g1.testPath(), "created.txt"), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper sealed content: %v", err)
	}
	verified, err := store.VerifyEvidence()
	requireVerifyEvidenceRefused(t, store, verified, err, true)
}

// Populated admission scopes are copied at both boundaries: changing the bundle a
// caller received must change neither its Clone nor the Store's policy, and the reverse.
func TestEvidenceBundleAdmissionScopeCopiesDoNotAliasStorePolicy(t *testing.T) {
	scopes := map[string]func() AdmissionScope{
		"product-scope":        func() AdmissionScope { return productScope([]string{"out"}, []string{"in"}, []string{"secret"}) },
		"exact-path-protected": func() AdmissionScope { return AdmissionScope{ProtectedPaths: []string{"a.txt", "b/c.txt"}} },
	}
	for name, newScope := range scopes {
		t.Run(name, func(t *testing.T) {
			store, _, _, _ := newE2EStore(t)
			store.options.AdmissionScope = newScope()
			source := filepath.Join(t.TempDir(), "source")
			buildWriteFixtureTree(t, source, writeFixtureFiles())
			if _, err := store.Seed(openFixtureRoot(t, source), RootAttestation{Quiescent: true}); err != nil {
				t.Fatalf("seed g0: %v", err)
			}
			original, err := store.EvidenceBundle()
			if err != nil {
				t.Fatalf("EvidenceBundle: %v", err)
			}
			if !reflect.DeepEqual(original.AdmissionScope, newScope()) {
				t.Fatal("bundle does not carry the populated Store scope")
			}
			clone := original.Clone()
			scrambleAdmissionScope(&original.AdmissionScope)
			if !reflect.DeepEqual(clone.AdmissionScope, newScope()) || !reflect.DeepEqual(store.options.AdmissionScope, newScope()) {
				t.Fatal("mutating the original bundle changed its clone or the Store policy")
			}
			scrambleAdmissionScope(&clone.AdmissionScope)
			fresh, err := store.EvidenceBundle()
			if err != nil {
				t.Fatalf("fresh EvidenceBundle: %v", err)
			}
			if !reflect.DeepEqual(fresh.AdmissionScope, newScope()) || !reflect.DeepEqual(store.options.AdmissionScope, newScope()) {
				t.Fatal("mutating the clone changed a fresh bundle or the Store policy")
			}
		})
	}
}

// scrambleAdmissionScope overwrites the first entry of every path list, so an alias
// to any of them is observable.
func scrambleAdmissionScope(scope *AdmissionScope) {
	for _, paths := range []*[]string{&scope.ProtectedPaths, &scope.Writable, &scope.ReadOnly, &scope.Protected} {
		if len(*paths) != 0 {
			(*paths)[0] = "mutated"
		}
	}
}

// requireVerifyEvidenceRefused checks the fail-closed contract for a mutation made
// while the decision verifier runs: the mutation happened, the call returned no
// partial reports, and the Store quarantined the generation the sweep rejected.
func requireVerifyEvidenceRefused(t *testing.T, store *Store, verified VerifiedEvidence, err error, mutated bool) {
	t.Helper()
	if !mutated {
		t.Fatal("decision verifier never ran the mutation; the regression is vacuous")
	}
	if !errors.Is(err, ErrGenerationModified) {
		t.Fatalf("VerifyEvidence error = %v, want ErrGenerationModified", err)
	}
	if !reflect.DeepEqual(verified, VerifiedEvidence{}) {
		t.Fatal("VerifyEvidence returned partial reports after failing")
	}
	if !store.quarantined {
		t.Fatal("store was not quarantined after the mutated generation was swept")
	}
}

// swapGenerationDirectory moves the sealed directory aside and installs an empty
// directory under the same name, so the held descriptor and the name differ.
func swapGenerationDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatalf("move sealed generation aside: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("install replacement generation directory: %v", err)
	}
}

// scrambleVerifiedBundle overwrites one element of every slice-backed field
// reachable from the bundle, so an alias to any of them would be observable.
func scrambleVerifiedBundle(t *testing.T, bundle *EvidenceBundle) {
	t.Helper()
	if len(bundle.AuditJournal) == 0 || len(bundle.AdmissionJournal) == 0 || len(bundle.Baseline.Manifest.Objects) == 0 ||
		len(bundle.Generations) == 0 || len(bundle.Generations[0].Manifest.Objects) == 0 ||
		len(bundle.ApprovedDeltaLedger) == 0 || len(bundle.ApprovedDeltaLedger[0].Changes) == 0 {
		t.Fatal("fixture bundle lacks a slice-backed field to mutate")
	}
	bundle.AuditJournal[0] ^= 0xff
	bundle.AdmissionJournal[0] ^= 0xff
	bundle.Baseline.Manifest.Objects[0].Path = "mutated"
	bundle.Generations[0].Manifest.Objects[0].Path = "mutated"
	bundle.ApprovedDeltaLedger[0].Changes[0].Path = "mutated"
	scrambleFirstAdmission(t, bundle.Operations)
	scrambleVerifiedChain(t, &bundle.ValidatedChain)
}

func scrambleVerifiedArtifact(t *testing.T, artifact *EvidenceArtifact) {
	t.Helper()
	if len(artifact.Baseline.Manifest.Objects) == 0 || len(artifact.Generations) == 0 ||
		len(artifact.Generations[0].Manifest.Objects) == 0 || len(artifact.ApprovedDeltaLedger) == 0 ||
		len(artifact.ApprovedDeltaLedger[0].Changes) == 0 {
		t.Fatal("fixture artifact lacks a slice-backed field to mutate")
	}
	artifact.Baseline.Manifest.Objects[0].Path = "mutated"
	artifact.Generations[0].Manifest.Objects[0].Path = "mutated"
	artifact.ApprovedDeltaLedger[0].Changes[0].Path = "mutated"
	scrambleFirstAdmission(t, artifact.Operations)
	scrambleVerifiedChain(t, &artifact.ValidatedChain)
}

func scrambleVerifiedChain(t *testing.T, chain *delta.Result) {
	t.Helper()
	if len(chain.FinalManifest.Objects) == 0 || len(chain.ComposedChanges) == 0 || len(chain.TouchedPaths) == 0 {
		t.Fatal("fixture chain lacks a slice-backed field to mutate")
	}
	chain.FinalManifest.Objects[0].Path = "mutated"
	chain.ComposedChanges[0].Path = "mutated"
	chain.TouchedPaths[0] = "mutated"
}

func scrambleFirstAdmission(t *testing.T, operations []OperationEvidence) {
	t.Helper()
	for index := range operations {
		if operations[index].Admission != nil {
			operations[index].Admission.RecordHash = "mutated"
			return
		}
	}
	t.Fatal("fixture has no operation with an admission reference to mutate")
}
