//go:build linux

package sessionrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestSessionRepositoryDeniedReadMatrixE2E is the E06 denied-read matrix for the
// declared-read route. It exercises reads of protected and out-of-workspace
// paths through the session repository and requires, for every row, that the
// read is denied or confined, that no content is released, and that the
// resulting disposition is reconstructable from retained evidence.
//
// Mapping to 11-evidence-contract.md E06 and 02-process-model.md §5.1:
//   - "path grammar" rows are synchronous confinements: the path never leaves
//     the approved relative grammar, so no read effect is attempted and the
//     repository stays usable.
//   - "target confinement" rows pass the grammar and the trusted receipt but the
//     descriptor-relative no-follow open fails closed; RunEffect records a
//     durable unknown outcome and reopening reproduces the quarantine.
//   - "proposal-only denial" is the one row with a durable denied-read record:
//     the trusted authorizer refuses the exact request before the effect, the
//     denial is journaled, and reopening reconstructs it.
//
// The provider-broker route is out of scope for this prototype (no real
// provider exchange); the Bash/direct-runtime routes are covered by the
// sandbox cell path-containment matrix.
func TestSessionRepositoryDeniedReadMatrixE2E(t *testing.T) {
	t.Run("absolute path is confined before any effect", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		data, err := grantedReadCall(t, g0, authority, "e06-absolute", "/etc/passwd", 1024)
		assertGrammarConfinement(t, store, data, err)
	})

	t.Run("parent traversal is confined before any effect", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		data, err := grantedReadCall(t, g0, authority, "e06-dotdot", "../outside-secret.txt", 1024)
		assertGrammarConfinement(t, store, data, err)
	})

	t.Run("nested traversal is confined before any effect", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		data, err := grantedReadCall(t, g0, authority, "e06-nested", "build/../../outside-secret.txt", 1024)
		assertGrammarConfinement(t, store, data, err)
	})

	t.Run("unknown target fails closed and quarantines", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		data, err := grantedReadCall(t, g0, authority, "e06-missing", "missing.txt", 1024)
		assertTargetConfinement(t, store, data, err)
	})

	t.Run("over-bound target fails closed and quarantines", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		data, err := grantedReadCall(t, g0, authority, "e06-over-bound", "task.txt", 4)
		assertTargetConfinement(t, store, data, err)
	})

	t.Run("symlink escape is denied and fails closed", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		outsideDir := filepath.Join(t.TempDir(), "outside")
		if err := os.Mkdir(outsideDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("outside-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// A symlink cannot be part of a sealed generation (workspace import
		// rejects it), so substitute one into the sealed tree to exercise the
		// read path's no-follow descriptor resolution.
		if err := os.Symlink(outsideDir, filepath.Join(g0.testPath(), "escape")); err != nil {
			t.Fatal(err)
		}
		data, err := grantedReadCall(t, g0, authority, "e06-symlink", "escape/secret.txt", 1024)
		assertTargetConfinement(t, store, data, err)
	})

	t.Run("proposal-only denial is recorded and reconstructable", func(t *testing.T) {
		store, g0, _ := newE06ReadStore(t)
		operation, decision := proposal("e06-ungranted"), allow("e06-ungranted")
		data, err := g0.Read(context.Background(), operation, decision, "task.txt", 1024)
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("ungranted read returned %v, want %v", err, ErrDenied)
		}
		if len(data) != 0 {
			t.Fatalf("denied read released %d bytes", len(data))
		}
		if _, err := store.Verify(); err != nil {
			t.Fatalf("denied read disturbed the sealed chain: %v", err)
		}
		assertDeniedReadEvidence(t, store, "e06-ungranted", "task.txt", 1024)

		rootPath, options := store.rootPath, store.options
		if err := store.Close(); err != nil {
			t.Fatalf("close store before reopen: %v", err)
		}
		recovered, err := Open(openFixtureRoot(t, rootPath), options)
		if err != nil {
			t.Fatalf("reopen after denied read: %v", err)
		}
		t.Cleanup(func() { _ = recovered.Close() })
		if _, err := recovered.Verify(); err != nil {
			t.Fatalf("recovered chain after denied read: %v", err)
		}
		assertDeniedReadEvidence(t, recovered, "e06-ungranted", "task.txt", 1024)
	})
}

