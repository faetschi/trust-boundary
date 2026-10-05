//go:build linux

package sessionrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestSessionRepositoryEvidenceBundleReconstructE2E is the reconstructable-
// evidence fixture (thesis todo exit criterion #5). It runs an E04 create/delete
// revert plus an E06 proposal-only denied read, captures the self-contained
// bundle, discards the live Store, and re-derives the identical disposition
// from the retained bytes alone. It also checks that the retained journal bytes
// are exactly the on-disk journal and that both emission and reconstruction are
// deterministic.
func TestSessionRepositoryEvidenceBundleReconstructE2E(t *testing.T) {
	store, _, journalPath, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	buildWriteFixtureTree(t, sourcePath, writeFixtureFiles())
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}

	// g0 -> g1 create, g1 -> g2 delete: a net-reverted tree with both
	// intervening mutations retained in the approved-delta ledger.
	created := append(cloneWriteFixtureFiles(writeFixtureFiles()), writeFixtureFileSpec{path: "created.txt", content: "created\n", mode: 0o644})
	g1 := writeAndAssertMode(t, store, g0, authority, "replay-create", "created.txt", []byte("created\n"), expectedWriteSnapshot(t, "g1", created), 1, 0o644)
	deleteAndAssert(t, store, g1, authority, "replay-delete", "created.txt", expectedWriteSnapshot(t, "g2", cloneWriteFixtureFiles(writeFixtureFiles())), 2)

	// A proposal-only denied read is durable denial evidence with no effect.
	deniedOp, deniedDecision := proposal("replay-denied"), allow("replay-denied")
	if _, err := g0.Read(context.Background(), deniedOp, deniedDecision, "README.md", 1024); !errors.Is(err, ErrDenied) {
		t.Fatalf("ungranted read = %v, want %v", err, ErrDenied)
	}

	artifact, err := store.Evidence()
	if err != nil {
		t.Fatalf("evidence before bundle: %v", err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatalf("evidence bundle: %v", err)
	}
	onDisk, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bundle.AuditJournal, onDisk) {
		t.Fatalf("retained audit journal is not the exact on-disk bytes")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store before replay: %v", err)
	}

	disposition, err := Reconstruct(bundle)
	if err != nil {
		t.Fatalf("reconstruct bundle without the store: %v", err)
	}
	if disposition.Quarantined {
		t.Fatalf("clean run reconstructed as quarantined: %s", disposition.QuarantineReason)
	}
	if !reflect.DeepEqual(disposition.Generations, artifact.Generations) {
		t.Fatalf("reconstructed generations differ:\n got %+v\nwant %+v", disposition.Generations, artifact.Generations)
	}
	if !reflect.DeepEqual(disposition.Ledger, artifact.ApprovedDeltaLedger) {
		t.Fatalf("reconstructed ledger differs:\n got %+v\nwant %+v", disposition.Ledger, artifact.ApprovedDeltaLedger)
	}
	if !reflect.DeepEqual(disposition.Operations, artifact.Operations) {
		t.Fatalf("reconstructed operations differ:\n got %+v\nwant %+v", disposition.Operations, artifact.Operations)
	}
	if disposition.Baseline != "g0" || disposition.Sealed != "g2" {
		t.Fatalf("reconstructed endpoints = %s -> %s, want g0 -> g2", disposition.Baseline, disposition.Sealed)
	}
	if disposition.Attempts != 3 || disposition.Effects != 3 || disposition.Successes != 3 ||
		disposition.Unknowns != 0 || disposition.Denials != 1 || disposition.Decisions != 3 {
		t.Fatalf("unexpected reconstructed counters: %+v", disposition)
	}
	if disposition.AuditRecordCount != artifact.AuditRecordCount || disposition.AuditHeadHash != artifact.AuditHeadHash {
		t.Fatalf("reconstructed journal commitment differs from the artifact: %+v", disposition)
	}
	if artifact.AuditHeadHash == "" {
		t.Fatal("bundle omitted the audit head commitment")
	}

	// Emission and reconstruction are deterministic.
	again, err := Reconstruct(bundle)
	if err != nil {
		t.Fatalf("reconstruct bundle a second time: %v", err)
	}
	if !reflect.DeepEqual(disposition, again) {
		t.Fatalf("reconstruction is not deterministic")
	}
	firstJSON, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(bundle)
	if err != nil || !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("bundle serialization is not deterministic")
	}
}

