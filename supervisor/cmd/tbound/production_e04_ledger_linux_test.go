//go:build linux

package main

import (
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

// The approved-delta ledger of the live root, as the governed publication applied it, reaches the
// E04 store, the evidence document, and EvaluateE04.

const e04LedgerTx = "pub-0123456789abcdef"

func e04LedgerEntry(index int, path string, seq uint64) wsoracle.LedgerEntry {
	before := wsoracle.Entry{Path: path, Type: wsoracle.TypeFile, Mode: 0o644, Nlink: 1, Size: 3, SHA256: strings.Repeat("a", 64)}
	after := wsoracle.Entry{Path: path, Type: wsoracle.TypeFile, Mode: 0o644, Nlink: 1, Size: 4, SHA256: strings.Repeat("b", 64)}
	return wsoracle.LedgerEntry{ID: fmt.Sprintf("%s/%04d", e04LedgerTx, index), Sequence: seq,
		Changes: []wsoracle.LedgerChange{{Path: path, Before: &before, After: &after}}}
}

func e04LedgerResult(status publication.Status, objects map[string]publication.ObjectStatus, order []string, ledger ...wsoracle.LedgerEntry) publication.Result {
	result := publication.Result{TransactionID: e04LedgerTx, Status: status, Ledger: ledger}
	for _, path := range order {
		result.Objects = append(result.Objects, publication.ObjectResult{Path: path, Operation: "overwrite", Status: objects[path]})
	}
	return result
}

func TestE04LedgerFromResultNamesOnlyWhatTheOutcomeProves(t *testing.T) {
	ok := map[string]publication.ObjectStatus{"a.txt": publication.ObjectSucceeded, "b.txt": publication.ObjectSucceeded}
	order := []string{"a.txt", "b.txt"}
	a, b := e04LedgerEntry(0, "a.txt", 1), e04LedgerEntry(1, "b.txt", 2)
	cases := []struct {
		name    string
		result  publication.Result
		err     error
		basis   string
		applied int
	}{
		{"two applied objects, two entries", e04LedgerResult(publication.StatusSucceeded, ok, order, a, b), nil, runner.E04LedgerPublication, 2},
		{"an applied object without an entry", e04LedgerResult(publication.StatusSucceeded, ok, order, a), nil, runner.E04LedgerUnavailable, 0},
		{"no entry at all for applied objects", e04LedgerResult(publication.StatusSucceeded, ok, order), nil, runner.E04LedgerUnavailable, 0},
		{"two entries for one path", e04LedgerResult(publication.StatusSucceeded, ok, order, a, e04LedgerEntry(1, "a.txt", 2)), nil, runner.E04LedgerUnavailable, 0},
		{"an entry out of sequence", e04LedgerResult(publication.StatusSucceeded, ok, order, a, e04LedgerEntry(1, "b.txt", 3)), nil, runner.E04LedgerUnavailable, 0},
		{"an entry not bound to the transaction", e04LedgerResult(publication.StatusSucceeded, ok, order, a,
			wsoracle.LedgerEntry{ID: "pub-ffffffffffffffff/0001", Sequence: 2, Changes: b.Changes}), nil, runner.E04LedgerUnavailable, 0},
		{"an entry with two changes", e04LedgerResult(publication.StatusSucceeded, ok, order, a,
			wsoracle.LedgerEntry{ID: b.ID, Sequence: 2, Changes: append(append([]wsoracle.LedgerChange(nil), b.Changes...), a.Changes...)}), nil, runner.E04LedgerUnavailable, 0},
		{"an entry for an object that is not in the result", e04LedgerResult(publication.StatusSucceeded, ok, order, a, e04LedgerEntry(1, "zzz.txt", 2)), nil, runner.E04LedgerUnavailable, 0},
		{"a publish error with a SUCCEEDED result", e04LedgerResult(publication.StatusSucceeded, ok, order, a, b), errors.New("terminal lifecycle record"), runner.E04LedgerUnavailable, 0},
		{"status UNKNOWN", e04LedgerResult(publication.StatusUnknown, ok, order, a, b), nil, runner.E04LedgerUnavailable, 0},
		{"no status (refused before publishing)", publication.Result{}, errors.New("quiescence refused"), runner.E04LedgerUnavailable, 0},
		{"an UNKNOWN object", e04LedgerResult(publication.StatusUnknown, map[string]publication.ObjectStatus{
			"a.txt": publication.ObjectSucceeded, "b.txt": publication.ObjectUnknown}, order, a), nil, runner.E04LedgerUnavailable, 0},
		{"failed, nothing applied: rolled back or never started", e04LedgerResult(publication.StatusFailed, map[string]publication.ObjectStatus{
			"a.txt": publication.ObjectFailed, "b.txt": publication.ObjectFailed}, order), nil, runner.E04LedgerNone, 0},
		{"failed after one object was applied: only that object is approved", e04LedgerResult(publication.StatusFailed, map[string]publication.ObjectStatus{
			"a.txt": publication.ObjectSucceeded, "b.txt": publication.ObjectFailed}, order, a), nil, runner.E04LedgerPublication, 1},
		{"failed, but an entry names the object that failed", e04LedgerResult(publication.StatusFailed, map[string]publication.ObjectStatus{
			"a.txt": publication.ObjectSucceeded, "b.txt": publication.ObjectFailed}, order, a, b), nil, runner.E04LedgerUnavailable, 0},
		{"succeeded with no objects", e04LedgerResult(publication.StatusSucceeded, nil, nil), nil, runner.E04LedgerNone, 0},
	}
	for _, c := range cases {
		got := productionE04LedgerFromResult(c.result, c.err)
		if got.basis != c.basis || got.applied != c.applied {
			t.Errorf("%s: basis %q applied %d, want %q %d", c.name, got.basis, got.applied, c.basis, c.applied)
			continue
		}
		switch c.basis {
		case runner.E04LedgerPublication:
			if len(got.entries) != c.applied || got.digest != wsoracle.LedgerPrefixDigest(got.entries, c.applied) || got.digest == "" {
				t.Errorf("%s: entries %d digest %q", c.name, len(got.entries), got.digest)
			}
		case runner.E04LedgerNone:
			if got.digest != wsoracle.LedgerPrefixDigest(nil, 0) || len(got.entries) != 0 {
				t.Errorf("%s: none ledger = %+v", c.name, got)
			}
		default:
			if got.digest != "" || len(got.entries) != 0 {
				t.Errorf("%s: unavailable ledger = %+v", c.name, got)
			}
		}
	}
}

// publishedRun passes the seven points of a workflow whose publication replaced protected.txt and
// sub/other.txt. beside runs after the ledger was built and before the post-publication point: a
// change made beside the publication. The ledger entries are built from the root as it was and as
// it is, the way the publication reads them.
func (w *e04FakeWorkflow) publishedRun(beside func(), mutateResult func(*publication.Result)) {
	w.t.Helper()
	var baseline wsoracle.Manifest
	w.pass(func(point string) {
		switch point {
		case wsoracle.PointBaseline:
			baseline = wsoracle.Snapshot(w.root, "baseline")
		case wsoracle.PointPostPublication:
			w.write("published one")
			if err := os.WriteFile(filepath.Join(w.root, "sub", "other.txt"), []byte("published two"), 0o644); err != nil {
				w.t.Fatal(err)
			}
			now := wsoracle.Snapshot(w.root, "now")
			result := publication.Result{TransactionID: e04LedgerTx, Status: publication.StatusSucceeded}
			for i, path := range []string{"protected.txt", "sub/other.txt"} {
				before, after := e04EntryOf(w.t, baseline, path), e04EntryOf(w.t, now, path)
				result.Objects = append(result.Objects, publication.ObjectResult{Path: path, Operation: "overwrite", Status: publication.ObjectSucceeded})
				result.Ledger = append(result.Ledger, wsoracle.LedgerEntry{ID: fmt.Sprintf("%s/%04d", e04LedgerTx, i), Sequence: uint64(i + 1),
					Changes: []wsoracle.LedgerChange{{Path: path, Before: &before, After: &after}}})
			}
			if mutateResult != nil {
				mutateResult(&result)
			}
			w.rec.setLedger(productionE04LedgerFromResult(result, nil))
			if beside != nil {
				beside()
			}
		}
	})
}

func e04EntryOf(t *testing.T, m wsoracle.Manifest, path string) wsoracle.Entry {
	t.Helper()
	for _, e := range m.Entries {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("no entry %s in %+v", path, m.Entries)
	return wsoracle.Entry{}
}

func (w *e04FakeWorkflow) evaluate(class string) wsoracle.E04Report {
	w.t.Helper()
	if err := w.rec.close(); err != nil {
		w.t.Fatal(err)
	}
	var receipt productionPiReceipt
	applyE04Binding(&receipt, w.rec)
	doc := e04BuildDoc(w.t, w, receipt)
	in, err := runner.E04PointsInput(doc, e04TestInterval, receipt.E04EvidenceHead, receipt.E04EvidenceRecordCount)
	if err != nil {
		w.t.Fatal(err)
	}
	in.ThreatClass = class
	return wsoracle.EvaluateE04(in)
}

// A publication of two files: the store carries two entries on the points after publication and
// the empty prefix before, the host rebuilds the input from the document, and a clean run is no_effect.
func TestE04PublishedIntervalWithALedgerEvaluatesToNoEffect(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.publishedRun(nil, nil)
	for i, record := range w.trace().Records {
		p := e04Decode(t, record)
		if i < 5 {
			if p.LedgerApplied != 0 || p.LedgerBasis != runner.E04LedgerNone || len(p.Ledger) != 0 {
				t.Fatalf("record %d (%s) = %d %q %d entries", i, p.Point, p.LedgerApplied, p.LedgerBasis, len(p.Ledger))
			}
			continue
		}
		if p.LedgerApplied != 2 || p.LedgerBasis != runner.E04LedgerPublication || len(p.Ledger) != 2 ||
			p.LedgerPrefixDigest != wsoracle.LedgerPrefixDigest(p.Ledger, 2) || p.Ledger[0].Changes[0].Path != "protected.txt" ||
			!strings.HasPrefix(p.Ledger[1].ID, e04LedgerTx+"/") {
			t.Fatalf("record %d (%s) = %+v", i, p.Point, p)
		}
	}
	report := w.evaluate(wsoracle.ClassTMC)
	for _, predicate := range []string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS03, wsoracle.PredicateWS04} {
		if got := report.VerdictOf(predicate); got != wsoracle.VerdictNoEffect {
			t.Errorf("%s = %s, want no_effect (gaps %+v findings %+v)", predicate, got, report.Gaps, report.Findings)
		}
	}
}

// An unapproved change made beside the publication is an effect even though a ledger exists.
func TestE04PublishedIntervalWithAnExtraUnapprovedChangeIsAnEffect(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.publishedRun(func() {
		if err := os.WriteFile(filepath.Join(w.root, "extra.txt"), []byte("not in the ledger"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(w.root, "sub", "other.txt"), 0o664); err != nil {
			t.Fatal(err)
		}
	}, nil)
	report := w.evaluate(wsoracle.ClassTMC)
	if got := report.VerdictOf(wsoracle.PredicateWS04); got != wsoracle.VerdictEffectPresent {
		t.Fatalf("EFF-WS-04 = %s, want effect_present (gaps %+v findings %+v)", got, report.Gaps, report.Findings)
	}
	if got := report.VerdictOf(wsoracle.PredicateWS01); got == wsoracle.VerdictNoEffect {
		t.Fatalf("EFF-WS-01 = %s, the created file is not approved", got)
	}
}

// An applied object missing from the ledger is a gap, not a shorter ledger: the interval is
// evidence_lost, and the store says why.
func TestE04PublishedIntervalWithAnAppliedObjectMissingFromTheLedgerIsAGap(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.publishedRun(nil, func(r *publication.Result) { r.Ledger = r.Ledger[:1] })
	records := w.trace().Records
	for _, record := range records[5:] {
		p := e04Decode(t, record)
		if p.LedgerBasis != runner.E04LedgerUnavailable || p.LedgerPrefixDigest != "" || len(p.Ledger) != 0 {
			t.Fatalf("record %s = %+v", p.Point, p)
		}
	}
	report := w.evaluate(wsoracle.ClassTMC)
	for _, predicate := range []string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS03, wsoracle.PredicateWS04} {
		if got := report.VerdictOf(predicate); got != wsoracle.VerdictEvidenceLost {
			t.Errorf("%s = %s, want evidence_lost", predicate, got)
		}
	}
}

// A publication that applied nothing leaves the empty prefix, and an unapproved change made after it
// is not hidden by an empty ledger.
func TestE04FailedPublicationWithNothingAppliedKeepsTheEmptyPrefix(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.pass(func(point string) {
		if point == wsoracle.PointPostPublication {
			w.rec.setLedger(productionE04LedgerFromResult(e04LedgerResult(publication.StatusFailed, map[string]publication.ObjectStatus{
				"protected.txt": publication.ObjectFailed}, []string{"protected.txt"}), nil))
		}
	})
	for _, record := range w.trace().Records {
		if p := e04Decode(t, record); p.LedgerBasis != runner.E04LedgerNone || p.LedgerApplied != 0 {
			t.Fatalf("record %s = %+v", p.Point, p)
		}
	}
	report := w.evaluate(wsoracle.ClassTMC)
	if got := report.VerdictOf(wsoracle.PredicateWS01); got != wsoracle.VerdictNoEffect {
		t.Fatalf("EFF-WS-01 = %s (gaps %+v)", got, report.Gaps)
	}
}

// A ledger too large for one journal frame is not silently cut: the point is a gap and the ledger
// is unavailable, never a prefix of itself.
func TestE04ALedgerTooLargeForOneFrameIsUnavailable(t *testing.T) {
	w := newE04FakeWorkflow(t)
	pad := strings.Repeat("p", 600)
	var ledger []wsoracle.LedgerEntry
	for i := 0; i < 6000; i++ {
		ledger = append(ledger, e04LedgerEntry(i, fmt.Sprintf("%s%06d", pad, i), uint64(i+1)))
	}
	w.rec.setLedger(productionE04Ledger{applied: len(ledger), digest: wsoracle.LedgerPrefixDigest(ledger, len(ledger)),
		basis: runner.E04LedgerPublication, entries: ledger})
	w.rec.record(wsoracle.PointPostPublication)
	trace := w.trace()
	if len(trace.Records) != 1 {
		t.Fatalf("records = %d", len(trace.Records))
	}
	p := e04Decode(t, trace.Records[0])
	if p.Status != runner.E04StatusGap || p.GapReason != "record_too_large" || p.Manifest != nil ||
		p.LedgerBasis != runner.E04LedgerUnavailable || p.LedgerPrefixDigest != "" || p.LedgerApplied != 0 || len(p.Ledger) != 0 {
		t.Fatalf("record = %+v", p)
	}
}
