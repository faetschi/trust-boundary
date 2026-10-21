//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

const (
	noChangeRunID         = "run-no-change"
	noChangeWorkflowID    = "workflow-no-change"
	noChangePublicationID = "production-publication-" + noChangeWorkflowID
	noChangeOwnerEpoch    = uint64(1)
)

// noChangeRun is one managed live-root run. Admission, ledger recovery, seeding
// of the Store baseline, and release all go through the real lease. Only the
// registration verifier is the existing test seam; the audit journals and the
// Store are real and file-backed. As in production, the lease writes the
// publication journal and the Store and its seed write the store journal.
type noChangeRun struct {
	authority    *managedLiveRootAuthority
	journal      *audit.Journal
	journalPath  string
	storeJournal *audit.Journal
	live         *os.File
	lease        *managedLiveRootLease
	registration piruntime.D09ManagedLiveRootRegistration
	identity     managedLiveRootDescriptorIdentity
	root         workflow.RootIdentity
	baseline     *sessionrepo.Generation
	options      workspace.Options
}

// startNoChangeRun admits the run and, as production does, seeds the Store
// baseline from the live root after ledger recovery. With seed false the Store
// takes the baseline directly, which production never does.
func startNoChangeRun(t *testing.T, seed bool) *noChangeRun {
	t.Helper()
	root := privateGovernedTestRoot(t)
	livePath := filepath.Join(root, "live")
	if err := os.Mkdir(livePath, 0o700); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(livePath, "task.txt")
	if err := os.WriteFile(taskPath, []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Workspace scans require the normalized file mode, and the test umask masks
	// WriteFile's mode, so set it explicitly.
	if err := os.Chmod(taskPath, 0o644); err != nil {
		t.Fatal(err)
	}
	live, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	journalPath := filepath.Join(root, "publication-audit.jsonl")
	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	store, storeJournal := newNoChangeStore(t, root)
	authority := newFakeManagedLiveRootAuthority()
	ctx := context.Background()
	lease, err := authority.acquire(ctx, live, piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest},
		journal, noChangeRunID, noChangeWorkflowID, noChangePublicationID, noChangeOwnerEpoch)
	if err != nil {
		t.Fatalf("acquire test-only registered-root boundary: %v", err)
	}
	if err := lease.completeAdmissionRecovery(ctx); err != nil {
		t.Fatalf("complete admission recovery: %v", err)
	}
	var baseline *sessionrepo.Generation
	if seed {
		proof := managedRootSeedQualificationProof{qualificationDigest: testManagedRootDigest,
			snapshot:   piruntime.D09ManagedLiveRootSnapshotEvidence{RunID: noChangeRunID, EvidenceDigest: managedRootTestPolicy},
			xattr:      piruntime.D09ManagedRootXattrEvidence{RunID: noChangeRunID, EvidenceDigest: managedRootTestXattr},
			revalidate: func(context.Context, *os.File) error { return nil }}
		baseline, err = seedManagedRootSourceWithProof(ctx, store, live, storeJournal, noChangeRunID, proof)
	} else {
		baseline, err = store.Seed(live, sessionrepo.RootAttestation{Quiescent: true})
	}
	if err != nil {
		t.Fatalf("seed the Store baseline from the live root: %v", err)
	}
	return &noChangeRun{authority: authority, journal: journal, journalPath: journalPath, storeJournal: storeJournal,
		live: live, lease: lease,
		registration: lease.verified.Registration(), identity: lease.identity,
		root:     workflow.RootIdentity{Device: lease.identity.Device, Inode: lease.identity.Inode},
		baseline: baseline,
		options: workspace.Options{MetadataPolicyDigest: managedRootTestMetadata, Limits: workspace.DefaultLimits(),
			XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: managedRootTestXattr, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}}}
}

