//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
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
	g1RegistrationID := digestBytes([]byte("registration identity of the G1 service"))

	entry, receipt, err := catalog.completedPriorAdmission(state, defect31G1ConfigDigest, g1RegistrationID)
	if err != nil {
		t.Fatalf("the completed smoke prior was refused by the G1 service: %v", err)
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
	if _, _, err := catalog.completedPriorAdmission(forged, defect31G1ConfigDigest, g1RegistrationID); !errors.Is(err, ErrPiWorkflowCatalog) {
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
	settlementID, err := verifyManagedPiNoPublicationSettlement(run.journal, run.trace(t), run.identity, run.root, next,
		noChangeWorkflowID, noChangeRunID, noChangeOwnerEpoch)
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
	if _, err := verifyManagedPiNoPublicationSettlement(run.journal, run.trace(t), run.identity, run.root, next,
		noChangeWorkflowID, noChangeRunID, noChangeOwnerEpoch); !errors.Is(err, ErrManagedLiveRootUnavailable) {
		t.Fatalf("a run admitted under two registrations was settled: %v", err)
	}
}

// The launch proof a receipt names was checked against the run's own signed profile and template
// before it was journaled. Its settlement binds it to the profile and template the receipt names.
func TestServiceTCBProofOfAPriorIsBoundToTheProfileItsReceiptNames(t *testing.T) {
	root := privateGovernedTestRoot(t)
	journal, err := audit.Open(filepath.Join(root, "session-audit.jsonl"))
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
	const runID, sessionID, workflowID, conversationID, generationID = "prior-run", "prior-run", "prior-workflow", "prior-conversation", "g0"
	sourceBinding := digestBytes([]byte("prior source binding"))
	priorProfile, priorTemplate, proof := productionProofFixture()
	proof.SourceGenerationID, proof.SourceBindingDigest = generationID, sourceBinding
	proof.SourceStoreAuditCount, proof.SourceStoreAuditHead = 1, auditHeadHash(before.Records)
	evidence := &productionLifecycleEvidence{}
	if err := journalProductionLaunchProof(journal, evidence, runID, sessionID, workflowID, conversationID, generationID,
		sourceBinding, priorProfile, priorTemplate, proof.LauncherProcessPID, proof); err != nil {
		t.Fatal(err)
	}
	snapshot := evidence.snapshot()
	receipt := productionPiReceipt{RunID: runID, ProfileID: priorProfile.ID, ProfileHash: priorProfile.Digest,
		ContainmentProfileDigest: priorProfile.ContainmentProfileDigest,
		TCBProofDigest:           snapshot.proofDigest, TCBProofJournalSequence: snapshot.proofSeq}

	// The service that verifies the prior runs under the next cycle's profile, and after a freeze a new image.
	currentProfile, currentTemplate := priorProfile, priorTemplate
	currentTemplate.ExpectedImageDigest = "sha256:" + strings.Repeat("6", 64)
	currentProfile.ID, currentProfile.Digest = "next-cycle-profile", "sha256:"+strings.Repeat("7", 64)
	if currentProfile.ContainmentProfileDigest, err = currentTemplate.Digest(); err != nil {
		t.Fatal(err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	record, event, err := verifyServiceTCBProof(journal, trace, receipt, sessionID, workflowID, conversationID,
		currentProfile, currentTemplate)
	if err != nil {
		t.Fatalf("the launch proof of a prior run was refused under the next cycle's profile: %v", err)
	}
	if record.Sequence != snapshot.proofSeq || event.Proof.ImageDigest != priorTemplate.ExpectedImageDigest {
		t.Fatalf("verified proof = sequence %d image %q", record.Sequence, event.Proof.ImageDigest)
	}

	for name, mutate := range map[string]func(*productionPiReceipt){
		"another profile id":       func(r *productionPiReceipt) { r.ProfileID = currentProfile.ID },
		"another profile digest":   func(r *productionPiReceipt) { r.ProfileHash = currentProfile.Digest },
		"another template digest":  func(r *productionPiReceipt) { r.ContainmentProfileDigest = currentProfile.ContainmentProfileDigest },
		"another proof digest":     func(r *productionPiReceipt) { r.TCBProofDigest = digestBytes([]byte("other proof")) },
		"another journal sequence": func(r *productionPiReceipt) { r.TCBProofJournalSequence = 1 },
	} {
		changed := receipt
		mutate(&changed)
		if _, _, err := verifyServiceTCBProof(journal, trace, changed, sessionID, workflowID, conversationID,
			currentProfile, currentTemplate); !errors.Is(err, ErrPiWorkflowCatalog) {
			t.Errorf("%s: a proof that is not the one the receipt names was accepted: %v", name, err)
		}
	}
}
