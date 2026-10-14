//go:build linux

package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// newD09SourceFixture builds real sessionrepo/privategit/journal state but no
// Binder. Exporting a source descriptor is Store/D06 work; no host mount is
// attempted or simulated by this fixture.
func newD09SourceFixture(t *testing.T) *sessionFixture {
	t.Helper()
	base, err := os.MkdirTemp(os.TempDir(), "tbound-sessionlaunch-exposure-")
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
	managed, err := CreateStore(root, sessionrepo.Options{
		PolicyDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Limits:               workspace.DefaultLimits(),
		XattrVisibility:      workspace.XattrVisibilityAttestation{ProfileDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Complete: true},
		Journal:              journal,
		// These synthetic authorities are test fixtures only; they never mint a
		// production profile/consumer mount proof. They permit one real Store write
		// in the stale-tip test below.
		AuthorizeOperation: func(sessionrepo.OperationRequest) error { return nil },
		VerifyDecision:     func(delta.PolicyDecision, delta.TransitionBinding) error { return nil },
		VerifySettlement:   func(sessionrepo.CommandSettlement, string, string) error { return nil },
	})
	_ = root.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	g0, err := managed.Store().Seed(source, sessionrepo.RootAttestation{Quiescent: true})
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
		journal: journal, managed: managed, g0: g0, admin: admin, git: git, gitProfile: gitProfile}
}

func prepareD09SourceFixture(t *testing.T) (*sessionFixture, *PreparedSession) {
	t.Helper()
	fixture := newD09SourceFixture(t)
	prepared, err := Prepare(context.Background(), fixture.managed, fixture.g0, fixture.admin, fixture.git, fixture.gitProfile)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	return fixture, prepared
}

type d09SourceContractForTest interface {
	SourceCapability() *sessionrepo.VerifiedSealedSource
	SourceEvidence() (sessionrepo.SealedSourceEvidence, error)
	ExportD09WorkerSource(context.Context) (*os.File, sessionrepo.SealedSourceEvidence, error)
	VerifyD09WorkerSource(context.Context, sessionrepo.SealedSourceEvidence) error
	Close() error
}

type d09RuntimeSourceContractForTest interface {
	ExportD09RuntimeAssetSource(context.Context) (*os.File, piruntime.WorkerRuntimeAssetEvidence, error)
	VerifyD09RuntimeAssetSource(context.Context, piruntime.WorkerRuntimeAssetEvidence) error
	Close() error
}

var _ d09SourceContractForTest = (*VerifiedD09ExposureSource)(nil)
var _ d09RuntimeSourceContractForTest = (*VerifiedD09RuntimeAssetSource)(nil)

func TestD09SourceCapabilitiesDoNotSatisfyLegacyConsumerViewInterfaces(t *testing.T) {
	var source any = (*VerifiedD09ExposureSource)(nil)
	if _, ok := source.(piruntime.VerifiedWorkerExposure); ok {
		t.Fatal("D09 host SOURCE capability must not claim the legacy read-only consumer-view interface")
	}
	var runtimeSource any = (*VerifiedD09RuntimeAssetSource)(nil)
	if _, ok := runtimeSource.(piruntime.VerifiedWorkerRuntimeAssets); ok {
		t.Fatal("D09 runtime SOURCE capability must not claim the legacy content/consumer interface")
	}
	var contentOnly any = (*VerifiedRuntimeAssets)(nil)
	if _, ok := contentOnly.(piruntime.VerifiedWorkerRuntimeAssets); ok {
		t.Fatal("content-only runtime asset fixture must not satisfy the D09 source/consumer interface")
	}
}

