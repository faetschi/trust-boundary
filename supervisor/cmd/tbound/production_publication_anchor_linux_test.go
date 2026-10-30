//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/runner"
)

const (
	anchorTestWorkflow = "workflow-anchor-1"
	anchorTestOther    = "workflow-anchor-2"
	anchorTestCase     = "case-anchor"
)

func anchorOwnerEpoch(workflowID string, epoch int) audit.Event {
	return publicationEvent("publication_owner_epoch", fmt.Sprintf("%s/%d", workflowID, epoch),
		map[string]any{"workflow_id": workflowID, "owner_epoch": epoch})
}

func anchorPrepare(workflowID, transactionID string) audit.Event {
	return publicationEvent("publication_prepare", transactionID,
		map[string]any{"workflow_id": workflowID, "transaction_id": transactionID, "owner_epoch": 1,
			"publication_id": "publication-" + workflowID})
}

func anchorTransactionRecord(kind, transactionID string) audit.Event {
	id := transactionID
	if kind == "publication_checkpoint" {
		id += "/checkpoint"
	}
	return publicationEvent(kind, id, map[string]any{"transaction_id": transactionID})
}

func anchorTransaction(workflowID, transactionID string) []audit.Event {
	return []audit.Event{anchorPrepare(workflowID, transactionID),
		anchorTransactionRecord("publication_checkpoint", transactionID),
		anchorTransactionRecord("publication_token_consumed", transactionID),
		anchorTransactionRecord("publication_terminal", transactionID)}
}

// anchorExpectedHead is the head of the interval journal as the audit package itself computes
// it: a real journal that holds the runner's marker record and then the events.
func anchorExpectedHead(t *testing.T, interval string, events ...audit.Event) string {
	t.Helper()
	trace := reconcileTestTrace(t, append([]audit.Event{{Kind: runner.IntervalMarkerKind, ID: interval}}, events...)...)
	return trace.Records[len(trace.Records)-1].Hash
}

func anchorTestInterval() string {
	return runner.IntervalRef(runner.Step{CaseID: anchorTestCase, Role: runner.RoleTrajectory, Configuration: runner.CfgC, Attempt: 1})
}

// noise is what the shared journal holds besides the workflow's own publication records.
func anchorNoise(workflowID string) []audit.Event {
	return []audit.Event{anchorOwnerEpoch(anchorTestOther, 1), anchorPrepare(anchorTestOther, "pub-other-1"),
		publicationPending(workflowID, "publication-noise"), runAdmitted(workflowID, "run-noise"),
		anchorTransactionRecord("publication_checkpoint", "pub-other-1"),
		{Kind: "pi_service_note", ID: "note-1"}}
}

func TestPublicationAnchorWorkflowThatPublishesNothing(t *testing.T) {
	interval := anchorTestInterval()
	want := anchorExpectedHead(t, interval)
	for name, events := range map[string][]audit.Event{
		"empty journal":                   nil,
		"other workflows and other kinds": anchorNoise(anchorTestWorkflow),
	} {
		anchor, err := publicationAnchorFor(reconcileTestTrace(t, events...), anchorTestWorkflow, interval)
		if err != nil || anchor.Count != 0 || anchor.Head != want || anchor.IntervalRef != interval {
			t.Fatalf("%s: anchor=%+v err=%v, want count 0 and head %s", name, anchor, err, want)
		}
	}
}

func TestPublicationAnchorCountsThePublicationRecordsOfTheWorkflow(t *testing.T) {
	interval := anchorTestInterval()
	prepare := anchorPrepare(anchorTestWorkflow, "pub-1")
	anchor, err := publicationAnchorFor(reconcileTestTrace(t, append(anchorNoise(anchorTestWorkflow), prepare)...), anchorTestWorkflow, interval)
	if err != nil || anchor.Count != 1 || anchor.Head != anchorExpectedHead(t, interval, prepare) {
		t.Fatalf("one publication record: anchor=%+v err=%v", anchor, err)
	}

	// A whole transaction is four records, interleaved with another workflow's records. The
	// slice keeps the workflow's own records in journal order and nothing else.
	own := anchorTransaction(anchorTestWorkflow, "pub-2")
	foreign := anchorTransaction(anchorTestOther, "pub-other-2")
	var mixed []audit.Event
	for index, event := range own {
		mixed = append(mixed, event, foreign[index])
	}
	anchor, err = publicationAnchorFor(reconcileTestTrace(t, mixed...), anchorTestWorkflow, interval)
	if err != nil || anchor.Count != 4 || anchor.Head != anchorExpectedHead(t, interval, own...) {
		t.Fatalf("one transaction: anchor=%+v err=%v", anchor, err)
	}
}

