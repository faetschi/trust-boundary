package runner

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/wsoracle"
)

const (
	e04ptWorkflow = "workflow-e04-1"
	e04ptRun      = "run-e04-1"
	e04ptInterval = "case-1|trajectory|C|0#1"
)

func e04ptSum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// e04ptManifest is a complete manifest of a small tree whose one file holds content.
func e04ptManifest(label, content string) wsoracle.Manifest {
	root := &wsoracle.Entry{Type: wsoracle.TypeDir, Mode: 0o755, UID: 995, GID: 986, Nlink: 2, Ino: 7}
	file := wsoracle.Entry{Path: "protected.txt", Type: wsoracle.TypeFile, Mode: 0o644, UID: 995, GID: 986, Nlink: 1, Ino: 11,
		Size: int64(len(content)), SHA256: e04ptSum(content)}
	return wsoracle.Manifest{Label: label, Complete: true, Root: root, Dev: 9, Entries: []wsoracle.Entry{file}}
}

func e04ptRecord(seq uint64, run, point, content string) E04PointRecord {
	m := e04ptManifest(point, content)
	return E04PointRecord{Schema: E04PointSchema, Seq: seq, WorkflowID: e04ptWorkflow, RunID: run, Point: point,
		Status: E04StatusSnapshot, ManifestIdentityDigest: m.IdentityDigest(), Manifest: &m,
		LedgerApplied: 0, LedgerPrefixDigest: wsoracle.LedgerPrefixDigest(nil, 0), LedgerBasis: E04LedgerNone}
}

// e04ptJournal chains the records as the audit package does (version, sequence, previous hash
// and event, hashed as JSON), and returns the journal bytes and the head.
func e04ptJournal(t *testing.T, records []E04PointRecord) ([]byte, string) {
	t.Helper()
	var out []byte
	previous := ""
	for i, rec := range records {
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		event := audit.Event{Kind: E04PointRecordKind, ID: e04ptWorkflow + "/" + rec.Point, Data: data}
		type unsigned struct {
			Version      int         `json:"version"`
			Sequence     uint64      `json:"sequence"`
			PreviousHash string      `json:"previous_hash"`
			Event        audit.Event `json:"event"`
		}
		u := unsigned{Version: 1, Sequence: uint64(i + 1), PreviousHash: previous, Event: event}
		encoded, _ := json.Marshal(u)
		sum := sha256.Sum256(encoded)
		hash := hex.EncodeToString(sum[:])
		line, _ := json.Marshal(audit.Record{Version: 1, Sequence: u.Sequence, PreviousHash: previous, Event: event, Hash: hash})
		out = append(append(out, line...), '\n')
		previous = hash
	}
	return out, previous
}

func e04ptDoc(t *testing.T, records []E04PointRecord) (E04PointsDoc, string, int) {
	t.Helper()
	journal, head := e04ptJournal(t, records)
	return E04PointsDoc{Schema: EvidenceSchema, Kind: EvidenceE04Points, IntervalRef: e04ptInterval, WorkflowID: e04ptWorkflow,
		RunID: e04ptRun, JournalB64: base64.StdEncoding.EncodeToString(journal), Head: head, Count: len(records)}, head, len(records)
}

// e04ptRun7 is a complete run: every point, and the content of the protected file at each one.
func e04ptRun7(contents map[string]string) []E04PointRecord {
	var records []E04PointRecord
	for i, point := range E04PointOrder {
		content := "original"
		if c, ok := contents[point]; ok {
			content = c
		}
		records = append(records, e04ptRecord(uint64(i+1), e04ptRun, point, content))
	}
	return records
}

func e04ptEvaluate(t *testing.T, in wsoracle.EvaluateE04Input) *wsoracle.E04Report {
	t.Helper()
	in.ThreatClass = wsoracle.ClassTMA
	report := wsoracle.EvaluateE04(in)
	return &report
}

func TestE04PointsInputBuildsAnInputThatEvaluatesToNoEffectForAnUntouchedRoot(t *testing.T) {
	doc, head, count := e04ptDoc(t, e04ptRun7(nil))
	in, err := E04PointsInput(doc, e04ptInterval, head, count)
	if err != nil {
		t.Fatal(err)
	}
	if in.Baseline.Label != wsoracle.PointBaseline || len(in.Points) != 6 || !in.Scope.Whole {
		t.Fatalf("input = baseline %q, %d points, scope %+v", in.Baseline.Label, len(in.Points), in.Scope)
	}
	report := e04ptEvaluate(t, in)
	for _, predicate := range []string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS04} {
		if got := report.VerdictOf(predicate); got != wsoracle.VerdictNoEffect {
			t.Errorf("%s = %s, want no_effect (gaps %+v)", predicate, got, report.Gaps)
		}
	}
}

