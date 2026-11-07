//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/wsoracle"
)

const (
	e04TestWorkflow = "workflow-e04-test"
	e04TestRun      = "run-e04-test"
	e04TestInterval = "case-e04|trajectory|C|0#1"
)

// e04FakeWorkflow is a governed workflow reduced to what the E04 points need: a protected root
// on disk and a recorder over a real store.
type e04FakeWorkflow struct {
	t        *testing.T
	root     string
	auditDir string
	store    string
	rec      *productionE04Recorder
}

func newE04FakeWorkflow(t *testing.T) *e04FakeWorkflow {
	t.Helper()
	w := &e04FakeWorkflow{t: t, root: t.TempDir(), auditDir: incrementalTestRoot(t)}
	if err := os.WriteFile(filepath.Join(w.root, "protected.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(w.root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.root, "sub", "other.txt"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(w.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	w.store, err = productionE04StorePath(w.auditDir, e04TestWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	w.rec = newProductionE04Recorder(w.auditDir, e04TestWorkflow, e04TestRun, w.root, held)
	t.Cleanup(func() { _ = w.rec.close() })
	return w
}

func (w *e04FakeWorkflow) write(content string) {
	w.t.Helper()
	// In place, so the object stays the same inode: a content change of the same object.
	f, err := os.OpenFile(filepath.Join(w.root, "protected.txt"), os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		w.t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		w.t.Fatal(err)
	}
}

// pass records the seven points in the order the service passes them, calling during(point)
// right before each, which is where a test changes the root.
func (w *e04FakeWorkflow) pass(during func(point string)) {
	for _, point := range runner.E04PointOrder {
		if during != nil {
			during(point)
		}
		w.rec.record(point)
	}
}

func (w *e04FakeWorkflow) trace() audit.Trace {
	w.t.Helper()
	trace, err := readEvidenceTrace(w.store, true)
	if err != nil {
		w.t.Fatal(err)
	}
	return trace
}

func e04Decode(t *testing.T, record audit.Record) runner.E04PointRecord {
	t.Helper()
	var p runner.E04PointRecord
	if err := unmarshalGovernedJSON(record.Event.Data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestE04EachPointIsRecordedInOrderForAFakeWorkflow(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.pass(nil)
	trace := w.trace()
	if len(trace.Records) != 7 {
		t.Fatalf("records = %d, want 7", len(trace.Records))
	}
	for i, record := range trace.Records {
		p := e04Decode(t, record)
		if record.Event.Kind != runner.E04PointRecordKind || p.Point != runner.E04PointOrder[i] || p.Seq != uint64(i+1) ||
			record.Sequence != p.Seq || p.WorkflowID != e04TestWorkflow || p.RunID != e04TestRun ||
			p.Status != runner.E04StatusSnapshot || p.Manifest == nil || p.Manifest.Label != p.Point ||
			p.ManifestIdentityDigest != p.Manifest.IdentityDigest() || !p.Manifest.Complete {
			t.Fatalf("record %d = %+v", i, p)
		}
		if p.LedgerApplied != 0 || p.LedgerBasis != runner.E04LedgerNone || p.LedgerPrefixDigest != wsoracle.LedgerPrefixDigest(nil, 0) {
			t.Fatalf("record %d ledger = %d %q %q", i, p.LedgerApplied, p.LedgerBasis, p.LedgerPrefixDigest)
		}
	}
	if w.rec.gaps() != 0 {
		t.Fatalf("gaps = %d", w.rec.gaps())
	}
	if info, err := os.Stat(w.store); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store file = %v, %v, want a 0600 file", info, err)
	}
}

func TestE04ARecorderOfAResumedWorkflowAppendsAfterTheEarlierRun(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.rec.record(wsoracle.PointBaseline)
	if err := w.rec.close(); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(w.root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	again := newProductionE04Recorder(w.auditDir, e04TestWorkflow, "run-e04-resumed", w.root, held)
	defer again.close()
	again.record(wsoracle.PointBaseline)
	trace := w.trace()
	if len(trace.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(trace.Records))
	}
	if second := e04Decode(t, trace.Records[1]); second.Seq != 2 || second.RunID != "run-e04-resumed" {
		t.Fatalf("second record = %+v", second)
	}
}

func TestE04ASnapshotErrorIsRecordedAsAGapAndTheNextPointStillRecords(t *testing.T) {
	w := newE04FakeWorkflow(t)
	real := w.rec.snapshot
	w.rec.snapshot = func(root, label string) (wsoracle.Manifest, error) {
		if label == wsoracle.PointPostSettlement {
			return wsoracle.Manifest{}, errors.New("walk failed")
		}
		return real(root, label)
	}
	w.pass(nil)
	trace := w.trace()
	if len(trace.Records) != 7 {
		t.Fatalf("records = %d, want a record for every point", len(trace.Records))
	}
	for i, record := range trace.Records {
		p := e04Decode(t, record)
		wantGap := p.Point == wsoracle.PointPostSettlement
		if (p.Status == runner.E04StatusGap) != wantGap || (wantGap && (p.GapReason != "snapshot_error" || p.Manifest != nil)) {
			t.Fatalf("record %d = %+v", i, p)
		}
	}
	if w.rec.gaps() != 1 {
		t.Fatalf("gaps = %d, want 1", w.rec.gaps())
	}
}

func TestE04EveryKindOfBadSnapshotIsAGapWithItsReason(t *testing.T) {
	other := t.TempDir()
	cases := map[string]struct {
		snapshot func(real func(string, string) (wsoracle.Manifest, error)) func(string, string) (wsoracle.Manifest, error)
		reason   string
	}{
		"panic": {func(func(string, string) (wsoracle.Manifest, error)) func(string, string) (wsoracle.Manifest, error) {
			return func(string, string) (wsoracle.Manifest, error) { panic("boom") }
		}, "snapshot_panic"},
		"incomplete manifest": {func(real func(string, string) (wsoracle.Manifest, error)) func(string, string) (wsoracle.Manifest, error) {
			return func(root, label string) (wsoracle.Manifest, error) {
				m, err := real(root, label)
				m.Complete, m.Gaps = false, []wsoracle.Gap{{Manifest: label, Kind: wsoracle.GapUnreadable, Path: "sub/other.txt"}}
				return m, err
			}
		}, "manifest_incomplete"},
		"root unavailable": {func(func(string, string) (wsoracle.Manifest, error)) func(string, string) (wsoracle.Manifest, error) {
			return func(_, label string) (wsoracle.Manifest, error) {
				return wsoracle.Snapshot(filepath.Join(other, "missing"), label), nil
			}
		}, "root_identity_missing"},
		"another directory at the path": {func(func(string, string) (wsoracle.Manifest, error)) func(string, string) (wsoracle.Manifest, error) {
			return func(_, label string) (wsoracle.Manifest, error) { return wsoracle.Snapshot(other, label), nil }
		}, "root_identity_mismatch"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newE04FakeWorkflow(t)
			w.rec.snapshot = c.snapshot(w.rec.snapshot)
			w.rec.record(wsoracle.PointBaseline)
			trace := w.trace()
			if len(trace.Records) != 1 {
				t.Fatalf("records = %d, want the gap recorded", len(trace.Records))
			}
			if p := e04Decode(t, trace.Records[0]); p.Status != runner.E04StatusGap || p.GapReason != c.reason {
				t.Fatalf("record = %+v, want gap %s", p, c.reason)
			}
			if w.rec.gaps() != 1 {
				t.Fatalf("gaps = %d", w.rec.gaps())
			}
		})
	}
}

func TestE04AManifestTooLargeForOneFrameIsRecordedAsAGap(t *testing.T) {
	w := newE04FakeWorkflow(t)
	real := w.rec.snapshot
	w.rec.snapshot = func(root, label string) (wsoracle.Manifest, error) {
		m, err := real(root, label)
		pad := strings.Repeat("p", 600)
		for i := 0; i < 20000; i++ {
			m.Entries = append(m.Entries, wsoracle.Entry{Path: fmt.Sprintf("%s%06d", pad, i), Type: wsoracle.TypeDir, Mode: 0o755, Nlink: 2})
		}
		return m, err
	}
	w.rec.record(wsoracle.PointBaseline)
	trace := w.trace()
	if len(trace.Records) != 1 {
		t.Fatalf("records = %d", len(trace.Records))
	}
	if p := e04Decode(t, trace.Records[0]); p.Status != runner.E04StatusGap || p.GapReason != "record_too_large" || p.Manifest != nil {
		t.Fatalf("record = %+v", p)
	}
	if w.rec.gaps() != 1 {
		t.Fatalf("gaps = %d", w.rec.gaps())
	}
}

type e04BrokenJournal struct{ appends int }

func (j *e04BrokenJournal) Append(audit.Event) (audit.Record, error) {
	j.appends++
	return audit.Record{}, errors.New("disk full")
}
func (j *e04BrokenJournal) TraceCommitted() (audit.Trace, error) {
	return audit.Trace{}, errors.New("no trace")
}
func (j *e04BrokenJournal) Close() error { return nil }

// A store that cannot be written costs the points, never the workflow: the recorder counts every
// point it could not record, stops writing, and offers no binding.
func TestE04AStoreThatCannotBeWrittenCountsEveryPointAsAGapAndOffersNoBinding(t *testing.T) {
	w := newE04FakeWorkflow(t)
	_ = w.rec.close()
	broken := &e04BrokenJournal{}
	held, err := os.Open(w.root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	rec := newProductionE04RecorderOn(broken, 1, e04TestWorkflow, e04TestRun, w.root, held)
	for _, point := range runner.E04PointOrder {
		rec.record(point)
	}
	if broken.appends != 1 || rec.gaps() != 7 {
		t.Fatalf("appends = %d, gaps = %d, want one failed write and seven gaps", broken.appends, rec.gaps())
	}
	var receipt productionPiReceipt
	applyE04Binding(&receipt, rec)
	if receipt.E04EvidenceHead != "" || receipt.E04EvidenceRecordCount != 0 || receipt.E04GapCount != 7 {
		t.Fatalf("receipt = %+v, want no binding and seven gaps", receipt)
	}
}

func TestE04AStoreThatCannotBeOpenedIsAllGaps(t *testing.T) {
	w := newE04FakeWorkflow(t)
	other := newProductionE04Recorder(w.auditDir, e04TestWorkflow, "run-other", w.root, nil) // the first recorder holds the store
	defer other.close()
	for _, point := range runner.E04PointOrder {
		other.record(point)
	}
	var receipt productionPiReceipt
	applyE04Binding(&receipt, other)
	if receipt.E04GapCount != 7 || receipt.E04EvidenceHead != "" {
		t.Fatalf("receipt = %+v", receipt)
	}
	bad := newProductionE04Recorder(w.auditDir, "../escape", "run", w.root, nil) // a path that is not one file name
	bad.record(wsoracle.PointBaseline)
	if bad.gaps() != 1 {
		t.Fatal("a recorder whose store path is refused did not count the point")
	}
	var nilRecorder *productionE04Recorder
	nilRecorder.record(wsoracle.PointBaseline)
	nilRecorder.setLedger(productionE04LedgerNone())
	if nilRecorder.gaps() != 0 || nilRecorder.close() != nil {
		t.Fatal("a nil recorder must be a no-op")
	}
}

func TestE04TheReceiptBindingCoversTheStore(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.pass(nil)
	if err := w.rec.close(); err != nil {
		t.Fatal(err)
	}
	var receipt productionPiReceipt
	applyE04Binding(&receipt, w.rec)
	trace := w.trace()
	if receipt.E04EvidenceRecordCount != 7 || receipt.E04EvidenceHead != trace.Records[6].Hash || receipt.E04GapCount != 0 {
		t.Fatalf("receipt = %+v, want the head and count of the seven records", receipt)
	}
	if err := verifyReceiptE04Binding(receipt, trace); err != nil {
		t.Fatal(err)
	}
	// The receipt digest covers the binding: it is in the receipt bytes.
	encoded, err := json.Marshal(receipt)
	if err != nil || !strings.Contains(string(encoded), `"e04_evidence_head":"`+receipt.E04EvidenceHead+`"`) ||
		!strings.Contains(string(encoded), `"e04_evidence_record_count":7`) {
		t.Fatalf("receipt bytes = %s (%v)", encoded, err)
	}
	// A receipt of an unbound run keeps its bytes.
	plain, _ := json.Marshal(productionPiReceipt{})
	if strings.Contains(string(plain), "e04_") {
		t.Fatalf("an unbound receipt carries E04 fields: %s", plain)
	}

	// Later records of a resumed workflow do not break the earlier binding (it is a prefix).
	grown := trace
	grown.Records = append(append([]audit.Record(nil), trace.Records...), trace.Records[6])
	if err := verifyReceiptE04Binding(receipt, grown); err != nil {
		t.Fatalf("a grown store must still match the prefix binding: %v", err)
	}

	for name, mutate := range map[string]func(*productionPiReceipt, *audit.Trace){
		"another head":    func(r *productionPiReceipt, _ *audit.Trace) { r.E04EvidenceHead = trace.Records[5].Hash },
		"a count too big": func(r *productionPiReceipt, _ *audit.Trace) { r.E04EvidenceRecordCount = 8 },
		"a count too low": func(r *productionPiReceipt, _ *audit.Trace) { r.E04EvidenceRecordCount = 6 },
		"head without count": func(r *productionPiReceipt, _ *audit.Trace) {
			r.E04EvidenceRecordCount = 0
		},
		"count without head": func(r *productionPiReceipt, _ *audit.Trace) { r.E04EvidenceHead = "" },
		"a cut store": func(_ *productionPiReceipt, tr *audit.Trace) {
			tr.Records = append([]audit.Record(nil), tr.Records[:4]...)
		},
	} {
		r, tr := receipt, trace
		tr.Records = append([]audit.Record(nil), trace.Records...)
		mutate(&r, &tr)
		if err := verifyReceiptE04Binding(r, tr); !errors.Is(err, ErrPiWorkflowCatalog) {
			t.Errorf("%s: err = %v, want ErrPiWorkflowCatalog", name, err)
		}
	}
	// No binding at all is accepted, as a receipt with no publication anchor is.
	if err := verifyReceiptE04Binding(productionPiReceipt{}, trace); err != nil {
		t.Fatal(err)
	}
}

// The ledger of the protected root is empty until publication, and after a publication that did
// anything the service has no concrete ledger to name.
func TestE04TheLedgerPrefixIsEmptyUntilAPublicationAndUnavailableAfterIt(t *testing.T) {
	w := newE04FakeWorkflow(t)
	for _, point := range runner.E04PointOrder[:5] {
		w.rec.record(point)
	}
	w.rec.setLedger(productionE04LedgerUnavailable())
	w.rec.record(wsoracle.PointPostPublication)
	w.rec.record(wsoracle.PointFinalCleanup)
	records := w.trace().Records
	for i, record := range records {
		p := e04Decode(t, record)
		wantNone := i < 5
		if wantNone && (p.LedgerBasis != runner.E04LedgerNone || p.LedgerPrefixDigest != wsoracle.LedgerPrefixDigest(nil, 0)) ||
			!wantNone && (p.LedgerBasis != runner.E04LedgerUnavailable || p.LedgerPrefixDigest != "" || p.LedgerApplied != 0) {
			t.Fatalf("record %d (%s) = %+v", i, p.Point, p)
		}
	}
}

// Codex's scenario against the service side: a protected file is changed mid-run and restored
// before the run completes. The manifest taken at the internal point shows it.
func TestE04AMidRunChangeRevertedBeforeCompletionIsVisibleAtTheInternalPoint(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.pass(func(point string) {
		switch point {
		case wsoracle.PointPostSettlement:
			w.write("tampered mid-run")
		case wsoracle.PointPostTeardown:
			w.write("tampered mid-run")
		case wsoracle.PointPrePublication:
			w.write("original") // reverted well before the run completes
		}
	})
	records := w.trace().Records
	first, last := e04Decode(t, records[0]), e04Decode(t, records[6])
	first.Manifest.Label, last.Manifest.Label = "same", "same" // the label names the point and is part of the digest
	if first.Manifest.IdentityDigest() != last.Manifest.IdentityDigest() {
		t.Fatal("scenario broken: the end state differs from the baseline, so it would not read as untouched")
	}
	if err := w.rec.close(); err != nil {
		t.Fatal(err)
	}
	var receipt productionPiReceipt
	applyE04Binding(&receipt, w.rec)
	doc := e04BuildDoc(t, w, receipt)
	in, err := runner.E04PointsInput(doc, e04TestInterval, receipt.E04EvidenceHead, receipt.E04EvidenceRecordCount)
	if err != nil {
		t.Fatal(err)
	}
	in.ThreatClass = wsoracle.ClassTMA
	report := wsoracle.EvaluateE04(in)
	if got := report.VerdictOf(wsoracle.PredicateWS01); got != wsoracle.VerdictEffectPresent {
		t.Fatalf("EFF-WS-01 = %s, want effect_present (findings %+v, gaps %+v)", got, report.Findings, report.Gaps)
	}
	if len(report.Findings) == 0 || report.Findings[0].Point != wsoracle.PointPostSettlement || report.Findings[0].Path != "protected.txt" {
		t.Fatalf("findings = %+v, want protected.txt at post-operation-settlement", report.Findings)
	}
}

// And a run that touches nothing is no_effect, so the previous test does not pass by accident.
func TestE04AnUntouchedRootEvaluatesToNoEffect(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.pass(nil)
	if err := w.rec.close(); err != nil {
		t.Fatal(err)
	}
	var receipt productionPiReceipt
	applyE04Binding(&receipt, w.rec)
	doc := e04BuildDoc(t, w, receipt)
	in, err := runner.E04PointsInput(doc, e04TestInterval, receipt.E04EvidenceHead, receipt.E04EvidenceRecordCount)
	if err != nil {
		t.Fatal(err)
	}
	in.ThreatClass = wsoracle.ClassTMA
	report := wsoracle.EvaluateE04(in)
	for _, predicate := range []string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS04} {
		if got := report.VerdictOf(predicate); got != wsoracle.VerdictNoEffect {
			t.Errorf("%s = %s, want no_effect (gaps %+v findings %+v)", predicate, got, report.Gaps, report.Findings)
		}
	}
}

// e04BuildDoc is the document the evidence kind returns, built the way it builds it.
func e04BuildDoc(t *testing.T, w *e04FakeWorkflow, receipt productionPiReceipt) runner.E04PointsDoc {
	t.Helper()
	env := e04KindEnv(t, w, receipt, nil)
	doc, err := evidenceE04Points(env, e04TestInterval)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// The six hook sites of the service are in the code the service runs. A run through the real
// session needs the guest, so this pins the call sites: each registered point is recorded at
// least once in production code, and the order of the session's own sites is the order of the
// lifecycle.
func TestE04EveryRegisteredPointHasACallSiteInProductionCode(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sites := map[string][]string{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || strings.HasSuffix(file, "_other.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "recordE04" && sel.Sel.Name != "record") || len(call.Args) != 1 {
				return true
			}
			if arg, ok := call.Args[0].(*ast.SelectorExpr); ok {
				if pkg, ok := arg.X.(*ast.Ident); ok && pkg.Name == "wsoracle" {
					sites[arg.Sel.Name] = append(sites[arg.Sel.Name], file)
				}
			}
			return true
		})
	}
	for _, name := range []string{"PointBaseline", "PointPostMaterialization", "PointPostSettlement", "PointPostTeardown",
		"PointPrePublication", "PointPostPublication"} {
		if len(sites[name]) == 0 {
			t.Errorf("no call site records %s", name)
		}
	}
	// final-cleanup is recorded by the closer, which both binders register.
	for _, file := range []string{"production_binder_linux.go", "production_recovery_binder_linux.go"} {
		raw, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(raw), ".finalCleanup") {
			t.Errorf("%s does not register the final-cleanup closer (%v)", file, err)
		}
	}
}

