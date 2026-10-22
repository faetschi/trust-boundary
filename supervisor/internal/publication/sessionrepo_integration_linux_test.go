//go:build linux

package publication_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	publicationpkg "tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

func TestPublishFromSeededSessionRepositoryEvidence(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "seed-source")
	livePath := filepath.Join(root, "live")
	makeSessionRepoTestTree(t, sourcePath, map[string]string{"file.txt": "before"})
	makeSessionRepoTestTree(t, livePath, map[string]string{"file.txt": "before"})
	storePath, storeAuditPath := filepath.Join(root, "session"), filepath.Join(root, "session-audit")
	publicationPath := filepath.Join(root, "publication-state")
	for _, path := range []string{storePath, storeAuditPath, publicationPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	storeRoot, err := os.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	storeJournal, err := audit.Open(filepath.Join(storeAuditPath, "audit.jsonl"))
	if err != nil {
		_ = storeRoot.Close()
		t.Fatal(err)
	}
	publicationState, err := os.Open(publicationPath)
	if err != nil {
		_ = storeRoot.Close()
		_ = storeJournal.Close()
		t.Fatal(err)
	}
	publicationJournal, err := audit.Open(filepath.Join(publicationPath, "audit.jsonl"))
	if err != nil {
		_ = storeRoot.Close()
		_ = storeJournal.Close()
		_ = publicationState.Close()
		t.Fatal(err)
	}
	policy, metadata := sessionRepoTestDigest("session-policy"), sessionRepoTestDigest("session-metadata")
	xattr := workspace.XattrVisibilityAttestation{ProfileDigest: sessionRepoTestDigest("session-xattr-profile"), Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}
	operation := delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: "session-proposal-1", CallIssuer: "fixture-provider", CallID: "session-call-1"}
	decision := delta.PolicyDecision{ID: "session-policy-decision-1", Outcome: delta.PolicyAllow, PolicyDigest: policy, MetadataPolicyDigest: metadata}
	var authorizedRequest sessionrepo.OperationRequest
	storeOptions := sessionrepo.Options{PolicyDigest: policy, MetadataPolicyDigest: metadata, Limits: workspace.DefaultLimits(),
		XattrVisibility: xattr, Journal: storeJournal,
		AuthorizeOperation: func(request sessionrepo.OperationRequest) error {
			if request.Tool != "write" || request.Operation != operation || request.Decision != decision ||
				request.InputGeneration != "g0" || request.ArgumentDigest == "" {
				return errors.New("test policy fixture did not authorize the exact seeded write")
			}
			authorizedRequest = request
			return nil
		},
		VerifyDecision: func(receipt delta.PolicyDecision, binding delta.TransitionBinding) error {
			before := delta.ObjectState{Exists: true, Type: delta.ObjectRegular, ContentDigest: sessionRepoTestDigest("before"), MetadataIdentity: sessionRepoMetadataIdentity(delta.ObjectRegular, 0o644)}
			after := delta.ObjectState{Exists: true, Type: delta.ObjectRegular, ContentDigest: sessionRepoTestDigest("after-store-write"), MetadataIdentity: sessionRepoMetadataIdentity(delta.ObjectRegular, 0o644)}
			if receipt != decision || binding.ID != "transition-000001" || binding.Sequence != 1 || binding.Tool != "write" ||
				binding.ArgumentDigest != authorizedRequest.ArgumentDigest || binding.InputGeneration != "g0" || binding.OutputGeneration != "g1" ||
				binding.InputTreeDigest == "" || binding.OutputTreeDigest == "" || binding.Operation != operation ||
				!reflect.DeepEqual(binding.Changes, []delta.ObjectChange{{Path: "file.txt", Before: before, After: after}}) {
				return errors.New("test policy fixture did not verify the exact seeded write transition")
			}
			return nil
		},
		VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error {
			return errors.New("unused by write-only fixture")
		},
	}
	store, err := sessionrepo.Create(storeRoot, storeOptions)
	if err != nil {
		_ = storeJournal.Close()
		_ = publicationJournal.Close()
		_ = publicationState.Close()
		t.Fatal(err)
	}
	defer func() {
		_ = store.Close()
		_ = storeRoot.Close()
		_ = storeJournal.Close()
		_ = publicationJournal.Close()
		_ = publicationState.Close()
	}()
	seedSource, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := store.Seed(seedSource, sessionrepo.RootAttestation{Quiescent: true})
	_ = seedSource.Close()
	if err != nil {
		t.Fatalf("Seed through public sessionrepo API: %v", err)
	}
	mutation, err := store.Write(context.Background(), baseline, operation, decision, "file.txt", []byte("after-store-write"))
	if err != nil || mutation.Generation == nil || mutation.Outcome != "success" {
		t.Fatalf("Write through public sessionrepo API: result=%+v err=%v", mutation, err)
	}
	verifiedChain, err := store.Verify()
	if err != nil {
		t.Fatalf("sessionrepo.Verify: %v", err)
	}
	evidence, err := store.Evidence()
	if err != nil {
		t.Fatalf("sessionrepo.Evidence: %v", err)
	}
	if len(evidence.Generations) != 2 || len(evidence.ApprovedDeltaLedger) != 1 || verifiedChain.TransitionCount != 1 ||
		verifiedChain.BaselineTreeDigest != evidence.Baseline.TreeDigest || verifiedChain.SealedTreeDigest != mutation.Generation.TreeDigest() {
		t.Fatalf("seeded session evidence/verified chain mismatch: evidence=%+v chain=%+v", evidence, verifiedChain)
	}
	sealedPathRoot, err := mutation.Generation.ReadOnlyMountSource()
	if err != nil {
		t.Fatalf("Generation.ReadOnlyMountSource: %v", err)
	}
	sealedFD, err := syscall.Openat(int(sealedPathRoot.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	_ = sealedPathRoot.Close()
	if err != nil {
		t.Fatalf("open readable descriptor relative to generation FD: %v", err)
	}
	sealedRoot := os.NewFile(uintptr(sealedFD), "seeded-generation-root")
	defer sealedRoot.Close()
	liveRoot, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer liveRoot.Close()
	transitions := append([]delta.Transition(nil), evidence.ApprovedDeltaLedger...)
	verifyLedger := func(receipt delta.PolicyDecision, binding delta.TransitionBinding) error {
		for _, transition := range transitions {
			if receipt == transition.Decision && binding.ID == transition.ID && binding.Sequence == transition.Sequence &&
				binding.Tool == transition.Tool && binding.ArgumentDigest == transition.ArgumentDigest &&
				binding.InputGeneration == transition.InputGeneration && binding.OutputGeneration == transition.OutputGeneration &&
				binding.InputTreeDigest == transition.InputTreeDigest && binding.OutputTreeDigest == transition.OutputTreeDigest &&
				binding.Operation == transition.Operation && reflect.DeepEqual(binding.Changes, transition.Changes) {
				return nil
			}
		}
		return errors.New("transition is not a member of the verified sessionrepo evidence ledger")
	}
	request := publicationpkg.Request{LiveRoot: liveRoot, SealedRoot: sealedRoot,
		Chain: delta.ChainSpec{Baseline: evidence.Baseline.Manifest, Sealed: evidence.Generations[1].Manifest,
			ExpectedBaselineTreeDigest: evidence.Baseline.TreeDigest, ExpectedSealedTreeDigest: evidence.Generations[1].TreeDigest,
			PolicyDigest: evidence.PolicyDigest, MetadataPolicyDigest: evidence.MetadataPolicyDigest},
		Transitions: transitions, OriginProposalID: operation.ProposalID, PublicationID: "sessionrepo-integration-publication",
		VerifyTransition: verifyLedger}
	publicationRepo, err := publicationpkg.Acquire(publicationpkg.Options{WorkflowID: "sessionrepo-integration-workflow", OwnerEpoch: 1, StateRoot: publicationState,
		Journal: publicationJournal, Workspace: workspace.Options{Generation: "unused", MetadataPolicyDigest: metadata,
			Limits: workspace.DefaultLimits(), QuiescentRoot: true, XattrVisibility: xattr}})
	if err != nil {
		t.Fatal(err)
	}
	authorizationCalls := 0
	result, err := publicationRepo.Publish(context.Background(), request, func(binding publicationpkg.CommitBinding) (publicationpkg.Authorization, error) {
		authorizationCalls++
		if binding.OriginProposalID != operation.ProposalID || binding.BaselineTreeDigest != evidence.Baseline.TreeDigest ||
			binding.SealedTreeDigest != mutation.Generation.TreeDigest() || binding.ChainDigest == "" || binding.DeltaDigest == "" {
			return publicationpkg.Authorization{}, errors.New("internal-commit fixture received an incomplete session binding")
		}
		return publicationpkg.Authorization{DecisionID: "sessionrepo-internal-commit-1", BindingDigest: binding.BindingDigest}, nil
	}, nil)
	if err != nil || result.Status != publicationpkg.StatusSucceeded || authorizationCalls != 1 {
		_ = publicationRepo.Close()
		t.Fatalf("publish actual seeded session generation: result=%+v authorizations=%d err=%v", result, authorizationCalls, err)
	}
	if err := publicationRepo.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(livePath, "file.txt")); err != nil || string(got) != "after-store-write" {
		t.Fatalf("published bytes do not match sessionrepo tip: %q err=%v", got, err)
	}
}

func makeSessionRepoTestTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// The managed live root holds the effect oracle's fence directory, and the baseline generation
	// is imported from it.
	if err := os.Mkdir(filepath.Join(root, "lost+found"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "lost+found"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, contents := range files {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(fullPath, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func sessionRepoTestDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sessionRepoMetadataIdentity(kind delta.ObjectType, mode uint32) string {
	value := fmt.Sprintf("tbound-normalized-metadata/v1/type=%s/mode=%04o", kind, mode)
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