func TestVerifiedD09ExposureSourceExportsStoreOwnedSourceNotPrivateGitRoot(t *testing.T) {
	fixture, prepared := prepareD09SourceFixture(t)
	exposure, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), fixture.g0)
	if err != nil {
		t.Fatalf("OpenVerifiedD09ExposureSource without a Binder: %v", err)
	}
	t.Cleanup(func() { _ = exposure.Close() })

	descriptor, evidence, err := exposure.ExportD09WorkerSource(context.Background())
	if err != nil {
		t.Fatalf("ExportD09WorkerSource: %v", err)
	}
	defer descriptor.Close()

	if evidence.GenerationID != fixture.g0.ID() || evidence.TreeDigest != DigestBytes([]byte(fixture.g0.TreeDigest())) ||
		evidence.ManifestDigest == "" || evidence.SourceBindingDigest == "" || evidence.ProvenanceDigest == "" ||
		evidence.SourceIdentityDigest == "" || evidence.SourceDevice == 0 || evidence.SourceInode == 0 || evidence.SourceMountID == 0 {
		t.Fatalf("incomplete sealed SOURCE evidence: %+v", evidence)
	}
	info, err := descriptor.Stat()
	if err != nil || !info.IsDir() {
		t.Fatalf("exported descriptor is not a directory: info=%v err=%v", info, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != evidence.SourceDevice || stat.Ino != evidence.SourceInode {
		t.Fatalf("descriptor identity differs from evidence: stat=%+v evidence=%+v", stat, evidence)
	}

	// D06: the worker-visible descriptor must not be the private Git admin root
	// or the session Store root.
	adminInfo, err := fixture.admin.root.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info, adminInfo) {
		t.Fatal("D09 source aliases the supervisor-private Git admin root")
	}
	storeInfo, err := os.Stat(fixture.storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info, storeInfo) {
		t.Fatal("D09 source aliases the session Store root")
	}
	if containsGitMetadata(fixture.g0.Manifest()) {
		t.Fatal("selected generation manifest carries live .git metadata")
	}

	if err := exposure.VerifyD09WorkerSource(context.Background(), evidence); err != nil {
		t.Fatalf("VerifyD09WorkerSource source revalidation: %v", err)
	}
	capability := exposure.SourceCapability()
	if capability == nil {
		t.Fatal("opaque sessionrepo source capability is absent")
	}
	if err := fixture.managed.Store().Close(); !errors.Is(err, sessionrepo.ErrSealedSourceOutstanding) {
		t.Fatalf("Store teardown did not retain the source-cap owner: %v", err)
	}
	sourceEvidence, err := capability.Evidence()
	if err != nil || sourceEvidence != evidence {
		t.Fatalf("common cap source evidence differs: cap=%+v worker=%+v err=%v", sourceEvidence, evidence, err)
	}
	// This export proves only source identity. No host read-only bind/consumer
	// mount was done by the sessionlaunch fixture; D09 must supply a separate
	// consumer mount proof before Node can start.
	if err := capability.Revalidate(context.Background(), sourceEvidence); err != nil {
		t.Fatalf("opaque Store-issued source cap did not revalidate: %v", err)
	}
	// A fabricated evidence record with the same labels but a different
	// descriptor identity must not verify.
	forged := evidence
	forged.SourceInode++
	if err := exposure.VerifyD09WorkerSource(context.Background(), forged); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("forged descriptor identity was not refused: %v", err)
	}
	if _, _, err := exposure.ExportD09WorkerSource(context.Background()); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("source cap allowed a second exported descriptor: %v", err)
	}
}

func TestD09SourceCapabilityRequiresDurableD06Records(t *testing.T) {
	// A raw Store generation with no sessionlaunch binding/private-Git-ready
	// records cannot mint a D09 source cap, even though Store.Verify succeeds.
	fixture := newSessionFixture(t, false)
	if _, err := fixture.g0.ExportSealedSource(context.Background()); !errors.Is(err, sessionrepo.ErrSealedSourceUnavailable) {
		t.Fatalf("Store without D06 provenance minted a D09 source: %v", err)
	}
	prepared, err := Prepare(context.Background(), fixture.managed, fixture.g0, fixture.admin, fixture.git, fixture.gitProfile)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	if _, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), fixture.g0); err != nil {
		t.Fatalf("D06-prepared source failed without any Binder: %v", err)
	}
}

func TestVerifiedD09ExposureSourceDetectsTamperedGeneration(t *testing.T) {
	fixture, prepared := prepareD09SourceFixture(t)
	exposure, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), fixture.g0)
	if err != nil {
		t.Fatalf("OpenVerifiedD09ExposureSource: %v", err)
	}
	t.Cleanup(func() { _ = exposure.Close() })
	_, evidence, err := exposure.ExportD09WorkerSource(context.Background())
	if err != nil {
		t.Fatalf("ExportD09WorkerSource: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.storeRoot, "generations", fixture.g0.ID(), "task.txt"), []byte("tampered after sealing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exposure.VerifyD09WorkerSource(context.Background(), evidence); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("tampered sealed generation was not refused: %v", err)
	}
	if _, _, err := exposure.ExportD09WorkerSource(context.Background()); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("export after tamper did not fail closed: %v", err)
	}
}

