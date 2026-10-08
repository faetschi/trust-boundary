//go:build linux

package sessionlaunch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/sandbox"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

func TestPreparePersistsVerifiedGenerationAndPrivateGitBeforeExposure(t *testing.T) {
	fixture := newSessionFixture(t, false)
	prepared, err := Prepare(context.Background(), fixture.managed, fixture.g0, fixture.admin, fixture.git, fixture.gitProfile)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	provenance := prepared.Provenance()
	if provenance.SchemaVersion != provenanceSchema || provenance.BindingDigest == "" || provenance.SourceGeneration != "g0" ||
		provenance.SourceTreeDigest != fixture.g0.TreeDigest() || provenance.StoreAuditHead == "" || provenance.StoreAuditRecords == 0 ||
		provenance.PrivateGitDigest == "" || provenance.GitExecutableDigest != fixture.gitProfile.ExecutableDigest ||
		provenance.GitExecutionBoundary != fixture.gitProfile.ExecutionBoundary ||
		provenance.PrivateGit.GitExecutableDigest != fixture.gitProfile.ExecutableDigest ||
		provenance.PrivateGit.GitExecutionBoundary != fixture.gitProfile.ExecutionBoundary ||
		provenance.SourceRecord == 0 || provenance.PrivateGitRecord <= provenance.SourceRecord {
		t.Fatalf("incomplete D06 provenance: %+v", provenance)
	}
	if !prepared.HasDurableD06Provenance() {
		t.Fatal("prepared session returned before durable D06 provenance could be verified")
	}
	if err := fixture.managed.Close(); err == nil {
		t.Fatal("managed store closed while prepared D06 session still owned its generation")
	}
	bundle, err := fixture.managed.Store().EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	trace, err := audit.Verify(strings.NewReader(string(bundle.AuditJournal)))
	if err != nil {
		t.Fatal(err)
	}
	var sourceSeq, gitSeq uint64
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case sourceBindingEventKind:
			sourceSeq = record.Sequence
		case privateGitEventKind:
			gitSeq = record.Sequence
		}
	}
	if sourceSeq != provenance.SourceRecord || gitSeq != provenance.PrivateGitRecord {
		t.Fatalf("provenance journal order = source %d, Git %d; receipt = %+v", sourceSeq, gitSeq, provenance)
	}
	if _, err := prepared.ExposeReadOnly(fixture.g0, sessionrepo.ViewRecreated); !errors.Is(err, sessionrepo.ErrExposureBinderUnavailable) {
		t.Fatalf("unconfigured read-only exposure did not fail closed: %v", err)
	}
	if !prepared.HasDurableD06Provenance() {
		t.Fatal("failed exposure attempt erased the durable source provenance")
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("close prepared session: %v", err)
	}
	if err := fixture.managed.Close(); err != nil {
		t.Fatalf("close managed store after D06 teardown: %v", err)
	}
}

func TestPrepareRejectsModifiedRetainedGenerationBeforeProvenanceOrCopy(t *testing.T) {
	fixture := newSessionFixture(t, false)
	generationPath := filepath.Join(fixture.storeRoot, "generations", fixture.g0.ID(), "task.txt")
	if err := os.WriteFile(generationPath, []byte("tampered after sealing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), fixture.managed, fixture.g0, fixture.admin, fixture.git, fixture.gitProfile); err == nil {
		t.Fatal("modified retained generation was accepted")
	}
	trace, err := fixture.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind == sourceBindingEventKind || record.Event.Kind == privateGitEventKind {
			t.Fatalf("D06 provenance was written after source verification failed: %+v", record.Event)
		}
	}
	entries, err := os.ReadDir(fixture.adminPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".tbound-sessionlaunch.lock" {
			t.Fatalf("Git snapshot was created before source verification: %s", entry.Name())
		}
	}
}

func TestPrepareRejectsCorruptedStoreAuditJournalBeforeCopy(t *testing.T) {
	fixture := newSessionFixture(t, false)
	file, err := os.OpenFile(fixture.journalPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("tampered-frame\n")); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := Prepare(context.Background(), fixture.managed, fixture.g0, fixture.admin, fixture.git, fixture.gitProfile); err == nil {
		t.Fatal("corrupt store audit journal was accepted")
	}
	entries, err := os.ReadDir(fixture.adminPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".tbound-sessionlaunch.lock" {
			t.Fatalf("private Git copy preceded audit-chain verification: %s", entry.Name())
		}
	}
}