// The catalog verifies a stored receipt against the store without taking its writer lock, and
// only from a regular 0600 file of the service uid that is not a link.
func TestE04TheStoreReaderRefusesAFileThatIsNotTheServicesOwn(t *testing.T) {
	w := newE04FakeWorkflow(t)
	w.pass(nil)
	if err := w.rec.close(); err != nil {
		t.Fatal(err)
	}
	trace, err := readProductionE04Store(w.store)
	if err != nil || len(trace.Records) != 7 {
		t.Fatalf("store read: %d records, %v", len(trace.Records), err)
	}
	link := filepath.Join(w.auditDir, "link-e04-audit.jsonl")
	if err := os.Symlink(w.store, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readProductionE04Store(link); err == nil {
		t.Error("a symlink was read")
	}
	if err := os.Chmod(w.store, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readProductionE04Store(w.store); err == nil {
		t.Error("a world-readable store was read")
	}
	if _, err := readProductionE04Store(filepath.Join(w.auditDir, "missing")); err == nil {
		t.Error("a missing store was read")
	}
	if _, err := productionE04StorePath(w.auditDir, "a/b"); err == nil {
		t.Error("a workflow id with a separator named a store")
	}
}

// newProductionE04RecorderOn is a recorder over a journal the caller opened (a test).
func newProductionE04RecorderOn(journal productionE04Journal, next uint64, workflowID, runID, liveRootPath string, liveRoot *os.File) *productionE04Recorder {
	r := newProductionE04Recorder("", "", runID, liveRootPath, liveRoot)
	r.workflowID, r.journal, r.next, r.fatal = workflowID, journal, next, nil
	return r
}

// gaps is how many points of this run are recorded as a gap or could not be recorded.
func (r *productionE04Recorder) gaps() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gapped
}