// assertGrammarConfinement requires a pre-effect path-grammar rejection: no
// bytes are released and the repository remains fully usable.
func assertGrammarConfinement(t *testing.T, store *Store, data []byte, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("read outside the path grammar returned %v, want %v", err, ErrInvalidPath)
	}
	if len(data) != 0 {
		t.Fatalf("confined read released %d bytes", len(data))
	}
	if _, err := store.Verify(); err != nil {
		t.Fatalf("path-grammar confinement disturbed the store: %v", err)
	}
}

// assertTargetConfinement requires a fail-closed target confinement: no bytes
// are released, the in-memory store is quarantined, and reopening from the
// durable journal reproduces the quarantine rather than guessing an outcome.
func assertTargetConfinement(t *testing.T, store *Store, data []byte, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("target confinement returned a successful read")
	}
	if len(data) != 0 {
		t.Fatalf("confined read released %d bytes", len(data))
	}
	if _, err := store.Verify(); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("store after target confinement = %v, want quarantine", err)
	}
	rootPath, options := store.rootPath, store.options
	if err := store.Close(); err != nil {
		t.Fatalf("close store before reopen: %v", err)
	}
	if _, err := Open(openFixtureRoot(t, rootPath), options); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("reopen after target confinement = %v, want quarantine", err)
	}
}

// assertDeniedReadEvidence requires exactly one durable denied-read record with
// the exact path argument digest and no released content.
func assertDeniedReadEvidence(t *testing.T, store *Store, id, path string, maxBytes int64) {
	t.Helper()
	artifact, err := store.Evidence()
	if err != nil {
		t.Fatalf("evidence after denied read: %v", err)
	}
	if len(artifact.Operations) != 1 {
		t.Fatalf("denied read recorded %d operation results, want exactly 1 denial: %+v", len(artifact.Operations), artifact.Operations)
	}
	var denied []OperationEvidence
	for _, operation := range artifact.Operations {
		if operation.Outcome == "denied" {
			denied = append(denied, operation)
		}
	}
	if len(denied) != 1 {
		t.Fatalf("durable denied-read records = %d, want 1: %+v", len(denied), artifact.Operations)
	}
	operation := denied[0]
	decisionID := "decision-" + id
	if operation.Tool != "read" || operation.DecisionID != decisionID ||
		operation.ArgumentDigest != readArgumentsDigest(path, maxBytes) ||
		operation.OutputGeneration != "" {
		t.Fatalf("denied-read evidence = %+v, want a denied read of %q", operation, path)
	}
	for _, generation := range artifact.Generations {
		if len(generation.Manifest.Objects) == 0 {
			t.Fatalf("denied read corrupted a generation manifest: %+v", generation)
		}
	}
}

// newE06ReadStore seeds a fresh repository whose only readable regular files are
// task.txt and build/result.txt.
func newE06ReadStore(t *testing.T) (*Store, *Generation, *e2eReceiptAuthority) {
	t.Helper()
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureDir(t, filepath.Join(sourcePath, "build"), 0o755)
	writeFixtureFile(t, filepath.Join(sourcePath, "task.txt"), "protected content\n", 0o644)
	writeFixtureFile(t, filepath.Join(sourcePath, "build", "result.txt"), "build result\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	return store, g0, authority
}

// grantedReadCall installs the trusted receipt for an exact read request and
// performs it against the sealed tip.
func grantedReadCall(t *testing.T, g0 *Generation, authority *e2eReceiptAuthority, id, path string, maxBytes int64) ([]byte, error) {
	t.Helper()
	operation, decision := proposal(id), allow(id)
	authority.grant(OperationRequest{Tool: "read", Operation: operation, Decision: decision,
		InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: readArgumentsDigest(path, maxBytes)})
	return g0.Read(context.Background(), operation, decision, path, maxBytes)
}
