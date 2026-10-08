//go:build linux

package privategit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCreateInitializesIndependentPrivateGitRepository(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
	if err := os.Mkdir(filepath.Join(sourcePath, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateFile(t, filepath.Join(sourcePath, "nested", "run.sh"), "#!/bin/sh\n", 0o700)

	git := testPinnedGit(t)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(git))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() {
		if err := repository.Destroy(); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	}()

	if err := repository.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	rootPath := descriptorPath(t, repository.Root())
	assertPrivateDirectory(t, filepath.Join(rootPath, ".git"))
	if _, err := os.Stat(filepath.Join(rootPath, "README.md")); err != nil {
		t.Fatalf("copied README: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "nested", "run.sh")); err != nil {
		t.Fatalf("copied nested file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "git-template")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary template remains: %v", err)
	}
	for _, forbidden := range []string{"commondir", "worktrees", "alternates"} {
		if _, err := os.Stat(filepath.Join(rootPath, ".git", forbidden)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("forbidden Git metadata %q exists: %v", forbidden, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(rootPath, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(config) == "" || containsUnsafeGitConfig(string(config)) {
		t.Fatalf("unsafe private config: %q", config)
	}
	provenance := repository.Provenance()
	if provenance.SourceTreeDigest == "" || provenance.SourceBindingDigest != testDigest || provenance.GitExecutableDigest == "" || provenance.GitExecutionBoundary == "" || provenance.Digest() == "" || provenance.InitialBranch != "main" || provenance.FilterPolicy != "workspace-reserved-git/v1" {
		t.Fatalf("incomplete provenance: %+v", provenance)
	}
	if provenance.MetadataPolicyDigest != testDigest || provenance.SourceManifest.MetadataPolicyDigest != testDigest {
		t.Fatalf("metadata policy identity was not retained: %+v", provenance)
	}
	if got := repository.Snapshot().TreeDigest; got != provenance.SourceTreeDigest {
		t.Fatalf("snapshot/provenance digest mismatch: %q != %q", got, provenance.SourceTreeDigest)
	}
	if err := repository.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	entries, err := os.ReadDir(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("bounded Destroy left parent entries: %+v", entries)
	}
}

func TestCreateRejectsLiveGitDirectoryAndPointerBeforeDestination(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(string) error
	}{
		{name: "directory", make: func(path string) error { return os.Mkdir(path, 0o700) }},
		{name: "pointer", make: func(path string) error { return os.WriteFile(path, []byte("gitdir: /live/.git/worktrees/x\n"), 0o600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentPath := privateTestDirectory(t, "parent")
			sourcePath := privateTestDirectory(t, "source")
			if err := test.make(filepath.Join(sourcePath, ".git")); err != nil {
				t.Fatal(err)
			}
			parent := openPrivateTestDirectory(t, parentPath)
			source := openPrivateTestDirectory(t, sourcePath)
			_, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
			if !errors.Is(err, ErrSourceContainsGit) {
				t.Fatalf("Create error = %v, want ErrSourceContainsGit", err)
			}
			entries, readErr := os.ReadDir(parentPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("destination was created on rejected source: %+v", entries)
			}
		})
	}
}

func TestCreateRequiresTrustedSourceBindingEvidence(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	descriptor := testSource(source)
	descriptor.Binding = nil
	_, err := Create(context.Background(), parent, descriptor, testOptions(testPinnedGit(t)))
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("Create without source binding = %v, want ErrInvalidOptions", err)
	}
	entries, readErr := os.ReadDir(parentPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("destination was created without source binding: %+v", entries)
	}
}

func TestCreateRejectsNestedSubmoduleAndLinkedWorktreePointers(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "submodule pointer", path: filepath.Join("modules", "child", ".git")},
		{name: "linked worktree pointer", path: filepath.Join("worktree", ".git")},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentPath := privateTestDirectory(t, "parent")
			sourcePath := privateTestDirectory(t, "source")
			if err := os.MkdirAll(filepath.Dir(filepath.Join(sourcePath, test.path)), 0o700); err != nil {
				t.Fatal(err)
			}
			writePrivateFile(t, filepath.Join(sourcePath, test.path), "gitdir: /outside/.git/worktrees/child\n", 0o600)
			parent := openPrivateTestDirectory(t, parentPath)
			source := openPrivateTestDirectory(t, sourcePath)
			_, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
			if !errors.Is(err, ErrSourceContainsGit) {
				t.Fatalf("Create error = %v, want ErrSourceContainsGit", err)
			}
			entries, readErr := os.ReadDir(parentPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("destination was created on nested admin rejection: %+v", entries)
			}
		})
	}
}

func TestCreateRejectsRealGitHistoryAndLinkedWorktreeFixture(t *testing.T) {
	git := testPinnedGit(t)
	fixtureRoot := privateTestDirectory(t, "fixture")
	mainRepo := filepath.Join(fixtureRoot, "main")
	if err := os.Mkdir(mainRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, git.path, mainRepo, "init", "--initial-branch=main")
	writePrivateFile(t, filepath.Join(mainRepo, "tracked.txt"), "history fixture\n", 0o600)
	runFixtureGit(t, git.path, mainRepo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "add", "tracked.txt")
	runFixtureGit(t, git.path, mainRepo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "fixture")
	linked := filepath.Join(fixtureRoot, "linked")
	runFixtureGit(t, git.path, mainRepo, "worktree", "add", "--quiet", "--detach", linked, "HEAD")

	parentPath := privateTestDirectory(t, "parent")
	parent := openPrivateTestDirectory(t, parentPath)
	linkedRoot := openPrivateTestDirectory(t, linked)
	_, err := Create(context.Background(), parent, testSource(linkedRoot), testOptions(git))
	if !errors.Is(err, ErrSourceContainsGit) {
		t.Fatalf("Create real linked worktree = %v, want ErrSourceContainsGit", err)
	}
	entries, readErr := os.ReadDir(parentPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("destination was created for real linked worktree: %+v", entries)
	}
}

func TestCreateRejectsRealGitSubmodulePointerFixture(t *testing.T) {
	git := testPinnedGit(t)
	fixtureRoot := privateTestDirectory(t, "fixture")
	childRepo := filepath.Join(fixtureRoot, "child")
	mainRepo := filepath.Join(fixtureRoot, "main")
	for _, repo := range []string{childRepo, mainRepo} {
		if err := os.Mkdir(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		runFixtureGit(t, git.path, repo, "init", "--initial-branch=main")
	}
	writePrivateFile(t, filepath.Join(childRepo, "child.txt"), "submodule history\n", 0o600)
	runFixtureGit(t, git.path, childRepo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "add", "child.txt")
	runFixtureGit(t, git.path, childRepo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "child")
	runFixtureGit(t, git.path, mainRepo, "-c", "protocol.file.allow=always", "submodule", "add", "../child", "modules/child")
	submoduleRoot := filepath.Join(mainRepo, "modules", "child")
	parentPath := privateTestDirectory(t, "parent")
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, submoduleRoot)
	_, err := Create(context.Background(), parent, testSource(source), testOptions(git))
	if !errors.Is(err, ErrSourceContainsGit) {
		t.Fatalf("Create real submodule = %v, want ErrSourceContainsGit", err)
	}
	entries, readErr := os.ReadDir(parentPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("destination was created for real submodule: %+v", entries)
	}
}

func TestCreatePreservesSafeGitWorktreeAttributesWithoutHistory(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, ".gitattributes"), "*.sh text eol=lf\n", 0o600)
	writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "safe worktree\n", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Destroy() }()
	rootPath := descriptorPath(t, repository.Root())
	if _, err := os.Stat(filepath.Join(rootPath, ".gitattributes")); err != nil {
		t.Fatalf("safe .gitattributes was not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, ".git", "objects")); err != nil {
		t.Fatalf("new Git admin directory missing objects area: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, ".git", "refs", "heads", "main")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected imported history/ref: %v", err)
	}
}

