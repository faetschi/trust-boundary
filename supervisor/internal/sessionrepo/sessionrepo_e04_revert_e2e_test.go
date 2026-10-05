//go:build linux

package sessionrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

// TestSessionRepositoryRecordsCreateDeleteAndModifyRevertE2E is the E04
// temporal-workspace fixture: create/delete-and-revert and modify/revert both
// leave the sealed tip byte-identical to the baseline, but the approved-delta
// ledger and ordered transitions must still record every intervening mutation.
// A later equal manifest does not establish that no transient mutation occurred
// (11-evidence-contract.md E04; 13-oracle-coverage-matrix.md EFF-WS-02;
// 02-process-model.md §5.4 "delete/recreate and revert are composed").
//
// Each subtest also closes the store and reopens it from the durable journal
// and repository state to prove the same disposition is reconstructable.
func TestSessionRepositoryRecordsCreateDeleteAndModifyRevertE2E(t *testing.T) {
	t.Run("create then delete reverts the tree with both transitions retained", func(t *testing.T) {
		store, _, _, authority := newE2EStore(t)
		sourcePath := filepath.Join(t.TempDir(), "source")
		buildWriteFixtureTree(t, sourcePath, writeFixtureFiles())
		g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
		if err != nil {
			t.Fatalf("seed g0: %v", err)
		}
		baselineDigest := g0.TreeDigest()
		baselineManifest := g0.Manifest()

		// g0 -> g1: create a regular file that is absent from the baseline.
		created := append(cloneWriteFixtureFiles(writeFixtureFiles()), writeFixtureFileSpec{path: "created.txt", content: "created\n", mode: 0o644})
		g1 := writeAndAssertMode(t, store, g0, authority, "e04-create", "created.txt", []byte("created\n"), expectedWriteSnapshot(t, "g1", created), 1, 0o644)

		// g1 -> g2: delete it. The final sealed tree content equals g0, but the
		// creation and deletion are both claim-bearing ledger events.
		reverted := cloneWriteFixtureFiles(writeFixtureFiles())
		g2 := deleteAndAssert(t, store, g1, authority, "e04-delete", "created.txt", expectedWriteSnapshot(t, "g2", reverted), 2)

		assertRevertedSameTree(t, g0, g2, baselineDigest, baselineManifest)
		assertRevertLedger(t, store, "write", false, true, "delete", true, false, "created.txt")
	})

	t.Run("modify then revert retains both transitions", func(t *testing.T) {
		store, _, _, authority := newE2EStore(t)
		sourcePath := filepath.Join(t.TempDir(), "source")
		buildWriteFixtureTree(t, sourcePath, writeFixtureFiles())
		g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
		if err != nil {
			t.Fatalf("seed g0: %v", err)
		}
		baselineDigest := g0.TreeDigest()
		baselineManifest := g0.Manifest()

		// g0 -> g1: overwrite README.md with different bytes.
		changed := cloneWriteFixtureFiles(writeFixtureFiles())
		changed[0].content = "changed text\n"
		g1 := writeAndAssertMode(t, store, g0, authority, "e04-modify", "README.md", []byte("changed text\n"), expectedWriteSnapshot(t, "g1", changed), 1, 0o644)

		// g1 -> g2: restore the original bytes.
		restored := cloneWriteFixtureFiles(writeFixtureFiles())
		g2 := writeAndAssertMode(t, store, g1, authority, "e04-revert", "README.md", []byte("baseline text\n"), expectedWriteSnapshot(t, "g2", restored), 2, 0o644)

		assertRevertedSameTree(t, g0, g2, baselineDigest, baselineManifest)
		assertRevertLedger(t, store, "write", true, true, "write", true, true, "README.md")
	})
}

