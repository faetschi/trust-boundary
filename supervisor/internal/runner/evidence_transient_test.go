package runner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/wsoracle"
)

// watchGuest answers transient-watch the way the guest does (readiness line, then the document when the window is
// over) and e04-points from a store.
type watchGuest struct {
	mu      sync.Mutex
	calls   [][]string
	report  effectoracle.Report
	e04     E04PointsDoc
	hold    chan struct{} // the document is withheld until this closes (nil: at once)
	noReady bool
	fail    bool
	ready   func(*TransientReadyDoc)
	doc     func(*TransientDoc)
}

func (g *watchGuest) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return g.RunReady(ctx, argv, nil)
}

func (g *watchGuest) argvs() [][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([][]string(nil), g.calls...)
}

func (g *watchGuest) RunReady(ctx context.Context, argv []string, ready func([]byte)) ([]byte, []byte, int, error) {
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
		if g.hold != nil {
			select {
			case <-g.hold:
			case <-ctx.Done():
				return nil, nil, 0, ctx.Err()
			}
		}
		d := TransientDoc{Schema: EvidenceSchema, Kind: EvidenceTransientWatch, IntervalRef: argv[2], Root: EvidenceLiveRoot,
			DurationSeconds: 30, Report: g.report}
		if g.doc != nil {
			g.doc(&d)
		}
		return append(first, line(d)...), nil, 0, nil
	}
	return nil, []byte("unknown"), 2, nil
}

type plainWatchTransport struct{ g *watchGuest }

func (p plainWatchTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return p.g.Run(ctx, argv)
}

func okReport(events ...effectoracle.Event) effectoracle.Report {
	return effectoracle.Report{OracleVersion: effectoracle.OracleVersion, Instrument: effectoracle.Instrument,
		Verdict: effectoracle.VerdictOK, Events: events}
}

func ev(path, kind string) effectoracle.Event {
	return effectoracle.Event{Seq: 1, Kind: kind, Path: path}
}

func newTransient(g *watchGuest) *TransientCollector {
	return &TransientCollector{Client: EvidenceClient{Transport: g}, Duration: 30 * time.Second}
}

// publishedRef is the reference of the published run of e04ptPublishedRun: the store binds one ledger entry for protected.txt.
func publishedGuest(t *testing.T, report effectoracle.Report) (*watchGuest, EvidenceRef) {
	t.Helper()
	records, _ := e04ptPublishedRun("published")
	doc, head, count := e04ptDoc(t, records)
	return &watchGuest{report: report, e04: doc}, EvidenceRef{Interval: e04ptInterval, E04Head: head, E04Count: count}
}

func runTransient(t *testing.T, c *TransientCollector, ref EvidenceRef) (ObserverEvidence, error) {
	t.Helper()
	if err := c.BeginInterval(context.Background(), ref); err != nil {
		t.Fatalf("BeginInterval: %v", err)
	}
	defer c.EndInterval(ref)
	return c.Transient(context.Background(), ref)
}

func TestTransientCollectorPublishedIntervalWithExactlyTheLedgerWritesIsNone(t *testing.T) {
	g, ref := publishedGuest(t, okReport(ev("protected.txt", effectoracle.KindCreate), ev("protected.txt", effectoracle.KindCloseWrite)))
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
	g := &watchGuest{report: okReport(), e04: doc}
	got, err := runTransient(t, newTransient(g), EvidenceRef{Interval: e04ptInterval, E04Head: head, E04Count: count})
	if err != nil || got.Findings[OracleTransient] != FindingNone {
		t.Fatalf("%v, %v", got.Findings, err)
	}
}

// A write to the live root that publication did not apply is an effect, even if it was undone before any
// snapshot, and even if the report is otherwise not qualified.
func TestTransientCollectorAnEventTheLedgerDoesNotNameIsObserved(t *testing.T) {
	for name, report := range map[string]effectoracle.Report{
		"created and deleted": okReport(ev("protected.txt", effectoracle.KindCloseWrite), ev("tmp.bin", effectoracle.KindCreate), ev("tmp.bin", effectoracle.KindDelete)),
		"report with a gap": func() effectoracle.Report {
			r := okReport(ev("protected.txt", effectoracle.KindCloseWrite), ev("evil.sh", effectoracle.KindCreate))
			r.Verdict = effectoracle.VerdictEvidenceLost
			return r
		}(),
	} {
		g, ref := publishedGuest(t, report)
		got, err := runTransient(t, newTransient(g), ref)
		if err != nil || got.Findings[OracleTransient] != FindingObserved {
			t.Errorf("%s: %v, %v", name, got.Findings, err)
		}
	}
	// With no publication at all every event is unexplained.
	doc, head, count := e04ptDoc(t, e04ptRun7(nil))
	g := &watchGuest{report: okReport(ev("a.txt", effectoracle.KindModify)), e04: doc}
	got, err := runTransient(t, newTransient(g), EvidenceRef{Interval: e04ptInterval, E04Head: head, E04Count: count})
	if err != nil || got.Findings[OracleTransient] != FindingObserved {
		t.Errorf("unpublished: %v, %v", got.Findings, err)
	}
}

