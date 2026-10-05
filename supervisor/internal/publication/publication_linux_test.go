//go:build linux

package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

type fixture struct {
	t         *testing.T
	root      string
	statePath string
	state     *os.File
	journal   *audit.Journal
	workspace workspace.Options
	live      *os.File
	sealed    *os.File
	request   Request
	policy    string
	metadata  string
}

func newFixture(t *testing.T, liveFiles, baseFiles, middleFiles, finalFiles map[string]string, baseDirs, middleDirs, finalDirs []string) *fixture {
	t.Helper()
	root := t.TempDir()
	livePath, basePath := filepath.Join(root, "live"), filepath.Join(root, "base")
	middlePath, finalPath := filepath.Join(root, "middle"), filepath.Join(root, "sealed")
	makeTree(t, livePath, liveFiles, baseDirs)
	makeTree(t, basePath, baseFiles, baseDirs)
	makeTree(t, middlePath, middleFiles, middleDirs)
	makeTree(t, finalPath, finalFiles, finalDirs)
	statePath := filepath.Join(root, "state")
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := os.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(statePath, "audit.jsonl"))
	if err != nil {
		t.Fatalf("open private audit journal: %v", err)
	}
	policy := testDigest("policy")
	metadata := testDigest("metadata")
	wsOptions := workspace.Options{Generation: "unused", MetadataPolicyDigest: metadata,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: testDigest("xattr-profile"), Complete: true}}
	live, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.Open(basePath)
	if err != nil {
		t.Fatal(err)
	}
	middle, err := os.Open(middlePath)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := os.Open(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	baseManifest, err := workspace.Scan(base, withGeneration(wsOptions, "g0"))
	if err != nil {
		t.Fatalf("scan baseline fixture: %v", err)
	}
	middleManifest, err := workspace.Scan(middle, withGeneration(wsOptions, "g1"))
	if err != nil {
		t.Fatalf("scan intermediate fixture: %v", err)
	}
	sealedManifest, err := workspace.Scan(sealed, withGeneration(wsOptions, "g2"))
	if err != nil {
		t.Fatalf("scan sealed fixture: %v", err)
	}
	if liveSnapshot, err := workspace.Scan(live, withGeneration(wsOptions, "g0")); err != nil || liveSnapshot.TreeDigest != baseManifest.TreeDigest {
		t.Fatalf("live fixture differs from baseline: snapshot=%+v err=%v", liveSnapshot, err)
	}
	transitions := []delta.Transition{
		makeTransition(t, baseManifest, middleManifest, policy, metadata, 1),
		makeTransition(t, middleManifest, sealedManifest, policy, metadata, 2),
	}
	request := Request{LiveRoot: live, SealedRoot: sealed,
		Chain: delta.ChainSpec{Baseline: baseManifest.Manifest, Sealed: sealedManifest.Manifest,
			ExpectedBaselineTreeDigest: baseManifest.TreeDigest, ExpectedSealedTreeDigest: sealedManifest.TreeDigest,
			PolicyDigest: policy, MetadataPolicyDigest: metadata},
		Transitions: transitions, OriginProposalID: "proposal-1", PublicationID: "publication-effect-1",
		VerifyTransition: func(delta.PolicyDecision, delta.TransitionBinding) error { return nil }}
	f := &fixture{t: t, root: root, statePath: statePath, state: state, journal: journal, workspace: wsOptions,
		live: live, sealed: sealed, request: request, policy: policy, metadata: metadata}
	t.Cleanup(func() { f.close() })
	_ = base.Close()
	_ = middle.Close()
	return f
}

func (f *fixture) options(epoch uint64) Options {
	return Options{WorkflowID: "workflow-publication-test", OwnerEpoch: epoch, StateRoot: f.state, Journal: f.journal, Workspace: f.workspace}
}

func (f *fixture) acquire(epoch uint64) *Repository {
	f.t.Helper()
	repo, err := Acquire(f.options(epoch))
	if err != nil {
		f.t.Fatalf("Acquire epoch %d: %v", epoch, err)
	}
	return repo
}

func (f *fixture) close() {
	if f.live != nil {
		_ = f.live.Close()
		f.live = nil
	}
	if f.sealed != nil {
		_ = f.sealed.Close()
		f.sealed = nil
	}
	if f.state != nil {
		_ = f.state.Close()
		f.state = nil
	}
	if f.journal != nil {
		_ = f.journal.Close()
		f.journal = nil
	}
}