func TestPrepareRejectsLiveGitMetadataInSealedSource(t *testing.T) {
	fixture := newSessionFixture(t, true)
	if _, err := Prepare(context.Background(), fixture.managed, fixture.g0, fixture.admin, fixture.git, fixture.gitProfile); !errors.Is(err, privategit.ErrSourceContainsGit) {
		t.Fatalf("live .git metadata was not refused before copy: %v", err)
	}
	trace, err := fixture.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind == sourceBindingEventKind || record.Event.Kind == privateGitEventKind {
			t.Fatalf("refused live Git source was durably marked prepared: %+v", record.Event)
		}
	}
}

func TestPrivateAdminParentIsExclusiveAndModeBound(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	first, err := OpenAdminParent(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAdminParent(root); err == nil {
		t.Fatal("concurrent admin parent owner acquired the same lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(rootPath, "orphaned-private-git")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAdminParent(root); !errors.Is(err, ErrPrivateParent) {
		t.Fatalf("parent with stale private Git residue was admitted: %v", err)
	}
	if info, err := os.Lstat(stale); err != nil || !info.IsDir() {
		t.Fatalf("stale private Git residue was silently removed: info=%v err=%v", info, err)
	}
	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAdminParent(root); err != nil {
		t.Fatalf("released private admin parent lock was not reacquirable: %v", err)
	}
}

func TestMeasuredDevelopmentRunnerUsesDurableDisposableCommandView(t *testing.T) {
	runner, err := NewDevelopmentCommandRunner(sandbox.Limits{})
	if err != nil {
		t.Skipf("measured development sandbox profile is unavailable; no direct-host fallback: %v", err)
	}
	base, err := os.MkdirTemp(os.TempDir(), "tbound-sessionlaunch-cell-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	storePath, sourcePath, adminPath := filepath.Join(base, "store"), filepath.Join(base, "source"), filepath.Join(base, "private-admin")
	for _, path := range []string{storePath, sourcePath, adminPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sourcePath, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(base, "session-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	leaseID := "fixture-lease-0001"
	policyDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	metadataDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	xattrDigest := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	options := sessionrepo.Options{
		PolicyDigest: policyDigest, MetadataPolicyDigest: metadataDigest, Limits: workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: xattrDigest, Complete: true}, Journal: journal,
		AuthorizeOperation: func(request sessionrepo.OperationRequest) error {
			if request.Tool != "bash" || request.Operation.Kind != delta.OperationLease || request.Operation.LeaseID != leaseID ||
				request.InputGeneration != "g0" || request.Decision.ID != "fixture-decision" || request.Decision.PolicyDigest != policyDigest {
				return errors.New("test-only Bash authority refused operation")
			}
			return runner.RegisterLease(request.ViewID, request.Operation.LeaseID)
		},
		VerifyDecision: func(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
			if decision.ID != "fixture-decision" || decision.PolicyDigest != policyDigest || decision.MetadataPolicyDigest != metadataDigest ||
				decision.Outcome != delta.PolicyAllow || binding.Tool != "bash" || binding.Operation.Kind != delta.OperationLease ||
				binding.Operation.LeaseID != leaseID || binding.InputGeneration != "g0" || binding.OutputGeneration != "g1" {
				return errors.New("test-only transition binding mismatch")
			}
			return nil
		},
		VerifySettlement: runner.VerifySettlement,
	}
	root, err := os.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := CreateStore(root, options)
	_ = root.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	g0, err := managed.Store().Seed(source, sessionrepo.RootAttestation{Quiescent: true}) // synthetic source fixture
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	adminRoot, err := os.Open(adminPath)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := OpenAdminParent(adminRoot)
	_ = adminRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	git, gitProfile := pinFixtureGit(t)
	prepared, err := Prepare(context.Background(), managed, g0, admin, git, gitProfile)
	if err != nil {
		t.Fatalf("prepare D06 source for sealed command view: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	mutation, err := managed.Store().RunBash(context.Background(), g0,
		delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: leaseID},
		delta.PolicyDecision{ID: "fixture-decision", Outcome: delta.PolicyAllow, PolicyDigest: policyDigest, MetadataPolicyDigest: metadataDigest},
		sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "printf 'after\\n' > task.txt"}}, runner)
	if err != nil {
		t.Fatalf("sandbox command/sessionrepo operation: %v", err)
	}
	if mutation.Generation == nil || mutation.Generation.ID() != "g1" || mutation.Command == nil || !mutation.Command.ExitObserved ||
		mutation.Command.ExitCode != 0 || !prepared.HasDurableD06Provenance() {
		t.Fatalf("durable development command result lost generations/provenance: mutation=%+v command=%+v provenance=%+v", mutation, mutation.Command, prepared.Provenance())
	}
	content, err := os.ReadFile(filepath.Join(storePath, "generations", "g1", "task.txt"))
	if err != nil || string(content) != "after\n" {
		t.Fatalf("sealed g1 command output = %q, err=%v", content, err)
	}
	if strings.Contains(runner.Profile(), "non-claim-bearing") == false {
		t.Fatalf("development sandbox did not retain its non-claim-bearing profile: %q", runner.Profile())
	}
}

type sessionFixture struct {
	base        string
	storeRoot   string
	adminPath   string
	journalPath string
	journal     *audit.Journal
	managed     *ManagedStore
	g0          *sessionrepo.Generation
	admin       *AdminParent
	git         privategit.PinnedGit
	gitProfile  GitProfile
}

func newSessionFixture(t *testing.T, withGit bool) *sessionFixture {
	t.Helper()
	base, err := os.MkdirTemp(os.TempDir(), "tbound-sessionlaunch-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	storePath := filepath.Join(base, "store")
	adminPath := filepath.Join(base, "private-admin")
	sourcePath := filepath.Join(base, "source")
	for _, path := range []string{storePath, adminPath, sourcePath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sourcePath, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	if withGit {
		if err := os.Mkdir(filepath.Join(sourcePath, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourcePath, ".git", "config"), []byte("[core]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	journalPath := filepath.Join(base, "session-audit.jsonl")
	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	root, err := os.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	metadataDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	xattrDigest := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	store, err := CreateStore(root, sessionrepo.Options{
		PolicyDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetadataPolicyDigest: metadataDigest, Limits: workspace.DefaultLimits(),
		XattrVisibility:    workspace.XattrVisibilityAttestation{ProfileDigest: xattrDigest, Complete: true},
		Journal:            journal,
		AuthorizeOperation: func(sessionrepo.OperationRequest) error { return errors.New("fixture has no operation authority") },
		VerifyDecision: func(delta.PolicyDecision, delta.TransitionBinding) error {
			return errors.New("fixture has no decision authority")
		},
		VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error {
			return errors.New("fixture has no settlement authority")
		},
	})
	_ = root.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	g0, err := store.Store().Seed(source, sessionrepo.RootAttestation{Quiescent: true}) // synthetic fixture only
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	adminRoot, err := os.Open(adminPath)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := OpenAdminParent(adminRoot)
	_ = adminRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	git, gitProfile := pinFixtureGit(t)
	return &sessionFixture{base: base, storeRoot: storePath, adminPath: adminPath, journalPath: journalPath,
		journal: journal, managed: store, g0: g0, admin: admin, git: git, gitProfile: gitProfile}
}

func pinFixtureGit(t *testing.T) (privategit.PinnedGit, GitProfile) {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("installed Git unavailable: %v", err)
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	expected := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	git, err := privategit.PinGitExecutable(path, privategit.GitExecutableApproval{
		ExpectedDigest: expected, Boundary: "test-only-supervisor-private-admin-fixture",
		Verifier: func(identity privategit.GitExecutableIdentity) error {
			if identity.Digest != expected || identity.Mode&0o022 != 0 {
				return errors.New("fixture Git byte/permission verification failed")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = git.Close() })
	return git, GitProfile{ExecutableDigest: expected, ExecutionBoundary: "test-only-supervisor-private-admin-fixture"}
}
