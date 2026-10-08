//go:build linux

package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testOptions() Options {
	return Options{
		Generation:           "g0",
		MetadataPolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Limits:               DefaultLimits(),
		QuiescentRoot:        true,
		XattrVisibility: XattrVisibilityAttestation{
			ProfileDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Complete:      true,
		},
	}
}

func openPrivateRoot(t *testing.T, path string) *os.File {
	t.Helper()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func newRoots(t *testing.T) (*os.File, *os.File, string) {
	t.Helper()
	base := t.TempDir()
	sourcePath := filepath.Join(base, "source")
	destinationPath := filepath.Join(base, "destination")
	if err := os.Mkdir(sourcePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destinationPath, 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source, openPrivateRoot(t, destinationPath), sourcePath
}

func TestImportNormalizesAndBuildsDeltaManifest(t *testing.T) {
	source, destination, sourcePath := newRoots(t)
	if err := os.Mkdir(filepath.Join(sourcePath, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(sourcePath, "plain.txt")
	executable := filepath.Join(sourcePath, "nested", "run")
	if err := os.WriteFile(plain, []byte("same bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Import(source, destination, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Objects != 3 || snapshot.Bytes == 0 || snapshot.TreeDigest == "" {
		t.Fatalf("unexpected snapshot summary: %+v", snapshot)
	}
	if got := snapshot.Manifest.Objects[0].Path; got != "nested" {
		t.Fatalf("manifest is not path-sorted: first path %q", got)
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{
		{"plain.txt", 0o644},
		{filepath.Join("nested", "run"), 0o755},
		{"nested", 0o755},
	} {
		info, err := os.Stat(filepath.Join(destination.Name(), item.path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != item.mode {
			t.Errorf("%s mode = %04o, want %04o", item.path, got, item.mode)
		}
	}
	if snapshot.Manifest.Objects[1].State.ContentDigest == "" {
		t.Fatal("regular file has no content digest")
	}
}

func TestTimestampAndNonExecutablePermissionChangesDoNotChangeIdentity(t *testing.T) {
	makeSnapshot := func(t *testing.T, sourceMode os.FileMode, stamp time.Time) Snapshot {
		t.Helper()
		source, destination, sourcePath := newRoots(t)
		file := filepath.Join(sourcePath, "same.txt")
		if err := os.WriteFile(file, []byte("identical"), sourceMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		snapshot, err := Import(source, destination, testOptions())
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	first := makeSnapshot(t, 0o600, time.Unix(1_700_000_000, 0))
	second := makeSnapshot(t, 0o644, time.Unix(1_800_000_000, 0))
	if first.TreeDigest != second.TreeDigest {
		t.Fatalf("normalized-identical trees have different digests: %s != %s", first.TreeDigest, second.TreeDigest)
	}
}

func TestSparseFileIsImportedAsOrdinaryContents(t *testing.T) {
	source, destination, sourcePath := newRoots(t)
	file := filepath.Join(sourcePath, "sparse")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("head"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("tail"), (1<<20)-4); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Import(source, destination, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(destination.Name(), "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 1<<20 || string(copied[:4]) != "head" || string(copied[len(copied)-4:]) != "tail" {
		t.Fatal("sparse file contents were not preserved")
	}
	if snapshot.Bytes != 1<<20 {
		t.Fatalf("logical byte count = %d, want %d", snapshot.Bytes, 1<<20)
	}
}

func TestImportRejectsLinksSpecialFilesAndVisibleXattrs(t *testing.T) {
	tests := []struct {
		name string
		make func(string) error
		want error
	}{
		{"symlink", func(path string) error { return os.Symlink("missing", path) }, ErrSymlink},
		{"hardlink", func(path string) error {
			if err := os.WriteFile(path+".source", []byte("linked"), 0o600); err != nil {
				return err
			}
			return os.Link(path+".source", path)
		}, ErrHardLink},
		{"fifo", func(path string) error { return syscall.Mkfifo(path, 0o600) }, ErrSpecialFile},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, destination, sourcePath := newRoots(t)
			if err := test.make(filepath.Join(sourcePath, "bad")); err != nil {
				t.Fatal(err)
			}
			_, err := Import(source, destination, testOptions())
			if !errors.Is(err, test.want) {
				t.Fatalf("Import error = %v, want %v", err, test.want)
			}
		})
	}
	source, destination, sourcePath := newRoots(t)
	file := filepath.Join(sourcePath, "xattr")
	if err := os.WriteFile(file, []byte("attribute"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setxattr(file, "user.tbound-test", []byte("x"), 0); err != nil {
		if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) {
			t.Skipf("temporary filesystem does not support xattrs: %v", err)
		}
		t.Fatal(err)
	}
	_, err := Import(source, destination, testOptions())
	if !errors.Is(err, ErrUnsupportedMetadata) {
		t.Fatalf("Import error = %v, want ErrUnsupportedMetadata", err)
	}
}

func TestImportFailsClosedWithoutXattrVisibilityAttestation(t *testing.T) {
	source, destination, _ := newRoots(t)
	options := testOptions()
	options.XattrVisibility = XattrVisibilityAttestation{}
	_, err := Import(source, destination, options)
	if !errors.Is(err, ErrXattrVisibilityUnproven) {
		t.Fatalf("Import error = %v, want ErrXattrVisibilityUnproven", err)
	}
}

func TestImportEnforcesStreamingLimits(t *testing.T) {
	source, destination, sourcePath := newRoots(t)
	if err := os.WriteFile(filepath.Join(sourcePath, "large"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.Limits.MaxFileBytes = 4
	options.Limits.MaxTotalBytes = 4
	_, err := Import(source, destination, options)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("Import error = %v, want ErrLimit", err)
	}
}

func TestImportReservedGitFilterRejectsBeforeOpeningMetadata(t *testing.T) {
	source, destination, sourcePath := newRoots(t)
	if err := os.Mkdir(filepath.Join(sourcePath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".git", "secret"), []byte("live Git bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.ImportPolicy = ImportPolicy{ReservedMetadata: ReservedMetadataRejectLiveGit}
	_, err := Import(source, destination, options)
	if !errors.Is(err, ErrReservedMetadata) {
		t.Fatalf("Import error = %v, want ErrReservedMetadata", err)
	}
	if _, statErr := os.Stat(filepath.Join(destination.Name(), ".git", "secret")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("reserved Git bytes reached destination: %v", statErr)
	}
}

func TestImportReservedGitFilterRaceBarrierCopiesZeroInsertedGitBytes(t *testing.T) {
	source, destination, sourcePath := newRoots(t)
	if err := os.WriteFile(filepath.Join(sourcePath, "payload"), []byte("ordinary payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	var injected sync.Once
	options.ImportPolicy = ImportPolicy{ReservedMetadata: ReservedMetadataRejectLiveGit}
	previousBarrier := admissionBarrierForTests
	admissionBarrierForTests = func(path string) {
		if path != "payload" {
			return
		}
		injected.Do(func() {
			if err := os.Mkdir(filepath.Join(sourcePath, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sourcePath, ".git", "secret"), []byte("must never be copied"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
	defer func() { admissionBarrierForTests = previousBarrier }()
	_, err := Import(source, destination, options)
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("Import error = %v, want ErrSourceChanged", err)
	}
	if _, statErr := os.Stat(filepath.Join(destination.Name(), ".git", "secret")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("race-inserted Git bytes reached destination: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(destination.Name(), "payload")); statErr != nil {
		t.Fatalf("ordinary payload was not the only partial output: %v", statErr)
	}
}

func TestImportDefaultPolicyPreservesReservedMetadataCompatibility(t *testing.T) {
	source, destination, sourcePath := newRoots(t)
	if err := os.Mkdir(filepath.Join(sourcePath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".git", "description"), []byte("ordinary default-policy fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(source, destination, testOptions()); err != nil {
		t.Fatalf("default Import changed semantics: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination.Name(), ".git", "description")); err != nil {
		t.Fatalf("default Import no longer copies reserved metadata: %v", err)
	}
}

func TestScanExcludesOnlyExactPrivateRootGitWithinLogicalObjectLimit(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "payload"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(rootPath, "payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, ".git", "secret"), []byte("must not be scanned"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := openPrivateRoot(t, rootPath)
	options := testOptions()
	options.Limits.MaxObjects = 1
	options.ImportPolicy = ImportPolicy{ReservedMetadata: ReservedMetadataExcludeRootPrivateGit}
	snapshot, err := Scan(root, options)
	if err != nil {
		t.Fatalf("Scan private root with excluded .git: %v", err)
	}
	if snapshot.Objects != 1 || snapshot.Manifest.Objects[0].Path != "payload" {
		t.Fatalf("unexpected private-root manifest: %+v", snapshot.Manifest)
	}
}

func TestScanRejectsCaseVariantPrivateRootGitName(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, ".GIT"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := openPrivateRoot(t, rootPath)
	options := testOptions()
	options.ImportPolicy = ImportPolicy{ReservedMetadata: ReservedMetadataExcludeRootPrivateGit}
	_, err := Scan(root, options)
	if !errors.Is(err, ErrReservedMetadata) {
		t.Fatalf("Scan case-variant .GIT = %v, want ErrReservedMetadata", err)
	}
}

func TestImportBoundsSingleDirectoryByRemainingObjectBudget(t *testing.T) {
	source, destination, sourcePath := newRoots(t)
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(sourcePath, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options := testOptions()
	options.Limits.MaxObjects = 2
	_, err := Import(source, destination, options)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("Import error = %v, want ErrLimit", err)
	}
}

func TestImportRejectsEqualOrOverlappingRoots(t *testing.T) {
	t.Run("same directory", func(t *testing.T) {
		rootPath := t.TempDir()
		root, err := os.Open(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		destination, err := os.Open(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = destination.Close() })
		if err := os.Chmod(rootPath, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err = Import(root, destination, testOptions())
		if !errors.Is(err, ErrOverlappingRoots) {
			t.Fatalf("Import error = %v, want ErrOverlappingRoots", err)
		}
	})

	t.Run("destination inside source", func(t *testing.T) {
		base := t.TempDir()
		sourcePath := filepath.Join(base, "source")
		destinationPath := filepath.Join(sourcePath, "destination")
		if err := os.Mkdir(sourcePath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(destinationPath, 0o700); err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = source.Close() })
		destination, err := os.Open(destinationPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = destination.Close() })
		_, err = Import(source, destination, testOptions())
		if !errors.Is(err, ErrOverlappingRoots) {
			t.Fatalf("Import error = %v, want ErrOverlappingRoots", err)
		}
	})
}

func TestScanRejectsNoncanonicalStoredMode(t *testing.T) {
	source, destination, sourcePath := newRoots(t)
	if err := os.WriteFile(filepath.Join(sourcePath, "file"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(source, destination, testOptions()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(destination.Name(), "file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Scan(destination, testOptions())
	if !errors.Is(err, ErrNoncanonicalMode) {
		t.Fatalf("Scan error = %v, want ErrNoncanonicalMode", err)
	}
}

// This exercises the fail-closed comparator only. Bind-mount traversal needs a
// separate Linux integration test with mount privileges.
func TestMountIDComparatorRejectsUnexpectedIdentity(t *testing.T) {
	if err := verifyMountIdentity(41, 41, "file"); err != nil {
		t.Fatalf("matching mount identity rejected: %v", err)
	}
	if err := verifyMountIdentity(42, 41, "file"); !errors.Is(err, ErrNestedMount) {
		t.Fatalf("mismatched mount identity error = %v, want ErrNestedMount", err)
	}
}
