package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/wsoracle"
)

// watchGuest answers transient-watch the way the guest does: the readiness line, then (when the host writes the stop
// line to its stdin) the document, and an abort when the stdin ends without the stop line. It answers e04-points from
// a store.
type watchGuest struct {
	mu      sync.Mutex
	calls   [][]string
	report  effectoracle.Report
	e04     E04PointsDoc
	noReady bool
	fail    bool
	ready   func(*TransientReadyDoc)
	doc     func(*TransientDoc)
	// ends records how each transient watch ended: "stop" (the stop line), "eof" (stdin ended without it) or
	// "cancelled" (the session was killed).
	ends chan string
}

func newWatchGuest(report effectoracle.Report) *watchGuest {
	return &watchGuest{report: report, ends: make(chan string, 8)}
}

func (g *watchGuest) argvs() [][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([][]string(nil), g.calls...)
}

func (g *watchGuest) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return g.RunReadyStdin(ctx, argv, nil, nil)
}

func (g *watchGuest) RunReady(ctx context.Context, argv []string, ready func([]byte)) ([]byte, []byte, int, error) {
	return g.RunReadyStdin(ctx, argv, nil, ready)
}

func (g *watchGuest) end(how string) {
	if g.ends != nil {
		g.ends <- how
	}
}

func (g *watchGuest) RunReadyStdin(ctx context.Context, argv []string, stdin io.Reader, ready func([]byte)) ([]byte, []byte, int, error) {
	g.mu.Lock()
	g.calls = append(g.calls, append([]string(nil), argv...))
	g.mu.Unlock()
	line := func(v any) []byte { b, _ := json.Marshal(v); return append(b, '\n') }
	switch argv[0] {
	case EvidenceE04Points:
		return line(g.e04), nil, 0, nil
	case EvidenceTransientWatch:
		if g.fail {
			return nil, []byte("watch failed"), 2, nil
		}
		var first []byte
		if !g.noReady {
			rd := TransientReadyDoc{Schema: EvidenceSchema, Kind: TransientReadyKind, IntervalRef: argv[2], Root: EvidenceLiveRoot}
			if g.ready != nil {
				g.ready(&rd)
			}
			first = line(rd)
			if ready != nil {
				ready(first)
			}
		}
		// The guest waits for the host's stop line.
		got := make(chan string, 1)
		go func() {
			if stdin == nil {
				got <- "eof"
				return
			}
			l, err := bufio.NewReader(stdin).ReadString('\n')
			if err == nil && l == TransientStopLine {
				got <- "stop"
				return
			}
			got <- "eof"
		}()
		select {
		case how := <-got:
			g.end(how)
			if how != "stop" {
				return nil, []byte("aborted: stdin ended without a stop request"), 2, nil
			}
		case <-ctx.Done():
			g.end("cancelled")
			return nil, nil, 0, ctx.Err()
		}
		d := TransientDoc{Schema: EvidenceSchema, Kind: EvidenceTransientWatch, IntervalRef: argv[2], Root: EvidenceLiveRoot,
			DurationSeconds: 30, ElapsedMicros: int64(time.Minute / time.Microsecond), Stop: TransientStopRequested, Report: g.report}
		if g.doc != nil {
			g.doc(&d)
		}
		return append(first, line(d)...), nil, 0, nil
	}
	return nil, []byte("unknown"), 2, nil
}

// readyOnlyTransport can report the first line but cannot give the command a stdin.
type readyOnlyTransport struct{ g *watchGuest }

func (p readyOnlyTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return p.g.Run(ctx, argv)
}

func (p readyOnlyTransport) RunReady(ctx context.Context, argv []string, ready func([]byte)) ([]byte, []byte, int, error) {
	return p.g.RunReady(ctx, argv, ready)
}

type plainWatchTransport struct{ g *watchGuest }

func (p plainWatchTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return p.g.Run(ctx, argv)
}

