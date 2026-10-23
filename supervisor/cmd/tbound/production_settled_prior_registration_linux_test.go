//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

// Defect #31 on the real guest. Every run of the re-registration cycle (G1, route probe, pilot)
// gets a service config, registration and host profile of its own, and a freeze changes the runner
// and the image. A completed prior is verified at the next admission, which then runs under the
// next registration, so the settlement of a prior must be checked against the bindings that prior
// ran under, not the running service's.

const (
	defect31SmokeClientUID = 994
	defect31SmokeRequestID = "smoke-req-20261021T110233Z"
	defect31SmokeWorkflow  = "tbound-workflow-351198c3c0efc315d0394379"
	defect31SmokeRunID     = "production-run-351198c3c0efc315d0394379"
	// The G1 service that met the smoke prior: its config digest, from its signed registration.
	defect31G1ConfigDigest = "sha256:2cb3e1938ef938ed93d6f103185080bc036c97efb4479477342fdd7f9a849540"
)

// openDefect31Journal copies a journal exported from the guest (testdata/defect31, byte for byte)
// into a private test root and opens it.
func openDefect31Journal(t *testing.T, root, name string) *audit.Journal {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "defect31", name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatalf("open exported %s: %v", name, err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	return journal
}

// openDefect31Catalog replays the exported catalog's events, in order and unchanged except for the
// owner incarnation's effective uid: the catalog accepts only incarnations of its own service uid
// (995 on the guest), so the test's uid takes its place. No record refers to a catalog record hash.
func openDefect31Catalog(t *testing.T, root string) *piServiceWorkflowCatalog {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "defect31", "catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(root, "catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record audit.Record
		var event piServiceWorkflowEvent
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil {
			t.Fatalf("exported catalog record %d: %v", record.Sequence, err)
		}
		if event.Incarnation.EffectiveUID != 0 {
			event.Incarnation.EffectiveUID = uint32(os.Geteuid())
		}
		if record.Event.Data, err = json.Marshal(event); err != nil {
			t.Fatal(err)
		}
		if _, err := journal.Append(record.Event); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := loadPiServiceWorkflowCatalog(journal)
	if err != nil {
		t.Fatalf("replay exported catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	return catalog
}

// The request journal (accepted and completed terminal) and the six catalog records F1 wrote for
// the smoke (reserved, fresh authorized, checkpoint bound, one advance, owner scope bound and
// drained), as exported from the guest after the G1 request was refused prior-workflow-unsettled.
func TestSmokePriorIsCheckedAgainstTheConfigAndRegistrationItWasAdmittedUnder(t *testing.T) {
	root := privateGovernedTestRoot(t)
	requests, err := newPiServiceRequestJournal(openDefect31Journal(t, root, "serve-pi-requests.jsonl"))
	if err != nil {
		t.Fatalf("replay exported request journal: %v", err)
	}
	catalog := openDefect31Catalog(t, root)
	state, ok := requests.entries[piServiceRequestKey(defect31SmokeClientUID, defect31SmokeRequestID)]
	if !ok || state.terminal == nil || state.terminal.Status != "completed" {
		t.Fatalf("exported request journal has no completed smoke request: %+v", state)
	}
	entry, receipt, err := catalog.completedPriorAdmission(state)
	if err != nil {
		t.Fatalf("the completed smoke prior was refused under the G1 service: %v", err)
	}
	if entry.WorkflowID != defect31SmokeWorkflow || receipt.RunID != defect31SmokeRunID {
		t.Fatalf("prior admission = workflow %q run %q", entry.WorkflowID, receipt.RunID)
	}

	// The prior's own records must still agree: an accepted record that names another config than
	// the catalog admitted it under is refused.
	forged := state
	forged.accepted.ConfigDigest = defect31G1ConfigDigest
	if forged.acceptedRecord.Event.Data, err = json.Marshal(forged.accepted); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.completedPriorAdmission(forged); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("an accepted record that disagrees with its catalog admission was accepted: %v", err)
	}
}

// A no-change run is settled by the admission and release it journaled under its own registration.
// The registration that verifies it later belongs to the next cycle.
func TestNoChangeSettlementOfARunAdmittedUnderAnEarlierRegistration(t *testing.T) {
	run := startNoChangeRun(t, true)
	if err := run.release(); err != nil {
		t.Fatalf("real lease close: %v", err)
	}
	next := run.registration
	next.Digest = digestBytes([]byte("registration of the next cycle"))
	store := run.storeTrace(t)
	settlementID, err := verifyManagedPiNoPublicationSettlement(run.journal, run.trace(t), store, uint64(len(store.Records)), run.identity, run.root, next,
		noChangeWorkflowID, noChangeRunID)
	if err != nil || !strings.HasPrefix(settlementID, "no-publication:") {
		t.Fatalf("a run settled under its own registration was refused under the next one: id=%q err=%v", settlementID, err)
	}

	// The run's admission must be unambiguous: a second admission of the same run under another
	// registration is refused.
	var admitted managedLiveRootRunAdmittedEvent
	for _, record := range run.trace(t).Records {
		if record.Event.Kind == "managed_live_root_run_admitted" {
			if err := json.Unmarshal(record.Event.Data, &admitted); err != nil {
				t.Fatal(err)
			}
		}
	}
	admitted.RegistrationDigest = next.Digest
	data, err := json.Marshal(admitted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.journal.Append(audit.Event{Kind: "managed_live_root_run_admitted",
		ID: managedLiveRootRunEventID(run.root, next.Digest, noChangeRunID, noChangeOwnerEpoch), Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyManagedPiNoPublicationSettlement(run.journal, run.trace(t), store, uint64(len(store.Records)), run.identity, run.root, next,
		noChangeWorkflowID, noChangeRunID); !errors.Is(err, ErrManagedLiveRootUnavailable) {
		t.Fatalf("a run admitted under two registrations was settled: %v", err)
	}
}

// refusedBy fails unless err is the named settlement check's refusal of sentinel.
func refusedBy(t *testing.T, err error, sentinel error, check string) {
	t.Helper()
	if !errors.Is(err, sentinel) || !strings.Contains(fmt.Sprint(err), "settlement check "+check+":") {
		t.Errorf("want the %s refusal (%v), got %v", check, sentinel, err)
	}
}

const (
	priorProofRunID, priorProofSessionID, priorProofWorkflowID = "prior-run", "prior-run", "prior-workflow"
	priorProofConversationID, priorProofGenerationID           = "prior-conversation", "g0"
)

// priorLaunchProof journals a prior run's launch proof after one store record, as the launch does,
// and returns the journal records, the proof record and the receipt that names it.
func priorLaunchProof(t *testing.T) (audit.Trace, audit.Record, productionPiReceipt, piruntime.D09CellTemplate) {
	t.Helper()
	journal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "session-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := journal.Append(audit.Event{Kind: "test_store_baseline", ID: "test-store-baseline", Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	before, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	sourceBinding := digestBytes([]byte("prior source binding"))
	profile, template, proof := productionProofFixture()
	proof.SourceGenerationID, proof.SourceBindingDigest = priorProofGenerationID, sourceBinding
	proof.SourceStoreAuditCount, proof.SourceStoreAuditHead = 1, auditHeadHash(before.Records)
	evidence := &productionLifecycleEvidence{}
	if err := journalProductionLaunchProof(journal, evidence, priorProofRunID, priorProofSessionID, priorProofWorkflowID,
		priorProofConversationID, priorProofGenerationID, sourceBinding, profile, template, proof.LauncherProcessPID, proof); err != nil {
		t.Fatal(err)
	}
	snapshot := evidence.snapshot()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	receipt := productionPiReceipt{RunID: priorProofRunID, ProfileID: profile.ID, ProfileHash: profile.Digest,
		ContainmentProfileDigest: profile.ContainmentProfileDigest,
		TCBProofDigest:           snapshot.proofDigest, TCBProofJournalSequence: snapshot.proofSeq}
	return trace, trace.Records[len(trace.Records)-1], receipt, template
}

// rejournal writes events into a fresh journal in order and returns its records, so a test can
// place a changed or repeated proof record in an intact hash chain.
func rejournal(t *testing.T, events ...audit.Event) audit.Trace {
	t.Helper()
	journal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "session-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	for _, event := range events {
		if _, err := journal.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func verifyPriorProof(trace audit.Trace, receipt productionPiReceipt, imagePin string) (audit.Record, productionLaunchTCBProofEvent, error) {
	return verifyServiceTCBProof(trace, receipt, priorProofSessionID, priorProofWorkflowID, priorProofConversationID, imagePin)
}

// The launch proof a receipt names was checked against the run's own signed profile and template
// before it was journaled. Its settlement binds it to the profile and template the receipt names.
func TestServiceTCBProofOfAPriorIsBoundToTheProfileItsReceiptNames(t *testing.T) {
	trace, proofRecord, receipt, template := priorLaunchProof(t)
	imagePin := bareWorkflowDigest(template.ExpectedImageDigest)
	record, event, err := verifyPriorProof(trace, receipt, imagePin)
	if err != nil {
		t.Fatalf("the launch proof the receipt of a prior run names was refused: %v", err)
	}
	if record.Sequence != proofRecord.Sequence || event.Proof.ImageDigest != template.ExpectedImageDigest {
		t.Fatalf("verified proof = sequence %d image %q", record.Sequence, event.Proof.ImageDigest)
	}

	for name, mutate := range map[string]func(*productionPiReceipt){
		"another profile id":       func(r *productionPiReceipt) { r.ProfileID = "next-cycle-profile" },
		"another profile digest":   func(r *productionPiReceipt) { r.ProfileHash = digestBytes([]byte("next-cycle-profile")) },
		"another template digest":  func(r *productionPiReceipt) { r.ContainmentProfileDigest = digestBytes([]byte("next-cycle-template")) },
		"another proof digest":     func(r *productionPiReceipt) { r.TCBProofDigest = digestBytes([]byte("other proof")) },
		"another journal sequence": func(r *productionPiReceipt) { r.TCBProofJournalSequence = 1 },
	} {
		changed := receipt
		mutate(&changed)
		if _, _, err := verifyPriorProof(trace, changed, imagePin); !errors.Is(err, ErrPiWorkflowCatalog) {
			t.Errorf("%s: a proof that is not the one the receipt names was accepted: %v", name, err)
		}
	}
}

// The proof digest commits to the proof the launch checked: a record whose proof was changed while
// it kept the digest is not that proof.
func TestServiceTCBProofWithChangedContentUnderItsDigestIsRefused(t *testing.T) {
	trace, proofRecord, receipt, template := priorLaunchProof(t)
	var event productionLaunchTCBProofEvent
	if err := json.Unmarshal(proofRecord.Event.Data, &event); err != nil {
		t.Fatal(err)
	}
	event.Proof.CapEff = "0000003fffffffff"
	changed, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	forged := rejournal(t, trace.Records[0].Event,
		audit.Event{Kind: proofRecord.Event.Kind, ID: proofRecord.Event.ID, Data: changed})
	_, _, err = verifyPriorProof(forged, receipt, bareWorkflowDigest(template.ExpectedImageDigest))
	refusedBy(t, err, ErrPiWorkflowCatalog, "tcb-proof-event")
}

// A run has one launch proof. A second record under the run's proof ID is refused even when the
// receipt names the later one.
func TestServiceTCBProofRecordedTwiceIsRefused(t *testing.T) {
	trace, proofRecord, receipt, template := priorLaunchProof(t)
	twice := rejournal(t, trace.Records[0].Event, proofRecord.Event, proofRecord.Event)
	receipt.TCBProofJournalSequence = twice.Records[2].Sequence
	_, _, err := verifyPriorProof(twice, receipt, bareWorkflowDigest(template.ExpectedImageDigest))
	refusedBy(t, err, ErrPiWorkflowCatalog, "tcb-proof-duplicate")
}

// The settled checkpoint pins the image the run launched; a proof of another image is not its proof.
func TestServiceTCBProofOfAnotherImageThanTheCheckpointPinIsRefused(t *testing.T) {
	trace, _, receipt, _ := priorLaunchProof(t)
	_, _, err := verifyPriorProof(trace, receipt, strings.Repeat("6", 64))
	refusedBy(t, err, ErrPiWorkflowCatalog, "checkpoint-image-pin")
}

// publishedPriorRun runs one PUBLISHED managed live-root run through the real lease and RootLedger
// (permit, pending, transaction, succeeded resolution, durable terminal, release), with the
// production publication ID, and returns its publication journal. The run is admitted at managed-root
// epoch ownerEpoch: each earlier epoch is a run on the same root that ends before publication.
func publishedPriorRun(t *testing.T, ownerEpoch uint64) (*audit.Journal, managedLiveRootDescriptorIdentity, piruntime.D09ManagedLiveRootRegistration, string) {
	t.Helper()
	root := privateGovernedTestRoot(t)
	livePath := filepath.Join(root, "live")
	if err := os.Mkdir(livePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(livePath, "task.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	journal, err := audit.Open(filepath.Join(root, "publication-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	store, baseline, tip, bundle, metadataDigest := managedRootTestSession(t)
	t.Cleanup(func() { _ = store.Close() })
	options := workspace.Options{Generation: baseline.ID(), MetadataPolicyDigest: metadataDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: managedRootTestXattr, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}}
	rootLedger, err := workflow.NewRootLedger(journal)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	authority := newFakeManagedLiveRootAuthority()
	for epoch := uint64(1); epoch < ownerEpoch; epoch++ {
		earlier := fmt.Sprintf("earlier-run-%d", epoch)
		lease, err := authority.acquire(ctx, live, piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest},
			journal, earlier, earlier, "production-publication-"+earlier, epoch)
		if err != nil {
			t.Fatalf("admit the earlier run at managed-root epoch %d: %v", epoch, err)
		}
		if err := lease.completeAdmissionRecovery(ctx); err != nil {
			t.Fatal(err)
		}
		if err := lease.close(); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := authority.acquire(ctx, live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest}, journal,
		publishedPriorRunID, publishedPriorWorkflowID, publishedPriorPublicationID, ownerEpoch)
	if err != nil {
		t.Fatalf("acquire test-only registered-root boundary: %v", err)
	}
	if err := lease.completeAdmissionRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lease.issuePublicationPermit(ctx, live, baseline, tip); err != nil {
		t.Fatal(err)
	}
	if err := lease.authorizePublication(ctx, live, workflow.EvidenceRequest{Bundle: bundle, Verified: bundle.ValidatedChain,
		LiveRoot: live, Workflow: publishedPriorWorkflowID, OwnerEpoch: ownerEpoch}, baseline); err != nil {
		t.Fatal(err)
	}
	reservation, err := rootLedger.Begin(live, publishedPriorWorkflowID, ownerEpoch, publishedPriorPublicationID, options)
	if err != nil {
		t.Fatal(err)
	}
	const transactionID = "published-prior-transaction"
	if err := rootLedger.RecordTransaction(reservation, live, publishedPriorWorkflowID, publishedPriorPublicationID, transactionID); err != nil {
		t.Fatal(err)
	}
	if err := rootLedger.Resolve(reservation, live, publishedPriorWorkflowID, publishedPriorPublicationID, transactionID,
		publication.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	reservation.Release()
	if err := lease.recordDurableTerminal(ctx, journal, publication.StatusSucceeded, transactionID); err != nil {
		t.Fatal(err)
	}
	identity, registration := lease.identity, lease.verified.Registration()
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
	return journal, identity, registration, transactionID
}

const (
	publishedPriorRunID         = "published-prior-run"
	publishedPriorWorkflowID    = "published-prior-workflow"
	publishedPriorPublicationID = "production-publication-" + publishedPriorWorkflowID
)

// G1 publishes (read, edit, bash, read), and the next cycle, under its own registration, settles it.
func TestPublishedSettlementOfARunAdmittedUnderAnEarlierRegistration(t *testing.T) {
	journal, identity, registration, transactionID := publishedPriorRun(t, 1)
	root := workflow.RootIdentity{Device: identity.Device, Inode: identity.Inode}
	next := registration
	next.Digest = digestBytes([]byte("registration of the next cycle"))
	settle := func(transaction string) error {
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		return verifyManagedPiPublicationSettlement(journal, trace, identity, root, next, publishedPriorWorkflowID,
			publishedPriorRunID, transaction)
	}
	if err := settle(transactionID); err != nil {
		t.Fatalf("a run published under its own registration was refused under the next one: %v", err)
	}
	refusedBy(t, settle("another-transaction"), ErrManagedLiveRootUnavailable, "publication-transaction-record")

	var admitted managedLiveRootRunAdmittedEvent
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind == "managed_live_root_run_admitted" {
			if err := json.Unmarshal(record.Event.Data, &admitted); err != nil {
				t.Fatal(err)
			}
		}
	}
	admitted.RegistrationDigest = next.Digest
	data, err := json.Marshal(admitted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(audit.Event{Kind: "managed_live_root_run_admitted",
		ID: managedLiveRootRunEventID(root, next.Digest, publishedPriorRunID, 1), Data: data}); err != nil {
		t.Fatal(err)
	}
	refusedBy(t, settle(transactionID), ErrManagedLiveRootUnavailable, "run-admission-ambiguous")
}

// settleNoChange verifies a released no-change run whose launch bound the first launchSourceCount
// records of its store journal as its source state.
func settleNoChange(t *testing.T, run *noChangeRun, launchSourceCount uint64) error {
	t.Helper()
	_, err := verifyManagedPiNoPublicationSettlement(run.journal, run.trace(t), run.storeTrace(t), launchSourceCount,
		run.identity, run.root, run.registration, noChangeWorkflowID, noChangeRunID)
	return err
}

// noChangeSeed returns the store-journal record of the run's Store seed.
func noChangeSeed(t *testing.T, run *noChangeRun) audit.Record {
	t.Helper()
	for _, record := range run.storeTrace(t).Records {
		if record.Event.Kind == "managed_root_source_capture_seeded" {
			return record
		}
	}
	t.Fatal("the run has no seed record")
	return audit.Record{}
}

// The no-change settlement takes the run's seed only from the store records its launch bound as the
// source state, where the run seeded its Store before launch.
func TestNoChangeSeedIsTakenFromTheLaunchBoundSourceState(t *testing.T) {
	released := func(t *testing.T, seed bool) *noChangeRun {
		t.Helper()
		run := startNoChangeRun(t, seed)
		if err := run.release(); err != nil {
			t.Fatalf("real lease close: %v", err)
		}
		return run
	}
	t.Run("seed at the last record of the prefix", func(t *testing.T) {
		run := released(t, true)
		if err := settleNoChange(t, run, noChangeSeed(t, run).Sequence); err != nil {
			t.Fatalf("a seed at the end of the launch-bound source state was refused: %v", err)
		}
	})
	t.Run("seed only after the prefix", func(t *testing.T) {
		run := released(t, true)
		refusedBy(t, settleNoChange(t, run, noChangeSeed(t, run).Sequence-1), ErrManagedLiveRootUnavailable, "no-change-seed-missing")
	})
	t.Run("two seeds in the prefix", func(t *testing.T) {
		run := released(t, true)
		if _, err := run.storeJournal.Append(noChangeSeed(t, run).Event); err != nil {
			t.Fatal(err)
		}
		refusedBy(t, settleNoChange(t, run, uint64(len(run.storeTrace(t).Records))), ErrManagedLiveRootUnavailable, "no-change-seed-duplicate")
	})
	t.Run("only another run's seed", func(t *testing.T) {
		donor := released(t, true)
		var evidence map[string]any
		if err := json.Unmarshal(noChangeSeed(t, donor).Event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		evidence["run_id"] = "run-other"
		data, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		run := released(t, false)
		if _, err := run.storeJournal.Append(audit.Event{Kind: "managed_root_source_capture_seeded",
			ID: "managed-root-source-capture-seeded:run-other", Data: data}); err != nil {
			t.Fatal(err)
		}
		refusedBy(t, settleNoChange(t, run, uint64(len(run.storeTrace(t).Records))), ErrManagedLiveRootUnavailable, "no-change-seed-missing")
	})
}

// F10 on the real guest: G1 (published) was the first run on the managed root, so its managed-root
// epoch was 1; the route probe (no change) was the second, so the publication journal admitted and
// released it at managed-root epoch 2. The next service verifies the probe with its workflow's
// checkpoint, whose owner epoch is the workflow's own (1 for every fresh workflow). The settlement
// must take the run's managed-root epoch from the run's admission, not from the checkpoint.
func TestNoChangeSettlementOfTheSecondRunOnTheManagedRoot(t *testing.T) {
	first := startNoChangeRun(t, true)
	if err := first.release(); err != nil {
		t.Fatalf("release the first run: %v", err)
	}
	const runID, workflowID = "run-no-change-second", "workflow-no-change-second"
	ctx := context.Background()
	lease, err := first.authority.acquire(ctx, first.live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest},
		first.journal, runID, workflowID, "production-publication-"+workflowID, 2)
	if err != nil {
		t.Fatalf("admit the second run at managed-root epoch 2: %v", err)
	}
	if err := lease.completeAdmissionRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	store, storeJournal := newNoChangeStore(t, privateGovernedTestRoot(t))
	proof := managedRootSeedQualificationProof{qualificationDigest: testManagedRootDigest,
		snapshot:   piruntime.D09ManagedLiveRootSnapshotEvidence{RunID: runID, EvidenceDigest: managedRootTestPolicy},
		xattr:      piruntime.D09ManagedRootXattrEvidence{RunID: runID, EvidenceDigest: managedRootTestXattr},
		revalidate: func(context.Context, *os.File) error { return nil }}
	baseline, err := seedManagedRootSourceWithProof(ctx, store, first.live, storeJournal, runID, proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.markNoPublication(first.options, baseline); err != nil {
		t.Fatal(err)
	}
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
	source, err := storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	next := first.registration
	next.Digest = digestBytes([]byte("registration of the next cycle"))
	settlementID, err := verifyManagedPiNoPublicationSettlement(first.journal, first.trace(t), source,
		uint64(len(source.Records)), first.identity, first.root, next, workflowID, runID)
	if err != nil || !strings.HasPrefix(settlementID, "no-publication:") {
		t.Fatalf("the second run on the managed root, released with no change, was refused: id=%q err=%v", settlementID, err)
	}
}

// G1 published as the first run on the managed root. A run that publishes after another run on the
// same root is admitted at managed-root epoch 2, while its workflow checkpoint's owner epoch is 1;
// the next cycle settles it at the epoch of its admission.
func TestPublishedSettlementOfTheSecondRunOnTheManagedRoot(t *testing.T) {
	journal, identity, registration, transactionID := publishedPriorRun(t, 2)
	root := workflow.RootIdentity{Device: identity.Device, Inode: identity.Inode}
	next := registration
	next.Digest = digestBytes([]byte("registration of the next cycle"))
	settle := func() error {
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		return verifyManagedPiPublicationSettlement(journal, trace, identity, root, next, publishedPriorWorkflowID,
			publishedPriorRunID, transactionID)
	}
	if err := settle(); err != nil {
		t.Fatalf("the second run on the managed root, published, was refused under the next registration: %v", err)
	}
}
