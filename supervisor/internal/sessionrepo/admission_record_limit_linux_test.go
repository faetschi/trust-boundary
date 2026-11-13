//go:build linux

package sessionrepo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/workspace"
)

// The bound sits at about 2,500 files of this fixture (manifest entries of about 1,250 bytes, two
// manifests, base64 framing, 8 MiB records). Long paths make the manifest large with few files, so
// both cases seed quickly.
const (
	recordFixtureFilesOver  = 2900
	recordFixtureFilesUnder = 2350
)

// buildLongPathTree writes files files under four 250-byte directory levels and returns the root and
// the relative directory that holds them.
func buildLongPathTree(t *testing.T, root string, files int, extra map[string]string) string {
	t.Helper()
	writeFixtureDir(t, root, 0o700)
	deep, relative := root, ""
	for i := 0; i < 4; i++ {
		segment := strings.Repeat(string(rune('a'+i)), 250)
		deep, relative = filepath.Join(deep, segment), filepath.Join(relative, segment)
		writeFixtureDir(t, deep, 0o755)
	}
	for i := 0; i < files; i++ {
		writeFixtureFile(t, filepath.Join(deep, fmt.Sprintf("f%05d", i)), "x", 0o644)
	}
	for path, content := range extra {
		writeFixtureFile(t, filepath.Join(root, filepath.FromSlash(path)), content, 0o644)
	}
	return filepath.ToSlash(relative)
}

// A workspace whose smallest admission record cannot fit one audit record is refused before any
// effect: the command never runs, nothing is copied, no decision is used, and the Store stays usable.
func TestMutationRefusesOversizeAdmissionRecordBeforeEffect(t *testing.T) {
	store, _, _, authority := newE2EStore(t)
	source := filepath.Join(t.TempDir(), "source")
	relative := buildLongPathTree(t, source, recordFixtureFilesOver, nil)
	g0, err := store.Seed(openFixtureRoot(t, source), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	ran := false
	_, err = store.RunBash(context.Background(), g0, lease("oversize"), allow("oversize"),
		CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "true"}, DeclaredOutputPaths: []string{"out.txt"}},
		CommandRunnerFunc(func(context.Context, *CommandView, CommandSpec) (CommandResult, CommandSettlement, error) {
			ran = true
			return CommandResult{}, CommandSettlement{}, errors.New("runner must not be called")
		}))
	if !errors.Is(err, ErrAdmissionRecordLimit) || !errors.Is(err, workspace.ErrLimit) || ran {
		t.Fatalf("bash on oversize workspace: err=%v ran=%v, want ErrAdmissionRecordLimit before the runner", err, ran)
	}
	if _, err := store.Write(context.Background(), g0, proposal("oversize-write"), allow("oversize-write"), "out.txt", []byte("x")); !errors.Is(err, ErrAdmissionRecordLimit) {
		t.Fatalf("write on oversize workspace: %v, want ErrAdmissionRecordLimit", err)
	}

	path := relative + "/f00000"
	readOp, readDecision := proposal("after-refusal"), allow("after-refusal")
	authority.grant(OperationRequest{Tool: "read", Operation: readOp, Decision: readDecision, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: readArgumentsDigest(path, 16)})
	if got, err := g0.Read(context.Background(), readOp, readDecision, path, 16); err != nil || string(got) != "x" {
		t.Fatalf("store unusable after refusal: bytes=%q err=%v", got, err)
	}
}

// Just under the bound, a write still commits: its record really fits, so the bound does not
// understate what an admission record holds.
func TestMutationJustUnderTheAdmissionRecordBoundCommits(t *testing.T) {
	store, _, _, authority := newE2EStore(t)
	source := filepath.Join(t.TempDir(), "source")
	buildLongPathTree(t, source, recordFixtureFilesUnder, nil)
	g0, err := store.Seed(openFixtureRoot(t, source), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := checkAdmissionRecordFits(g0.snapshot.Manifest, []string{"out.txt"}); err != nil {
		t.Fatalf("fixture is not under the bound: %v", err)
	}
	expectedRoot := filepath.Join(t.TempDir(), "expected")
	buildLongPathTree(t, expectedRoot, recordFixtureFilesUnder, map[string]string{"out.txt": "x"})
	expected, err := workspace.Scan(openFixtureRoot(t, expectedRoot), store.options.workspaceOptions("g1", true))
	if err != nil {
		t.Fatal(err)
	}
	writeAndAssertMode(t, store, g0, authority, "under-bound", "out.txt", []byte("x"), expected, 1, 0o644)
}