func TestPublicationAnchorDoesNotCountTheOwnerEpochRecord(t *testing.T) {
	interval := anchorTestInterval()
	epoch := anchorOwnerEpoch(anchorTestWorkflow, 1)
	anchor, err := publicationAnchorFor(reconcileTestTrace(t, epoch), anchorTestWorkflow, interval)
	if err != nil || anchor.Count != 0 {
		t.Fatalf("an owner epoch was counted as a publication: anchor=%+v err=%v", anchor, err)
	}
	// The record is in the chain the runner verifies (it tolerates it), so the head moves.
	if anchor.Head != anchorExpectedHead(t, interval, epoch) || anchor.Head == anchorExpectedHead(t, interval) {
		t.Fatalf("the owner epoch record is not part of the interval chain: %s", anchor.Head)
	}
	prepare := anchorPrepare(anchorTestWorkflow, "pub-1")
	anchor, err = publicationAnchorFor(reconcileTestTrace(t, epoch, prepare, anchorOwnerEpoch(anchorTestWorkflow, 2)), anchorTestWorkflow, interval)
	if err != nil || anchor.Count != 1 {
		t.Fatalf("owner epochs beside one publication: anchor=%+v err=%v", anchor, err)
	}
}

func TestPublicationAnchorAttributesRestoreRecords(t *testing.T) {
	interval := anchorTestInterval()
	intent := publicationEvent("publication_restore_intent", "restore-1", map[string]any{"restore_id": "restore-1", "workflow_id": anchorTestWorkflow})
	foreign := publicationEvent("publication_restore_intent", "restore-2", map[string]any{"restore_id": "restore-2", "workflow_id": anchorTestOther})
	checkpoint := publicationEvent("publication_restore_checkpoint", "restore-1/restore-checkpoint", map[string]any{"restore_id": "restore-1"})
	foreignOutcome := publicationEvent("publication_restore_outcome", "restore-2", map[string]any{"restore_id": "restore-2"})
	materialized := publicationEvent("publication_restore_materialized", "restore-1", map[string]any{"restore_id": "restore-1"})
	anchor, err := publicationAnchorFor(reconcileTestTrace(t, intent, foreign, checkpoint, foreignOutcome, materialized), anchorTestWorkflow, interval)
	if err != nil || anchor.Count != 3 || anchor.Head != anchorExpectedHead(t, interval, intent, checkpoint, materialized) {
		t.Fatalf("restore records: anchor=%+v err=%v", anchor, err)
	}
}

// A publication record that cannot be attributed could hide a publication, so it is an error
// and not a skipped record.
func TestPublicationAnchorFailsClosedOnUnattributableRecords(t *testing.T) {
	for name, events := range map[string][]audit.Event{
		"unknown publication kind": {publicationEvent("publication_future", "x", map[string]any{"workflow_id": anchorTestWorkflow})},
		"orphan terminal":          {anchorTransactionRecord("publication_terminal", "pub-missing")},
		"orphan restore outcome":   {publicationEvent("publication_restore_outcome", "restore-9", map[string]any{"restore_id": "restore-9"})},
		"unreadable data":          {{Kind: "publication_prepare", ID: "pub-1", Data: []byte("not json")}},
		"prepare without workflow": {publicationEvent("publication_prepare", "pub-1", map[string]any{"transaction_id": "pub-1"})},
	} {
		if anchor, err := publicationAnchorFor(reconcileTestTrace(t, events...), anchorTestWorkflow, anchorTestInterval()); err == nil {
			t.Fatalf("%s: anchored anyway: %+v", name, anchor)
		}
	}
	if _, err := publicationAnchorFor(reconcileTestTrace(t), "", anchorTestInterval()); err == nil {
		t.Fatal("an empty workflow id was anchored")
	}
	if _, err := publicationAnchorFor(reconcileTestTrace(t), anchorTestWorkflow, ""); err == nil {
		t.Fatal("an anchor without an interval identity was produced")
	}
}

