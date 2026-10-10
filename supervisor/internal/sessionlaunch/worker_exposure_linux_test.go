//go:build linux

package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// recordingBinder is a no-privilege sessionrepo.Binder used by the exposure
// tests. It records the bind/unbind calls but does not perform a real mount, so
// the tests never require mount privileges. It is a test seam only; production
// uses sessionrepo.LinuxReadOnlyBinder.
type recordingBinder struct {
	mu    sync.Mutex
	binds map[string]bool
}

func (b *recordingBinder) BindReadOnly(source *os.File, target string) error {
	if source == nil || !filepath.IsAbs(target) {
		return errors.New("recording binder requires a pinned source and absolute target")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.binds == nil {
		b.binds = make(map[string]bool)
	}
	b.binds[target] = true
	return nil
}

func (b *recordingBinder) Unmount(target string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.binds, target)
	return nil
}

// newExposureFixture builds the same real Store/Git/admin fixture as
// newSessionFixture but with a registered exposure binder, which
// ExposeReadOnly requires.
func newExposureFixture(t *testing.T) *sessionFixture {
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
		ExposureBinder:       &recordingBinder{},
		AuthorizeOperation: func(sessionrepo.OperationRequest) error {
			return errors.New("exposure fixture has no operation authority")
		},
		VerifyDecision: func(delta.PolicyDecision, delta.TransitionBinding) error {
			return errors.New("exposure fixture has no decision authority")
		},
		VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error {
			return errors.New("exposure fixture has no settlement authority")
		},
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

func prepareExposureFixture(t *testing.T) (*sessionFixture, *PreparedSession) {
	t.Helper()
	fixture := newExposureFixture(t)
	prepared, err := Prepare(context.Background(), fixture.managed, fixture.g0, fixture.admin, fixture.git, fixture.gitProfile)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	return fixture, prepared
}

func TestSealedWorkerExposureBindsRegisteredViewNotPrivateGitRoot(t *testing.T) {
	fixture, prepared := prepareExposureFixture(t)
	exposure, err := prepared.OpenSealedWorkerExposure(fixture.g0)
	if err != nil {
		t.Fatalf("OpenSealedWorkerExposure: %v", err)
	}
	t.Cleanup(func() { _ = exposure.Close() })

	descriptor, evidence, err := exposure.ExportVerifiedWorkerExposure(context.Background())
	if err != nil {
		t.Fatalf("ExportVerifiedWorkerExposure: %v", err)
	}
	defer descriptor.Close()

	expectedTreeDigest := DigestBytes([]byte("tbound/sessionlaunch/worker-tree/v1\x00" + fixture.g0.TreeDigest()))
	if evidence.GenerationID != fixture.g0.ID() || evidence.TreeDigest != expectedTreeDigest ||
		evidence.ManifestDigest == "" || evidence.BindingDigest == "" || evidence.ProvenanceDigest == "" ||
		evidence.ViewIdentity == "" || evidence.Device == 0 || evidence.Inode == 0 || evidence.MountID == 0 {
		t.Fatalf("incomplete sealed exposure evidence: %+v", evidence)
	}
	info, err := descriptor.Stat()
	if err != nil || !info.IsDir() {
		t.Fatalf("exported descriptor is not a directory: info=%v err=%v", info, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != evidence.Device || stat.Ino != evidence.Inode {
		t.Fatalf("descriptor identity differs from evidence: stat=%+v evidence=%+v", stat, evidence)
	}

	// D06: the worker-visible descriptor must not be the private Git admin root
	// or the session Store root.
	adminInfo, err := fixture.admin.root.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info, adminInfo) {
		t.Fatal("worker exposure aliases the supervisor-private Git admin root")
	}
	storeInfo, err := os.Stat(fixture.storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info, storeInfo) {
		t.Fatal("worker exposure aliases the session Store root")
	}
	if containsGitMetadata(fixture.g0.Manifest()) {
		t.Fatal("selected generation manifest carries live .git metadata")
	}

	if err := exposure.VerifyWorkerExposure(context.Background(), evidence); err != nil {
		t.Fatalf("VerifyWorkerExposure: %v", err)
	}
	// A fabricated evidence record with the same labels but a different
	// descriptor identity must not verify.
	forged := evidence
	forged.Inode++
	if err := exposure.VerifyWorkerExposure(context.Background(), forged); !errors.Is(err, ErrWorkerExposureContract) {
		t.Fatalf("forged descriptor identity was not refused: %v", err)
	}
}

func TestSealedWorkerExposureRefusesUnboundStore(t *testing.T) {
	// newSessionFixture does not register an exposure binder, so the concrete
	// adapter must fail closed instead of fabricating a read-only view.
	fixture := newSessionFixture(t, false)
	prepared, err := Prepare(context.Background(), fixture.managed, fixture.g0, fixture.admin, fixture.git, fixture.gitProfile)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	if _, err := prepared.OpenSealedWorkerExposure(fixture.g0); !errors.Is(err, sessionrepo.ErrExposureBinderUnavailable) {
		t.Fatalf("exposure without a registered binder did not fail closed: %v", err)
	}
}

func TestSealedWorkerExposureDetectsTamperedGeneration(t *testing.T) {
	fixture, prepared := prepareExposureFixture(t)
	exposure, err := prepared.OpenSealedWorkerExposure(fixture.g0)
	if err != nil {
		t.Fatalf("OpenSealedWorkerExposure: %v", err)
	}
	t.Cleanup(func() { _ = exposure.Close() })
	_, evidence, err := exposure.ExportVerifiedWorkerExposure(context.Background())
	if err != nil {
		t.Fatalf("ExportVerifiedWorkerExposure: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.storeRoot, "generations", fixture.g0.ID(), "task.txt"), []byte("tampered after sealing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exposure.VerifyWorkerExposure(context.Background(), evidence); !errors.Is(err, ErrWorkerExposureContract) {
		t.Fatalf("tampered sealed generation was not refused: %v", err)
	}
	if _, _, err := exposure.ExportVerifiedWorkerExposure(context.Background()); !errors.Is(err, ErrWorkerExposureContract) {
		t.Fatalf("export after tamper did not fail closed: %v", err)
	}
}

func TestSealedWorkerExposureCloseWithdrawsView(t *testing.T) {
	fixture, prepared := prepareExposureFixture(t)
	exposure, err := prepared.OpenSealedWorkerExposure(fixture.g0)
	if err != nil {
		t.Fatalf("OpenSealedWorkerExposure: %v", err)
	}
	_, evidence, err := exposure.ExportVerifiedWorkerExposure(context.Background())
	if err != nil {
		t.Fatalf("ExportVerifiedWorkerExposure: %v", err)
	}
	if err := exposure.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := exposure.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, _, err := exposure.ExportVerifiedWorkerExposure(context.Background()); !errors.Is(err, ErrWorkerExposureContract) {
		t.Fatalf("export after Close did not fail closed: %v", err)
	}
	if err := exposure.VerifyWorkerExposure(context.Background(), evidence); !errors.Is(err, ErrWorkerExposureContract) {
		t.Fatalf("verify after Close did not fail closed: %v", err)
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
	descriptor, evidence, err := assets.ExportVerifiedWorkerRuntimeAssets(context.Background())
	if err != nil {
		t.Fatalf("ExportVerifiedWorkerRuntimeAssets: %v", err)
	}
	_ = descriptor.Close()
	if evidence.BundleDigest != digest || evidence.NamespaceProfileDigest != namespace ||
		evidence.ViewIdentity == "" || evidence.Device == 0 || evidence.Inode == 0 || evidence.MountID == 0 {
		t.Fatalf("incomplete runtime-asset evidence: %+v", evidence)
	}
	if err := assets.VerifyWorkerRuntimeAssets(context.Background(), evidence); err != nil {
		t.Fatalf("VerifyWorkerRuntimeAssets: %v", err)
	}
	// An invalid namespace/readOnly claim must be refused.
	wrongNamespace := evidence
	wrongNamespace.NamespaceProfileDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := assets.VerifyWorkerRuntimeAssets(context.Background(), wrongNamespace); !errors.Is(err, ErrRuntimeAssets) {
		t.Fatalf("mismatched namespace-profile claim was not refused: %v", err)
	}
	// Content tamper must be detected.
	if err := os.WriteFile(filepath.Join(bundlePath, "src", "worker.js"), []byte("console.log(2)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := assets.VerifyWorkerRuntimeAssets(context.Background(), evidence); !errors.Is(err, ErrRuntimeAssets) {
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

// TestSealedExposureIntegratesWithLauncherChannels proves the concrete
// sessionlaunch adapters satisfy the launcher-owned public constructor: a CLI
// composition root (owner C) can build the real Pi worker channel set from
// these values without any caller-supplied trust boolean.
func TestSealedExposureIntegratesWithLauncherChannels(t *testing.T) {
	fixture, prepared := prepareExposureFixture(t)
	exposure, err := prepared.OpenSealedWorkerExposure(fixture.g0)
	if err != nil {
		t.Fatalf("OpenSealedWorkerExposure: %v", err)
	}
	t.Cleanup(func() { _ = exposure.Close() })

	bundlePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundlePath, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundleRoot, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer bundleRoot.Close()
	digest, err := runtimeAssetContentDigest(bundleRoot, defaultRuntimeAssetMaxFiles, defaultRuntimeAssetMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := OpenVerifiedRuntimeAssets(bundleRoot, RuntimeAssetSpec{
		BundleDigest:           digest,
		NamespaceProfileDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	})
	if err != nil {
		t.Fatalf("OpenVerifiedRuntimeAssets: %v", err)
	}
	t.Cleanup(func() { _ = assets.Close() })

	channels, err := piruntime.NewPiWorkerChannels(context.Background(), exposure, assets)
	if err != nil {
		t.Fatalf("NewPiWorkerChannels rejected the concrete sessionlaunch adapters: %v", err)
	}
	if err := channels.Close(); err != nil {
		t.Fatalf("close Pi worker channels: %v", err)
	}
}