// TestSessionRepositoryEvidenceBundleReconstructsQuarantineE2E proves that a
// legitimately open/unknown run is still emitted as a self-contained bundle and
// reconstructs to an explicit quarantine disposition rather than being treated
// as reconstruction failure or silently as success.
func TestSessionRepositoryEvidenceBundleReconstructsQuarantineE2E(t *testing.T) {
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(sourcePath, "task.txt"), "protected content\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	// A granted read of an absent target fails inside the durable effect: the
	// outcome is durably unknown and the store is quarantined.
	if _, err := grantedReadCall(t, g0, authority, "replay-missing", "missing.txt", 1024); err == nil {
		t.Fatal("read of an absent target unexpectedly succeeded")
	}
	if _, err := store.Verify(); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("store after unknown read = %v, want quarantine", err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatalf("evidence bundle from a quarantined store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store before replay: %v", err)
	}
	disposition, err := Reconstruct(bundle)
	if err != nil {
		t.Fatalf("reconstruct quarantine bundle: %v", err)
	}
	if !disposition.Quarantined || disposition.QuarantineReason == "" {
		t.Fatalf("unknown outcome did not reconstruct as quarantined: %+v", disposition)
	}
	if disposition.Baseline != "g0" || disposition.Sealed != "g0" {
		t.Fatalf("quarantined prefix endpoints = %s -> %s, want g0 -> g0", disposition.Baseline, disposition.Sealed)
	}
	if disposition.Attempts != 2 || disposition.Effects != 2 || disposition.Successes != 1 || disposition.Unknowns != 1 {
		t.Fatalf("unexpected quarantined counters: %+v", disposition)
	}
}

// TestSessionRepositoryEvidenceBundleTamperFailsClosedE2E mutates one retained
// field at a time and requires reconstruction to fail closed with
// ErrInvalidEvidenceBundle rather than trusting the serialized disposition.
func TestSessionRepositoryEvidenceBundleTamperFailsClosedE2E(t *testing.T) {
	base := buildReplayBundle(t)
	cases := []struct {
		name   string
		mutate func(*EvidenceBundle)
	}{
		{"audit journal byte", func(bundle *EvidenceBundle) {
			bundle.AuditJournal[0] ^= 0x01
		}},
		{"audit record count", func(bundle *EvidenceBundle) {
			bundle.AuditRecordCount++
		}},
		{"audit head hash", func(bundle *EvidenceBundle) {
			bundle.AuditHeadHash = flipEvidenceDigest(bundle.AuditHeadHash)
		}},
		{"generation tree digest", func(bundle *EvidenceBundle) {
			bundle.Generations[0].TreeDigest = flipEvidenceDigest(bundle.Generations[0].TreeDigest)
		}},
		{"baseline manifest", func(bundle *EvidenceBundle) {
			bundle.Baseline.Manifest.Objects[0].State.ContentDigest = flipEvidenceDigest(bundle.Baseline.Manifest.Objects[0].State.ContentDigest)
			bundle.Generations[0].Manifest.Objects[0].State.ContentDigest = bundle.Baseline.Manifest.Objects[0].State.ContentDigest
		}},
		{"approved ledger change", func(bundle *EvidenceBundle) {
			bundle.ApprovedDeltaLedger[0].Changes[0].After.ContentDigest = flipEvidenceDigest(bundle.ApprovedDeltaLedger[0].Changes[0].After.ContentDigest)
		}},
		{"operation result digest", func(bundle *EvidenceBundle) {
			bundle.Operations[0].ResultDigest = flipEvidenceDigest(bundle.Operations[0].ResultDigest)
		}},
		{"validated chain sealed digest", func(bundle *EvidenceBundle) {
			bundle.ValidatedChain.SealedTreeDigest = flipEvidenceDigest(bundle.ValidatedChain.SealedTreeDigest)
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tampered := deepCopyBundle(t, base)
			testCase.mutate(&tampered)
			disposition, err := Reconstruct(tampered)
			if !errors.Is(err, ErrInvalidEvidenceBundle) {
				t.Fatalf("tampered bundle reconstructed: disposition=%+v err=%v", disposition, err)
			}
		})
	}
}

// buildReplayBundle seeds a repository, commits one write, and returns the
// self-contained bundle after closing the live Store.
func buildReplayBundle(t *testing.T) EvidenceBundle {
	t.Helper()
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	buildWriteFixtureTree(t, sourcePath, writeFixtureFiles())
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	created := append(cloneWriteFixtureFiles(writeFixtureFiles()), writeFixtureFileSpec{path: "created.txt", content: "created\n", mode: 0o644})
	writeAndAssertMode(t, store, g0, authority, "tamper-create", "created.txt", []byte("created\n"), expectedWriteSnapshot(t, "g1", created), 1, 0o644)
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatalf("evidence bundle: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store before replay: %v", err)
	}
	return bundle
}

func deepCopyBundle(t *testing.T, bundle EvidenceBundle) EvidenceBundle {
	t.Helper()
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var cloned EvidenceBundle
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

// flipEvidenceDigest changes one hex character while preserving the digest
// grammar, so the tampered value is structurally valid but wrong.
func flipEvidenceDigest(value string) string {
	if value == "" {
		return value
	}
	mutated := []byte(value)
	last := len(mutated) - 1
	if mutated[last] == '0' {
		mutated[last] = '1'
	} else {
		mutated[last] = '0'
	}
	return string(mutated)
}