// assertRevertedSameTree checks that the reverted tip has the same complete
// object set and tree digest as the baseline while remaining a distinct
// generation label.
func assertRevertedSameTree(t *testing.T, baseline, reverted *Generation, baselineDigest string, baselineManifest delta.TreeManifest) {
	t.Helper()
	if reverted.ID() == baseline.ID() {
		t.Fatalf("reverted generation reused the baseline label %q", baseline.ID())
	}
	if got := reverted.TreeDigest(); got != baselineDigest {
		t.Fatalf("reverted tree digest = %s, want baseline %s", got, baselineDigest)
	}
	if got := reverted.Manifest(); !sameObjectsOnly(got, baselineManifest) {
		t.Fatalf("reverted manifest objects differ from the baseline:\n got %+v\nwant %+v", got, baselineManifest)
	}
}

// assertRevertLedger checks that the ordered approved-delta ledger records both
// intervening mutations, that the net-reverted path is retained as touched but
// omitted from the composed net delta, and that the durable repository state
// and evidence artifact each carry the complete ledger.
func assertRevertLedger(t *testing.T, store *Store, firstTool string, firstBefore, firstAfter bool, secondTool string, secondBefore, secondAfter bool, path string) {
	t.Helper()
	if len(store.transitions) != 2 {
		t.Fatalf("approved-delta ledger has %d transitions, want 2", len(store.transitions))
	}
	assertTransitionChange(t, store.transitions[0], firstTool, path, firstBefore, firstAfter)
	assertTransitionChange(t, store.transitions[1], secondTool, path, secondBefore, secondAfter)

	chain, err := store.Verify()
	if err != nil {
		t.Fatalf("verify create/delete or modify/revert chain: %v", err)
	}
	if chain.TransitionCount != 2 || chain.BaselineGeneration != "g0" || chain.SealedGeneration != "g2" {
		t.Fatalf("unexpected validated chain: %+v", chain)
	}
	if len(chain.ComposedChanges) != 0 {
		t.Fatalf("net-reverted fixture must compose to no change, got %+v", chain.ComposedChanges)
	}
	if got := strings.Join(chain.TouchedPaths, ","); got != path {
		t.Fatalf("touched paths = %q, want %q", got, path)
	}

	state, exists, err := readRepositoryState(store.root)
	if err != nil || !exists {
		t.Fatalf("read durable repository state: exists=%t err=%v", exists, err)
	}
	if len(state.ApprovedDeltaLedger) != 2 {
		t.Fatalf("durable repository state ledger has %d transitions, want 2", len(state.ApprovedDeltaLedger))
	}

	artifact, err := store.Evidence()
	if err != nil {
		t.Fatalf("build evidence artifact: %v", err)
	}
	if len(artifact.Generations) != 3 || len(artifact.ApprovedDeltaLedger) != 2 {
		t.Fatalf("evidence artifact omits generations/ledger: generations=%d ledger=%d", len(artifact.Generations), len(artifact.ApprovedDeltaLedger))
	}

	assertRecoveredRevertDisposition(t, store, chain, path)
}