// okReport is a complete qualified window with the given events.
func okReport(events ...effectoracle.Event) effectoracle.Report {
	return effectoracle.Report{OracleVersion: effectoracle.OracleVersion, Instrument: effectoracle.Instrument,
		Verdict: effectoracle.VerdictOK, Events: events,
		Heartbeats: []effectoracle.Heartbeat{
			{Seq: 1, Label: "mark:start:pre", Observed: true}, {Seq: 2, Label: "mark:start:post", Observed: true},
			{Seq: 4, Label: "mark:stop:pre", Observed: true}, {Seq: 5, Label: "mark:stop:post", Observed: true}},
		Marks: []effectoracle.Mark{{Seq: 3, Name: effectoracle.MarkStart, Quiescent: true}, {Seq: 6, Name: effectoracle.MarkStop, Quiescent: true}}}
}

func ev(path, kind string) effectoracle.Event {
	return effectoracle.Event{Seq: 1, Kind: kind, Path: path}
}

func newTransient(g *watchGuest) *TransientCollector {
	return &TransientCollector{Client: EvidenceClient{Transport: g}, Duration: 30 * time.Second}
}

// publishedGuest serves the store of e04ptPublishedRun: the ledger has one entry, an overwrite of protected.txt.
func publishedGuest(t *testing.T, report effectoracle.Report) (*watchGuest, EvidenceRef) {
	t.Helper()
	records, _ := e04ptPublishedRun("published")
	doc, head, count := e04ptDoc(t, records)
	g := newWatchGuest(report)
	g.e04 = doc
	return g, EvidenceRef{Interval: e04ptInterval, RunID: e04ptRun, E04Head: head, E04Count: count}
}

// publicationEvents are the events publication makes for the overwrite of protected.txt: the new file renamed in and
// the old one renamed away.
func publicationEvents() []effectoracle.Event {
	return []effectoracle.Event{ev("protected.txt", effectoracle.KindMovedTo), ev("protected.txt", effectoracle.KindMovedFrom)}
}

func runTransient(t *testing.T, c *TransientCollector, ref EvidenceRef) (ObserverEvidence, error) {
	t.Helper()
	if err := c.BeginInterval(context.Background(), ref); err != nil {
		t.Fatalf("BeginInterval: %v", err)
	}
	defer c.EndInterval(ref)
	return c.Transient(context.Background(), ref)
}

func TestTransientCollectorPublishedIntervalWithExactlyThePublicationRenamesIsNone(t *testing.T) {
	g, ref := publishedGuest(t, okReport(publicationEvents()...))
	c := newTransient(g)
	got, err := runTransient(t, c, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Findings[OracleTransient] != FindingNone {
		t.Fatalf("findings = %v", got.Findings)
	}
	if r := got.Refs[OracleTransient]; r.Version != effectoracle.OracleVersion || len(r.Report) != 64 {
		t.Fatalf("ref = %+v", r)
	}
	want := []string{"transient-watch", "--interval-ref", e04ptInterval, "--duration", "30"}
	if calls := g.argvs(); !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("watch argv = %q, want %q", calls[0], want)
	}
	// The key is a result an outcome accepts.
	var o Outcome
	if err := o.recordObserver("TRANSIENT", transientKeys, got); err != nil || !o.refValid(OracleTransient) {
		t.Fatalf("record: %v", err)
	}
}

func TestTransientCollectorUnpublishedIntervalWithNoEventsIsNone(t *testing.T) {
	records := e04ptRun7(nil)
	doc, head, count := e04ptDoc(t, records)
	g := newWatchGuest(okReport())
	g.e04 = doc
	got, err := runTransient(t, newTransient(g), EvidenceRef{Interval: e04ptInterval, RunID: e04ptRun, E04Head: head, E04Count: count})
	if err != nil || got.Findings[OracleTransient] != FindingNone {
		t.Fatalf("%v, %v", got.Findings, err)
	}
}