func TestVerifiedD09ExposureSourceDetectsPrivateGitSnapshotTamper(t *testing.T) {
	_, prepared := prepareD09SourceFixture(t)
	exposure, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), prepared.generation)
	if err != nil {
		t.Fatalf("OpenVerifiedD09ExposureSource: %v", err)
	}
	defer exposure.Close()
	_, evidence, err := exposure.ExportD09WorkerSource(context.Background())
	if err != nil {
		t.Fatalf("ExportD09WorkerSource: %v", err)
	}
	ownedRepository, ok := prepared.git.(*privategit.Repository)
	if !ok {
		t.Fatal("fresh test fixture does not own a mutable private Git repository")
	}
	directory := ownedRepository.Root()
	if directory == nil {
		t.Fatal("prepared private Git root missing")
	}
	fd, err := syscall.Openat(int(directory.Fd()), "task.txt", syscall.O_WRONLY|syscall.O_TRUNC|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatalf("open private Git snapshot file through test fixture descriptor: %v", err)
	}
	file := os.NewFile(uintptr(fd), "test-private-git-config")
	if _, err := file.Write([]byte("tampered private Git snapshot\n")); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := exposure.VerifyD09WorkerSource(context.Background(), evidence); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("private-Git private-snapshot tamper was not refused: %v", err)
	}
}

func TestVerifiedD09ExposureSourceRejectsRewrittenAuditPrefix(t *testing.T) {
	fixture, prepared := prepareD09SourceFixture(t)
	source, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), fixture.g0)
	if err != nil {
		t.Fatalf("OpenVerifiedD09ExposureSource: %v", err)
	}
	defer source.Close()
	descriptor, evidence, err := source.ExportD09WorkerSource(context.Background())
	if err != nil {
		t.Fatalf("ExportD09WorkerSource: %v", err)
	}
	defer descriptor.Close()
	file, err := os.OpenFile(fixture.journalPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("{\"tampered_prefix\":true}\n")); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.VerifyD09WorkerSource(context.Background(), evidence); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("rewritten Store audit/D06 prefix was not refused: %v", err)
	}
}

func TestVerifiedD09ExposureSourceCloseKeepsExportCallerOwned(t *testing.T) {
	fixture, prepared := prepareD09SourceFixture(t)
	exposure, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), fixture.g0)
	if err != nil {
		t.Fatalf("OpenVerifiedD09ExposureSource: %v", err)
	}
	descriptor, evidence, err := exposure.ExportD09WorkerSource(context.Background())
	if err != nil {
		t.Fatalf("ExportD09WorkerSource: %v", err)
	}
	if err := exposure.Close(); !errors.Is(err, sessionrepo.ErrSealedSourceExportOpen) {
		t.Fatalf("wrapper released its source cap before the caller-owned FD closed: %v", err)
	}
	if _, err := descriptor.Stat(); err != nil {
		t.Fatalf("wrapper close failure closed caller-owned source fd: %v", err)
	}
	if err := descriptor.Close(); err != nil {
		t.Fatalf("close caller-owned D09 source fd: %v", err)
	}
	if err := exposure.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := exposure.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := descriptor.Stat(); err == nil {
		t.Fatal("caller-owned source FD remained open after its explicit Close")
	}
	if exposure.SourceCapability() != nil {
		t.Fatal("source capability remained available after wrapper Close")
	}
	if _, _, err := exposure.ExportD09WorkerSource(context.Background()); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("export after Close did not fail closed: %v", err)
	}
	if err := exposure.VerifyD09WorkerSource(context.Background(), evidence); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("verify after Close did not fail closed: %v", err)
	}
}

func TestVerifiedD09ExposureSourceRejectsStaleGenerationTip(t *testing.T) {
	fixture, prepared := prepareD09SourceFixture(t)
	current, err := fixture.managed.Store().Write(context.Background(), fixture.g0,
		delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: "source-test-proposal", CallIssuer: "source-test-issuer", CallID: "source-test-call"},
		delta.PolicyDecision{ID: "source-test-decision", Outcome: delta.PolicyAllow,
			PolicyDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		"task.txt", []byte("next generation\n"))
	if err != nil {
		t.Fatalf("write a genuine sealed Store descendant: %v", err)
	}
	if current.Generation == nil || current.Generation.ID() == fixture.g0.ID() {
		t.Fatalf("Store did not advance generation: %+v", current)
	}
	if _, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), fixture.g0); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("stale non-tip generation was accepted: %v", err)
	}
	source, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), current.Generation)
	if err != nil {
		t.Fatalf("current descendant with retained D06/private Git proof was rejected: %v", err)
	}
	defer source.Close()
	_, evidence, err := source.ExportD09WorkerSource(context.Background())
	if err != nil || evidence.GenerationID != current.Generation.ID() {
		t.Fatalf("current selected generation source not exported: evidence=%+v err=%v", evidence, err)
	}
}

