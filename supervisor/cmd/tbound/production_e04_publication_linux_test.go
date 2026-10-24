//go:build linux

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/wsoracle"
)

func e04TestPublicationEvidence(sealed wsoracle.Manifest) *runner.E04PublicationEvidence {
	return &runner.E04PublicationEvidence{TransactionID: "pub-1", PublicationID: "pubid-1", Status: "SUCCEEDED",
		LockIdentity: "lock@1:2", AffectedPaths: []string{"protected.txt"}, Sealed: sealed, SealedIdentityDigest: sealed.IdentityDigest()}
}

// storeDoc is the e04-points document of the workflow's store, built from the records as the guest kind does.
func (w *e04FakeWorkflow) storeDoc() runner.E04PointsDoc {
	w.t.Helper()
	trace := w.trace()
	var journal bytes.Buffer
	for _, record := range trace.Records {
		line, err := json.Marshal(record)
		if err != nil {
			w.t.Fatal(err)
		}
		journal.Write(line)
		journal.WriteByte('\n')
	}
	head := auditHeadHash(trace.Records)
	return runner.E04PointsDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceE04Points, IntervalRef: e04TestInterval,
		WorkflowID: e04TestWorkflow, RunID: e04TestRun, JournalB64: base64.StdEncoding.EncodeToString(journal.Bytes()), Head: head, Count: len(trace.Records)}
}

// A publication that replaced the protected file, as the service records it: the evidence rides on the
// post-publication record only, and the store alone rebuilds inputs that both publication oracles judge as no effect.
func TestE04PublicationEvidenceRidesOnThePostPublicationRecordAndJudgesCleanly(t *testing.T) {
	w := newE04FakeWorkflow(t)
	sealedDir := t.TempDir()
	w.rec.record(wsoracle.PointBaseline)
	before := wsoracle.Snapshot(w.root, "x").Entries
	for _, point := range runner.E04PointOrder[1:5] {
		w.rec.record(point)
	}
	w.write("published")
	after := wsoracle.Snapshot(w.root, "x").Entries
	var changed []wsoracle.LedgerChange
	for i := range after {
		if after[i].Path == "protected.txt" {
			b := before[i]
			a := after[i]
			changed = append(changed, wsoracle.LedgerChange{Path: "protected.txt", Before: &b, After: &a})
		}
	}
	entries := []wsoracle.LedgerEntry{{ID: "pub-1/0000", Sequence: 1, Changes: changed}}
	w.rec.setLedger(productionE04Ledger{applied: 1, digest: wsoracle.LedgerPrefixDigest(entries, 1), basis: runner.E04LedgerPublication, entries: entries})
	// The sealed generation is a tree of its own.
	if err := os.WriteFile(filepath.Join(sealedDir, "protected.txt"), []byte("published"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.rec.setPublication(e04TestPublicationEvidence(wsoracle.Snapshot(sealedDir, "sealed")))
	w.rec.record(wsoracle.PointPostPublication)
	w.rec.record(wsoracle.PointFinalCleanup)

	for i, record := range w.trace().Records {
		p := e04Decode(t, record)
		if (p.Publication != nil) != (p.Point == wsoracle.PointPostPublication) {
			t.Fatalf("record %d (%s): publication evidence = %v", i, p.Point, p.Publication != nil)
		}
	}
	doc := w.storeDoc()
	in, err := runner.E04PublicationInputs(doc, e04TestInterval, doc.Head, doc.Count)
	if err != nil {
		t.Fatal(err)
	}
	if in.WS05 == nil || in.WS06 == nil {
		t.Fatalf("inputs %+v", in)
	}
	if r := wsoracle.EvaluateWS06(*in.WS06); r.Verdict != wsoracle.VerdictNoEffect {
		t.Errorf("EFF-WS-06 = %s (gaps %+v findings %+v)", r.Verdict, r.Gaps, r.Findings)
	}
	if r := wsoracle.EvaluateWS05(*in.WS05); r.Verdict != wsoracle.VerdictNoEffect {
		t.Errorf("EFF-WS-05 = %s (gaps %+v findings %+v)", r.Verdict, r.Gaps, r.Findings)
	}
}

// Without setPublication (no Finalize, or none that left evidence) no record carries any, and the host finds none.
func TestE04NoPublicationEvidenceMeansNoneIsRecorded(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.pass(nil)
	for _, record := range w.trace().Records {
		if p := e04Decode(t, record); p.Publication != nil {
			t.Fatalf("%s carries publication evidence", p.Point)
		}
	}
	doc := w.storeDoc()
	if _, err := runner.E04PublicationInputs(doc, e04TestInterval, doc.Head, doc.Count); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("err = %v, want absent", err)
	}
}

// A sealed manifest that does not fit beside the point costs the publication evidence, not the E04 point.
func TestE04PublicationEvidenceTooLargeIsDroppedAndTheE04PointSurvives(t *testing.T) {
	w := newE04FakeWorkflow(t)
	sealed := wsoracle.Snapshot(w.root, "sealed")
	pad := strings.Repeat("p", 600)
	for i := 0; i < 20000; i++ {
		sealed.Entries = append(sealed.Entries, wsoracle.Entry{Path: fmt.Sprintf("%s%06d", pad, i), Type: wsoracle.TypeDir, Mode: 0o755, Nlink: 2})
	}
	w.rec.setPublication(e04TestPublicationEvidence(sealed))
	w.rec.record(wsoracle.PointPostPublication)
	records := w.trace().Records
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	if p := e04Decode(t, records[0]); p.Status != runner.E04StatusSnapshot || p.Manifest == nil || p.Publication != nil || w.rec.gaps() != 0 {
		t.Fatalf("record = %+v, gaps %d", p, w.rec.gaps())
	}
}

func TestProductionE04PublicationFromResult(t *testing.T) {
	if got := productionE04PublicationFromResult(publication.Result{TransactionID: "pub-1"}); got != nil {
		t.Errorf("a result without evidence gave %+v", got)
	}
	if got := productionE04PublicationFromResult(publication.Result{Evidence: &publication.OracleEvidence{}}); got != nil {
		t.Errorf("a result without a transaction gave %+v", got)
	}
	sealed := wsoracle.Manifest{Label: "sealed", Complete: true, Root: &wsoracle.Entry{Type: wsoracle.TypeDir}, Dev: 1}
	got := productionE04PublicationFromResult(publication.Result{TransactionID: "pub-1", PublicationID: "pid", Status: publication.StatusFailed,
		Evidence: &publication.OracleEvidence{Sealed: sealed, AffectedPaths: []string{"a", "b"}, LockIdentity: "lock@1:2"}})
	if got == nil || got.TransactionID != "pub-1" || got.PublicationID != "pid" || got.Status != string(publication.StatusFailed) ||
		got.LockIdentity != "lock@1:2" || len(got.AffectedPaths) != 2 || got.SealedIdentityDigest != sealed.IdentityDigest() {
		t.Fatalf("evidence = %+v", got)
	}
	// An unset status is recorded as UNKNOWN, never as success.
	if got := productionE04PublicationFromResult(publication.Result{TransactionID: "pub-1", Evidence: &publication.OracleEvidence{}}); got == nil || got.Status != string(publication.StatusUnknown) {
		t.Fatalf("evidence = %+v", got)
	}
}