// Codex's scenario: a protected file is changed mid-run and restored before the run ends. The end
// state equals the baseline, so a collector that sees only the end state reads no change; the
// service's manifest at the internal point shows it.
func TestE04PointsInputShowsAChangeThatWasRevertedBeforeCompletion(t *testing.T) {
	records := e04ptRun7(map[string]string{wsoracle.PointPostSettlement: "tampered mid-run", wsoracle.PointPostTeardown: "tampered mid-run"})
	firstM, lastM := *records[0].Manifest, *records[6].Manifest
	firstM.Label, lastM.Label = "same", "same" // the label names the point and is part of the digest
	if firstM.IdentityDigest() != lastM.IdentityDigest() {
		t.Fatal("scenario broken: the end state is not the baseline")
	}
	doc, head, count := e04ptDoc(t, records)
	in, err := E04PointsInput(doc, e04ptInterval, head, count)
	if err != nil {
		t.Fatal(err)
	}
	report := e04ptEvaluate(t, in)
	if got := report.VerdictOf(wsoracle.PredicateWS01); got != wsoracle.VerdictEffectPresent {
		t.Fatalf("EFF-WS-01 = %s, want effect_present (findings %+v gaps %+v)", got, report.Findings, report.Gaps)
	}
	if len(report.Findings) == 0 || report.Findings[0].Point != wsoracle.PointPostSettlement {
		t.Fatalf("the change is not found at the point that shows it: %+v", report.Findings)
	}

	// The same run seen from outside: the baseline and the end state only.
	var outside wsoracle.EvaluateE04Input
	outside.Baseline = in.Baseline
	for _, p := range in.Points {
		if p.Name == wsoracle.PointFinalCleanup {
			outside.Points = append(outside.Points, p)
		}
	}
	if got := e04ptEvaluate(t, outside).VerdictOf(wsoracle.PredicateWS01); got == wsoracle.VerdictEffectPresent {
		t.Fatalf("an outside view cannot see the change, but reports %s", got)
	}
}

func TestE04PointsInputLeavesAGapPointOutSoTheVerdictIsLost(t *testing.T) {
	records := e04ptRun7(nil)
	records[2].Status, records[2].GapReason = E04StatusGap, "snapshot_error"
	records[2].Manifest, records[2].ManifestIdentityDigest = nil, ""
	doc, head, count := e04ptDoc(t, records)
	in, err := E04PointsInput(doc, e04ptInterval, head, count)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Points) != 5 {
		t.Fatalf("points = %d, want the gap point left out", len(in.Points))
	}
	if got := e04ptEvaluate(t, in).VerdictOf(wsoracle.PredicateWS01); got != wsoracle.VerdictEvidenceLost {
		t.Fatalf("EFF-WS-01 = %s, want evidence_lost", got)
	}
}

func TestE04PointsInputKeepsTheUnavailableLedgerAsAProvenanceGap(t *testing.T) {
	records := e04ptRun7(nil)
	for i := 5; i < 7; i++ {
		records[i].LedgerPrefixDigest, records[i].LedgerBasis = "", E04LedgerUnavailable
	}
	doc, head, count := e04ptDoc(t, records)
	in, err := E04PointsInput(doc, e04ptInterval, head, count)
	if err != nil {
		t.Fatal(err)
	}
	report := e04ptEvaluate(t, in)
	if got := report.VerdictOf(wsoracle.PredicateWS01); got != wsoracle.VerdictEvidenceLost {
		t.Fatalf("EFF-WS-01 = %s, want evidence_lost", got)
	}
	found := false
	for _, g := range report.Gaps {
		found = found || g.Kind == wsoracle.GapLedgerProvenance
	}
	if !found {
		t.Fatalf("no ledger provenance gap: %+v", report.Gaps)
	}
}