func TestPublishComposesCreateOverwriteDeleteRenameAndDirectoryCreate(t *testing.T) {
	base := map[string]string{"over.txt": "old-value", "gone.txt": "remove-me", "move.txt": "rename-me"}
	middle := map[string]string{"over.txt": "middle-value", "gone.txt": "remove-me", "move.txt": "rename-me", "ephemeral.txt": "transient"}
	final := map[string]string{"over.txt": "final-value", "newdir/moved.txt": "rename-me", "created.txt": "new-value"}
	f := newFixture(t, base, base, middle, final, nil, nil, []string{"newdir"})
	repo := f.acquire(1)
	authorized := false
	result, err := repo.Publish(context.Background(), f.request, func(binding CommitBinding) (Authorization, error) {
		authorized = true
		if binding.WorkflowID != "workflow-publication-test" {
			t.Fatal("binding lost workflow identity")
		}
		if binding.OriginProposalID != f.request.OriginProposalID || binding.PublicationID != f.request.PublicationID ||
			binding.BaselineTreeDigest != f.request.Chain.ExpectedBaselineTreeDigest ||
			binding.SealedTreeDigest != f.request.Chain.ExpectedSealedTreeDigest || binding.PolicyDigest != f.policy ||
			binding.MetadataPolicyDigest != f.metadata || binding.ChainDigest == "" || binding.DeltaDigest == "" || binding.BindingDigest == "" {
			t.Fatalf("incomplete internal-commit binding: %+v", binding)
		}
		return Authorization{DecisionID: "publication-decision-1", BindingDigest: binding.BindingDigest}, nil
	}, nil)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !authorized || result.Status != StatusSucceeded {
		t.Fatalf("unexpected publication result: authorized=%t result=%+v", authorized, result)
	}
	if got, err := os.ReadFile(filepath.Join(f.root, "live", "over.txt")); err != nil || string(got) != "final-value" {
		t.Fatalf("overwrite result=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(f.root, "live", "newdir", "moved.txt")); err != nil || string(got) != "rename-me" {
		t.Fatalf("rename result=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(f.root, "live", "created.txt")); err != nil || string(got) != "new-value" {
		t.Fatalf("create result=%q err=%v", got, err)
	}
	for _, name := range []string{"gone.txt", "move.txt", "ephemeral.txt"} {
		if _, err := os.Lstat(filepath.Join(f.root, "live", name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should be absent, stat err=%v", name, err)
		}
	}
	operations := map[string]bool{}
	for _, object := range result.Objects {
		operations[object.Operation] = true
		if object.Status != ObjectSucceeded {
			t.Errorf("object not successful: %+v", object)
		}
	}
	for _, operation := range []string{"create", "overwrite", "delete", "rename", "directory_create"} {
		if !operations[operation] {
			t.Errorf("effective operation %q missing from result: %+v", operation, result.Objects)
		}
	}
	if len(result.Objects) != 6 {
		t.Errorf("net-reverted transient path must not be published; got %d object results: %+v", len(result.Objects), result.Objects)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	assertRetainedOriginal(t, f.root, "old-value")
	assertRetainedOriginal(t, f.root, "remove-me")
}

func TestPublishFromSeededSessionRepositoryEvidence(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "seed-source")
	livePath := filepath.Join(root, "live")
	makeTree(t, sourcePath, map[string]string{"file.txt": "before"}, nil)
	makeTree(t, livePath, map[string]string{"file.txt": "before"}, nil)
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
	policy, metadata := testDigest("session-policy"), testDigest("session-metadata")
	xattr := workspace.XattrVisibilityAttestation{ProfileDigest: testDigest("session-xattr-profile"), Complete: true}
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
			before := delta.ObjectState{Exists: true, Type: delta.ObjectRegular, ContentDigest: testDigest("before"), MetadataIdentity: metadataIdentity(delta.ObjectRegular, 0o644)}
			after := delta.ObjectState{Exists: true, Type: delta.ObjectRegular, ContentDigest: testDigest("after-store-write"), MetadataIdentity: metadataIdentity(delta.ObjectRegular, 0o644)}
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
	request := Request{LiveRoot: liveRoot, SealedRoot: sealedRoot,
		Chain: delta.ChainSpec{Baseline: evidence.Baseline.Manifest, Sealed: evidence.Generations[1].Manifest,
			ExpectedBaselineTreeDigest: evidence.Baseline.TreeDigest, ExpectedSealedTreeDigest: evidence.Generations[1].TreeDigest,
			PolicyDigest: evidence.PolicyDigest, MetadataPolicyDigest: evidence.MetadataPolicyDigest},
		Transitions: transitions, OriginProposalID: operation.ProposalID, PublicationID: "sessionrepo-integration-publication",
		VerifyTransition: verifyLedger}
	publication, err := Acquire(Options{WorkflowID: "sessionrepo-integration-workflow", OwnerEpoch: 1, StateRoot: publicationState,
		Journal: publicationJournal, Workspace: workspace.Options{Generation: "unused", MetadataPolicyDigest: metadata,
			Limits: workspace.DefaultLimits(), QuiescentRoot: true, XattrVisibility: xattr}})
	if err != nil {
		t.Fatal(err)
	}
	authorizationCalls := 0
	result, err := publication.Publish(context.Background(), request, func(binding CommitBinding) (Authorization, error) {
		authorizationCalls++
		if binding.OriginProposalID != operation.ProposalID || binding.BaselineTreeDigest != evidence.Baseline.TreeDigest ||
			binding.SealedTreeDigest != mutation.Generation.TreeDigest() || binding.ChainDigest == "" || binding.DeltaDigest == "" {
			return Authorization{}, errors.New("internal-commit fixture received an incomplete session binding")
		}
		return Authorization{DecisionID: "sessionrepo-internal-commit-1", BindingDigest: binding.BindingDigest}, nil
	}, nil)
	if err != nil || result.Status != StatusSucceeded || authorizationCalls != 1 {
		_ = publication.Close()
		t.Fatalf("publish actual seeded session generation: result=%+v authorizations=%d err=%v", result, authorizationCalls, err)
	}
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(livePath, "file.txt")); err != nil || string(got) != "after-store-write" {
		t.Fatalf("published bytes do not match sessionrepo tip: %q err=%v", got, err)
	}
}

func TestPublicationConflictAndAuthorizationBindingFailClosed(t *testing.T) {
	base := map[string]string{"file.txt": "before"}
	middle := map[string]string{"file.txt": "intermediate"}
	final := map[string]string{"file.txt": "after"}
	f := newFixture(t, base, base, middle, final, nil, nil, nil)
	repo := f.acquire(1)
	if err := os.WriteFile(filepath.Join(f.root, "live", "file.txt"), []byte("concurrent edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := repo.Publish(context.Background(), f.request, func(CommitBinding) (Authorization, error) { called = true; return Authorization{}, nil }, nil)
	if !errors.Is(err, ErrBaselineConflict) || called {
		t.Fatalf("baseline conflict must precede authority; called=%t err=%v", called, err)
	}
	_ = repo.Close()
	_ = f.live.Close()
	f.live, err = os.Open(filepath.Join(f.root, "live"))
	if err != nil {
		t.Fatal(err)
	}
	f.request.LiveRoot = f.live
	// Restore the exact fixture preimage only inside this disposable test.
	if err := os.WriteFile(filepath.Join(f.root, "live", "file.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The inode identity differs, but the registered baseline is content and
	// metadata addressed; a fresh scan is still the expected baseline.
	repo = f.acquire(2)
	result, err := repo.Publish(context.Background(), f.request, func(binding CommitBinding) (Authorization, error) {
		return Authorization{DecisionID: "bad-binding-decision", BindingDigest: testDigest("wrong-binding")}, nil
	}, nil)
	if !errors.Is(err, ErrTokenBinding) || result.TransactionID == "" {
		t.Fatalf("wrong receipt should be durable abort: result=%+v err=%v", result, err)
	}
	if got, readErr := os.ReadFile(filepath.Join(f.root, "live", "file.txt")); readErr != nil || string(got) != "before" {
		t.Fatalf("wrong binding mutated live tree: %q, %v", got, readErr)
	}
	_ = repo.Close()
}

func TestPublicationRejectsSymlinkAndHardLinkObjects(t *testing.T) {
	for _, test := range []struct {
		name        string
		breakSealed func(t *testing.T, f *fixture)
	}{
		{name: "symlink", breakSealed: func(t *testing.T, f *fixture) {
			t.Helper()
			target := filepath.Join(f.root, "sealed", "file.txt")
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(f.root, "outside.txt"), target); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hard link", breakSealed: func(t *testing.T, f *fixture) {
			t.Helper()
			target := filepath.Join(f.root, "sealed", "file.txt")
			outside := filepath.Join(f.root, "outside.txt")
			if err := os.WriteFile(outside, []byte("after"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(outside, target); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := map[string]string{"file.txt": "before"}
			f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
			test.breakSealed(t, f)
			repo := f.acquire(1)
			called := false
			_, err := repo.Publish(context.Background(), f.request, func(CommitBinding) (Authorization, error) {
				called = true
				return Authorization{}, nil
			}, nil)
			if err == nil || called {
				t.Fatalf("unsafe sealed object must fail before authorization: called=%t err=%v", called, err)
			}
			if got, readErr := os.ReadFile(filepath.Join(f.root, "live", "file.txt")); readErr != nil || string(got) != "before" {
				t.Fatalf("unsafe source changed the live baseline: %q err=%v", got, readErr)
			}
			_ = repo.Close()
		})
	}
}

func TestPublicationRetainsValidatedSealedSourceDescriptor(t *testing.T) {
	base := map[string]string{"file.txt": "before"}
	f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
	repo := f.acquire(1)
	swapped := false
	result, err := repo.Publish(context.Background(), f.request, allowFor("retained-source"), func(point string, _ int) error {
		if point != "after-source-validation" {
			return nil
		}
		path := filepath.Join(f.root, "sealed", "file.txt")
		if renameErr := os.Rename(path, filepath.Join(f.root, "sealed", "held-file.txt")); renameErr != nil {
			return renameErr
		}
		if writeErr := os.WriteFile(path, []byte("other"), 0o644); writeErr != nil {
			return writeErr
		}
		swapped = true
		return nil
	})
	if err != nil || !swapped || result.Status != StatusSucceeded {
		t.Fatalf("publication did not use validated source descriptor: swapped=%t result=%+v err=%v", swapped, result, err)
	}
	if got, readErr := os.ReadFile(filepath.Join(f.root, "live", "file.txt")); readErr != nil || string(got) != "after" {
		t.Fatalf("published bytes came from the substituted pathname: got=%q err=%v", got, readErr)
	}
	if got, readErr := os.ReadFile(filepath.Join(f.root, "sealed", "file.txt")); readErr != nil || string(got) != "other" {
		t.Fatalf("source-path substitution fixture was not retained: got=%q err=%v", got, readErr)
	}
	_ = repo.Close()
}

func TestPublicationSourceValidationFaultPrecedesDurablePrepare(t *testing.T) {
	base := map[string]string{"file.txt": "before"}
	f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
	repo := f.acquire(1)
	result, err := repo.Publish(context.Background(), f.request, allowFor("source-validation-fault"), func(point string, _ int) error {
		if point == "after-source-validation" {
			return errors.New("stop after descriptor validation")
		}
		return nil
	})
	if !errors.Is(err, ErrInjectedFault) || result.TransactionID != "" {
		t.Fatalf("source-validation fault should precede prepare: result=%+v err=%v", result, err)
	}
	trace, err := f.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if countAuditKind(trace, "publication_prepare") != 0 {
		t.Fatal("source-validation fault appended a prepare record")
	}
	if got, err := os.ReadFile(filepath.Join(f.root, "live", "file.txt")); err != nil || string(got) != "before" {
		t.Fatalf("source-validation fault changed live state: %q err=%v", got, err)
	}
	_ = repo.Close()
}

func TestPublicationRejectsOriginOutsideApprovedProposalCalls(t *testing.T) {
	base := map[string]string{"file.txt": "before"}
	f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
	repo := f.acquire(1)
	request := f.request
	request.OriginProposalID = "unregistered-proposal"
	authorized := false
	if _, err := repo.Publish(context.Background(), request, func(CommitBinding) (Authorization, error) {
		authorized = true
		return Authorization{}, nil
	}, nil); !errors.Is(err, ErrOriginProposal) || authorized {
		t.Fatalf("unregistered proposal must fail before authority: authorized=%t err=%v", authorized, err)
	}
	trace, err := f.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind == "publication_prepare" || record.Event.Kind == "publication_token_consumed" {
			t.Fatalf("wrong origin left a publication effect record: %+v", record.Event)
		}
	}
	if got, err := os.ReadFile(filepath.Join(f.root, "live", "file.txt")); err != nil || string(got) != "before" {
		t.Fatalf("wrong origin mutated live tree: %q err=%v", got, err)
	}
	_ = repo.Close()
}

func TestPublicationOSLockAndOwnerEpochFence(t *testing.T) {
	base := map[string]string{"file.txt": "before"}
	f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
	first := f.acquire(1)
	if _, err := Acquire(f.options(1)); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("competing owner should fail flock, got %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(f.options(1)); !errors.Is(err, ErrOwnerEpoch) {
		t.Fatalf("stale owner epoch should fail, got %v", err)
	}
	second := f.acquire(2)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDifferentWorkflowsAndStateRootsShareLiveRootLock(t *testing.T) {
	base := map[string]string{"file.txt": "before"}
	f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
	first := f.acquire(1)
	firstResult, firstErr := first.Publish(context.Background(), f.request, allowFor("first-owner"), func(point string, _ int) error {
		if point == "after-stage-checkpoint" {
			return errors.New("pause first owner before authorization")
		}
		return nil
	})
	if !errors.Is(firstErr, ErrInjectedFault) || firstResult.TransactionID == "" {
		t.Fatalf("first owner did not pause with a prepared transaction: result=%+v err=%v", firstResult, firstErr)
	}

	otherStatePath := filepath.Join(f.root, "other-state")
	if err := os.Mkdir(otherStatePath, 0o700); err != nil {
		t.Fatal(err)
	}
	otherState, err := os.Open(otherStatePath)
	if err != nil {
		t.Fatal(err)
	}
	otherJournal, err := audit.Open(filepath.Join(otherStatePath, "audit.jsonl"))
	if err != nil {
		_ = otherState.Close()
		t.Fatal(err)
	}
	second, err := Acquire(Options{WorkflowID: "different-workflow", OwnerEpoch: 1, StateRoot: otherState,
		Journal: otherJournal, Workspace: f.workspace})
	if err != nil {
		_ = otherJournal.Close()
		_ = otherState.Close()
		t.Fatal(err)
	}
	request := f.request
	request.PublicationID = "different-workflow-effect"
	authorized := false
	_, err = second.Publish(context.Background(), request, func(CommitBinding) (Authorization, error) {
		authorized = true
		return Authorization{}, nil
	}, nil)
	if !errors.Is(err, ErrLiveRootLockBusy) || authorized {
		t.Fatalf("different repository must not publish the same live inode concurrently: authorized=%t err=%v", authorized, err)
	}
	if got, readErr := os.ReadFile(filepath.Join(f.root, "live", "file.txt")); readErr != nil || string(got) != "before" {
		t.Fatalf("blocked second owner mutated live root: %q err=%v", got, readErr)
	}
	_ = second.Close()
	_ = otherJournal.Close()
	_ = otherState.Close()
	_ = first.Close()
}

func TestDisposableCheckpointRestoreNeverOverwritesLaterLiveEffects(t *testing.T) {
	base := map[string]string{"before.txt": "baseline"}
	middle := map[string]string{"before.txt": "middle"}
	final := map[string]string{"restored.txt": "owned checkpoint"}
	f := newFixture(t, base, base, middle, final, nil, nil, nil)
	if err := os.WriteFile(filepath.Join(f.root, "live", "later-effect.txt"), []byte("later-known"), 0o644); err != nil {
		t.Fatal(err)
	}
	sealedSnapshot, err := workspace.Scan(f.sealed, withGeneration(f.workspace, "g2"))
	if err != nil {
		t.Fatal(err)
	}
	repo := f.acquire(1)
	result, err := repo.RestoreDisposableCheckpoint(f.sealed, "owned-synthetic-checkpoint-7", sealedSnapshot.Manifest, sealedSnapshot.TreeDigest, nil)
	if err != nil {
		t.Fatalf("RestoreDisposableCheckpoint: %v", err)
	}
	if result.Status != StatusSucceeded || result.Root == nil || result.Snapshot.TreeDigest != sealedSnapshot.TreeDigest {
		t.Fatalf("invalid disposable restore: %+v", result)
	}
	restored, err := os.ReadFile(filepath.Join(f.statePath, result.Root.Name(), "restored.txt"))
	if err != nil || string(restored) != "owned checkpoint" {
		t.Fatalf("disposable tree=%q err=%v", restored, err)
	}
	later, err := os.ReadFile(filepath.Join(f.root, "live", "later-effect.txt"))
	if err != nil || string(later) != "later-known" {
		t.Fatalf("later live effect was overwritten: %q err=%v", later, err)
	}
	_ = result.Root.Close()
	_ = repo.Close()
}

func TestDisposableCheckpointRestoreRecoveryBoundaries(t *testing.T) {
	for _, test := range []struct {
		name  string
		point string
		want  Status
	}{
		{name: "intent before destination", point: "after-restore-intent-checkpoint", want: StatusFailed},
		{name: "directory identity checkpoint", point: "after-restore-directory-checkpoint", want: StatusFailed},
		{name: "materialization before terminal checkpoint", point: "after-checkpoint-materialization", want: StatusSucceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := map[string]string{"old.txt": "old"}
			middle := map[string]string{"old.txt": "middle"}
			final := map[string]string{"checkpoint.txt": "sealed"}
			f := newFixture(t, base, base, middle, final, nil, nil, nil)
			if err := os.WriteFile(filepath.Join(f.root, "live", "later.txt"), []byte("later"), 0o644); err != nil {
				t.Fatal(err)
			}
			snapshot, err := workspace.Scan(f.sealed, withGeneration(f.workspace, "g2"))
			if err != nil {
				t.Fatal(err)
			}
			repo := f.acquire(1)
			result, err := repo.RestoreDisposableCheckpoint(f.sealed, "checkpoint-recovery-fixture", snapshot.Manifest, snapshot.TreeDigest,
				func(point string, _ int) error {
					if point == test.point {
						return errors.New("synthetic restore interruption")
					}
					return nil
				})
			if !errors.Is(err, ErrInjectedFault) || result.RestoreID == "" {
				t.Fatalf("expected interrupted restore, result=%+v err=%v", result, err)
			}
			_ = repo.Close()
			repo = f.acquire(2)
			recovered, recoverErr := repo.RecoverDisposableCheckpoint(f.sealed, result.RestoreID, nil)
			if recovered.Status != test.want {
				t.Fatalf("recovery status=%s want=%s err=%v", recovered.Status, test.want, recoverErr)
			}
			if test.want == StatusSucceeded {
				if recoverErr != nil || recovered.Root == nil {
					t.Fatalf("complete materialization should recover: %+v err=%v", recovered, recoverErr)
				}
				_ = recovered.Root.Close()
			} else if recoverErr != nil {
				t.Fatalf("pre-effect restore should classify as known failure: %v", recoverErr)
			}
			if got, readErr := os.ReadFile(filepath.Join(f.root, "live", "later.txt")); readErr != nil || string(got) != "later" {
				t.Fatalf("later known effect changed: %q err=%v", got, readErr)
			}
			_ = repo.Close()
		})
	}
}

func TestDisposableRestoreRecoveryTerminalCheckpointFault(t *testing.T) {
	base := map[string]string{"old.txt": "old"}
	f := newFixture(t, base, base, map[string]string{"old.txt": "middle"}, map[string]string{"checkpoint.txt": "sealed"}, nil, nil, nil)
	snapshot, err := workspace.Scan(f.sealed, withGeneration(f.workspace, "g2"))
	if err != nil {
		t.Fatal(err)
	}
	first := f.acquire(1)
	result, err := first.RestoreDisposableCheckpoint(f.sealed, "restore-recovery-terminal-fixture", snapshot.Manifest, snapshot.TreeDigest,
		func(point string, _ int) error {
			if point == "after-checkpoint-materialization" {
				return errors.New("interrupt before restore terminal record")
			}
			return nil
		})
	if !errors.Is(err, ErrInjectedFault) || result.RestoreID == "" {
		t.Fatalf("expected complete restore interrupted before terminal: result=%+v err=%v", result, err)
	}
	_ = first.Close()
	second := f.acquire(2)
	recovered, err := second.RecoverDisposableCheckpoint(f.sealed, result.RestoreID, func(point string, _ int) error {
		if point == "after-checkpoint-materialization" {
			return errors.New("interrupt recovery before terminal record")
		}
		return nil
	})
	if !errors.Is(err, ErrInjectedFault) || recovered.Status != StatusUnknown {
		t.Fatalf("restore recovery checkpoint fault = %+v err=%v", recovered, err)
	}
	_ = second.Close()
	third := f.acquire(3)
	final, err := third.RecoverDisposableCheckpoint(f.sealed, result.RestoreID, nil)
	if err != nil || final.Status != StatusSucceeded || final.Root == nil {
		t.Fatalf("final restore recovery = %+v err=%v", final, err)
	}
	_ = final.Root.Close()
	_ = third.Close()
}

func TestPublicationFaultBoundariesRecoverByObservationWithoutReplay(t *testing.T) {
	tests := []struct {
		name       string
		point      string
		deletion   bool
		directory  bool
		want       Status
		wantExists bool
	}{
		{name: "stage write before file sync", point: "after-stage-write", want: StatusFailed, wantExists: true},
		{name: "stage durability checkpoint before token", point: "after-stage-checkpoint", want: StatusFailed, wantExists: true},
		{name: "token consumed before first object", point: "after-token-checkpoint", want: StatusFailed, wantExists: true},
		{name: "object start checkpoint", point: "after-object-start-checkpoint", want: StatusFailed, wantExists: true},
		{name: "directory object start checkpoint", point: "after-object-start-checkpoint", directory: true, want: StatusFailed, wantExists: false},
		{name: "rename syscall before sync", point: "after-rename", want: StatusUnknown, wantExists: true},
		{name: "rename parent sync before checkpoint", point: "after-parent-sync", want: StatusUnknown, wantExists: true},
		{name: "rename object checkpoint", point: "after-object-checkpoint", want: StatusSucceeded, wantExists: true},
		{name: "unlink syscall before sync", point: "after-unlink", deletion: true, want: StatusUnknown, wantExists: false},
		{name: "unlink parent sync before checkpoint", point: "after-parent-sync", deletion: true, want: StatusUnknown, wantExists: false},
		{name: "unlink object checkpoint", point: "after-object-checkpoint", deletion: true, want: StatusSucceeded, wantExists: false},
		{name: "mkdir before sync/checkpoint", point: "after-mkdir", directory: true, want: StatusUnknown, wantExists: true},
		{name: "directory parent sync before object checkpoint", point: "after-parent-sync", directory: true, want: StatusUnknown, wantExists: true},
		{name: "directory object checkpoint", point: "after-object-checkpoint", directory: true, want: StatusSucceeded, wantExists: true},
	}
	for caseIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := map[string]string{"file.txt": "before"}
			middle := map[string]string{"file.txt": "middle"}
			final := map[string]string{"file.txt": "after"}
			var finalDirs []string
			if test.deletion {
				final = map[string]string{}
			}
			if test.directory {
				base, middle, final = map[string]string{}, map[string]string{}, map[string]string{}
				finalDirs = []string{"created-dir"}
			}
			f := newFixture(t, base, base, middle, final, nil, nil, finalDirs)
			// Give each table case an independent workflow namespace in the shared
			// package fixture (each test itself has its own journal).
			f.request.PublicationID = fmt.Sprintf("publication-effect-%d", caseIndex+1)
			repo := f.acquire(1)
			result, err := repo.Publish(context.Background(), f.request, allowFor(test.name), func(point string, index int) error {
				if point == test.point {
					return errors.New("synthetic abrupt boundary")
				}
				return nil
			})
			if !errors.Is(err, ErrInjectedFault) {
				t.Fatalf("expected injected fault at %q, result=%+v err=%v", test.point, result, err)
			}
			if result.TransactionID == "" {
				t.Fatal("interrupted publication must return its transaction ID")
			}
			if err := repo.Close(); err != nil {
				t.Fatal(err)
			}
			repo = f.acquire(2)
			recovered, recoverErr := repo.Recover(f.live, result.TransactionID, nil)
			if recovered.Status != test.want {
				t.Fatalf("recovery status got %s, want %s (objects=%+v, err=%v)", recovered.Status, test.want, recovered.Objects, recoverErr)
			}
			if test.want == StatusUnknown && !errors.Is(recoverErr, ErrRecoveryUnknown) {
				t.Fatalf("UNKNOWN must be an explicit pause: %v", recoverErr)
			}
			if test.want != StatusUnknown && recoverErr != nil {
				t.Fatalf("known recovery should complete: %v", recoverErr)
			}
			path := "file.txt"
			if test.directory {
				path = "created-dir"
			}
			_, statErr := os.Lstat(filepath.Join(f.root, "live", path))
			exists := statErr == nil
			if exists != test.wantExists {
				t.Fatalf("post-recovery path %q exists=%t want=%t err=%v", path, exists, test.wantExists, statErr)
			}
			if test.want == StatusFailed && !test.deletion && !test.directory {
				if got, readErr := os.ReadFile(filepath.Join(f.root, "live", "file.txt")); readErr != nil || string(got) != "before" {
					t.Fatalf("preimage should remain untouched: %q err=%v", got, readErr)
				}
			}
			if test.want == StatusSucceeded && !test.deletion && !test.directory {
				if got, readErr := os.ReadFile(filepath.Join(f.root, "live", "file.txt")); readErr != nil || string(got) != "after" {
					t.Fatalf("checkpointed result missing: %q err=%v", got, readErr)
				}
			}
			if test.want == StatusFailed {
				// Recovery has not replayed the consumed token. The original effect
				// identity remains permanently spent.
				if _, replayErr := repo.Publish(context.Background(), f.request, allowFor("replay"), nil); !errors.Is(replayErr, ErrTokenReplay) {
					t.Fatalf("consumed publication identity replay: %v", replayErr)
				}
			}
			_ = repo.Close()
		})
	}
}

func TestPublicationPreparationAndTerminalFaultBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		point     string
		deny      bool
		want      Status
		wantPause bool
	}{
		{name: "durable prepare before artifact", point: "after-prepare-checkpoint", want: StatusFailed},
		{name: "artifact create before identity checkpoint", point: "after-artifact-create", want: StatusUnknown, wantPause: true},
		{name: "artifact parent sync checkpoint", point: "after-artifact-parent-sync", want: StatusFailed},
		{name: "stage file sync before stage checkpoint", point: "after-stage-sync", want: StatusFailed},
		{name: "retained original write", point: "after-backup-write", want: StatusFailed},
		{name: "retained original sync", point: "after-backup-sync", want: StatusFailed},
		{name: "parent identity checkpoint", point: "after-parent-checkpoint", want: StatusFailed},
		{name: "reconcile per-object checkpoint", point: "after-checkpoint", want: StatusSucceeded},
		{name: "terminal checkpoint", point: "after-terminal-checkpoint", want: StatusSucceeded},
		{name: "pre-token abort cleanup unlink", point: "after-unlink", deny: true, want: StatusFailed},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := map[string]string{"file.txt": "before"}
			f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
			f.request.PublicationID = fmt.Sprintf("fault-boundary-%d", index+1)
			first := f.acquire(1)
			authorize := allowFor(test.name)
			if test.deny {
				authorize = func(CommitBinding) (Authorization, error) { return Authorization{}, errors.New("fixture denial") }
			}
			result, err := first.Publish(context.Background(), f.request, authorize, func(point string, objectIndex int) error {
				if point == test.point && (test.point != "after-checkpoint" || objectIndex == 0) {
					return errors.New("synthetic fault at complete boundary")
				}
				return nil
			})
			if !errors.Is(err, ErrInjectedFault) || result.TransactionID == "" {
				t.Fatalf("injection did not interrupt %s: result=%+v err=%v", test.point, result, err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			second := f.acquire(2)
			recovered, recoverErr := second.Recover(f.live, result.TransactionID, nil)
			if recovered.Status != test.want {
				t.Fatalf("recovery at %s = %s, want %s (objects=%+v err=%v)", test.point, recovered.Status, test.want, recovered.Objects, recoverErr)
			}
			if test.wantPause {
				if !errors.Is(recoverErr, ErrRecoveryUnknown) {
					t.Fatalf("ambiguous artifact identity must remain paused: %v", recoverErr)
				}
			} else if recoverErr != nil {
				t.Fatalf("recovery at %s: %v", test.point, recoverErr)
			}
			_ = second.Close()
		})
	}
}

func TestMixedMultiObjectRecoveryDoesNotReplayAnyRemainingOperation(t *testing.T) {
	base := map[string]string{"a.txt": "before-a", "b.txt": "before-b", "c.txt": "before-c"}
	middle := map[string]string{"a.txt": "middle-a", "b.txt": "middle-b", "c.txt": "middle-c"}
	final := map[string]string{"a.txt": "final-a", "b.txt": "final-b", "c.txt": "final-c"}
	f := newFixture(t, base, base, middle, final, nil, nil, nil)
	f.request.PublicationID = "mixed-recovery-effect"
	authorizationCalls := 0
	first := f.acquire(1)
	result, err := first.Publish(context.Background(), f.request, func(binding CommitBinding) (Authorization, error) {
		authorizationCalls++
		return Authorization{DecisionID: "mixed-recovery-decision", BindingDigest: binding.BindingDigest}, nil
	}, func(point string, index int) error {
		if point == "after-rename" && index == 1 {
			return errors.New("interrupt after second rename syscall")
		}
		return nil
	})
	if !errors.Is(err, ErrInjectedFault) || result.TransactionID == "" || authorizationCalls != 1 {
		t.Fatalf("expected interruption after one durable object and one visible uncertain object: result=%+v calls=%d err=%v", result, authorizationCalls, err)
	}
	firstInodes := map[string]uint64{}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		var info syscall.Stat_t
		if err := syscall.Stat(filepath.Join(f.root, "live", name), &info); err != nil {
			t.Fatal(err)
		}
		firstInodes[name] = info.Ino
	}
	beforeTrace, err := f.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	checkpointCount, tokenCount := countAuditKind(beforeTrace, "publication_checkpoint"), countAuditKind(beforeTrace, "publication_token_consumed")
	_ = first.Close()
	second := f.acquire(2)
	recovered, recoverErr := second.Recover(f.live, result.TransactionID, nil)
	if recovered.Status != StatusUnknown || !errors.Is(recoverErr, ErrRecoveryUnknown) {
		t.Fatalf("mixed observation must pause UNKNOWN: result=%+v err=%v", recovered, recoverErr)
	}
	wantStatuses := []ObjectStatus{ObjectSucceeded, ObjectUnknown, ObjectFailed}
	if len(recovered.Objects) != len(wantStatuses) {
		t.Fatalf("mixed recovery returned %d object results, want %d: %+v", len(recovered.Objects), len(wantStatuses), recovered.Objects)
	}
	for i, want := range wantStatuses {
		if recovered.Objects[i].Status != want {
			t.Errorf("object %s status=%s want=%s detail=%s", recovered.Objects[i].Path, recovered.Objects[i].Status, want, recovered.Objects[i].Detail)
		}
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		var info syscall.Stat_t
		if err := syscall.Stat(filepath.Join(f.root, "live", name), &info); err != nil {
			t.Fatal(err)
		}
		if info.Ino != firstInodes[name] {
			t.Errorf("recovery replayed or replaced %s: inode %d became %d", name, firstInodes[name], info.Ino)
		}
	}
	if authorizationCalls != 1 {
		t.Fatalf("recovery re-invoked commit authorization %d times", authorizationCalls-1)
	}
	afterTrace, err := f.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if countAuditKind(afterTrace, "publication_checkpoint") != checkpointCount || countAuditKind(afterTrace, "publication_token_consumed") != tokenCount {
		t.Fatalf("recovery appended object/token effects: checkpoints %d->%d tokens %d->%d", checkpointCount,
			countAuditKind(afterTrace, "publication_checkpoint"), tokenCount, countAuditKind(afterTrace, "publication_token_consumed"))
	}
	_, retryErr := second.Recover(f.live, result.TransactionID, nil)
	if !errors.Is(retryErr, ErrRecoveryUnknown) {
		t.Fatalf("terminal UNKNOWN changed on repeat observation: %v", retryErr)
	}
	_ = second.Close()
}