// The anchor must satisfy the runner's own checks on the interval journal it describes: the
// marker kind is the runner's, the chain verifies, and the head and the count are what the
// runner recomputes and counts.
func TestPublicationAnchorMatchesWhatTheRunnerRecomputes(t *testing.T) {
	if publicationIntervalMarkerKind != runner.IntervalMarkerKind {
		t.Fatalf("marker kind %q is not the runner's %q", publicationIntervalMarkerKind, runner.IntervalMarkerKind)
	}
	interval := anchorTestInterval()
	own := append([]audit.Event{anchorOwnerEpoch(anchorTestWorkflow, 1)}, anchorTransaction(anchorTestWorkflow, "pub-1")...)
	slice, count, err := workflowPublicationSlice(reconcileTestTrace(t, append(anchorNoise(anchorTestWorkflow), own...)...), anchorTestWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	intervalJournal := reconcileTestTrace(t, append([]audit.Event{{Kind: runner.IntervalMarkerKind, ID: interval}}, slice...)...)
	// the runner's rule, from runner/provenance.go
	counted := 0
	for index, record := range intervalJournal.Records {
		switch kind := record.Event.Kind; {
		case index == 0:
			if kind != runner.IntervalMarkerKind || record.Event.ID != interval {
				t.Fatalf("first record %+v is not the marker of %s", record.Event, interval)
			}
		case strings.HasPrefix(kind, "publication") && kind != "publication_owner_epoch":
			counted++
		}
	}
	anchor, err := publicationAnchorFor(reconcileTestTrace(t, append(anchorNoise(anchorTestWorkflow), own...)...), anchorTestWorkflow, interval)
	head := intervalJournal.Records[len(intervalJournal.Records)-1].Hash
	if err != nil || anchor.Head != head || anchor.Count != counted || count != counted || counted != 4 {
		t.Fatalf("anchor %+v err=%v; runner head %s count %d (slice count %d)", anchor, err, head, counted, count)
	}
}

// The receipt's interval identity is the runner's IntervalRef(step), derived from the
// corpus admission id <case_id>/<role>/<attempt> and the runner's own GovernedRequestFor.
func TestPublicationAnchorIntervalRefIsTheRunnersIntervalRef(t *testing.T) {
	covered := map[string]bool{}
	for _, role := range []runner.Role{runner.RoleTrajectory, runner.RoleStaticReplay, runner.RoleAdaptiveReplay,
		runner.RoleCompliant, runner.RoleAmbiguous, runner.RoleVarianceRepeat} {
		for repeat := 0; repeat <= 2; repeat++ {
			// the plan emits Repeat 1..VarianceRepeats for variance repeats and 0 for every other role
			if (role == runner.RoleVarianceRepeat) != (repeat != 0) {
				continue
			}
			for _, cfg := range []runner.Configuration{runner.CfgB, runner.CfgC, runner.CfgP} {
				for attempt := 1; attempt <= 2; attempt++ {
					step := runner.Step{CaseID: anchorTestCase, Role: role, Configuration: cfg, Repeat: repeat, Attempt: attempt}
					request := runner.GovernedRequestFor("plan-digest", step)
					if request.Role == "" {
						continue
					}
					admission := corpusAdmissionID(request.TaskID, request.Role, request.Attempt)
					ref, ok := corpusIntervalRef(admission, request.TaskID)
					if !ok || ref != runner.IntervalRef(step) {
						t.Fatalf("%s: interval ref %q ok=%v, runner says %q", admission, ref, ok, runner.IntervalRef(step))
					}
					covered[request.Role] = true
				}
			}
		}
	}
	for _, role := range runner.GovernedRoles() {
		if !covered[role] {
			t.Fatalf("governed role %q has no interval mapping", role)
		}
	}
	if len(covered) != len(runner.GovernedRoles()) {
		t.Fatalf("covered %v", covered)
	}
	for name, id := range map[string]string{
		"attempt 3":             anchorTestCase + "/trajectory/3",
		"attempt 0":             anchorTestCase + "/trajectory/0",
		"attempt not canonical": anchorTestCase + "/trajectory/01",
		"attempt not a number":  anchorTestCase + "/trajectory/x",
		"unknown role":          anchorTestCase + "/mystery/1",
		"another case":          "other-case/trajectory/1",
		"no case":               "/trajectory/1",
		"no role":               anchorTestCase + "/1",
		"single task id":        "single-task-admission",
	} {
		if ref, ok := corpusIntervalRef(id, anchorTestCase); ok {
			t.Fatalf("%s: %q mapped to %q", name, id, ref)
		}
	}
}

func anchorTestEntry(admissionID string) piServiceWorkflowEntry {
	return piServiceWorkflowEntry{HostAdmissionID: admissionID, TaskID: anchorTestCase, WorkflowID: anchorTestWorkflow,
		CorpusManifestDigest: corpusTestDigest("manifest")}
}

func anchorTestReceipt(t *testing.T) productionPiReceipt {
	t.Helper()
	receipt := productionPiReceipt{SchemaVersion: "tbound-production-pi-receipt/v1", Mode: "production", RunID: "run-anchor",
		PublicationStatus: "NO_DIFF", Disposition: "NO_DIFF", Success: true}
	trace := reconcileTestTrace(t, append(anchorNoise(anchorTestWorkflow), anchorOwnerEpoch(anchorTestWorkflow, 1))...)
	admission := &productionWorkflowAdmission{Entry: anchorTestEntry(anchorTestCase + "/variance-2/2")}
	if err := applyPublicationAnchor(&receipt, admission, anchorTestWorkflow, trace); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestApplyPublicationAnchorTakesItFromTheServiceEvidence(t *testing.T) {
	receipt := anchorTestReceipt(t)
	wantRef := "case-anchor|variance-repeat|C|2#2"
	if receipt.PublicationIntervalRef != wantRef || receipt.PublicationJournalRecordCount != 0 ||
		receipt.PublicationJournalHead != anchorExpectedHead(t, wantRef, anchorOwnerEpoch(anchorTestWorkflow, 1)) {
		t.Fatalf("receipt anchor: %+v", receipt)
	}
}

func TestApplyPublicationAnchorIgnoresEverythingTheClientChooses(t *testing.T) {
	trace := reconcileTestTrace(t, anchorNoise(anchorTestWorkflow)...)
	admission := &productionWorkflowAdmission{Entry: anchorTestEntry(anchorTestCase + "/trajectory/1")}

	// The anchor is a function of the catalog entry and the journal alone. A receipt that
	// arrives with values in the fields (nothing the client sends reaches the receipt, but
	// suppose it did) has them replaced.
	forged := productionPiReceipt{PublicationIntervalRef: "forged", PublicationJournalHead: strings.Repeat("f", 64), PublicationJournalRecordCount: 99}
	clean := productionPiReceipt{}
	for _, receipt := range []*productionPiReceipt{&forged, &clean} {
		if err := applyPublicationAnchor(receipt, admission, anchorTestWorkflow, trace); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(forged, clean) || clean.PublicationJournalHead == "" || clean.PublicationJournalRecordCount != 0 {
		t.Fatalf("forged=%+v clean=%+v", forged, clean)
	}

	// Records of other workflows that name this workflow in free-text fields, or reuse a
	// transaction id, do not move the slice.
	hostile := append(anchorNoise(anchorTestWorkflow),
		publicationEvent("publication_prepare", "pub-hostile", map[string]any{"workflow_id": anchorTestOther, "transaction_id": "pub-hostile",
			"publication_id": "publication-" + anchorTestWorkflow, "origin_proposal_id": anchorTestWorkflow, "checkpoint_id": anchorTestWorkflow}),
		anchorTransactionRecord("publication_terminal", "pub-hostile"))
	moved := productionPiReceipt{}
	if err := applyPublicationAnchor(&moved, admission, anchorTestWorkflow, reconcileTestTrace(t, hostile...)); err != nil || !reflect.DeepEqual(moved, clean) {
		t.Fatalf("another workflow's records moved the anchor: %+v err=%v", moved, err)
	}

	// The interval is the catalog's admission. An entry whose admission names another case
	// than the one admitted, or another workflow than the session's, is refused.
	for name, entry := range map[string]piServiceWorkflowEntry{
		"admission names another case": anchorTestEntry("other-case/trajectory/1"),
		"admission is not a trial id":  anchorTestEntry("free-form-admission"),
		"unknown role":                 anchorTestEntry(anchorTestCase + "/mystery/1"),
	} {
		receipt := productionPiReceipt{}
		if err := applyPublicationAnchor(&receipt, &productionWorkflowAdmission{Entry: entry}, anchorTestWorkflow, trace); err == nil ||
			!reflect.DeepEqual(receipt, productionPiReceipt{}) {
			t.Fatalf("%s: err=%v receipt=%+v", name, err, receipt)
		}
	}
	receipt := productionPiReceipt{}
	if err := applyPublicationAnchor(&receipt, admission, "another-workflow", trace); err == nil || !reflect.DeepEqual(receipt, productionPiReceipt{}) {
		t.Fatalf("a session of another workflow was anchored: err=%v receipt=%+v", err, receipt)
	}

	// Without a corpus admission (a single-task service, whose admission id the operator
	// configured) there is no interval identity, and so no anchor.
	single := anchorTestEntry(anchorTestCase + "/trajectory/1")
	single.CorpusManifestDigest = ""
	receipt = productionPiReceipt{}
	if err := applyPublicationAnchor(&receipt, &productionWorkflowAdmission{Entry: single}, anchorTestWorkflow, trace); err != nil ||
		!reflect.DeepEqual(receipt, productionPiReceipt{}) {
		t.Fatalf("a non-corpus admission was anchored: err=%v receipt=%+v", err, receipt)
	}
	if err := applyPublicationAnchor(&receipt, nil, anchorTestWorkflow, trace); err != nil || !reflect.DeepEqual(receipt, productionPiReceipt{}) {
		t.Fatalf("a launch without admission was anchored: err=%v receipt=%+v", err, receipt)
	}
}

// The terminal digest of a request (abandon records bind it) is the digest of the whole
// response, receipt bytes included, so each anchor field is covered. The receipt also
// survives the strict decoder every reader of it uses.
func TestPublicationAnchorFieldsAreCoveredByTheReceiptDigest(t *testing.T) {
	digestOf := func(receipt productionPiReceipt) string {
		encoded, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := piServiceTerminalDigest(piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: "tbr-1",
			Status: "completed", Receipt: encoded})
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	base := anchorTestReceipt(t)
	baseDigest := digestOf(base)
	for name, mutate := range map[string]func(*productionPiReceipt){
		"interval ref": func(r *productionPiReceipt) { r.PublicationIntervalRef += "x" },
		"head":         func(r *productionPiReceipt) { r.PublicationJournalHead = strings.Repeat("0", 64) },
		"count":        func(r *productionPiReceipt) { r.PublicationJournalRecordCount++ },
		"no anchor": func(r *productionPiReceipt) {
			r.PublicationIntervalRef, r.PublicationJournalHead, r.PublicationJournalRecordCount = "", "", 0
		},
	} {
		changed := base
		mutate(&changed)
		if digestOf(changed) == baseDigest {
			t.Fatalf("%s is not covered by the receipt digest", name)
		}
	}

	var written bytes.Buffer
	if err := writeProductionPiReceipt(&written, base); err != nil {
		t.Fatal(err)
	}
	var decoded productionPiReceipt
	if err := unmarshalGovernedJSON(bytes.TrimSpace(written.Bytes()), &decoded); err != nil || !reflect.DeepEqual(decoded, base) {
		t.Fatalf("receipt did not survive the strict decoder: %+v err=%v", decoded, err)
	}
	for _, name := range []string{`"publication_interval_ref"`, `"publication_journal_head"`, `"publication_journal_record_count"`} {
		if !strings.Contains(written.String(), name) {
			t.Fatalf("receipt lacks %s: %s", name, written.String())
		}
	}
}

// A stored completed receipt is re-derived from the publication journal when the service
// verifies the settled request, so an edited anchor does not verify.
func TestVerifyReceiptPublicationAnchorRederivesTheAnchor(t *testing.T) {
	entry := anchorTestEntry(anchorTestCase + "/variance-2/2")
	trace := reconcileTestTrace(t, append(anchorNoise(anchorTestWorkflow), anchorOwnerEpoch(anchorTestWorkflow, 1))...)
	receipt := anchorTestReceipt(t)
	if err := verifyReceiptPublicationAnchor(receipt, entry, trace); err != nil {
		t.Fatalf("a genuine anchor did not verify: %v", err)
	}
	for name, mutate := range map[string]func(*productionPiReceipt){
		"head":              func(r *productionPiReceipt) { r.PublicationJournalHead = strings.Repeat("0", 64) },
		"count":             func(r *productionPiReceipt) { r.PublicationJournalRecordCount = 1 },
		"ref":               func(r *productionPiReceipt) { r.PublicationIntervalRef = "case-anchor|variance-repeat|C|1#2" },
		"head only removed": func(r *productionPiReceipt) { r.PublicationJournalHead = "" },
	} {
		tampered := receipt
		mutate(&tampered)
		if err := verifyReceiptPublicationAnchor(tampered, entry, trace); !errors.Is(err, ErrPiWorkflowCatalog) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
	// a publication appended after the receipt moves the slice, so the receipt no longer verifies
	later := reconcileTestTrace(t, append(anchorNoise(anchorTestWorkflow), anchorOwnerEpoch(anchorTestWorkflow, 1),
		anchorPrepare(anchorTestWorkflow, "pub-late"))...)
	if err := verifyReceiptPublicationAnchor(receipt, entry, later); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("a later publication was not noticed: %v", err)
	}
	// a receipt without an anchor claims nothing and is accepted
	if err := verifyReceiptPublicationAnchor(productionPiReceipt{}, entry, trace); err != nil {
		t.Fatalf("an unanchored receipt was refused: %v", err)
	}
}

// anchorTestReceiptJSON is the receipt of a completed response that carries the anchor.
func anchorTestReceiptJSON(t *testing.T) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(anchorTestReceipt(t))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
