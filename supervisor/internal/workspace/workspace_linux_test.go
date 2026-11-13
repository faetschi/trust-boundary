//go:build linux

package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/faultfs"
)

func TestValidSHA256RequiresCanonicalLowercaseEncoding(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	for value, want := range map[string]bool{
		valid: true, "sha256:" + strings.Repeat("A", 64): false,
		"sha256:" + strings.Repeat("a", 63): false, "sha256:" + strings.Repeat("g", 64): false,
		"sha-256:" + strings.Repeat("a", 64): false,
	} {
		if got := ValidSHA256(value); got != want {
			t.Errorf("ValidSHA256(%q) = %v, want %v", value, got, want)
		}
	}
}

func testOptions() Options {
	return Options{
		Generation:           "g0",
		MetadataPolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Limits:               DefaultLimits(),
		QuiescentRoot:        true,
		XattrVisibility: XattrVisibilityAttestation{
			ProfileDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Authority:     XattrAuthorityBoundedNoHiddenTrusted,
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

// The only admitted authority is the BOUNDED root-attested one; it is not a
// complete-enumeration claim. Empty, retired and unknown labels are refused by
// both Import and Scan.
func TestAdmissionAcceptsExactlyTheBoundedXattrAuthority(t *testing.T) {
	if string(XattrAuthorityBoundedNoHiddenTrusted) != "root-attested-bounded-no-hidden-trusted/v1" {
		t.Fatalf("bounded authority label drifted: %q", XattrAuthorityBoundedNoHiddenTrusted)
	}
	for name, authority := range map[string]XattrVisibilityAuthority{
		"empty":                   "",
		"retired complete label":  "root-profile-measured-complete-enumeration/v1",
		"bounded next version":    "root-attested-bounded-no-hidden-trusted/v2",
		"bounded with whitespace": "root-attested-bounded-no-hidden-trusted/v1 ",
		"upper case":              "ROOT-ATTESTED-BOUNDED-NO-HIDDEN-TRUSTED/V1",
		"bare complete":           "complete",
	} {
		source, destination, _ := newRoots(t)
		options := testOptions()
		options.XattrVisibility.Authority = authority
		if _, err := Import(source, destination, options); !errors.Is(err, ErrXattrVisibilityUnproven) {
			t.Fatalf("%s: Import error = %v, want ErrXattrVisibilityUnproven", name, err)
		}
		if _, err := Scan(source, options); !errors.Is(err, ErrXattrVisibilityUnproven) {
			t.Fatalf("%s: Scan error = %v, want ErrXattrVisibilityUnproven", name, err)
		}
	}
	source, destination, _ := newRoots(t)
	if _, err := Import(source, destination, testOptions()); err != nil {
		t.Fatalf("bounded authority was refused: %v", err)
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

// ImportUnsynced must build the same tree as Import and skip only the per-file fsync,
// which its callers replace with one filesystem sync or do not need.
func TestImportUnsyncedSkipsOnlyFileSync(t *testing.T) {
	base := t.TempDir()
	sourcePath := filepath.Join(base, "source")
	if err := os.MkdirAll(filepath.Join(sourcePath, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "nested/b.txt"} {
		if err := os.WriteFile(filepath.Join(sourcePath, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	fsyncs := 0
	var fsyncErr error
	defer faultfs.Install(func(site faultfs.Site, op faultfs.Op) error {
		if site == faultfs.SiteWorkspaceImport && op == faultfs.OpFsync {
			fsyncs++
			return fsyncErr
		}
		return nil
	})()
	var digests []string
	for _, run := range []struct {
		name       string
		importTree func(*os.File, *os.File, Options) (Snapshot, error)
		wantFsyncs int
	}{{"Import", Import, 2}, {"ImportUnsynced", ImportUnsynced, 0}} {
		destinationPath := filepath.Join(base, run.name)
		if err := os.Mkdir(destinationPath, 0o700); err != nil {
			t.Fatal(err)
		}
		fsyncs = 0
		snapshot, err := run.importTree(source, openPrivateRoot(t, destinationPath), testOptions())
		if err != nil {
			t.Fatalf("%s: %v", run.name, err)
		}
		if fsyncs != run.wantFsyncs {
			t.Fatalf("%s made %d file fsyncs, want %d", run.name, fsyncs, run.wantFsyncs)
		}
		digests = append(digests, snapshot.TreeDigest)
	}
	if digests[0] != digests[1] {
		t.Fatalf("Import and ImportUnsynced built different trees: %v", digests)
	}
	// Private Git and publication restore rely on Import failing when a file sync fails.
	fsyncErr = syscall.EIO
	destinationPath := filepath.Join(base, "failed-sync")
	if err := os.Mkdir(destinationPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(source, openPrivateRoot(t, destinationPath), testOptions()); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Import with a failing file sync: err = %v, want EIO", err)
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

// The statx mount ID must be the fdinfo mnt_id for every descriptor kind the walk holds,
// and differ across mounts, or the nested-mount check would compare unlike IDs.
func TestStatxMountIDMatchesFdinfo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	regular, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	pathNode, err := openPathNode(directory, "file")
	if err != nil {
		t.Fatal(err)
	}
	defer pathNode.Close()
	proc, err := os.Open("/proc")
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	for name, file := range map[string]*os.File{"directory": directory, "regular": regular, "o_path": pathNode, "proc": proc} {
		got, err := mountID(file)
		if err != nil {
			t.Fatalf("%s: mountID: %v", name, err)
		}
		want, err := fdinfoMountID(file)
		if err != nil {
			t.Fatalf("%s: fdinfoMountID: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s: statx mount ID %d, fdinfo mnt_id %d", name, got, want)
		}
	}
	tmpID, _ := mountID(directory)
	procID, _ := mountID(proc)
	if tmpID == procID {
		t.Fatalf("temp dir and /proc report the same mount ID %d", tmpID)
	}
}

// fdinfoMountID is the test oracle: the kernel's fdinfo mnt_id, the ID the walk compared
// before it used statx.
func fdinfoMountID(file *os.File) (uint64, error) {
	info, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrMountIdentityUnavailable, err)
	}
	defer info.Close()
	data, err := io.ReadAll(io.LimitReader(info, 16<<10))
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrMountIdentityUnavailable, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "mnt_id" {
			continue
		}
		id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err == nil {
			return id, nil
		}
	}
	return 0, ErrMountIdentityUnavailable
}
