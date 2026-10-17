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
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
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
	if provenance.SchemaVersion != "tbound-private-git/v3" || !validRepositoryName(provenance.RepositoryName) || provenance.PrivateParentDevice == 0 || provenance.PrivateParentInode == 0 ||
		provenance.SourceTreeDigest == "" || provenance.SourceBindingDigest != testDigest || provenance.GitExecutableDigest == "" || provenance.GitExecutionBoundary == "" || provenance.Digest() == "" || provenance.InitialBranch != "main" || provenance.FilterPolicy != "workspace-reserved-git/v1" {
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

func TestReopenVerifiesPinnedSnapshotWithoutRestoringAuthorityOrMutatingEvidence(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	git := testPinnedGit(t)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(git))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approval := approvalForRepository(t, parent, repository)
	if err := repository.Close(); err != nil {
		t.Fatalf("close original repository descriptors: %v", err)
	}
	if err := git.Close(); err != nil {
		t.Fatalf("close pinned Git executable before reopen: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	journalPath := filepath.Join(parentPath, "retained-d06-journal.jsonl")
	journalBytes := []byte("retained authenticated-owner evidence is outside this API\n")
	writePrivateFile(t, journalPath, string(journalBytes), 0o600)
	entriesBefore, err := os.ReadDir(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Reopen(parent, approval)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := verified.Verify(); err != nil {
		t.Fatalf("Verify reopened repository: %v", err)
	}
	if got := verified.Snapshot(); !reflect.DeepEqual(got, approval.Snapshot) {
		t.Fatalf("reopened snapshot changed: got=%+v want=%+v", got, approval.Snapshot)
	}
	if got := verified.Provenance(); !reflect.DeepEqual(got, approval.Provenance) {
		t.Fatalf("reopened provenance changed: got=%+v want=%+v", got, approval.Provenance)
	}
	if err := verified.Close(); err != nil {
		t.Fatalf("close verification handle: %v", err)
	}
	entriesAfter, err := os.ReadDir(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entryNames(entriesAfter), entryNames(entriesBefore)) {
		t.Fatalf("reopen/close changed parent entries: before=%v after=%v", entryNames(entriesBefore), entryNames(entriesAfter))
	}
	if got, err := os.ReadFile(journalPath); err != nil || !reflect.DeepEqual(got, journalBytes) {
		t.Fatalf("reopen/close changed retained D06 journal: got=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(parentPath, approval.Name, "README.md")); err != nil {
		t.Fatalf("retained repository was removed on Close: %v", err)
	}
}

func TestReopenRejectsMissingOrForgedTrustedIdentityAndWrongAnchors(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	otherParentPath := privateTestDirectory(t, "other-parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	otherParent := openPrivateTestDirectory(t, otherParentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approval := approvalForRepository(t, parent, repository)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	missingVerifier := cloneReopenApproval(approval)
	missingVerifier.TrustedVerifier = nil
	if _, err := Reopen(parent, missingVerifier); !errors.Is(err, ErrReopenBinding) {
		t.Fatalf("Reopen without external trusted pin = %v, want ErrReopenBinding", err)
	}

	wrongName := cloneReopenApproval(approval)
	wrongName.Name = "staging-00000000000000000000000000000000"
	if _, err := Reopen(parent, wrongName); !errors.Is(err, ErrReopenBinding) {
		t.Fatalf("Reopen with journal-style discovered/wrong name = %v, want ErrReopenBinding", err)
	}

	wrongParent := cloneReopenApproval(approval)
	if _, err := Reopen(otherParent, wrongParent); !errors.Is(err, ErrReopenBinding) {
		t.Fatalf("Reopen through another parent descriptor = %v, want ErrReopenBinding", err)
	}

	forged := cloneReopenApproval(approval)
	forged.Provenance.PrivateGitInode++
	forgedIdentity, err := repositoryIdentity(parent, forged.Name, forged.Provenance, forged.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	forged.ExpectedIdentityDigest = forgedIdentity.Digest()
	if _, err := Reopen(parent, forged); !errors.Is(err, ErrReopenBinding) {
		t.Fatalf("Reopen with self-rehashed local identity = %v, want external pin rejection", err)
	}

	legacy := cloneReopenApproval(approval)
	legacy.Provenance.SchemaVersion = "tbound-private-git/v1"
	legacyIdentity, err := repositoryIdentity(parent, legacy.Name, legacy.Provenance, legacy.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	legacy.ExpectedIdentityDigest = legacyIdentity.Digest()
	if _, err := Reopen(parent, legacy); !errors.Is(err, ErrReopenBinding) || !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Reopen historical v1 provenance = %v, want explicit refusal", err)
	}
	if _, err := os.Stat(filepath.Join(parentPath, approval.Name, "README.md")); err != nil {
		t.Fatalf("rejected reopen removed retained repository: %v", err)
	}
}

func TestReopenRejectsRootNameSymlinkAndModeChangesWithoutCleanup(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func(t *testing.T, parentPath string, approval ReopenApproval)
	}{
		{name: "root-name-symlink", tamper: func(t *testing.T, parentPath string, approval ReopenApproval) {
			root := filepath.Join(parentPath, approval.Name)
			moved := filepath.Join(parentPath, "retained-after-rename")
			if err := os.Rename(root, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, root); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "parent-mode", tamper: func(t *testing.T, parentPath string, _ ReopenApproval) {
			if err := os.Chmod(parentPath, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "root-mode", tamper: func(t *testing.T, parentPath string, approval ReopenApproval) {
			if err := os.Chmod(filepath.Join(parentPath, approval.Name), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentPath := privateTestDirectory(t, "parent")
			sourcePath := privateTestDirectory(t, "source")
			writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
			parent := openPrivateTestDirectory(t, parentPath)
			source := openPrivateTestDirectory(t, sourcePath)
			repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			approval := approvalForRepository(t, parent, repository)
			if err := repository.Close(); err != nil {
				t.Fatal(err)
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			test.tamper(t, parentPath, approval)
			if _, err := Reopen(parent, approval); !errors.Is(err, ErrReopenBinding) && !errors.Is(err, ErrGitMetadata) {
				t.Fatalf("Reopen after %s tamper = %v, want fail-closed identity/metadata error", test.name, err)
			}
			if _, err := os.Lstat(filepath.Join(parentPath, approval.Name)); err != nil {
				t.Fatalf("rejected reopen removed/replaced retained name: %v", err)
			}
		})
	}
}

func TestReopenRechecksParentNameAfterFullScan(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approval := approvalForRepository(t, parent, repository)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parentPath, "moved-retained-root")
	oldHook := beforeRepositoryNameVerifyForTests
	var injected bool
	beforeRepositoryNameVerifyForTests = func(_ *os.File, _ *os.File, name string) {
		if injected {
			return
		}
		injected = true
		if err := os.Rename(filepath.Join(parentPath, name), moved); err != nil {
			t.Errorf("rename retained root during verification: %v", err)
			return
		}
		if err := os.Mkdir(filepath.Join(parentPath, name), 0o700); err != nil {
			t.Errorf("install replacement root during verification: %v", err)
		}
	}
	defer func() { beforeRepositoryNameVerifyForTests = oldHook }()
	if _, err := Reopen(parent, approval); !errors.Is(err, ErrGitMetadata) {
		t.Fatalf("Reopen after name substitution = %v, want ErrGitMetadata", err)
	}
	if !injected {
		t.Fatal("name substitution hook did not run")
	}
	if _, err := os.Stat(filepath.Join(moved, "README.md")); err != nil {
		t.Fatalf("rejected reopen deleted or lost the retained root: %v", err)
	}
}

func TestReopenRechecksGitAdminAfterFullScan(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approval := approvalForRepository(t, parent, repository)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parentPath, approval.Name)
	moved := filepath.Join(root, ".git-retained-after-swap")
	oldHook := beforeRepositoryNameVerifyForTests
	var injected bool
	beforeRepositoryNameVerifyForTests = func(_ *os.File, _ *os.File, _ string) {
		if injected {
			return
		}
		injected = true
		if err := os.Rename(filepath.Join(root, ".git"), moved); err != nil {
			t.Errorf("rename private Git admin during verification: %v", err)
			return
		}
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
			t.Errorf("install replacement private Git admin during verification: %v", err)
		}
	}
	defer func() { beforeRepositoryNameVerifyForTests = oldHook }()
	if _, err := Reopen(parent, approval); !errors.Is(err, ErrGitMetadata) {
		t.Fatalf("Reopen after .git substitution = %v, want ErrGitMetadata", err)
	}
	if !injected {
		t.Fatal(".git substitution hook did not run")
	}
	if _, err := os.Stat(filepath.Join(moved, "config")); err != nil {
		t.Fatalf("rejected reopen deleted or lost retained .git admin: %v", err)
	}
}

func TestReopenAndVerifyRecheckAfterOwnerAssertionCallback(t *testing.T) {
	for _, stage := range []string{"Reopen", "Verify"} {
		for _, mutation := range []string{"repository-name", "git-admin", "worktree-content"} {
			t.Run(stage+"/"+mutation, func(t *testing.T) {
				parentPath := privateTestDirectory(t, "parent")
				sourcePath := privateTestDirectory(t, "source")
				writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
				parent := openPrivateTestDirectory(t, parentPath)
				source := openPrivateTestDirectory(t, sourcePath)
				repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				approval := approvalForRepository(t, parent, repository)
				if err := repository.Close(); err != nil {
					t.Fatal(err)
				}
				if err := source.Close(); err != nil {
					t.Fatal(err)
				}

				ownerCheck := approval.VerifyExclusiveParent
				calls := 0
				trigger := 3 // Reopen's post-scan owner assertion.
				if stage == "Verify" {
					trigger = -1
				}
				approval.VerifyExclusiveParent = func(file *os.File, expected DirectoryIdentity) error {
					calls++
					if err := ownerCheck(file, expected); err != nil {
						return err
					}
					if calls == trigger {
						injectOwnerCallbackMutation(t, parentPath, approval.Name, mutation)
					}
					return nil
				}

				verified, err := Reopen(parent, approval)
				if stage == "Reopen" {
					if !errors.Is(err, ErrGitMetadata) {
						t.Fatalf("Reopen after owner callback %s mutation = %v, want ErrGitMetadata", mutation, err)
					}
					assertOwnerCallbackMutationRetained(t, parentPath, approval.Name, mutation)
					return
				}
				if err != nil {
					t.Fatalf("Reopen before later Verify mutation: %v", err)
				}
				trigger = calls + 2 // Verify's post-scan owner assertion.
				if err := verified.Verify(); !errors.Is(err, ErrGitMetadata) {
					t.Fatalf("Verify after owner callback %s mutation = %v, want ErrGitMetadata", mutation, err)
				}
				if err := verified.Close(); err != nil {
					t.Fatal(err)
				}
				assertOwnerCallbackMutationRetained(t, parentPath, approval.Name, mutation)
			})
		}
	}
}

func injectOwnerCallbackMutation(t *testing.T, parentPath, name, mutation string) {
	t.Helper()
	root := filepath.Join(parentPath, name)
	switch mutation {
	case "repository-name":
		moved := filepath.Join(parentPath, "retained-after-owner-callback")
		if err := os.Rename(root, moved); err != nil {
			t.Errorf("rename retained root from owner callback: %v", err)
			return
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Errorf("install replacement root from owner callback: %v", err)
		}
	case "git-admin":
		moved := filepath.Join(root, ".git-retained-after-owner-callback")
		if err := os.Rename(filepath.Join(root, ".git"), moved); err != nil {
			t.Errorf("rename retained .git from owner callback: %v", err)
			return
		}
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
			t.Errorf("install replacement .git from owner callback: %v", err)
		}
	case "worktree-content":
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed from owner callback\n"), 0o600); err != nil {
			t.Errorf("change retained worktree from owner callback: %v", err)
		}
	default:
		t.Errorf("unknown owner callback mutation %q", mutation)
	}
}

func assertOwnerCallbackMutationRetained(t *testing.T, parentPath, name, mutation string) {
	t.Helper()
	root := filepath.Join(parentPath, name)
	switch mutation {
	case "repository-name":
		if _, err := os.Stat(filepath.Join(parentPath, "retained-after-owner-callback", "README.md")); err != nil {
			t.Fatalf("rejected owner callback deleted retained root: %v", err)
		}
	case "git-admin":
		if _, err := os.Stat(filepath.Join(root, ".git-retained-after-owner-callback", "config")); err != nil {
			t.Fatalf("rejected owner callback deleted retained Git admin: %v", err)
		}
	case "worktree-content":
		if got, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(got) != "changed from owner callback\n" {
			t.Fatalf("rejected owner callback changed retained evidence: got=%q err=%v", got, err)
		}
	}
}

func TestReopenRejectsWorktreeAndGitAdminTampering(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func(t *testing.T, root string)
	}{
		{name: "worktree-content", tamper: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("tampered bytes\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "git-config", tamper: func(t *testing.T, root string) {
			path := filepath.Join(root, ".git", "config")
			config, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			config = append(config, []byte("[include]\n\tpath = /outside/config\n")...)
			if err := os.WriteFile(path, config, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "git-admin-replacement", tamper: func(t *testing.T, root string) {
			if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, ".git-retained")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentPath := privateTestDirectory(t, "parent")
			sourcePath := privateTestDirectory(t, "source")
			writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
			parent := openPrivateTestDirectory(t, parentPath)
			source := openPrivateTestDirectory(t, sourcePath)
			repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			approval := approvalForRepository(t, parent, repository)
			if err := repository.Close(); err != nil {
				t.Fatal(err)
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(parentPath, approval.Name)
			test.tamper(t, root)
			if _, err := Reopen(parent, approval); !errors.Is(err, ErrGitMetadata) {
				t.Fatalf("Reopen after %s tamper = %v, want ErrGitMetadata", test.name, err)
			}
			if _, err := os.Stat(filepath.Join(root, "README.md")); err != nil {
				t.Fatalf("rejected reopen deleted retained worktree content: %v", err)
			}
			if test.name == "git-admin-replacement" {
				if _, err := os.Stat(filepath.Join(root, ".git-retained", "config")); err != nil {
					t.Fatalf("rejected reopen deleted retained Git admin: %v", err)
				}
			}
		})
	}
}

func TestRepositoryIdentityDigestBindsD06ProfileAndAnchoredNodes(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	base, err := repository.ReopenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	provenance := repository.Provenance()
	snapshot := repository.Snapshot()
	mutations := []struct {
		name   string
		mutate func(*Provenance, *Snapshot, *string)
	}{
		{name: "child-name", mutate: func(p *Provenance, _ *Snapshot, name *string) {
			p.RepositoryName = "staging-00000000000000000000000000000000"
			*name = p.RepositoryName
		}},
		{name: "parent-device", mutate: func(p *Provenance, _ *Snapshot, _ *string) { p.PrivateParentDevice++ }},
		{name: "parent-inode", mutate: func(p *Provenance, _ *Snapshot, _ *string) { p.PrivateParentInode++ }},
		{name: "root-device", mutate: func(p *Provenance, _ *Snapshot, _ *string) { p.PrivateRootDevice++ }},
		{name: "root-inode", mutate: func(p *Provenance, _ *Snapshot, _ *string) { p.PrivateRootInode++ }},
		{name: "git-admin-device", mutate: func(p *Provenance, _ *Snapshot, _ *string) { p.PrivateGitDevice++ }},
		{name: "git-admin-inode", mutate: func(p *Provenance, _ *Snapshot, _ *string) { p.PrivateGitInode++ }},
		{name: "baseline-tree", mutate: func(p *Provenance, _ *Snapshot, _ *string) {
			p.SourceTreeDigest = delta.TreeDigestProfile + strings.Repeat("1", 64)
		}},
		{name: "metadata-profile", mutate: func(p *Provenance, _ *Snapshot, _ *string) {
			p.MetadataPolicyDigest = "sha256:" + strings.Repeat("b", 64)
		}},
		{name: "xattr-profile", mutate: func(p *Provenance, _ *Snapshot, _ *string) {
			p.XattrProfileDigest = "sha256:" + strings.Repeat("b", 64)
		}},
		{name: "xattr-authority", mutate: func(p *Provenance, _ *Snapshot, _ *string) {
			p.XattrAuthority = XattrVisibilityAuthority("root-attested-bounded-no-hidden-trusted/v2")
		}},
		{name: "source-binding-profile", mutate: func(p *Provenance, _ *Snapshot, _ *string) {
			p.SourceBindingDigest = "sha256:" + strings.Repeat("b", 64)
		}},
		{name: "capture-limits", mutate: func(p *Provenance, _ *Snapshot, _ *string) { p.CaptureLimits.MaxObjects++ }},
		{name: "git-executable-profile", mutate: func(p *Provenance, _ *Snapshot, _ *string) {
			p.GitExecutableDigest = "sha256:" + strings.Repeat("b", 64)
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidateProvenance := cloneProvenance(provenance)
			candidateSnapshot := cloneSnapshot(snapshot)
			candidateName := provenance.RepositoryName
			mutation.mutate(&candidateProvenance, &candidateSnapshot, &candidateName)
			candidate, err := repositoryIdentity(parent, candidateName, candidateProvenance, candidateSnapshot)
			if err != nil {
				t.Fatalf("compute candidate identity: %v", err)
			}
			if candidate.Digest() == base.Digest() {
				t.Fatal("identity digest did not bind changed trusted input")
			}
		})
	}
	if err := repository.Destroy(); err != nil {
		t.Fatal(err)
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
		XattrVisibility: XattrVisibilityAttestation{ProfileDigest: testDigest, Authority: XattrAuthorityBoundedNoHiddenTrusted},
		Binding:         testSourceVerifier{},
	}
}

func approvalForRepository(t *testing.T, parent *os.File, repository *Repository) ReopenApproval {
	t.Helper()
	identity, err := repository.ReopenIdentity()
	if err != nil {
		t.Fatalf("derive trusted external reopen pin: %v", err)
	}
	provenance := repository.Provenance()
	snapshot := repository.Snapshot()
	pin := identity.Digest()
	if pin == "" {
		t.Fatal("repository produced an empty external identity digest")
	}
	approval := ReopenApproval{
		Name:                   provenance.RepositoryName,
		ParentIdentity:         identity.Parent,
		Provenance:             provenance,
		Snapshot:               snapshot,
		ExpectedIdentityDigest: pin,
		TrustedVerifier: func(candidate RepositoryIdentity) error {
			if candidate != identity || candidate.Digest() != pin {
				return errors.New("candidate identity differs from the test's trusted host pin")
			}
			return nil
		},
		VerifyExclusiveParent: func(file *os.File, expected DirectoryIdentity) error {
			info, err := statFile(file)
			if err != nil || uint64(info.Dev) != expected.Device || uint64(info.Ino) != expected.Inode ||
				validatePrivateDirectory(file) != nil {
				return errors.New("test host owner rejected parent descriptor or owner lock")
			}
			return nil
		},
	}
	if err := validateReopenApproval(parent, approval); err != nil {
		t.Fatalf("create valid reopen approval: %v (identity=%+v digest=%q provenance=%+v)", err, identity, pin, provenance)
	}
	return approval
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

// The private-Git receipt records the bounded authority, equal to the
// workspace label it is converted to, and refuses anything else at Create and
// at Reopen.
func TestPrivateGitProvenanceCarriesOnlyTheBoundedXattrAuthority(t *testing.T) {
	if string(XattrAuthorityBoundedNoHiddenTrusted) != string(workspace.XattrAuthorityBoundedNoHiddenTrusted) {
		t.Fatalf("private-Git authority label %q differs from the workspace label %q",
			XattrAuthorityBoundedNoHiddenTrusted, workspace.XattrAuthorityBoundedNoHiddenTrusted)
	}
	for _, authority := range []XattrVisibilityAuthority{"", "complete", "root-profile-measured-complete-enumeration/v1"} {
		parentPath := privateTestDirectory(t, "parent")
		sourcePath := privateTestDirectory(t, "source")
		parent := openPrivateTestDirectory(t, parentPath)
		source := openPrivateTestDirectory(t, sourcePath)
		descriptor := testSource(source)
		descriptor.XattrVisibility.Authority = authority
		if _, err := Create(context.Background(), parent, descriptor, testOptions(testPinnedGit(t))); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("Create with xattr authority %q = %v, want ErrInvalidOptions", authority, err)
		}
	}

	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := repository.Provenance().XattrAuthority; got != XattrAuthorityBoundedNoHiddenTrusted {
		t.Fatalf("provenance xattr authority = %q, want the bounded authority", got)
	}
	approval := approvalForRepository(t, parent, repository)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	for _, authority := range []XattrVisibilityAuthority{"", "complete"} {
		forged := cloneReopenApproval(approval)
		forged.Provenance.XattrAuthority = authority
		identity, err := repositoryIdentity(parent, forged.Name, forged.Provenance, forged.Snapshot)
		if err != nil {
			t.Fatal(err)
		}
		forged.ExpectedIdentityDigest = identity.Digest()
		if _, err := Reopen(parent, forged); !errors.Is(err, ErrReopenBinding) {
			t.Fatalf("Reopen with xattr authority %q = %v, want ErrReopenBinding", authority, err)
		}
	}
	verified, err := Reopen(parent, approval)
	if err != nil {
		t.Fatalf("Reopen with the bounded authority: %v", err)
	}
	if got := verified.Provenance().XattrAuthority; got != XattrAuthorityBoundedNoHiddenTrusted {
		t.Fatalf("reopened xattr authority = %q", got)
	}
	_ = verified.Close()
}

// A provenance written before the xattr_authority field existed carries the old
// version label. It is refused as an unsupported version (an upgrade requires
// draining retained sessions first); the absent authority is never read as bounded.
func TestReopenRefusesPreAuthorityProvenanceAsAnUnsupportedVersion(t *testing.T) {
	parentPath := privateTestDirectory(t, "parent")
	sourcePath := privateTestDirectory(t, "source")
	writePrivateFile(t, filepath.Join(sourcePath, "README.md"), "snapshot bytes\n", 0o600)
	parent := openPrivateTestDirectory(t, parentPath)
	source := openPrivateTestDirectory(t, sourcePath)
	repository, err := Create(context.Background(), parent, testSource(source), testOptions(testPinnedGit(t)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approval := approvalForRepository(t, parent, repository)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	reidentify := func(a ReopenApproval) ReopenApproval {
		identity, err := repositoryIdentity(parent, a.Name, a.Provenance, a.Snapshot)
		if err != nil {
			t.Fatal(err)
		}
		a.ExpectedIdentityDigest = identity.Digest()
		return a
	}
	old := cloneReopenApproval(approval)
	old.Provenance.SchemaVersion = "tbound-private-git/v2"
	old.Provenance.XattrAuthority = ""
	_, err = Reopen(parent, reidentify(old))
	if !errors.Is(err, ErrUnsupportedVersion) || !errors.Is(err, ErrReopenBinding) || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("Reopen of a pre-authority v2 provenance = %v, want the unsupported-version error", err)
	}
	// The current version with the authority removed is a field refusal, not a
	// version refusal, and the authority is not inferred.
	missing := cloneReopenApproval(approval)
	missing.Provenance.XattrAuthority = ""
	if _, err := Reopen(parent, reidentify(missing)); !errors.Is(err, ErrReopenBinding) || errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Reopen of a v3 provenance without authority = %v, want a plain binding refusal", err)
	}
}