// A write to the live root that publication did not apply is an effect, even if it was undone before any
// snapshot, and even if the report is otherwise not qualified.
func TestTransientCollectorAnEventTheLedgerDoesNotNameIsObserved(t *testing.T) {
	for name, report := range map[string]effectoracle.Report{
		"created and deleted": okReport(append(publicationEvents(), ev("tmp.bin", effectoracle.KindCreate), ev("tmp.bin", effectoracle.KindDelete))...),
		"report with a gap": func() effectoracle.Report {
			r := okReport(append(publicationEvents(), ev("evil.sh", effectoracle.KindCreate))...)
			r.Verdict = effectoracle.VerdictEvidenceLost
			return r
		}(),
		"a rename of a path the ledger does not name": okReport(append(publicationEvents(), ev("other.txt", effectoracle.KindMovedTo))...),
	} {
		g, ref := publishedGuest(t, report)
		got, err := runTransient(t, newTransient(g), ref)
		if err != nil || got.Findings[OracleTransient] != FindingObserved {
			t.Errorf("%s: %v, %v", name, got.Findings, err)
		}
	}
	// With no publication at all every event is unexplained.
	doc, head, count := e04ptDoc(t, e04ptRun7(nil))
	g := newWatchGuest(okReport(ev("a.txt", effectoracle.KindModify)))
	g.e04 = doc
	got, err := runTransient(t, newTransient(g), EvidenceRef{Interval: e04ptInterval, RunID: e04ptRun, E04Head: head, E04Count: count})
	if err != nil || got.Findings[OracleTransient] != FindingObserved {
		t.Errorf("unpublished: %v, %v", got.Findings, err)
	}
}

// A write-and-restore breach on a path the publication ledger names leaves events of kinds publication never makes
// (it renames); with or without the renames publication really made, it is an observed effect.
func TestTransientCollectorAnInPlaceWriteOnALedgerPathIsObserved(t *testing.T) {
	for _, kind := range []string{effectoracle.KindCreate, effectoracle.KindModify, effectoracle.KindCloseWrite, effectoracle.KindAttrib, effectoracle.KindDelete} {
		for name, events := range map[string][]effectoracle.Event{
			"alone":                  {ev("protected.txt", kind)},
			"around the publication": append(append([]effectoracle.Event{ev("protected.txt", kind)}, publicationEvents()...), ev("protected.txt", kind)),
		} {
			g, ref := publishedGuest(t, okReport(events...))
			got, err := runTransient(t, newTransient(g), ref)
			if err != nil || got.Findings[OracleTransient] != FindingObserved {
				t.Errorf("%s %s: %v, %v", kind, name, got.Findings, err)
			}
		}
	}
}

// Whatever the instrument could not establish is lost, never none.
func TestTransientCollectorLostEvidenceIsNeverNone(t *testing.T) {
	writes := publicationEvents()
	t.Run("ledger change the instrument did not see", func(t *testing.T) {
		g, ref := publishedGuest(t, okReport())
		if got, err := runTransient(t, newTransient(g), ref); err != nil || got.Findings[OracleTransient] != FindingLost {
			t.Fatalf("%v, %v", got.Findings, err)
		}
	})
	t.Run("the rename the ledger operation needs was not seen", func(t *testing.T) {
		// Only the displaced old file was seen, not the new one renamed in.
		g, ref := publishedGuest(t, okReport(ev("protected.txt", effectoracle.KindMovedFrom)))
		if got, err := runTransient(t, newTransient(g), ref); err != nil || got.Findings[OracleTransient] != FindingLost {
			t.Fatalf("%v, %v", got.Findings, err)
		}
	})
	for _, verdict := range []string{effectoracle.VerdictEvidenceLost, effectoracle.VerdictInstrumentUnqualified, "", "nonsense"} {
		r := okReport(writes...)
		r.Verdict = verdict
		g, ref := publishedGuest(t, r)
		if got, err := runTransient(t, newTransient(g), ref); err != nil || got.Findings[OracleTransient] != FindingLost {
			t.Errorf("verdict %q: %v, %v", verdict, got.Findings, err)
		}
	}
	t.Run("another oracle version", func(t *testing.T) {
		r := okReport(writes...)
		r.OracleVersion = "effectoracle/v0"
		g, ref := publishedGuest(t, r)
		if got, err := runTransient(t, newTransient(g), ref); err != nil || got.Findings[OracleTransient] != FindingLost {
			t.Fatalf("%v, %v", got.Findings, err)
		}
	})
	t.Run("no ledger to explain the events", func(t *testing.T) {
		g, ref := publishedGuest(t, okReport())
		for name, mut := range map[string]func(*EvidenceRef){
			"no receipt binding": func(r *EvidenceRef) { r.E04Head, r.E04Count = "", 0 },
			"wrong binding":      func(r *EvidenceRef) { r.E04Count++ },
			"another run":        func(r *EvidenceRef) { r.RunID = "run-other" },
			"no run":             func(r *EvidenceRef) { r.RunID = "" },
		} {
			r := ref
			mut(&r)
			if got, err := runTransient(t, newTransient(g), r); err != nil || got.Findings[OracleTransient] != FindingLost {
				t.Errorf("%s: %v, %v", name, got.Findings, err)
			}
		}
		// A ledger the service marked unavailable.
		records, _ := e04ptPublishedRun("published")
		for i := 5; i < 7; i++ {
			records[i].Ledger, records[i].LedgerApplied, records[i].LedgerPrefixDigest, records[i].LedgerBasis = nil, 0, "", E04LedgerUnavailable
		}
		doc, head, count := e04ptDoc(t, records)
		g = newWatchGuest(okReport())
		g.e04 = doc
		if got, err := runTransient(t, newTransient(g), EvidenceRef{Interval: e04ptInterval, RunID: e04ptRun, E04Head: head, E04Count: count}); err != nil || got.Findings[OracleTransient] != FindingLost {
			t.Errorf("unavailable ledger: %v, %v", got.Findings, err)
		}
	})
}