func TestCreateDoesNotInheritHostGitConfigurationOrDrivers(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "file"), "bytes", 0o600)
	hostConfig := filepath.Join(t.TempDir(), "host-git-config")
	writePrivateFile(t, hostConfig, "[include]\n\tpath = /outside/config\n[core]\n\tsshCommand = /outside/ssh\n", 0o600)
	t.Setenv("GIT_CONFIG_GLOBAL", hostConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", hostConfig)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.sshCommand")
	t.Setenv("GIT_CONFIG_VALUE_0", "/outside/ssh")
	t.Setenv("GIT_SSH_COMMAND", "touch /outside/ssh-marker")
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatalf("Create inherited hostile Git environment: %v", err)
	}
	defer func() { _ = repository.Destroy() }()
	if err := repository.Verify(); err != nil {
		t.Fatalf("Verify with hostile ambient Git environment: %v", err)
	}
}

func TestVerifyRejectsPrivateGitReachabilityMetadata(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "file"), "bytes", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Destroy() }()
	rootPath := descriptorPath(t, repository.Root())

	if err := os.WriteFile(filepath.Join(rootPath, ".git", "commondir"), []byte("/live/.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repository.Verify(); !errors.Is(err, ErrGitMetadata) {
		t.Fatalf("Verify commondir = %v, want ErrGitMetadata", err)
	}
	if err := os.Remove(filepath.Join(rootPath, ".git", "commondir")); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(rootPath, ".git", "config")
	config, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := config.WriteString("\n[include]\n\tpath = /live/.git/config\n")
	closeErr := config.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("tamper config: write=%v close=%v", writeErr, closeErr)
	}
	if err := repository.Verify(); !errors.Is(err, ErrGitMetadata) {
		t.Fatalf("Verify include = %v, want ErrGitMetadata", err)
	}
	cleanConfig := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n"
	for _, tampered := range []string{
		"[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n\tsshCommand = /tmp/ssh\n",
		"[remote \"origin\"]\n\turl = file:///outside\n\tfetch = +refs/*:refs/remotes/origin/*\n",
		"[filter \"lfs\"]\n\tclean = external-filter\n",
		"[credential]\n\thelper = store\n",
		"[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n\tfsmonitor = true\n",
	} {
		if err := os.WriteFile(configPath, []byte(tampered), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := repository.Verify(); !errors.Is(err, ErrGitMetadata) {
			t.Fatalf("Verify config %q = %v, want ErrGitMetadata", tampered, err)
		}
		if err := os.WriteFile(configPath, []byte(cleanConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVerifyRejectsPrivateGitExtendedAttributes(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "file"), "bytes", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Destroy() }()
	configPath := filepath.Join(descriptorPath(t, repository.Root()), ".git", "config")
	if err := syscall.Setxattr(configPath, "user.tbound-privategit", []byte("tampered"), 0); err != nil {
		if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) {
			t.Skipf("temporary filesystem does not support xattrs: %v", err)
		}
		t.Fatal(err)
	}
	if err := repository.Verify(); !errors.Is(err, ErrGitMetadata) {
		t.Fatalf("Verify Git config xattr = %v, want ErrGitMetadata", err)
	}
	if err := syscall.Removexattr(configPath, "user.tbound-privategit"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateCleansOnlyItsBoundedDirectoryWhenPinnedGitChanges(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "file"), "bytes", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	git := testPinnedGit(t)
	git.identity.Digest = testDigest
	_, err := Create(context.Background(), parent, testSource(source), testOptions(git))
	if !errors.Is(err, ErrGitExecutable) {
		t.Fatalf("Create error = %v, want ErrGitExecutable", err)
	}
	entries, err := os.ReadDir(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed creation left private staging entries: %+v", entries)
	}
}

func TestPinRejectsSameUIDGitLikeExecutableWithoutApprovedBytes(t *testing.T) {
	approved := testPinnedGit(t)
	fakePath := filepath.Join(t.TempDir(), "git-fake")
	writePrivateFile(t, fakePath, "#!/bin/sh\nprintf 'git version fake\\n'\n", 0o755)
	_, err := PinGitExecutable(fakePath, GitExecutableApproval{
		ExpectedDigest: approved.identity.Digest,
		Boundary:       "test-root-owned-private-fixture",
		Verifier: func(identity GitExecutableIdentity) error {
			if identity.Digest != approved.identity.Digest {
				return errors.New("unexpected executable bytes")
			}
			return nil
		},
	})
	if !errors.Is(err, ErrGitExecutable) {
		t.Fatalf("PinGitExecutable(fake) = %v, want ErrGitExecutable", err)
	}
}

func TestCreateExecutesHeldApprovedGitDescriptorAfterPathReplacement(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "file"), "bytes", 0o600)
	installed := testPinnedGit(t)
	fixtureDir := t.TempDir()
	if err := validateTrustedPath(fixtureDir); err != nil {
		t.Skipf("private executable fixture root is unavailable: %v", err)
	}
	fixtureGitPath := filepath.Join(fixtureDir, "git-approved")
	copyExecutable(t, installed.path, fixtureGitPath)
	approved := hashPath(t, fixtureGitPath)
	git, err := PinGitExecutable(fixtureGitPath, GitExecutableApproval{
		ExpectedDigest: approved,
		Boundary:       "test-root-owned-private-fixture",
		Verifier: func(identity GitExecutableIdentity) error {
			if identity.Digest != approved {
				return errors.New("approved fixture digest changed")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("PinGitExecutable copied fixture: %v", err)
	}
	t.Cleanup(func() { _ = git.Close() })
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	var once sync.Once
	var originalPath, heldPath, fakeMarker string
	previousHook := beforeGitStartForTests
	beforeGitStartForTests = func(root, _ *os.File) {
		once.Do(func() {
			originalPath = descriptorPath(t, root)
			heldPath = originalPath + ".held"
			fakeMarker = originalPath + ".fake-executed"
			if err := os.Rename(originalPath, heldPath); err != nil {
				t.Fatal(err)
			}
			writePrivateFile(t, originalPath, "#!/bin/sh\nprintf fake > '"+fakeMarker+"'\n", 0o755)
		})
	}
	defer func() { beforeGitStartForTests = previousHook }()
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(git))
	if err != nil {
		t.Fatalf("Create with replaced Git pathname: %v", err)
	}
	if _, err := os.Stat(fakeMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced pathname was executed: %v", err)
	}
	if err := os.Remove(originalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(heldPath, originalPath); err != nil {
		t.Fatal(err)
	}
	if err := repository.Verify(); err != nil {
		t.Fatalf("Verify after restoring replaced pathname: %v", err)
	}
	if err := repository.Destroy(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAndDestroyFailClosedOnPrivateRootNameReplacement(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "file"), "bytes", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatal(err)
	}
	rootPath := descriptorPath(t, repository.Root())
	replacement := rootPath + ".held"
	previousHook := beforeCleanupUnlinkForTests
	beforeCleanupUnlinkForTests = func(_ *os.File, name string) {
		if name != filepath.Base(rootPath) {
			return
		}
		if err := os.Rename(rootPath, replacement); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(rootPath, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { beforeCleanupUnlinkForTests = previousHook }()
	if err := repository.Destroy(); !errors.Is(err, ErrCleanupIncomplete) {
		t.Fatalf("Destroy replacement error = %v, want ErrCleanupIncomplete", err)
	}
	if entries, readErr := os.ReadDir(parentPath); readErr != nil || len(entries) != 2 {
		t.Fatalf("replacement boundary was not preserved: entries=%v err=%v", entries, readErr)
	}
	if err := os.Remove(rootPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, rootPath); err != nil {
		t.Fatal(err)
	}
	beforeCleanupUnlinkForTests = previousHook
	if err := repository.Destroy(); err != nil {
		t.Fatal(err)
	}
}

// Actual nested-mount traversal requires mount privileges and is covered only
// by a host-kernel integration fixture. This test covers the fail-closed
// identity comparator used at every cleanup directory boundary.
func TestCleanupMountComparatorRejectsUnexpectedIdentity(t *testing.T) {
	if err := verifyCleanupMountIdentity(41, 41, "file"); err != nil {
		t.Fatalf("matching cleanup mount identity rejected: %v", err)
	}
	if err := verifyCleanupMountIdentity(42, 41, "file"); !errors.Is(err, ErrCleanupIncomplete) {
		t.Fatalf("mismatched cleanup mount identity = %v, want ErrCleanupIncomplete", err)
	}
}

func TestVerifyRescansExactPrivateWorktreeManifest(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "file"), "bytes", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Destroy() }()
	rootPath := descriptorPath(t, repository.Root())
	if err := os.WriteFile(filepath.Join(rootPath, "file"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repository.Verify(); !errors.Is(err, ErrGitMetadata) {
		t.Fatalf("Verify worktree tamper = %v, want ErrGitMetadata", err)
	}
}

func TestVerifyRejectsReplacementOfRecordedPrivateGitAdmin(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "file"), "bytes", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Destroy() }()
	rootPath := descriptorPath(t, repository.Root())
	oldGit := filepath.Join(rootPath, ".git.old")
	if err := os.Rename(filepath.Join(rootPath, ".git"), oldGit); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := repository.Verify(); !errors.Is(err, ErrGitMetadata) {
		t.Fatalf("Verify replacement .git = %v, want ErrGitMetadata", err)
	}
	if err := os.Remove(filepath.Join(rootPath, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldGit, filepath.Join(rootPath, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := repository.Verify(); err != nil {
		t.Fatalf("Verify after restoring .git: %v", err)
	}
}

func TestCreateUsesHeldSourceDescriptorAcrossPathReplacement(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "held"), "descriptor bytes", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	oldPath := sourcePath + ".renamed"
	if err := os.Rename(sourcePath, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateFile(t, filepath.Join(sourcePath, ".git"), "gitdir: /live/.git\n", 0o600)
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(sourcePath, ".git"))
		_ = os.Remove(sourcePath)
		_ = os.Rename(oldPath, sourcePath)
	})

	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatalf("Create through replaced source path: %v", err)
	}
	defer func() { _ = repository.Destroy() }()
	if _, err := os.Stat(filepath.Join(descriptorPath(t, repository.Root()), "held")); err != nil {
		t.Fatalf("held descriptor tree was not copied: %v", err)
	}
}

func testSource(root *os.File) SourceDescriptor {
	return SourceDescriptor{
		Root: root, Generation: "g0", MetadataPolicyDigest: testDigest, BindingDigest: testDigest,
		Limits: DefaultLimits(), Quiescent: true,
		XattrVisibility: XattrVisibilityAttestation{ProfileDigest: testDigest, Complete: true},
		Binding:         testSourceVerifier{},
	}
}

func testOptions(git PinnedGit) Options {
	return Options{
		Git: git, InitialBranch: "main",
		CleanupBoundary: CleanupBoundary{ExclusiveParent: true, Description: "test parent is private and exclusively owned"},
	}
}

type testSourceVerifier struct{}

func (testSourceVerifier) VerifySource(root *os.File, generation, metadataPolicyDigest, xattrProfileDigest, bindingDigest string) error {
	if root == nil || generation != "g0" || metadataPolicyDigest != testDigest || xattrProfileDigest != testDigest || bindingDigest != testDigest {
		return errors.New("unexpected synthetic source binding")
	}
	info, err := statFile(root)
	if err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Uid != uint32(syscall.Geteuid()) {
		return errors.New("synthetic source descriptor is not supervisor-owned")
	}
	return nil
}

func testPinnedGit(t *testing.T) PinnedGit {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("real installed Git is unavailable: %v", err)
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	expected := hashPath(t, path)
	git, err := PinGitExecutable(path, GitExecutableApproval{
		ExpectedDigest: expected,
		Boundary:       "test-root-owned-private-fixture",
		Verifier: func(identity GitExecutableIdentity) error {
			if identity.Digest != expected || identity.Mode&0o022 != 0 {
				return errors.New("synthetic executable verifier rejected identity")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("PinGitExecutable(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = git.Close() })
	return git
}

func hashPath(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func copyExecutable(t *testing.T, source, destination string) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, 0o755); err != nil {
		t.Fatal(err)
	}
}

func runFixtureGit(t *testing.T, gitPath, directory string, args ...string) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = directory
	cmd.Env = []string{
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
		"LANG=C",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git %v: %v (%s)", args, err, output)
	}
}

func privateTestDirectory(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func openPrivateTestDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func writePrivateFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func descriptorPath(t *testing.T, file *os.File) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func assertPrivateDirectory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(syscall.Geteuid()) || stat.Gid != uint32(syscall.Getegid()) || stat.Mode&0o7777 != 0o700 {
		t.Fatalf("%s is not exact private directory: %+v", path, stat)
	}
}

func containsUnsafeGitConfig(config string) bool {
	lower := strings.ToLower(config)
	for _, token := range []string{"include", "alternat", "commondir", "gitdir", "worktree", "hookspath"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}