func TestIssuedD09SourceRemainsValidAsSealedStoreTipAdvances(t *testing.T) {
	fixture, prepared := prepareD09SourceFixture(t)
	source, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), fixture.g0)
	if err != nil {
		t.Fatalf("issue source for initial current tip: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	descriptor, sourceEvidence, err := source.ExportD09WorkerSource(context.Background())
	if err != nil {
		t.Fatalf("export one trusted staging fd: %v", err)
	}
	defer descriptor.Close()
	capability := source.SourceCapability()
	if capability == nil {
		t.Fatal("opaque source capability missing")
	}
	sourceEvidence, err = source.SourceEvidence()
	if err != nil {
		t.Fatal(err)
	}

	// A successful Store mutation advances the selected tip but leaves the
	// immutable source generation retained. Revalidation must check the captured
	// D06 audit prefix and this exact generation, not require it remain latest.
	current, err := fixture.managed.Store().Write(context.Background(), fixture.g0,
		delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: "advance-proposal", CallIssuer: "advance-issuer", CallID: "advance-call"},
		delta.PolicyDecision{ID: "advance-decision", Outcome: delta.PolicyAllow,
			PolicyDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		"task.txt", []byte("g1 after source issuance\n"))
	if err != nil || current.Generation == nil || current.Generation.ID() == fixture.g0.ID() {
		t.Fatalf("advance Store tip: result=%+v err=%v", current, err)
	}
	if err := capability.Revalidate(context.Background(), sourceEvidence); err != nil {
		t.Fatalf("issued immutable ancestor source became invalid after g0->g1: %v", err)
	}
	if err := source.VerifyD09WorkerSource(context.Background(), sourceEvidence); err != nil {
		t.Fatalf("worker source adapter rejected retained immutable generation after tip advance: %v", err)
	}
}

func TestVerifiedD09ExposureSourceRejectsQuarantinedStore(t *testing.T) {
	fixture, prepared := prepareD09SourceFixture(t)
	source, err := prepared.OpenVerifiedD09ExposureSource(context.Background(), fixture.g0)
	if err != nil {
		t.Fatalf("OpenVerifiedD09ExposureSource: %v", err)
	}
	defer source.Close()
	_, evidence, err := source.ExportD09WorkerSource(context.Background())
	if err != nil {
		t.Fatalf("ExportD09WorkerSource: %v", err)
	}
	_, mutationErr := fixture.managed.Store().Edit(context.Background(), fixture.g0,
		delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: "quarantine-proposal", CallIssuer: "quarantine-issuer", CallID: "quarantine-call"},
		delta.PolicyDecision{ID: "quarantine-decision", Outcome: delta.PolicyAllow,
			PolicyDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		"task.txt", []sessionrepo.Replacement{{OldText: "not present", NewText: "changed"}})
	if mutationErr == nil {
		t.Fatal("fixture preimage mismatch unexpectedly succeeded")
	}
	if err := source.VerifyD09WorkerSource(context.Background(), evidence); !errors.Is(err, ErrD09ExposureSource) {
		t.Fatalf("source remained valid after Store quarantine: %v", err)
	}
}

func TestVerifiedRuntimeAssetsPinsContentAndDetectsTamper(t *testing.T) {
	bundlePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundlePath, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(bundlePath, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundlePath, "src", "worker.js"), []byte("console.log(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	digest, err := runtimeAssetContentDigest(root, defaultRuntimeAssetMaxFiles, defaultRuntimeAssetMaxBytes)
	if err != nil {
		t.Fatalf("runtimeAssetContentDigest: %v", err)
	}
	namespace := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	assets, err := OpenVerifiedRuntimeAssets(root, RuntimeAssetSpec{BundleDigest: digest, NamespaceProfileDigest: namespace})
	if err != nil {
		t.Fatalf("OpenVerifiedRuntimeAssets: %v", err)
	}
	t.Cleanup(func() { _ = assets.Close() })
	descriptor, evidence, err := assets.ExportPinnedRuntimeAssets(context.Background())
	if err != nil {
		t.Fatalf("ExportPinnedRuntimeAssets: %v", err)
	}
	_ = descriptor.Close()
	if evidence.BundleDigest != digest || evidence.NamespaceProfileDigest != namespace ||
		evidence.ViewIdentity == "" || evidence.Device == 0 || evidence.Inode == 0 || evidence.MountID == 0 {
		t.Fatalf("incomplete runtime-asset evidence: %+v", evidence)
	}
	if err := assets.VerifyPinnedRuntimeAssets(context.Background(), evidence); err != nil {
		t.Fatalf("VerifyPinnedRuntimeAssets: %v", err)
	}
	// An invalid namespace/readOnly claim must be refused.
	wrongNamespace := evidence
	wrongNamespace.NamespaceProfileDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := assets.VerifyPinnedRuntimeAssets(context.Background(), wrongNamespace); !errors.Is(err, ErrRuntimeAssets) {
		t.Fatalf("mismatched namespace-profile claim was not refused: %v", err)
	}
	// Content tamper must be detected.
	if err := os.WriteFile(filepath.Join(bundlePath, "src", "worker.js"), []byte("console.log(2)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := assets.VerifyPinnedRuntimeAssets(context.Background(), evidence); !errors.Is(err, ErrRuntimeAssets) {
		t.Fatalf("tampered runtime bundle was not refused: %v", err)
	}
}

func TestOpenVerifiedRuntimeAssetsRefusesMismatchSymlinkAndWritable(t *testing.T) {
	bundlePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundlePath, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	digest, err := runtimeAssetContentDigest(root, defaultRuntimeAssetMaxFiles, defaultRuntimeAssetMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	namespace := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if _, err := OpenVerifiedRuntimeAssets(root, RuntimeAssetSpec{
		BundleDigest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", NamespaceProfileDigest: namespace,
	}); !errors.Is(err, ErrRuntimeAssets) {
		t.Fatalf("wrong pinned digest was not refused: %v", err)
	}
	if _, err := OpenVerifiedRuntimeAssets(root, RuntimeAssetSpec{BundleDigest: digest, NamespaceProfileDigest: "not-a-digest"}); !errors.Is(err, ErrRuntimeAssets) {
		t.Fatalf("invalid namespace profile identity was not refused: %v", err)
	}

	symlinkPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(symlinkPath, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("package.json", filepath.Join(symlinkPath, "link.json")); err != nil {
		t.Fatal(err)
	}
	symlinkRoot, err := os.Open(symlinkPath)
	if err != nil {
		t.Fatal(err)
	}
	defer symlinkRoot.Close()
	if _, err := runtimeAssetContentDigest(symlinkRoot, defaultRuntimeAssetMaxFiles, defaultRuntimeAssetMaxBytes); !errors.Is(err, ErrRuntimeAssets) {
		t.Fatalf("symlinked runtime bundle was not refused: %v", err)
	}

	writablePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(writablePath, "package.json"), []byte("{}\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(writablePath, "package.json"), 0o666); err != nil {
		t.Fatal(err)
	}
	writableRoot, err := os.Open(writablePath)
	if err != nil {
		t.Fatal(err)
	}
	defer writableRoot.Close()
	if _, err := runtimeAssetContentDigest(writableRoot, defaultRuntimeAssetMaxFiles, defaultRuntimeAssetMaxBytes); !errors.Is(err, ErrRuntimeAssets) {
		t.Fatalf("group/other-writable runtime bundle was not refused: %v", err)
	}
}

func TestOpenVerifiedD09RuntimeAssetSourceRefusesWritableHostSource(t *testing.T) {
	bundlePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundlePath, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	digest, err := runtimeAssetContentDigest(root, defaultRuntimeAssetMaxFiles, defaultRuntimeAssetMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = OpenVerifiedD09RuntimeAssetSource(root, RuntimeAssetSpec{
		BundleDigest: digest, NamespaceProfileDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	})
	if !errors.Is(err, ErrD09RuntimeAssetsUnavailable) {
		t.Fatalf("mutable/private user source got a D09 runtime source capability: %v", err)
	}
}