// The host does not trust the report's own verdict: a report that is not a complete window by its records is lost
// even when it says ok and holds exactly the publication's events.
func TestTransientCollectorAnIncompleteReportIsLostWhateverItsVerdictSays(t *testing.T) {
	for name, mutate := range map[string]func(*effectoracle.Report){
		"empty report": func(r *effectoracle.Report) {
			*r = effectoracle.Report{OracleVersion: effectoracle.OracleVersion, Verdict: effectoracle.VerdictOK}
		},
		"no marks":               func(r *effectoracle.Report) { r.Marks = nil },
		"no stop mark":           func(r *effectoracle.Report) { r.Marks = r.Marks[:1] },
		"no heartbeats":          func(r *effectoracle.Report) { r.Heartbeats = nil },
		"a canary not seen":      func(r *effectoracle.Report) { r.Heartbeats[2].Observed = false },
		"a gap under verdict ok": func(r *effectoracle.Report) { r.Gaps = []effectoracle.Gap{{Seq: 9, Kind: effectoracle.GapOverflow}} },
	} {
		r := okReport(publicationEvents()...)
		mutate(&r)
		g, ref := publishedGuest(t, r)
		if got, err := runTransient(t, newTransient(g), ref); err != nil || got.Findings[OracleTransient] != FindingLost {
			t.Errorf("%s: %v, %v", name, got.Findings, err)
		}
	}
	// An unpublished interval with an empty report is lost, not none.
	doc, head, count := e04ptDoc(t, e04ptRun7(nil))
	g := newWatchGuest(effectoracle.Report{OracleVersion: effectoracle.OracleVersion, Verdict: effectoracle.VerdictOK})
	g.e04 = doc
	if got, err := runTransient(t, newTransient(g), EvidenceRef{Interval: e04ptInterval, RunID: e04ptRun, E04Head: head, E04Count: count}); err != nil || got.Findings[OracleTransient] != FindingLost {
		t.Errorf("empty report, unpublished: %v, %v", got.Findings, err)
	}
}