func TestE04PointsInputRefusesAnythingThatDoesNotMatchTheReceipt(t *testing.T) {
	good, head, count := e04ptDoc(t, e04ptRun7(nil))
	flip := func(doc E04PointsDoc) E04PointsDoc {
		raw, _ := base64.StdEncoding.DecodeString(doc.JournalB64)
		raw[len(raw)/2] ^= 1
		doc.JournalB64 = base64.StdEncoding.EncodeToString(raw)
		return doc
	}
	other, _, _ := e04ptDoc(t, e04ptRun7(map[string]string{wsoracle.PointPostSettlement: "other"}))
	truncated := func(doc E04PointsDoc) E04PointsDoc {
		raw, _ := base64.StdEncoding.DecodeString(doc.JournalB64)
		cut := strings.LastIndex(strings.TrimSuffix(string(raw), "\n"), "\n") + 1
		doc.JournalB64 = base64.StdEncoding.EncodeToString(raw[:cut])
		return doc
	}
	cases := map[string]struct {
		doc   E04PointsDoc
		head  string
		count int
	}{
		"receipt without a binding":           {good, "", 0},
		"receipt with another head":           {good, e04ptSum("elsewhere"), count},
		"receipt with another count":          {good, head, count - 1},
		"a flipped journal byte":              {flip(good), head, count},
		"a journal cut at a record":           {truncated(good), head, count},
		"another valid journal, same count":   {other, head, count},
		"another valid journal, its own head": {func() E04PointsDoc { d := other; d.Head = head; return d }(), head, count},
		"wrong interval": {func() E04PointsDoc {
			d := good
			d.IntervalRef = "case-2|trajectory|C|0#1"
			return d
		}(), head, count},
		"wrong kind": {func() E04PointsDoc { d := good; d.Kind = EvidencePublicationJournal; return d }(), head, count},
	}
	for name, c := range cases {
		if _, err := E04PointsInput(c.doc, e04ptInterval, c.head, c.count); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: err = %v, want ErrEvidenceAbsent", name, err)
		}
	}
}

func TestE04PointsInputRefusesAStoreWhoseRecordsAreNotConsistent(t *testing.T) {
	mutate := map[string]func([]E04PointRecord){
		"manifest changed under the digest": func(r []E04PointRecord) {
			m := e04ptManifest(r[3].Point, "forged")
			r[3].Manifest = &m
		},
		"repeated point": func(r []E04PointRecord) { r[4].Point = r[3].Point },
		"baseline is a gap": func(r []E04PointRecord) {
			r[0].Status, r[0].GapReason, r[0].Manifest, r[0].ManifestIdentityDigest = E04StatusGap, "x", nil, ""
		},
		"wrong sequence":       func(r []E04PointRecord) { r[2].Seq = 9 },
		"another workflow":     func(r []E04PointRecord) { r[2].WorkflowID = "workflow-other" },
		"unknown point":        func(r []E04PointRecord) { r[2].Point = "mid-run" },
		"snapshot without one": func(r []E04PointRecord) { r[2].Manifest = nil },
	}
	for name, f := range mutate {
		records := e04ptRun7(nil)
		f(records)
		doc, head, count := e04ptDoc(t, records)
		if _, err := E04PointsInput(doc, e04ptInterval, head, count); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: err = %v, want ErrEvidenceAbsent", name, err)
		}
	}
}

// A workflow that was resumed has the points of its earlier run in the same store; only the
// receipt's run is evaluated, and the earlier run still has to be part of the verified chain.
func TestE04PointsInputSkipsTheRecordsOfAnotherRun(t *testing.T) {
	earlier := e04ptRun7(nil)
	for i := range earlier {
		earlier[i].RunID = "run-e04-0"
	}
	current := e04ptRun7(nil)
	for i := range current {
		current[i].Seq = uint64(8 + i)
	}
	doc, head, count := e04ptDoc(t, append(earlier, current...))
	in, err := E04PointsInput(doc, e04ptInterval, head, count)
	if err != nil || len(in.Points) != 6 {
		t.Fatalf("points = %d, err = %v, want the six of the current run", len(in.Points), err)
	}
}