// Whatever the instrument could not establish is lost, never none.
func TestTransientCollectorLostEvidenceIsNeverNone(t *testing.T) {
	writes := []effectoracle.Event{ev("protected.txt", effectoracle.KindCloseWrite)}
	t.Run("ledger change the instrument did not see", func(t *testing.T) {
		g, ref := publishedGuest(t, okReport())
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
		g = &watchGuest{report: okReport(), e04: doc}
		if got, err := runTransient(t, newTransient(g), EvidenceRef{Interval: e04ptInterval, E04Head: head, E04Count: count}); err != nil || got.Findings[OracleTransient] != FindingLost {
			t.Errorf("unavailable ledger: %v, %v", got.Findings, err)
		}
	})
	t.Run("the window was over before the request", func(t *testing.T) {
		g, ref := publishedGuest(t, okReport(writes...))
		now := time.Unix(1_800_000_000, 0)
		c := newTransient(g)
		c.Now = func() time.Time { return now }
		ref.Ended = now.Add(31 * time.Second)
		if got, err := runTransient(t, c, ref); err != nil || got.Findings[OracleTransient] != FindingLost {
			t.Fatalf("%v, %v", got.Findings, err)
		}
		ref.Ended = now.Add(30 * time.Second)
		if got, err := runTransient(t, c, ref); err != nil || got.Findings[OracleTransient] != FindingNone {
			t.Fatalf("a request that ended inside the window: %v, %v", got.Findings, err)
		}
	})
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
		g := &watchGuest{report: okReport(), doc: mut}
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

func TestTransientCollectorWaitsForTheWindowAndEndIntervalStopsTheWatch(t *testing.T) {
	g := &watchGuest{report: okReport(), hold: make(chan struct{})}
	c := newTransient(g)
	ref := EvidenceRef{Interval: e04ptInterval}
	if err := c.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Transient(context.Background(), ref); done <- err }()
	select {
	case <-done:
		t.Fatal("Transient returned before the guest's window was over")
	case <-time.After(50 * time.Millisecond):
	}
	c.EndInterval(ref) // cancels the watch's context: the held guest call returns
	select {
	case err := <-done:
		if !errors.Is(err, ErrEvidenceAbsent) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EndInterval did not stop the watch")
	}
}

func TestTransientCollectorValidate(t *testing.T) {
	g := &watchGuest{}
	for name, c := range map[string]*TransientCollector{
		"no duration":   {Client: EvidenceClient{Transport: g}},
		"too long":      {Client: EvidenceClient{Transport: g}, Duration: (MaxTransientWatchSeconds + 1) * time.Second},
		"no transport":  {Duration: time.Second},
		"cannot report": {Client: EvidenceClient{Transport: plainWatchTransport{g}}, Duration: time.Second},
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
	g, ref := publishedGuest(t, okReport(ev("protected.txt", effectoracle.KindCloseWrite)))
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

func TestJudgeTransientIsAPureFunctionOfReportAndLedger(t *testing.T) {
	ledger := []wsoracle.LedgerEntry{{ID: "p/0", Sequence: 1, Changes: []wsoracle.LedgerChange{{Path: "a"}, {Path: "d/b"}}}}
	for name, tc := range map[string]struct {
		rep    effectoracle.Report
		ledger []wsoracle.LedgerEntry
		known  bool
		want   Finding
	}{
		"explained":                {okReport(ev("a", "create"), ev("d/b", "moved_to")), ledger, true, FindingNone},
		"unexplained":              {okReport(ev("a", "create"), ev("d/b", "create"), ev("c", "create")), ledger, true, FindingObserved},
		"unseen":                   {okReport(ev("a", "create")), ledger, true, FindingLost},
		"quiet and no ledger":      {okReport(), nil, true, FindingNone},
		"unknown ledger":           {okReport(), nil, false, FindingLost},
		"unknown ledger and event": {okReport(ev("a", "create")), nil, false, FindingLost},
	} {
		if got := judgeTransient(&tc.rep, tc.ledger, tc.known); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}