// assertRecoveredRevertDisposition closes the store, reopens it from the durable
// journal and repository state, and requires the same chain disposition, the
// same two-entry ledger, and the same final on-disk state.
func assertRecoveredRevertDisposition(t *testing.T, store *Store, chain delta.Result, path string) {
	t.Helper()
	rootPath, options := store.rootPath, store.options
	if err := store.Close(); err != nil {
		t.Fatalf("close store before recovery: %v", err)
	}
	recovered, err := Open(openFixtureRoot(t, rootPath), options)
	if err != nil {
		t.Fatalf("reopen from durable journal and state: %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })

	recoveredChain, err := recovered.Verify()
	if err != nil {
		t.Fatalf("verify recovered chain: %v", err)
	}
	if recoveredChain.SealedGeneration != chain.SealedGeneration ||
		recoveredChain.BaselineGeneration != chain.BaselineGeneration ||
		recoveredChain.TransitionCount != chain.TransitionCount ||
		len(recoveredChain.ComposedChanges) != len(chain.ComposedChanges) ||
		strings.Join(recoveredChain.TouchedPaths, ",") != strings.Join(chain.TouchedPaths, ",") {
		t.Fatalf("recovered disposition = %+v, want %+v", recoveredChain, chain)
	}
	recoveredEvidence, err := recovered.Evidence()
	if err != nil {
		t.Fatalf("recovered evidence: %v", err)
	}
	if len(recoveredEvidence.Generations) != 3 || len(recoveredEvidence.ApprovedDeltaLedger) != 2 {
		t.Fatalf("recovered evidence is incomplete: generations=%d ledger=%d", len(recoveredEvidence.Generations), len(recoveredEvidence.ApprovedDeltaLedger))
	}
	if len(recoveredEvidence.Operations) != 2 {
		t.Fatalf("recovered evidence has %d operations, want 2", len(recoveredEvidence.Operations))
	}

	recoveredTip := recovered.generations[len(recovered.generations)-1]
	content, err := os.ReadFile(filepath.Join(recoveredTip.testPath(), filepath.FromSlash(path)))
	if path == "created.txt" {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovered tip still contains the deleted file: err=%v", err)
		}
	} else if err != nil || string(content) != "baseline text\n" {
		t.Fatalf("recovered tip did not restore %s: content=%q err=%v", path, content, err)
	}
}

// sameObjectsOnly compares the policy-relevant object set of two manifests,
// ignoring the generation label (the reverted tip is a distinct generation).
func sameObjectsOnly(left, right delta.TreeManifest) bool {
	if left.MetadataPolicyDigest != right.MetadataPolicyDigest || len(left.Objects) != len(right.Objects) {
		return false
	}
	for index := range left.Objects {
		if left.Objects[index] != right.Objects[index] {
			return false
		}
	}
	return true
}

func assertTransitionChange(t *testing.T, transition delta.Transition, tool, path string, beforeExists, afterExists bool) {
	t.Helper()
	if transition.Tool != tool || len(transition.Changes) != 1 {
		t.Fatalf("transition %s: tool=%q changes=%d, want one %s change", transition.ID, transition.Tool, len(transition.Changes), tool)
	}
	change := transition.Changes[0]
	if change.Path != path || change.Before.Exists != beforeExists || change.After.Exists != afterExists {
		t.Fatalf("transition %s change = %+v, want %s exists %t->%t", transition.ID, change, path, beforeExists, afterExists)
	}
}

// TestSessionRepositoryDeleteRejectsAbsentAndUnsupportedTargetsE2E is the
// focused negative test for the Delete capability added for the E04 fixtures.
// A delete may only remove a regular file already present in the approved tree;
// absent and non-regular targets fail inside the durable effect and quarantine
// the store exactly as an Edit of an absent target does. A path outside the
// approved relative-path grammar is rejected before any effect, and an
// unauthorized delete never advances the tip.
func TestSessionRepositoryDeleteRejectsAbsentAndUnsupportedTargetsE2E(t *testing.T) {
	seedDeleteFixture := func(t *testing.T) (*Store, *e2eReceiptAuthority, *Generation) {
		t.Helper()
		store, _, _, authority := newE2EStore(t)
		sourcePath := filepath.Join(t.TempDir(), "source")
		if err := os.Mkdir(sourcePath, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFixtureDir(t, filepath.Join(sourcePath, "build"), 0o755)
		writeFixtureFile(t, filepath.Join(sourcePath, "task.txt"), "unchanged\n", 0o644)
		g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
		if err != nil {
			t.Fatalf("seed g0: %v", err)
		}
		return store, authority, g0
	}

	t.Run("absent target fails closed and quarantines", func(t *testing.T) {
		store, authority, g0 := seedDeleteFixture(t)
		grantedDelete(t, authority, g0, "e04-delete-absent", "missing.txt")
		operation, decision := proposal("e04-delete-absent"), allow("e04-delete-absent")
		if _, err := store.Delete(context.Background(), g0, operation, decision, "missing.txt"); !errors.Is(err, ErrAudit) {
			t.Fatalf("delete of an absent target returned %v, want unknown audited effect", err)
		}
		if len(store.generations) != 1 || len(store.transitions) != 0 {
			t.Fatalf("failed delete advanced the head: generations=%d transitions=%d", len(store.generations), len(store.transitions))
		}
		if _, err := store.Verify(); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("store verification after absent-target delete = %v, want quarantine", err)
		}
	})

	t.Run("directory target is not a supported delete", func(t *testing.T) {
		store, authority, g0 := seedDeleteFixture(t)
		grantedDelete(t, authority, g0, "e04-delete-dir", "build")
		operation, decision := proposal("e04-delete-dir"), allow("e04-delete-dir")
		if _, err := store.Delete(context.Background(), g0, operation, decision, "build"); !errors.Is(err, ErrAudit) {
			t.Fatalf("delete of a directory returned %v, want unknown audited effect", err)
		}
		if _, err := store.Verify(); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("store verification after directory delete = %v, want quarantine", err)
		}
	})

	t.Run("traversal path is rejected before any effect", func(t *testing.T) {
		store, authority, g0 := seedDeleteFixture(t)
		grantedDelete(t, authority, g0, "e04-delete-traversal", "../task.txt")
		operation, decision := proposal("e04-delete-traversal"), allow("e04-delete-traversal")
		if _, err := store.Delete(context.Background(), g0, operation, decision, "../task.txt"); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("delete outside the path grammar returned %v, want %v", err, ErrInvalidPath)
		}
		if _, err := store.Verify(); err != nil {
			t.Fatalf("rejected traversal delete disturbed the store: %v", err)
		}
	})

	t.Run("unauthorized delete never runs", func(t *testing.T) {
		store, _, g0 := seedDeleteFixture(t)
		operation, decision := proposal("e04-delete-ungranted"), allow("e04-delete-ungranted")
		if _, err := store.Delete(context.Background(), g0, operation, decision, "task.txt"); !errors.Is(err, ErrDenied) {
			t.Fatalf("ungranted delete returned %v, want denial", err)
		}
		if len(store.generations) != 1 || len(store.transitions) != 0 {
			t.Fatalf("denied delete advanced the head: generations=%d transitions=%d", len(store.generations), len(store.transitions))
		}
	})
}

