//go:build linux

package sessionrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/workspace"
)

// TestSessionRepositoryWriteCanonicalModeE2E is a regression test for the
// new-file permission bug in Store.Write: it previously passed the literal
// 0o644 as replaceRegularAt's executable-bits argument, whose nonzero value
// yielded a 0755 output. New regular files must default to 0644, while an
// existing file's executable bit must be preserved across an overwrite.
func TestSessionRepositoryWriteCanonicalModeE2E(t *testing.T) {
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	buildWriteFixtureTree(t, sourcePath, writeFixtureFiles())
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}

	// New file: the path does not exist, so the output must be canonical 0644.
	created := append(cloneWriteFixtureFiles(writeFixtureFiles()), writeFixtureFileSpec{path: "created.txt", content: "created\n", mode: 0o644})
	g1 := writeAndAssertMode(t, store, g0, authority, "new-file", "created.txt", []byte("created\n"), expectedWriteSnapshot(t, "g1", created), 1, 0o644)

	// Overwrite an existing non-executable file: mode stays 0644.
	plain := cloneWriteFixtureFiles(created)
	plain[1].content = "plain updated\n"
	g2 := writeAndAssertMode(t, store, g1, authority, "overwrite-plain", "plain.txt", []byte("plain updated\n"), expectedWriteSnapshot(t, "g2", plain), 2, 0o644)

	// Overwrite an existing executable file: mode stays 0755.
	script := cloneWriteFixtureFiles(plain)
	script[2].content = "#!/bin/sh\necho updated\n"
	writeAndAssertMode(t, store, g2, authority, "overwrite-script", "script.sh", []byte("#!/bin/sh\necho updated\n"), expectedWriteSnapshot(t, "g3", script), 3, 0o755)

	chain, err := store.Verify()
	if err != nil {
		t.Fatalf("verify g0->g1->g2->g3 chain: %v", err)
	}
	if chain.TransitionCount != 3 || chain.BaselineGeneration != "g0" || chain.SealedGeneration != "g3" {
		t.Fatalf("unexpected validated chain after writes: %+v", chain)
	}
}

// writeFixtureFileSpec describes one regular file in a fixture tree. Parent
// directories are always created canonical (0755) and the root as 0700.
type writeFixtureFileSpec struct {
	path    string
	content string
	mode    os.FileMode
}

// writeFixtureFiles returns the canonical baseline shared by every expected
// snapshot in TestSessionRepositoryWriteCanonicalModeE2E. script.sh is
// executable so the write preserve branch is exercised for both modes.
func writeFixtureFiles() []writeFixtureFileSpec {
	return []writeFixtureFileSpec{
		{path: "README.md", content: "baseline text\n", mode: 0o644},
		{path: "plain.txt", content: "plain baseline\n", mode: 0o644},
		{path: "script.sh", content: "#!/bin/sh\necho baseline\n", mode: 0o755},
		{path: "build/result.txt", content: "baseline result\n", mode: 0o644},
	}
}

func cloneWriteFixtureFiles(files []writeFixtureFileSpec) []writeFixtureFileSpec {
	cloned := make([]writeFixtureFileSpec, len(files))
	copy(cloned, files)
	return cloned
}

func buildWriteFixtureTree(t *testing.T, rootPath string, files []writeFixtureFileSpec) {
	t.Helper()
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"build"} {
		writeFixtureDir(t, filepath.Join(rootPath, dir), 0o755)
	}
	for _, file := range files {
		writeFixtureFile(t, filepath.Join(rootPath, filepath.FromSlash(file.path)), file.content, file.mode)
	}
}

func expectedWriteSnapshot(t *testing.T, generation string, files []writeFixtureFileSpec) workspace.Snapshot {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "expected")
	buildWriteFixtureTree(t, rootPath, files)
	options := workspace.Options{
		Generation: generation, MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{
			ProfileDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Complete: true,
		},
	}
	snapshot, err := workspace.Scan(openFixtureRoot(t, rootPath), options)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func writeArgumentsDigestForTest(path string, content []byte) string {
	digest, _ := argumentsDigest(struct {
		Path          string `json:"path"`
		ContentDigest string `json:"content_digest"`
		ContentBytes  int    `json:"content_bytes"`
	}{path, digestBytes(content), len(content)})
	return digest
}

// writeAndAssertMode performs one authorized Write and asserts the mutation
// succeeded, the tip advanced to the expected generation, and the resulting
// file has exactly wantMode.
func writeAndAssertMode(t *testing.T, store *Store, input *Generation, authority *e2eReceiptAuthority, id, path string, content []byte, expected workspace.Snapshot, sequence uint64, wantMode os.FileMode) *Generation {
	t.Helper()
	operation, decision := proposal(id), allow(id)
	argumentDigest := writeArgumentsDigestForTest(path, content)
	request := OperationRequest{Tool: "write", Operation: operation, Decision: decision,
		InputGeneration: input.ID(), InputTreeDigest: input.TreeDigest(), ArgumentDigest: argumentDigest}
	authority.grant(request)
	authority.grantBinding(decision.ID, exactBinding(input.snapshot, expected, request, sequence))
	result, err := store.Write(context.Background(), input, operation, decision, path, content)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if result.Outcome != "success" || result.Generation == nil {
		t.Fatalf("write %s did not succeed: %+v", path, result)
	}
	if got := result.Generation.ID(); got != expected.Manifest.Generation {
		t.Fatalf("write %s advanced tip to %s, want %s", path, got, expected.Manifest.Generation)
	}
	if last := store.generations[len(store.generations)-1]; last != result.Generation {
		t.Fatalf("write %s did not advance the in-memory tip", path)
	}
	info, err := os.Stat(filepath.Join(result.Generation.testPath(), filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("stat written file %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != wantMode {
		t.Fatalf("mode of %s = %04o, want %04o", path, got, wantMode)
	}
	return result.Generation
}