func countAuditKind(trace audit.Trace, kind string) int {
	count := 0
	for _, record := range trace.Records {
		if record.Event.Kind == kind {
			count++
		}
	}
	return count
}

func TestPublicationRecoveryRejectsTamperedDurableStateRecord(t *testing.T) {
	base := map[string]string{"file.txt": "before"}
	f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
	repo := f.acquire(1)
	result, err := repo.Publish(context.Background(), f.request, allowFor("tamper-record"), func(point string, _ int) error {
		if point == "after-token-checkpoint" {
			return errors.New("stop before first object effect")
		}
		return nil
	})
	if !errors.Is(err, ErrInjectedFault) || result.TransactionID == "" {
		t.Fatalf("expected durable token checkpoint before tampering: result=%+v err=%v", result, err)
	}
	_ = repo.Close()
	if err := f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	f.journal = nil
	path := filepath.Join(f.statePath, "audit.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	needle := []byte("publication_token_consumed")
	index := bytes.Index(data, needle)
	if index < 0 {
		t.Fatal("durable token record not found for tamper fixture")
	}
	data[index+len(needle)-1] = 'D'
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(path)
	if journal != nil {
		_ = journal.Close()
	}
	if !errors.Is(err, audit.ErrCorrupt) {
		t.Fatalf("tampered recovery record must fail audit-chain verification, got %v", err)
	}
}

func TestRecoveryKeepsTamperedCheckpointedLiveStateUnknown(t *testing.T) {
	base := map[string]string{"file.txt": "before"}
	f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
	authorizationCalls := 0
	first := f.acquire(1)
	result, err := first.Publish(context.Background(), f.request, func(binding CommitBinding) (Authorization, error) {
		authorizationCalls++
		return Authorization{DecisionID: "tampered-live-state-decision", BindingDigest: binding.BindingDigest}, nil
	}, func(point string, index int) error {
		if point == "after-object-checkpoint" && index == 0 {
			return errors.New("interrupt after durable applied checkpoint")
		}
		return nil
	})
	if !errors.Is(err, ErrInjectedFault) || result.TransactionID == "" {
		t.Fatalf("expected interruption after apply checkpoint: result=%+v err=%v", result, err)
	}
	path := filepath.Join(f.root, "live", "file.txt")
	if err := os.WriteFile(path, []byte("later-tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	var beforeRecovery syscall.Stat_t
	if err := syscall.Stat(path, &beforeRecovery); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	second := f.acquire(2)
	recovered, recoverErr := second.Recover(f.live, result.TransactionID, nil)
	if recovered.Status != StatusUnknown || !errors.Is(recoverErr, ErrRecoveryUnknown) || len(recovered.Objects) != 1 || recovered.Objects[0].Status != ObjectUnknown {
		t.Fatalf("tampered checkpoint observation must stay UNKNOWN: result=%+v err=%v", recovered, recoverErr)
	}
	got, readErr := os.ReadFile(path)
	var afterRecovery syscall.Stat_t
	statErr := syscall.Stat(path, &afterRecovery)
	if readErr != nil || string(got) != "later-tampered" || statErr != nil || beforeRecovery.Ino != afterRecovery.Ino {
		t.Fatalf("recovery replayed/rolled back over tampered state: content=%q read=%v inode=%d->%d stat=%v", got, readErr, beforeRecovery.Ino, afterRecovery.Ino, statErr)
	}
	if authorizationCalls != 1 {
		t.Fatalf("recovery re-invoked authorization: calls=%d", authorizationCalls)
	}
	_ = second.Close()
}

func allowFor(label string) Authorizer {
	return func(binding CommitBinding) (Authorization, error) {
		return Authorization{DecisionID: "decision-" + strings.ReplaceAll(label, " ", "-"), BindingDigest: binding.BindingDigest}, nil
	}
}

func makeTransition(t *testing.T, before, after workspace.Snapshot, policy, metadata string, sequence uint64) delta.Transition {
	t.Helper()
	changes := manifestDiff(before.Manifest, after.Manifest)
	argument := sha256.Sum256([]byte(fmt.Sprintf("args-%d", sequence)))
	return delta.Transition{ID: fmt.Sprintf("transition-%d", sequence), Sequence: sequence, Tool: "write",
		ArgumentDigest: "sha256:" + hex.EncodeToString(argument[:]), InputGeneration: before.Manifest.Generation,
		OutputGeneration: after.Manifest.Generation, InputTreeDigest: before.TreeDigest, OutputTreeDigest: after.TreeDigest,
		Operation: delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: fmt.Sprintf("proposal-%d", sequence), CallIssuer: "fixture-provider", CallID: fmt.Sprintf("call-%d", sequence)},
		Changes:   changes, Decision: delta.PolicyDecision{ID: fmt.Sprintf("decision-%d", sequence), Outcome: delta.PolicyAllow, PolicyDigest: policy, MetadataPolicyDigest: metadata}}
}

func manifestDiff(before, after delta.TreeManifest) []delta.ObjectChange {
	left, right := map[string]delta.ObjectState{}, map[string]delta.ObjectState{}
	for _, object := range before.Objects {
		left[object.Path] = object.State
	}
	for _, object := range after.Objects {
		right[object.Path] = object.State
	}
	paths := map[string]bool{}
	for path := range left {
		paths[path] = true
	}
	for path := range right {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sortStrings(ordered)
	changes := make([]delta.ObjectChange, 0, len(ordered))
	for _, path := range ordered {
		beforeState, afterState := left[path], right[path]
		if beforeState != afterState {
			changes = append(changes, delta.ObjectChange{Path: path, Before: beforeState, After: afterState})
		}
	}
	return changes
}

func makeTree(t *testing.T, root string, files map[string]string, directories []string) {
	t.Helper()
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	allDirs := map[string]bool{}
	for _, dir := range directories {
		allDirs[dir] = true
	}
	for path := range files {
		parent := filepath.Dir(path)
		for parent != "." && parent != "" {
			allDirs[parent] = true
			parent = filepath.Dir(parent)
		}
	}
	dirs := make([]string, 0, len(allDirs))
	for dir := range allDirs {
		dirs = append(dirs, dir)
	}
	sortStrings(dirs)
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func withGeneration(options workspace.Options, generation string) workspace.Options {
	options.Generation = generation
	return options
}

func testDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func assertRetainedOriginal(t *testing.T, root, value string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(filepath.Join(root, "live")))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".tbound-publication-") {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if !strings.HasPrefix(file.Name(), "original-") {
				continue
			}
			contents, err := os.ReadFile(filepath.Join(root, entry.Name(), file.Name()))
			if err == nil && string(contents) == value {
				return
			}
		}
	}
	t.Fatalf("retained original %q not found in target-filesystem evidence directory", value)
}