// grantedDelete installs the trusted receipt for an exact Delete request.
func grantedDelete(t *testing.T, authority *e2eReceiptAuthority, input *Generation, id, path string) {
	t.Helper()
	operation, decision := proposal(id), allow(id)
	authority.grant(OperationRequest{Tool: "delete", Operation: operation, Decision: decision,
		InputGeneration: input.ID(), InputTreeDigest: input.TreeDigest(), ArgumentDigest: deleteArgumentsDigestForTest(path)})
}

func deleteArgumentsDigestForTest(path string) string {
	digest, _ := argumentsDigest(struct {
		Path string `json:"path"`
	}{path})
	return digest
}

// deleteAndAssert performs one authorized Delete and asserts the tip advanced,
// the in-memory head moved, and the target is gone from the resulting sealed
// generation.
func deleteAndAssert(t *testing.T, store *Store, input *Generation, authority *e2eReceiptAuthority, id, path string, expected workspace.Snapshot, sequence uint64) *Generation {
	t.Helper()
	operation, decision := proposal(id), allow(id)
	argumentDigest := deleteArgumentsDigestForTest(path)
	request := OperationRequest{Tool: "delete", Operation: operation, Decision: decision,
		InputGeneration: input.ID(), InputTreeDigest: input.TreeDigest(), ArgumentDigest: argumentDigest}
	authority.grant(request)
	authority.grantBinding(decision.ID, exactBinding(input.snapshot, expected, request, sequence))
	result, err := store.Delete(context.Background(), input, operation, decision, path)
	if err != nil {
		t.Fatalf("delete %s: %v", path, err)
	}
	if result.Outcome != "success" || result.Generation == nil {
		t.Fatalf("delete %s did not succeed: %+v", path, result)
	}
	if got := result.Generation.ID(); got != expected.Manifest.Generation {
		t.Fatalf("delete %s advanced tip to %s, want %s", path, got, expected.Manifest.Generation)
	}
	if last := store.generations[len(store.generations)-1]; last != result.Generation {
		t.Fatalf("delete %s did not advance the in-memory tip", path)
	}
	if _, err := os.Lstat(filepath.Join(result.Generation.testPath(), filepath.FromSlash(path))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete %s left the target present: %v", path, err)
	}
	return result.Generation
}