func newNoChangeStore(t *testing.T, root string) (*sessionrepo.Store, *audit.Journal) {
	t.Helper()
	storePath := filepath.Join(root, "store")
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	storeJournal, err := audit.Open(filepath.Join(root, "store-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeJournal.Close() })
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), sessionrepo.Options{
		PolicyDigest: managedRootTestPolicy, MetadataPolicyDigest: managedRootTestMetadata,
		Limits:          workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: managedRootTestXattr, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted},
		Journal:         storeJournal, AuthorizeOperation: func(sessionrepo.OperationRequest) error { return nil },
		VerifyDecision:   func(delta.PolicyDecision, delta.TransitionBinding) error { return nil },
		VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, storeJournal
}

// release closes the lease as the session does after a no-change run: mark no
// publication against the baseline, then close, which rescans the live root.
func (r *noChangeRun) release() error {
	if err := r.lease.markNoPublication(r.options, r.baseline); err != nil {
		return err
	}
	return r.lease.close()
}

// storeTrace is the store journal. The harness launches nothing; its verify takes all of it as the
// launch-bound source state.
func (r *noChangeRun) storeTrace(t *testing.T) audit.Trace {
	t.Helper()
	trace, err := r.storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func (r *noChangeRun) trace(t *testing.T) audit.Trace {
	t.Helper()
	trace, err := r.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

// verify runs the public settlement verifier against the journal as it stands.
func (r *noChangeRun) verify(t *testing.T, identity managedLiveRootDescriptorIdentity) (string, error) {
	t.Helper()
	store := r.storeTrace(t)
	return verifyManagedPiNoPublicationSettlement(r.journal, r.trace(t), store, uint64(len(store.Records)), identity, r.root,
		r.registration, noChangeWorkflowID, noChangeRunID, noChangeOwnerEpoch)
}

// writeRelease appends a release record in the shape close writes, anchored to
// the last ledger record, with mutate applied. Tests use it only for tampering
// with released fields; the positive path always uses close.
func (r *noChangeRun) writeRelease(t *testing.T, mutate func(*managedLiveRootRunResolvedEvent)) {
	t.Helper()
	trace := r.trace(t)
	admissionID := managedLiveRootRunEventID(r.root, r.registration.Digest, noChangeRunID, noChangeOwnerEpoch)
	var admitted audit.Record
	for _, record := range trace.Records {
		if record.Event.Kind == "managed_live_root_run_admitted" && record.Event.ID == admissionID {
			admitted = record
		}
	}
	last := trace.Records[len(trace.Records)-1]
	event := managedLiveRootRunResolvedEvent{SchemaVersion: "tbound-managed-live-root-run-resolved/v1", Root: r.root,
		RegistrationDigest: r.registration.Digest, RunID: noChangeRunID, WorkflowID: noChangeWorkflowID,
		PublicationID: noChangePublicationID, OwnerEpoch: noChangeOwnerEpoch, AdmissionSequence: admitted.Sequence,
		AdmissionRecordHash: admitted.Hash, Outcome: "no-publication", LifecycleJournalSequence: last.Sequence,
		LifecycleJournalRecordHash: last.Hash, ReleasedTreeDigest: r.baseline.TreeDigest()}
	mutate(&event)
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.journal.Append(audit.Event{Kind: "managed_live_root_run_released", ID: admissionID + "/released", Data: data}); err != nil {
		t.Fatal(err)
	}
}

func releaseRecordHash(t *testing.T, trace audit.Trace) string {
	t.Helper()
	var hash string
	for _, record := range trace.Records {
		if record.Event.Kind == "managed_live_root_run_released" {
			hash = record.Hash
		}
	}
	if hash == "" {
		t.Fatal("no managed live-root release record in the journal")
	}
	return hash
}

type noChangeFreshRequest struct {
	JournalPath      string
	StoreJournalPath string
	Identity         managedLiveRootDescriptorIdentity
	Digest           string
	ServiceUID       int
	ServiceGID       int
	ResultPath       string
}

type noChangeFreshResult struct {
	SettlementID string
	Error        string
}

// verifyInFreshProcess reruns the settlement verifier in a child test binary
// that opens the journal cold, so no in-memory state from this run is reused.
func (r *noChangeRun) verifyInFreshProcess(t *testing.T) noChangeFreshResult {
	t.Helper()
	if err := r.journal.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(r.journalPath)
	requestPath, resultPath := filepath.Join(dir, "fresh-request.json"), filepath.Join(dir, "fresh-result.json")
	encoded, err := json.Marshal(noChangeFreshRequest{JournalPath: r.journalPath,
		StoreJournalPath: filepath.Join(dir, "store-audit.jsonl"), Identity: r.identity,
		Digest: r.registration.Digest, ServiceUID: r.registration.ServiceUID, ServiceGID: r.registration.ServiceGID,
		ResultPath: resultPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestNoChangeSettlementFreshProcessHelper$", "-test.count=1")
	command.Env = append(os.Environ(), "TB_NO_CHANGE_FRESH_REQUEST_PATH="+requestPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh-process settlement verifier failed: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var result noChangeFreshResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// TestNoChangeSettlementFreshProcessHelper is the child side of
// verifyInFreshProcess. It does nothing unless the parent sets the request path.
func TestNoChangeSettlementFreshProcessHelper(t *testing.T) {
	path := os.Getenv("TB_NO_CHANGE_FRESH_REQUEST_PATH")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var request noChangeFreshRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(request.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	storeFile, err := os.Open(request.StoreJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer storeFile.Close()
	source, err := audit.Verify(storeFile)
	if err != nil {
		t.Fatal(err)
	}
	settlementID, verifyErr := verifyManagedPiNoPublicationSettlement(journal, trace, source, uint64(len(source.Records)), request.Identity,
		workflow.RootIdentity{Device: request.Identity.Device, Inode: request.Identity.Inode},
		piruntime.D09ManagedLiveRootRegistration{Digest: request.Digest, ServiceUID: request.ServiceUID, ServiceGID: request.ServiceGID},
		noChangeWorkflowID, noChangeRunID, noChangeOwnerEpoch)
	result := noChangeFreshResult{SettlementID: settlementID}
	if verifyErr != nil {
		result.Error = verifyErr.Error()
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.ResultPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestNoChangeReleaseSettlesThroughRealLeaseClose drives admission, seeding, and
// release through the real lease, then verifies in a fresh process and admits a
// new run on the same root.
func TestNoChangeReleaseSettlesThroughRealLeaseClose(t *testing.T) {
	run := startNoChangeRun(t, true)
	if err := run.release(); err != nil {
		t.Fatalf("real lease close for a verified no-change run: %v", err)
	}
	want := "no-publication:" + releaseRecordHash(t, run.trace(t))
	settlementID, err := run.verify(t, run.identity)
	if err != nil || settlementID != want {
		t.Fatalf("verified no-change release: id=%q want=%q err=%v", settlementID, want, err)
	}
	if result := run.verifyInFreshProcess(t); result.Error != "" || result.SettlementID != want {
		t.Fatalf("fresh process verified no-change release: id=%q err=%q want=%q", result.SettlementID, result.Error, want)
	}
	journal, err := audit.Open(run.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	if _, err := run.authority.acquire(context.Background(), run.live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest}, journal,
		"run-after-no-change", "workflow-after-no-change", "production-publication-workflow-after-no-change",
		noChangeOwnerEpoch+1); err != nil {
		t.Fatalf("new request admitted after verified no-change release was refused: %v", err)
	}
}

func TestNoChangeSettlementRefusesUnverifiedEvidence(t *testing.T) {
	otherTree := delta.TreeDigestProfile + strings.Repeat("e", 64)
	refused := func(t *testing.T, run *noChangeRun, identity managedLiveRootDescriptorIdentity) {
		t.Helper()
		if settlementID, err := run.verify(t, identity); !errors.Is(err, ErrManagedLiveRootUnavailable) {
			t.Fatalf("unverified no-change evidence was accepted: id=%q err=%v", settlementID, err)
		}
	}
	t.Run("matching hand-written release shape is accepted", func(t *testing.T) {
		run := startNoChangeRun(t, true)
		run.writeRelease(t, func(*managedLiveRootRunResolvedEvent) {})
		if settlementID, err := run.verify(t, run.identity); err != nil || !strings.HasPrefix(settlementID, "no-publication:") {
			t.Fatalf("release in the close shape was refused: id=%q err=%v", settlementID, err)
		}
	})
	t.Run("live root changed after seed", func(t *testing.T) {
		run := startNoChangeRun(t, true)
		if err := os.WriteFile(filepath.Join(run.live.Name(), "task.txt"), []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run.release(); !errors.Is(err, ErrManagedLiveRootRetained) {
			t.Fatalf("release after a changed live root: err=%v, want ErrManagedLiveRootRetained", err)
		}
		refused(t, run, run.identity)
	})
	t.Run("run never seeded from the root", func(t *testing.T) {
		run := startNoChangeRun(t, false)
		if err := run.release(); err != nil {
			t.Fatalf("real lease close for an unseeded run: %v", err)
		}
		refused(t, run, run.identity)
	})
	t.Run("missing release record", func(t *testing.T) {
		run := startNoChangeRun(t, true)
		refused(t, run, run.identity)
	})
	t.Run("root identity changed since admission", func(t *testing.T) {
		run := startNoChangeRun(t, true)
		if err := run.release(); err != nil {
			t.Fatalf("real lease close: %v", err)
		}
		changed := run.identity
		changed.Inode++
		refused(t, run, changed)
	})
	for _, test := range []struct {
		name   string
		mutate func(*managedLiveRootRunResolvedEvent)
	}{
		{"release without tree digest", func(e *managedLiveRootRunResolvedEvent) { e.ReleasedTreeDigest = "" }},
		{"released tree digest differs from seed", func(e *managedLiveRootRunResolvedEvent) { e.ReleasedTreeDigest = otherTree }},
		{"forged ledger anchor", func(e *managedLiveRootRunResolvedEvent) {
			e.LifecycleJournalRecordHash = strings.Repeat("e", 64)
		}},
		{"forged admission hash", func(e *managedLiveRootRunResolvedEvent) { e.AdmissionRecordHash = strings.Repeat("e", 64) }},
		{"publication-succeeded outcome", func(e *managedLiveRootRunResolvedEvent) { e.Outcome = "publication-succeeded" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := startNoChangeRun(t, true)
			run.writeRelease(t, test.mutate)
			refused(t, run, run.identity)
		})
	}
	t.Run("publication pending without resolution", func(t *testing.T) {
		run := startNoChangeRun(t, true)
		data, err := json.Marshal(managedRootPendingWire{Root: run.root, WorkflowID: noChangeWorkflowID,
			OwnerEpoch: noChangeOwnerEpoch, PublicationID: noChangePublicationID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := run.journal.Append(audit.Event{Kind: "workflow_publication_pending",
			ID: managedWorkflowEventID(run.root, noChangeWorkflowID, noChangePublicationID), Data: data}); err != nil {
			t.Fatal(err)
		}
		run.writeRelease(t, func(*managedLiveRootRunResolvedEvent) {})
		refused(t, run, run.identity)
	})
}

// A net-reverted run has changes in its approved transitions, but its verified
// sealed tree equals the baseline. Publication would compose an empty delta, so
// it must take the no-change release and never reach Finalize.
func TestBundleHasNoChangeRoutesRevertedRunsWithoutPublication(t *testing.T) {
	treeA := delta.TreeDigestProfile + strings.Repeat("a", 64)
	treeB := delta.TreeDigestProfile + strings.Repeat("b", 64)
	changed := []delta.Transition{{Changes: []delta.ObjectChange{{Path: "task.txt"}}}}
	for _, test := range []struct {
		name   string
		bundle sessionrepo.EvidenceBundle
		want   bool
	}{
		{"no approved change", sessionrepo.EvidenceBundle{}, true},
		{"changes netting to the baseline tree", sessionrepo.EvidenceBundle{ApprovedDeltaLedger: changed,
			ValidatedChain: delta.Result{BaselineTreeDigest: treeA, SealedTreeDigest: treeA}}, true},
		{"changes leaving the tree different", sessionrepo.EvidenceBundle{ApprovedDeltaLedger: changed,
			ValidatedChain: delta.Result{BaselineTreeDigest: treeA, SealedTreeDigest: treeB}}, false},
		{"changes without validated trees", sessionrepo.EvidenceBundle{ApprovedDeltaLedger: changed}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := bundleHasNoChange(test.bundle); got != test.want {
				t.Fatalf("bundleHasNoChange = %t, want %t", got, test.want)
			}
		})
	}
}

// The admission record carries the mount ID of the namespace the run executed in. A prior is verified
// in full when the service starts again, in a new namespace, where the same root has another mount ID.
func TestNoChangeSettlementIgnoresTheNamespaceLocalMountIDButNothingElse(t *testing.T) {
	run := startNoChangeRun(t, true)
	if err := run.release(); err != nil {
		t.Fatalf("real lease close: %v", err)
	}
	restarted := run.identity
	restarted.MountID += 1000
	if settlementID, err := run.verify(t, restarted); err != nil || !strings.HasPrefix(settlementID, "no-publication:") {
		t.Fatalf("a settlement verified in another mount namespace was refused: id=%q err=%v", settlementID, err)
	}
	for name, mutate := range map[string]func(*managedLiveRootDescriptorIdentity){
		"inode":      func(d *managedLiveRootDescriptorIdentity) { d.Inode++ },
		"device":     func(d *managedLiveRootDescriptorIdentity) { d.Device++ },
		"filesystem": func(d *managedLiveRootDescriptorIdentity) { d.Filesystem++ },
		"owner":      func(d *managedLiveRootDescriptorIdentity) { d.UID++ },
		"group":      func(d *managedLiveRootDescriptorIdentity) { d.GID++ },
		"mode":       func(d *managedLiveRootDescriptorIdentity) { d.Mode ^= 0o070 },
		"path":       func(d *managedLiveRootDescriptorIdentity) { d.Path += "/other" },
	} {
		changed := restarted
		mutate(&changed)
		if _, err := run.verify(t, changed); !errors.Is(err, ErrManagedLiveRootUnavailable) {
			t.Errorf("%s changed since admission was accepted: %v", name, err)
		}
	}
}