// The window the guest reports has to cover the request: it must have stopped because the host asked, after the
// request, and have observed at least as long as the request lasted.
func TestTransientCollectorAWindowThatDoesNotCoverTheRequestIsLost(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, tc := range map[string]struct {
		mutate func(*TransientDoc)
		ended  time.Duration
		want   Finding
	}{
		"stopped on the host's request, observed longer": {func(d *TransientDoc) { d.ElapsedMicros = 31_000_000 }, 30 * time.Second, FindingNone},
		"observed exactly as long":                       {func(d *TransientDoc) { d.ElapsedMicros = 30_000_000 }, 30 * time.Second, FindingNone},
		"observed less than the request lasted":          {func(d *TransientDoc) { d.ElapsedMicros = 29_999_999 }, 30 * time.Second, FindingLost},
		"stopped on its own timer":                       {func(d *TransientDoc) { d.Stop = TransientStopTimer }, time.Second, FindingLost},
		"no stop reason":                                 {func(d *TransientDoc) { d.Stop = "" }, time.Second, FindingLost},
		"claims the full duration but observed nothing":  {func(d *TransientDoc) { d.ElapsedMicros = 0 }, time.Second, FindingLost},
	} {
		g, ref := publishedGuest(t, okReport(publicationEvents()...))
		g.doc = tc.mutate
		c := newTransient(g)
		c.Now = func() time.Time { return now }
		ref.Ended = now.Add(tc.ended)
		if got, err := runTransient(t, c, ref); err != nil || got.Findings[OracleTransient] != tc.want {
			t.Errorf("%s: %v, %v, want %s", name, got.Findings, err, tc.want)
		}
	}
	// An effect the short window did record stays observed.
	g, ref := publishedGuest(t, okReport(ev("evil.sh", effectoracle.KindCreate)))
	g.doc = func(d *TransientDoc) { d.Stop = TransientStopTimer }
	if got, err := runTransient(t, newTransient(g), ref); err != nil || got.Findings[OracleTransient] != FindingObserved {
		t.Errorf("observed effect in a short window: %v, %v", got.Findings, err)
	}
}

// The reference binds the report and the ledger the decision was made against.
func TestTransientCollectorReferenceFoldsInTheLedgerDigest(t *testing.T) {
	report := okReport()
	digest, err := report.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// No publication: the ledger is empty.
	doc, head, count := e04ptDoc(t, e04ptRun7(nil))
	g := newWatchGuest(report)
	g.e04 = doc
	none, err := runTransient(t, newTransient(g), EvidenceRef{Interval: e04ptInterval, RunID: e04ptRun, E04Head: head, E04Count: count})
	if err != nil {
		t.Fatal(err)
	}
	if want := foldDigest(digest, wsoracle.LedgerPrefixDigest(nil, 0)); none.Refs[OracleTransient].Report != want {
		t.Fatalf("ref without publication = %s, want %s", none.Refs[OracleTransient].Report, want)
	}
	// The same report judged against a published ledger carries another reference.
	records, ledger := e04ptPublishedRun("published")
	pdoc, phead, pcount := e04ptDoc(t, records)
	g = newWatchGuest(report)
	g.e04 = pdoc
	published, err := runTransient(t, newTransient(g), EvidenceRef{Interval: e04ptInterval, RunID: e04ptRun, E04Head: phead, E04Count: pcount})
	if err != nil {
		t.Fatal(err)
	}
	if want := foldDigest(digest, wsoracle.LedgerPrefixDigest(ledger, len(ledger))); published.Refs[OracleTransient].Report != want {
		t.Fatalf("ref with publication = %s, want %s", published.Refs[OracleTransient].Report, want)
	}
	if published.Refs[OracleTransient].Report == none.Refs[OracleTransient].Report {
		t.Fatal("the reference does not depend on the ledger")
	}
	// A ledger that cannot be had is bound as unknown.
	unknown, err := runTransient(t, newTransient(newWatchGuest(report)), EvidenceRef{Interval: e04ptInterval})
	if err != nil {
		t.Fatal(err)
	}
	if want := foldDigest(digest, "ledger-unknown"); unknown.Refs[OracleTransient].Report != want {
		t.Fatalf("ref without a ledger = %s, want %s", unknown.Refs[OracleTransient].Report, want)
	}
}

func TestTransientCollectorBeginNeedsTheGuestsAcknowledgement(t *testing.T) {
	ref := EvidenceRef{Interval: e04ptInterval}
	for name, g := range map[string]*watchGuest{
		"no acknowledgement":      {noReady: true, report: okReport()},
		"guest failure":           {fail: true},
		"acknowledges another":    {report: okReport(), ready: func(r *TransientReadyDoc) { r.IntervalRef = "other#1" }},
		"acknowledges wrong root": {report: okReport(), ready: func(r *TransientReadyDoc) { r.Root = "/tmp" }},
		"acknowledges wrong kind": {report: okReport(), ready: func(r *TransientReadyDoc) { r.Kind = "proc-monitor-ready" }},
	} {
		c := newTransient(g)
		c.ReadyTimeout = time.Second
		if err := c.BeginInterval(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: err = %v, want absent", name, err)
		}
		if _, err := c.Transient(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: Transient after a failed begin = %v, want absent", name, err)
		}
	}
}

