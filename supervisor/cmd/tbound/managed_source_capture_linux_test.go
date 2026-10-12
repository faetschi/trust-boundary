//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

func TestManagedSourceLeaseEvidenceRequiresDurableReadyMarkerForEmptyPublicationLedger(t *testing.T) {
	root := privateGovernedTestRoot(t)
	livePath := filepath.Join(root, "live")
	if err := os.Mkdir(livePath, 0o700); err != nil {
		t.Fatal(err)
	}
	live, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	journal, err := audit.Open(filepath.Join(root, "publication.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	lease, err := newFakeManagedLiveRootAuthority().acquire(context.Background(), live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest}, journal,
		"empty-ledger-run", "empty-ledger-workflow", "empty-ledger-publication", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lease.close(); err != nil {
			t.Errorf("release managed-root lease: %v", err)
		}
	}()

	if evidence, err := lease.currentManagedSourceLeaseEvidence(context.Background()); err == nil {
		t.Fatalf("source evidence was issued before recovery marker: %+v", evidence)
	}
	if err := lease.completeAdmissionRecovery(context.Background()); err != nil {
		t.Fatalf("complete admission recovery for an otherwise empty publication ledger: %v", err)
	}
	evidence, err := lease.currentManagedSourceLeaseEvidence(context.Background())
	if err != nil {
		t.Fatalf("issue source evidence after durable empty-ledger recovery: %v", err)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("durable recovery evidence did not validate: %v", err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 2 || trace.Records[0].Event.Kind != "managed_live_root_run_admitted" ||
		trace.Records[1].Event.Kind != managedLiveRootSourceCaptureReadyKind ||
		evidence.AdmissionSequence != trace.Records[0].Sequence || evidence.AdmissionRecordHash != managedSourceJournalDigest(trace.Records[0].Hash) ||
		evidence.RecoveryRecordSequence != trace.Records[1].Sequence || evidence.RecoveryRecordHash != managedSourceJournalDigest(trace.Records[1].Hash) ||
		evidence.LeaseJournalRecordCount != 2 || evidence.LeaseJournalHead != managedSourceJournalDigest(trace.Records[1].Hash) {
		t.Fatalf("empty-ledger evidence was not anchored to the durable ready tail: evidence=%+v records=%+v", evidence, trace.Records)
	}
	var ready managedLiveRootSourceCaptureReadyEvent
	if err := json.Unmarshal(trace.Records[1].Event.Data, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.LifecycleJournalSequence != trace.Records[0].Sequence ||
		ready.LifecycleJournalRecordHash != trace.Records[0].Hash ||
		ready.AdmissionSequence != lease.admissionSeq || ready.AdmissionRecordHash != lease.admissionHash {
		t.Fatalf("recovery-ready marker did not bind the admission-only journal head: %+v", ready)
	}

	lease.mu.Lock()
	lease.taskAdmitted = true
	lease.mu.Unlock()
	if evidence, err := lease.currentManagedSourceLeaseEvidence(context.Background()); err == nil {
		t.Fatalf("source evidence survived task admission: %+v", evidence)
	}
	lease.mu.Lock()
	lease.taskAdmitted = false
	lease.mu.Unlock()

	if _, err := journal.Append(audit.Event{Kind: "test_lifecycle_change", ID: "test-lifecycle-change"}); err != nil {
		t.Fatal(err)
	}
	if evidence, err := lease.currentManagedSourceLeaseEvidence(context.Background()); err == nil {
		t.Fatalf("source evidence survived a lifecycle journal change: %+v", evidence)
	}
}

func TestManagedSourceSeedBridgeBindsQualificationToG0(t *testing.T) {
	store, journal, source, _ := newManagedSourceSeedFixture(t)
	calls := 0
	proof := managedSourceSeedTestProof("seed-success", func(ctx context.Context, candidate *os.File) error {
		calls++
		if ctx == nil || candidate != source {
			return errors.New("unexpected source descriptor or context")
		}
		return nil
	})
	generation, err := seedManagedRootSourceWithProof(context.Background(), store, source, journal, "seed-success", proof)
	if err != nil {
		t.Fatalf("seed through a qualified source proof: %v", err)
	}
	if generation == nil || generation.ID() != "g0" || calls != 2 {
		t.Fatalf("qualified seed result=%v revalidation_calls=%d", generation, calls)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var seeded *audit.Record
	for index := range trace.Records {
		if trace.Records[index].Event.Kind == "managed_root_source_capture_seeded" {
			seeded = &trace.Records[index]
		}
	}
	if seeded == nil {
		t.Fatal("successful managed seed did not persist its g0 binding")
	}
	var receipt struct {
		RunID                  string `json:"run_id"`
		QualificationDigest    string `json:"qualification_digest"`
		SnapshotEvidenceDigest string `json:"snapshot_evidence_digest"`
		XattrEvidenceDigest    string `json:"xattr_evidence_digest"`
		GenerationID           string `json:"generation_id"`
		TreeDigest             string `json:"tree_digest"`
		ManifestDigest         string `json:"manifest_digest"`
	}
	if err := json.Unmarshal(seeded.Event.Data, &receipt); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.Marshal(generation.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RunID != "seed-success" || receipt.QualificationDigest != proof.qualificationDigest ||
		receipt.SnapshotEvidenceDigest != proof.snapshot.EvidenceDigest || receipt.XattrEvidenceDigest != proof.xattr.EvidenceDigest ||
		receipt.GenerationID != generation.ID() || receipt.TreeDigest != generation.TreeDigest() ||
		receipt.ManifestDigest != digestBytes(manifestBytes) {
		t.Fatalf("source qualification receipt does not bind exact g0/tree/manifest: %+v", receipt)
	}
}

func TestManagedSourceSeedBridgeRefusesBeforeAndAfterSeedWithoutSeedReceipt(t *testing.T) {
	t.Run("pre-seed root qualification changed", func(t *testing.T) {
		store, journal, source, _ := newManagedSourceSeedFixture(t)
		calls := 0
		proof := managedSourceSeedTestProof("seed-pre-refusal", func(context.Context, *os.File) error {
			calls++
			return errors.New("managed root identity changed")
		})
		generation, err := seedManagedRootSourceWithProof(context.Background(), store, source, journal, "seed-pre-refusal", proof)
		if err == nil || generation != nil || calls != 1 {
			t.Fatalf("pre-seed change was not refused: generation=%v calls=%d err=%v", generation, calls, err)
		}
		_, err = store.EvidenceBundle()
		if !errors.Is(err, sessionrepo.ErrNoBaseline) {
			t.Fatalf("pre-seed refusal should leave the Store without a baseline, EvidenceBundle error = %v", err)
		}
		if managedRootCaptureEventCount(t, journal, "managed_root_source_capture_seeded") != 0 ||
			managedRootCaptureEventCount(t, journal, "managed_root_source_capture_refused") != 1 {
			t.Fatal("pre-seed refusal created g0 or a successful-seed receipt")
		}
	})

	t.Run("post-seed source inventory changed", func(t *testing.T) {
		store, journal, source, sourceFile := newManagedSourceSeedFixture(t)
		calls := 0
		proof := managedSourceSeedTestProof("seed-post-refusal", func(_ context.Context, candidate *os.File) error {
			calls++
			if candidate != source {
				return errors.New("unexpected source descriptor")
			}
			if calls == 2 {
				if err := os.WriteFile(sourceFile, []byte("changed after seed\n"), 0o600); err != nil {
					return err
				}
				return errors.New("managed source inventory changed after seed")
			}
			return nil
		})
		generation, err := seedManagedRootSourceWithProof(context.Background(), store, source, journal, "seed-post-refusal", proof)
		if err == nil || generation != nil || calls != 2 {
			t.Fatalf("post-seed change was not refused before exposure: generation=%v calls=%d err=%v", generation, calls, err)
		}
		bundle, err := store.EvidenceBundle()
		if err != nil {
			t.Fatal(err)
		}
		if len(bundle.Generations) != 1 || bundle.Generations[0].ID != "g0" ||
			managedRootCaptureEventCount(t, journal, "managed_root_source_capture_seeded") != 0 ||
			managedRootCaptureEventCount(t, journal, "managed_root_source_capture_refused") != 1 {
			t.Fatal("post-seed refusal unexpectedly issued a qualification receipt or lost the local g0 evidence")
		}
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		var refusal struct {
			Stage          string `json:"stage"`
			GenerationID   string `json:"generation_id"`
			TreeDigest     string `json:"tree_digest"`
			ManifestDigest string `json:"manifest_digest"`
		}
		for _, record := range trace.Records {
			if record.Event.Kind == "managed_root_source_capture_refused" {
				if err := json.Unmarshal(record.Event.Data, &refusal); err != nil {
					t.Fatal(err)
				}
			}
		}
		manifestBytes, err := json.Marshal(bundle.Generations[0].Manifest)
		if err != nil {
			t.Fatal(err)
		}
		if refusal.Stage != "after-seed" || refusal.GenerationID != "g0" ||
			refusal.TreeDigest != bundle.Generations[0].TreeDigest || refusal.ManifestDigest != digestBytes(manifestBytes) {
			t.Fatalf("post-seed refusal did not bind retained private generation evidence: %+v", refusal)
		}
	})
}

func newManagedSourceSeedFixture(t *testing.T) (*sessionrepo.Store, *audit.Journal, *os.File, string) {
	t.Helper()
	root := privateGovernedTestRoot(t)
	storePath, sourcePath := filepath.Join(root, "store"), filepath.Join(root, "source")
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(sourcePath, "task.txt")
	if err := os.WriteFile(sourceFile, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(root, "store-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close Store journal: %v", err)
		}
	})
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), sessionrepo.Options{
		PolicyDigest: managedRootTestPolicy, MetadataPolicyDigest: managedRootTestMetadata,
		Limits:          workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: managedRootTestXattr, Complete: true},
		Journal:         journal, AuthorizeOperation: func(sessionrepo.OperationRequest) error { return nil },
		VerifyDecision:   func(delta.PolicyDecision, delta.TransitionBinding) error { return nil },
		VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Store: %v", err)
		}
	})
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return store, journal, source, sourceFile
}

func managedSourceSeedTestProof(runID string, revalidate func(context.Context, *os.File) error) managedRootSeedQualificationProof {
	return managedRootSeedQualificationProof{
		qualificationDigest: piruntime.DigestBytes([]byte("qualification:" + runID)),
		snapshot: piruntime.D09ManagedLiveRootSnapshotEvidence{RunID: runID,
			EvidenceDigest: piruntime.DigestBytes([]byte("snapshot:" + runID))},
		xattr:      piruntime.D09ManagedRootXattrEvidence{EvidenceDigest: piruntime.DigestBytes([]byte("xattr:" + runID))},
		revalidate: revalidate,
	}
}

func managedRootCaptureEventCount(t *testing.T, journal *audit.Journal, kind string) int {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, record := range trace.Records {
		if record.Event.Kind == kind {
			count++
		}
	}
	return count
}