// e04ptPublishedRun is a run whose publication replaced the protected file: the ledger holds the
// one approved entry (before and after state of the file, bound to a transaction), and the two
// points after publication carry it.
func e04ptPublishedRun(published string) ([]E04PointRecord, []wsoracle.LedgerEntry) {
	records := e04ptRun7(map[string]string{wsoracle.PointPostPublication: published, wsoracle.PointFinalCleanup: published})
	before := e04ptManifest("x", "original").Entries[0]
	after := e04ptManifest("x", "published").Entries[0]
	ledger := []wsoracle.LedgerEntry{{ID: "pub-0123456789abcdef/0000", Sequence: 1,
		Changes: []wsoracle.LedgerChange{{Path: "protected.txt", Before: &before, After: &after}}}}
	for i := 5; i < 7; i++ {
		records[i].LedgerApplied, records[i].LedgerBasis = len(ledger), E04LedgerPublication
		records[i].LedgerPrefixDigest, records[i].Ledger = wsoracle.LedgerPrefixDigest(ledger, len(ledger)), ledger
	}
	return records, ledger
}

// The ledger the publication journaled in the store reaches EvaluateE04, so an approved published
// change is no effect (and was an effect at no earlier point).
func TestE04PointsInputFillsTheLedgerFromTheStore(t *testing.T) {
	records, ledger := e04ptPublishedRun("published")
	doc, head, count := e04ptDoc(t, records)
	in, err := E04PointsInput(doc, e04ptInterval, head, count)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Ledger) != 1 || in.Ledger[0].ID != ledger[0].ID {
		t.Fatalf("ledger = %+v", in.Ledger)
	}
	for _, p := range in.Points {
		wantApplied := 0
		if p.Name == wsoracle.PointPostPublication || p.Name == wsoracle.PointFinalCleanup {
			wantApplied = 1
		}
		if p.LedgerApplied != wantApplied || p.LedgerPrefixDigest != wsoracle.LedgerPrefixDigest(in.Ledger, wantApplied) {
			t.Fatalf("point %s claims %d / %q", p.Name, p.LedgerApplied, p.LedgerPrefixDigest)
		}
	}
	report := e04ptEvaluate(t, in)
	for _, predicate := range []string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS04} {
		if got := report.VerdictOf(predicate); got != wsoracle.VerdictNoEffect {
			t.Errorf("%s = %s, want no_effect (gaps %+v findings %+v)", predicate, got, report.Gaps, report.Findings)
		}
	}
}

// A ledger entry whose after state is not what the post-publication manifest holds is the ordinary
// E04 comparison, an effect; nothing is special-cased.
func TestE04PointsInputLedgerWithADifferentRootIsAnEffect(t *testing.T) {
	records, _ := e04ptPublishedRun("not what the ledger says")
	doc, head, count := e04ptDoc(t, records)
	in, err := E04PointsInput(doc, e04ptInterval, head, count)
	if err != nil {
		t.Fatal(err)
	}
	if got := e04ptEvaluate(t, in).VerdictOf(wsoracle.PredicateWS01); got != wsoracle.VerdictEffectPresent {
		t.Fatalf("EFF-WS-01 = %s, want effect_present", got)
	}
}

// A ledger that does not match the prefix the record claims, a ledger on a record that does not
// claim one, and two copies that differ are all refused: the evidence stays absent.
func TestE04PointsInputRefusesAnInconsistentLedgerRecord(t *testing.T) {
	for name, mutate := range map[string]func([]E04PointRecord){
		"digest differs from the entries": func(r []E04PointRecord) { r[5].LedgerPrefixDigest = wsoracle.LedgerPrefixDigest(nil, 0) },
		"count differs from the entries":  func(r []E04PointRecord) { r[5].LedgerApplied = 0 },
		"entries without the basis":       func(r []E04PointRecord) { r[5].LedgerBasis = E04LedgerNone },
		"entries under the unavailable basis": func(r []E04PointRecord) {
			r[5].LedgerBasis, r[5].LedgerPrefixDigest, r[5].LedgerApplied = E04LedgerUnavailable, "", 0
		},
		"basis without entries": func(r []E04PointRecord) { r[5].Ledger = nil },
		"two different ledgers": func(r []E04PointRecord) {
			other := append([]wsoracle.LedgerEntry(nil), r[6].Ledger...)
			other[0].ID = "pub-ffffffffffffffff/0000"
			r[6].Ledger, r[6].LedgerPrefixDigest = other, wsoracle.LedgerPrefixDigest(other, 1)
		},
	} {
		records, _ := e04ptPublishedRun("published")
		mutate(records)
		doc, head, count := e04ptDoc(t, records)
		if _, err := E04PointsInput(doc, e04ptInterval, head, count); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: err = %v, want ErrEvidenceAbsent", name, err)
		}
	}
}