func TestTransientCollectorADocumentNotForTheRequestIsAbsent(t *testing.T) {
	for name, mut := range map[string]func(*TransientDoc){
		"other interval": func(d *TransientDoc) { d.IntervalRef = "other#1" },
		"other root":     func(d *TransientDoc) { d.Root = "/tmp" },
		"other duration": func(d *TransientDoc) { d.DurationSeconds = 5 },
		"other kind":     func(d *TransientDoc) { d.Kind = "proc-monitor" },
	} {
		g := newWatchGuest(okReport())
		g.doc = mut
		c := newTransient(g)
		if err := c.BeginInterval(context.Background(), EvidenceRef{Interval: e04ptInterval}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := c.Transient(context.Background(), EvidenceRef{Interval: e04ptInterval}); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: err = %v, want absent", name, err)
		}
		c.EndInterval(EvidenceRef{Interval: e04ptInterval})
	}
}

func waitEnd(t *testing.T, g *watchGuest) string {
	t.Helper()
	select {
	case how := <-g.ends:
		return how
	case <-time.After(5 * time.Second):
		t.Fatal("the guest watch never ended")
		return ""
	}
}

// The host ends the watch: Transient writes the stop line, and the guest does not end before it.
func TestTransientCollectorTheHostEndsTheWatchWithTheStopLine(t *testing.T) {
	g := newWatchGuest(okReport())
	c := newTransient(g)
	ref := EvidenceRef{Interval: e04ptInterval}
	if err := c.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	select {
	case how := <-g.ends:
		t.Fatalf("the guest watch ended (%s) before the host asked", how)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := c.Transient(context.Background(), ref); err != nil {
		t.Fatalf("Transient: %v", err)
	}
	if how := waitEnd(t, g); how != "stop" {
		t.Fatalf("the guest watch ended by %q, want the stop line", how)
	}
	// Asking again returns the same answer and does not hang.
	if _, err := c.Transient(context.Background(), ref); err != nil {
		t.Fatalf("second Transient: %v", err)
	}
	c.EndInterval(ref)
}

// An interval that ends without a report (the request failed early) aborts the watch: the guest sees its stdin end
// with no stop line, so it writes no stop canary, and the stop is never sent afterwards.
func TestTransientCollectorEndIntervalWithoutAReportAbortsTheWatch(t *testing.T) {
	g := newWatchGuest(okReport())
	c := newTransient(g)
	ref := EvidenceRef{Interval: e04ptInterval}
	if err := c.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	c.EndInterval(ref)
	if how := waitEnd(t, g); how == "stop" {
		t.Fatal("EndInterval without a report asked the guest to stop and write its canary")
	}
	if _, err := c.Transient(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("Transient after EndInterval = %v, want absent", err)
	}
}

// A watch whose guest never reports after the stop request is given up on, as absent.
func TestTransientCollectorAStopThatIsNeverAnsweredIsAbsent(t *testing.T) {
	g := newWatchGuest(okReport())
	g.doc = func(*TransientDoc) { select {} } // the guest hangs while stopping
	c := newTransient(g)
	c.StopTimeout = 100 * time.Millisecond
	ref := EvidenceRef{Interval: e04ptInterval}
	if err := c.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	defer c.EndInterval(ref)
	if _, err := c.Transient(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("err = %v, want absent", err)
	}
}

func TestTransientCollectorValidate(t *testing.T) {
	g := newWatchGuest(okReport())
	for name, c := range map[string]*TransientCollector{
		"no duration":        {Client: EvidenceClient{Transport: g}},
		"too long":           {Client: EvidenceClient{Transport: g}, Duration: (MaxTransientWatchSeconds + 1) * time.Second},
		"no transport":       {Duration: time.Second},
		"cannot report":      {Client: EvidenceClient{Transport: plainWatchTransport{g}}, Duration: time.Second},
		"cannot end the run": {Client: EvidenceClient{Transport: readyOnlyTransport{g}}, Duration: time.Second},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (&TransientCollector{Client: EvidenceClient{Transport: g}, Duration: time.Second}).Validate(); err != nil {
		t.Errorf("valid configuration: %v", err)
	}
}

func TestCollectorSetRoutesPublicationAndTransientToTheirSlots(t *testing.T) {
	g, ref := publishedGuest(t, okReport(publicationEvents()...))
	tc := newTransient(g)
	ssh := NewSSHCollectors(g)
	set := CollectorSet{PublicationFrom: ssh, TransientFrom: tc, E04From: ssh}
	if err := set.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := set.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	defer set.EndInterval(ref)
	if _, err := set.Publication(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
		// the fixture's store carries no publication evidence: the slot answered, and it is absent
		t.Fatalf("publication: %v", err)
	}
	if got, err := set.Transient(context.Background(), ref); err != nil || got.Findings[OracleTransient] != FindingNone {
		t.Fatalf("transient: %v, %v", got.Findings, err)
	}
	if _, err := (CollectorSet{}).Transient(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("empty slot: %v", err)
	}
}

// The ledger operations decide which events publication makes. These are the events of a real publish
// (internal/publication, TestPublishRenamesAreTheOnlyLiveRootEvents).
func TestJudgeTransientIsAPureFunctionOfReportAndLedger(t *testing.T) {
	before, after := wsoracle.Entry{Path: "x", Type: wsoracle.TypeFile}, wsoracle.Entry{Path: "x", Type: wsoracle.TypeFile, Size: 1}
	ledger := []wsoracle.LedgerEntry{{ID: "p/0", Sequence: 1, Changes: []wsoracle.LedgerChange{
		{Path: "created.txt", After: &after},
		{Path: "over.txt", Before: &before, After: &after},
		{Path: "gone.txt", Before: &before},
		{Path: "d/b", After: &after},
	}}}
	real := []effectoracle.Event{ev("created.txt", "moved_to"), ev("gone.txt", "moved_from"), ev("over.txt", "moved_to"),
		ev("over.txt", "moved_from"), ev("d/b", "moved_to")}
	without := func(i int) []effectoracle.Event {
		return append(append([]effectoracle.Event(nil), real[:i]...), real[i+1:]...)
	}
	for name, tc := range map[string]struct {
		rep    effectoracle.Report
		ledger []wsoracle.LedgerEntry
		known  bool
		want   Finding
	}{
		"the events of a real publication": {okReport(real...), ledger, true, FindingNone},
		"a displaced old file is optional": {okReport(without(3)...), ledger, true, FindingNone},
		"created file written in place":    {okReport(append(real, ev("created.txt", "close_write"))...), ledger, true, FindingObserved},
		"created file with a create event": {okReport(append(real, ev("created.txt", "create"))...), ledger, true, FindingObserved},
		"deleted file unlinked":            {okReport(append(real, ev("gone.txt", "delete"))...), ledger, true, FindingObserved},
		"deleted file moved in":            {okReport(append(real, ev("gone.txt", "moved_to"))...), ledger, true, FindingObserved},
		"created file moved away":          {okReport(append(real, ev("created.txt", "moved_from"))...), ledger, true, FindingObserved},
		"overwritten file modified":        {okReport(append(real, ev("over.txt", "modify"))...), ledger, true, FindingObserved},
		"overwritten file attrib":          {okReport(append(real, ev("over.txt", "attrib"))...), ledger, true, FindingObserved},
		"a path no ledger entry names":     {okReport(append(real, ev("c", "moved_to"))...), ledger, true, FindingObserved},
		"created file not seen":            {okReport(without(0)...), ledger, true, FindingLost},
		"deleted file not seen":            {okReport(without(1)...), ledger, true, FindingLost},
		"overwrite not seen":               {okReport(without(2)...), ledger, true, FindingLost},
		"unqualified window":               {func() effectoracle.Report { r := okReport(real...); r.Marks = nil; return r }(), ledger, true, FindingLost},
		"quiet and no ledger":              {okReport(), nil, true, FindingNone},
		"quiet and unqualified, no ledger": {effectoracle.Report{Verdict: effectoracle.VerdictOK}, nil, true, FindingLost},
		"unknown ledger":                   {okReport(), nil, false, FindingLost},
		"unknown ledger and event":         {okReport(ev("a", "create")), nil, false, FindingLost},
	} {
		if got := judgeTransient(&tc.rep, tc.ledger, tc.known); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}
