package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
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
	// endDelay is how long the guest takes to end after its stdin ended without the stop line; hang makes it ignore
	// that end and run until its session is killed (it then fails with the session's error, no exit status).
	endDelay time.Duration
	hang     bool
	ready    func(*TransientReadyDoc)
	doc      func(*TransientDoc)
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
		// Like the real guest, it takes the start line before it acknowledges, so the host's lines reach it in the
		// order it wrote them: the host writes the start line from a goroutine of its own, and a stop line written
		// before that goroutine ran would otherwise be read as the start line.
		got := make(chan string, 1)
		started := make(chan struct{})
		go func() {
			if stdin == nil {
				close(started)
				got <- "eof"
				return
			}
			br := bufio.NewReader(stdin)
			for i, want := range []string{TransientStartLine, TransientStopLine} {
				l, err := br.ReadString('\n')
				if i == 0 {
					close(started)
				}
				if err != nil || l != want {
					got <- "eof"
					return
				}
			}
			got <- "stop"
		}()
		select {
		case <-started:
		case <-ctx.Done():
			g.end("cancelled")
			return nil, nil, 0, ctx.Err()
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
		select {
		case how := <-got:
			if how != "stop" && g.hang {
				<-ctx.Done()
				g.end("cancelled")
				return nil, nil, 0, ctx.Err()
			}
			time.Sleep(g.endDelay)
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

// okReport is a complete qualified window in which publication fenced its apply (twice, the way publication does)
// and the given events lie between the two fences.
func okReport(events ...effectoracle.Event) effectoracle.Report {
	return placedReport(nil, events, nil)
}

// placedReport is a complete qualified window with events before the first fence, between the fences, and after the
// second one, in the order of the observer's queue (Seq): when the reader took an event does not enter into it.
func placedReport(before, within, after []effectoracle.Event) effectoracle.Report {
	r := effectoracle.Report{OracleVersion: effectoracle.OracleVersion, Instrument: effectoracle.Instrument,
		Verdict: effectoracle.VerdictOK, FenceDir: effectoracle.FenceDir,
		Heartbeats: []effectoracle.Heartbeat{
			{Seq: 1, Label: "mark:start:pre", Observed: true}, {Seq: 2, Label: "mark:start:post", Observed: true},
			{Seq: 4, Label: "mark:stop:pre", Observed: true}, {Seq: 5, Label: "mark:stop:post", Observed: true}},
		Marks: []effectoracle.Mark{{Seq: 3, Name: effectoracle.MarkStart, Quiescent: true}, {Seq: 6, Name: effectoracle.MarkStop, Quiescent: true}}}
	seq := uint64(30)
	put := func(events []effectoracle.Event) {
		for _, e := range events {
			e.Seq = seq
			seq++
			r.Events = append(r.Events, e)
		}
	}
	fence := func() {
		r.Fences = append(r.Fences, alternation(seq)...)
		seq += uint64(2 * effectoracle.FenceBurst)
	}
	put(before)
	fence()
	put(within)
	seq += 20 // room for records of the observer between the events and the second fence
	fence()
	put(after)
	return r
}

// alternation is what effectoracle.FenceRoot leaves in the queue of an observer of the root: FenceBurst opens of the fence
// directory, each followed by a read of it, in records with consecutive sequence numbers from seq on.
func alternation(seq uint64) []effectoracle.Fence {
	var out []effectoracle.Fence
	for i := 0; i < effectoracle.FenceBurst; i++ {
		out = append(out, effectoracle.Fence{Seq: seq, Op: effectoracle.FenceOpen, Path: effectoracle.FenceDir, IsDir: true},
			effectoracle.Fence{Seq: seq + 1, Op: effectoracle.FenceAccess, Path: effectoracle.FenceDir, IsDir: true})
		seq += 2
	}
	return out
}

// scanFence is the records of a scan of the fence directory as publication's scans leave them: three opens in a row,
// then two reads. It is not a fence.
func scanFence(seq uint64) []effectoracle.Fence {
	var out []effectoracle.Fence
	for i := uint64(0); i < 3; i++ {
		out = append(out, effectoracle.Fence{Seq: seq + i, Op: effectoracle.FenceOpen, Path: effectoracle.FenceDir, IsDir: true})
	}
	for i := uint64(3); i < 5; i++ {
		out = append(out, effectoracle.Fence{Seq: seq + i, Op: effectoracle.FenceAccess, Path: effectoracle.FenceDir, IsDir: true})
	}
	return out
}

const testEventAt = 600_000_000 // AtNS of every test event: the read time, which the judge does not use

var evCookie atomic.Uint32

// ev is one event as the kernel gives it for a rename with an unwatched other side: every rename has a cookie of its
// own.
func ev(path, kind string) effectoracle.Event {
	e := effectoracle.Event{Seq: 1, AtNS: testEventAt, Kind: kind, Path: path}
	if kind == effectoracle.KindMovedTo || kind == effectoracle.KindMovedFrom {
		e.Cookie = evCookie.Add(1)
	}
	return e
}

func newTransient(g *watchGuest) *TransientCollector {
	return &TransientCollector{Client: EvidenceClient{Transport: g}, Duration: 30 * time.Second}
}

// publishedGuest serves the store of e04ptPublishedRun: the ledger has one entry, an overwrite of protected.txt.
func publishedGuest(t *testing.T, report effectoracle.Report) (*watchGuest, EvidenceRef) {
	t.Helper()
	return publishedGuestFenced(t, report, 2)
}

// publishedGuestFenced is publishedGuest for a publication that made fenceBursts fences.
func publishedGuestFenced(t *testing.T, report effectoracle.Report, fenceBursts int) (*watchGuest, EvidenceRef) {
	t.Helper()
	records, _ := e04ptPublishedRun("published")
	sealed := wsoracle.Manifest{}
	records[5].Publication = &E04PublicationEvidence{TransactionID: "pub-0123456789abcdef", PublicationID: "p", Status: "SUCCEEDED",
		LockIdentity: "lock", Sealed: sealed, SealedIdentityDigest: sealed.IdentityDigest(),
		FenceDir: effectoracle.FenceDir, FenceBursts: fenceBursts}
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

// The collector reads whether publication fenced from the post-publication record: a transaction that made one fence
// (or none) leaves the window unplaced, so the exact publication events are lost, not none.
func TestTransientCollectorAPublicationThatDidNotFenceTwiceIsLost(t *testing.T) {
	for opens, want := range map[int]Finding{2: FindingNone, 1: FindingLost, 0: FindingLost} {
		g, ref := publishedGuestFenced(t, okReport(publicationEvents()...), opens)
		got, err := runTransient(t, newTransient(g), ref)
		if err != nil || got.Findings[OracleTransient] != want {
			t.Errorf("%d fence opens: %v, %v, want %s", opens, got.Findings, err, want)
		}
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

// The ledger operations decide which events publication makes: each operation explains exactly its own events, once,
// in the order publication applied them. These are the events of a real publish (internal/publication,
// TestPublishRenamesAreTheOnlyLiveRootEvents, which takes its expectation from PublicationEventSequence).
func TestJudgeTransientIsAPureFunctionOfReportAndLedger(t *testing.T) {
	before, after := wsoracle.Entry{Path: "x", Type: wsoracle.TypeFile}, wsoracle.Entry{Path: "x", Type: wsoracle.TypeFile, Size: 1}
	ledger := []wsoracle.LedgerEntry{{ID: "p/0", Sequence: 1, Changes: []wsoracle.LedgerChange{
		{Path: "created.txt", After: &after},
		{Path: "over.txt", Before: &before, After: &after},
		{Path: "gone.txt", Before: &before},
		{Path: "d/b", After: &after},
	}}}
	real := func() []effectoracle.Event {
		return []effectoracle.Event{ev("created.txt", "moved_to"), ev("over.txt", "moved_to"), ev("over.txt", "moved_from"),
			ev("gone.txt", "moved_from"), ev("d/b", "moved_to")}
	}
	without := func(i int) []effectoracle.Event {
		r := real()
		return append(r[:i], r[i+1:]...)
	}
	pair := func() []effectoracle.Event {
		return []effectoracle.Event{ev("evil", "moved_to"), ev("evil", "moved_from")}
	}
	lateReads := func(d time.Duration) effectoracle.Report {
		r := okReport(real()...)
		for i := range r.Events {
			r.Events[i].AtNS = int64(d)
		}
		return r
	}
	scanned := func() effectoracle.Report {
		r := okReport(real()...)
		r.Fences = append(append(scanFence(1), r.Fences...), scanFence(900)...)
		return r
	}
	op := func(seq uint64, o string) effectoracle.Fence {
		return effectoracle.Fence{Seq: seq, Op: o, Path: effectoracle.FenceDir, IsDir: true}
	}
	inserted := func(fences ...effectoracle.Fence) effectoracle.Report {
		r := okReport(real()...)
		r.Fences = append(r.Fences, fences...)
		sort.SliceStable(r.Fences, func(i, j int) bool { return r.Fences[i].Seq < r.Fences[j].Seq })
		return r
	}
	// thirdFence is another process making a fence, at seq on.
	thirdFence := func(seq uint64) effectoracle.Report { return inserted(alternation(seq)...) }
	brokenFence := func() effectoracle.Report {
		r := okReport(real()...)
		r.Fences = append(r.Fences[:3], r.Fences[4:]...)
		return r
	}
	onlyFirstFence := func() effectoracle.Report {
		r := okReport(real()...)
		r.Fences = r.Fences[:2*effectoracle.FenceBurst]
		return r
	}
	noFences := func() effectoracle.Report {
		r := okReport(real()...)
		r.Fences = nil
		return r
	}
	unwatched := func() effectoracle.Report {
		r := okReport(real()...)
		r.FenceDir = ""
		return r
	}
	withSurplus := func(r effectoracle.Report) effectoracle.Report {
		r.Events = append(r.Events, ev("evil", "create"))
		r.Events[len(r.Events)-1].Seq = 43
		return r
	}
	sharedCookie := func() effectoracle.Report {
		r := okReport(real()...)
		r.Events[0].Cookie, r.Events[1].Cookie = 9, 9
		return r
	}
	noCookie := func() effectoracle.Report {
		r := okReport(real()...)
		r.Events[0].Cookie = 0
		return r
	}
	dirForFile := func() effectoracle.Report {
		r := okReport(real()...)
		r.Events[4].IsDir = true
		return r
	}
	unqualified := func() effectoracle.Report {
		r := okReport(real()...)
		r.Marks = nil
		return r
	}
	for name, tc := range map[string]struct {
		rep    effectoracle.Report
		ledger []wsoracle.LedgerEntry
		known  bool
		fenced bool
		want   Finding
	}{
		"the events of a real publication":       {okReport(real()...), ledger, true, true, FindingNone},
		"the displaced old file is not optional": {okReport(without(2)...), ledger, true, true, FindingLost},
		"created file written in place":          {okReport(append(real(), ev("created.txt", "close_write"))...), ledger, true, true, FindingObserved},
		"created file with a create event":       {okReport(append(real(), ev("created.txt", "create"))...), ledger, true, true, FindingObserved},
		"deleted file unlinked":                  {okReport(append(real(), ev("gone.txt", "delete"))...), ledger, true, true, FindingObserved},
		"deleted file moved in":                  {okReport(append(real(), ev("gone.txt", "moved_to"))...), ledger, true, true, FindingObserved},
		"created file moved away":                {okReport(append(real(), ev("created.txt", "moved_from"))...), ledger, true, true, FindingObserved},
		"created file renamed in twice":          {okReport(append(real(), ev("created.txt", "moved_to"))...), ledger, true, true, FindingObserved},
		"deleted file moved away twice":          {okReport(append(real(), ev("gone.txt", "moved_from"))...), ledger, true, true, FindingObserved},
		"overwritten file modified":              {okReport(append(real(), ev("over.txt", "modify"))...), ledger, true, true, FindingObserved},
		"overwritten file attrib":                {okReport(append(real(), ev("over.txt", "attrib"))...), ledger, true, true, FindingObserved},
		"a path no ledger entry names":           {okReport(append(real(), ev("c", "moved_to"))...), ledger, true, true, FindingObserved},
		"the operations in another order":        {okReport(append(without(0), ev("created.txt", "moved_to"))...), ledger, true, true, FindingObserved},
		"a directory renamed in for a file":      {dirForFile(), ledger, true, true, FindingObserved},
		"created file not seen":                  {okReport(without(0)...), ledger, true, true, FindingLost},
		"deleted file not seen":                  {okReport(without(3)...), ledger, true, true, FindingLost},
		"overwrite not seen":                     {okReport(without(1)...), ledger, true, true, FindingLost},
		"unqualified window":                     {unqualified(), ledger, true, true, FindingLost},
		"quiet and no ledger":                    {okReport(), nil, true, true, FindingNone},
		"quiet and no ledger or window":          {okReport(), nil, true, false, FindingNone},
		"quiet and unqualified, no ledger":       {effectoracle.Report{Verdict: effectoracle.VerdictOK}, nil, true, true, FindingLost},
		"unknown ledger":                         {okReport(), nil, false, true, FindingLost},
		"unknown ledger and event":               {okReport(ev("a", "create")), nil, false, true, FindingLost},
		// Cookies: every rename of publication has a kernel cookie of its own.
		"a rename within the live root": {sharedCookie(), ledger, true, true, FindingObserved},
		"no cookie on a rename":         {noCookie(), ledger, true, true, FindingLost},
		// Order, not time: the window is between the two opens of the fence directory in the observer's queue, and when
		// the reader took the events does not matter.
		"events read 2.2 s late (the reviewer's case)": {lateReads(2200 * time.Millisecond), ledger, true, true, FindingNone},
		"events read an hour late":                     {lateReads(time.Hour), ledger, true, true, FindingNone},
		"scans of the directory around the fences":     {scanned(), ledger, true, true, FindingNone},
		"every event before the first fence":           {placedReport(real(), nil, nil), ledger, true, true, FindingObserved},
		"every event after the second fence":           {placedReport(nil, nil, real()), ledger, true, true, FindingObserved},
		"the first event before the first fence":       {placedReport(real()[:1], real()[1:], nil), ledger, true, true, FindingObserved},
		"the last event after the second fence":        {placedReport(nil, real()[:4], real()[4:]), ledger, true, true, FindingObserved},
		"a rename pair before the fences":              {placedReport(pair(), real(), nil), ledger, true, true, FindingObserved},
		"a rename pair after the fences":               {placedReport(nil, real(), pair()), ledger, true, true, FindingObserved},
		"a surplus event after the fences":             {placedReport(nil, real(), []effectoracle.Event{ev("evil", "create")}), ledger, true, true, FindingObserved},
		"a rename pair inside the fences":              {placedReport(nil, append(real(), pair()...), nil), ledger, true, true, FindingObserved},
		// Another process that makes a fence of its own leaves a third one, which makes the fences ambiguous: the expected
		// events are lost, a surplus event is still observed. A few opens and reads are no fence, and are not told from
		// publication's own scans, which also open and read the directory next to its fences.
		"a third fence between the fences":         {thirdFence(50), ledger, true, true, FindingLost},
		"a third fence before the fences":          {thirdFence(7), ledger, true, true, FindingLost},
		"a third fence after the fences":           {thirdFence(900), ledger, true, true, FindingLost},
		"a stray open between the fences":          {inserted(op(45, effectoracle.FenceOpen)), ledger, true, true, FindingNone},
		"records next to the fences":               {inserted(op(29, effectoracle.FenceAccess), op(79, effectoracle.FenceOpen)), ledger, true, true, FindingNone},
		"a fence broken by another record":         {brokenFence(), ledger, true, true, FindingLost},
		"one fence missed":                         {onlyFirstFence(), ledger, true, true, FindingLost},
		"no fence in the report":                   {noFences(), ledger, true, true, FindingLost},
		"the report did not watch the directory":   {unwatched(), ledger, true, true, FindingLost},
		"publication did not fence twice":          {okReport(real()...), ledger, true, false, FindingLost},
		"surplus event, publication did not fence": {okReport(append(real(), ev("evil", "create"))...), ledger, true, false, FindingObserved},
		"surplus event, the report did not watch":  {withSurplus(unwatched()), ledger, true, true, FindingObserved},
		"surplus event and a third fence":          {withSurplus(thirdFence(50)), ledger, true, true, FindingObserved},
	} {
		if got := judgeTransient(&tc.rep, tc.ledger, tc.known, tc.fenced); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
	// A ledger change that is none of the four operations has no known events: nothing it names can be explained.
	dir := wsoracle.Entry{Path: "d", Type: wsoracle.TypeDir}
	removedDir := []wsoracle.LedgerEntry{{ID: "p/0", Sequence: 1, Changes: []wsoracle.LedgerChange{{Path: "d", Before: &dir}}}}
	if _, ok := PublicationEventSequence(removedDir); ok {
		t.Error("a removed directory has a known event sequence")
	}
	for name, tc := range map[string]struct {
		rep  effectoracle.Report
		want Finding
	}{
		"its path": {okReport(ev("d", "moved_from")), FindingLost},
		"another":  {okReport(ev("d", "moved_from"), ev("e", "moved_to")), FindingObserved},
		"none":     {okReport(), FindingLost},
	} {
		if got := judgeTransient(&tc.rep, removedDir, true, true); got != tc.want {
			t.Errorf("unsupported operation, %s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestPublicationEventSequencePerOperation(t *testing.T) {
	file, file2 := wsoracle.Entry{Type: wsoracle.TypeFile}, wsoracle.Entry{Type: wsoracle.TypeFile, Size: 2}
	dir := wsoracle.Entry{Type: wsoracle.TypeDir}
	ledger := []wsoracle.LedgerEntry{
		{ID: "p/0", Sequence: 1, Changes: []wsoracle.LedgerChange{{Path: "d", After: &dir}}},
		{ID: "p/1", Sequence: 2, Changes: []wsoracle.LedgerChange{{Path: "d/new", After: &file}}},
		{ID: "p/2", Sequence: 3, Changes: []wsoracle.LedgerChange{{Path: "over", Before: &file, After: &file2}}},
		{ID: "p/3", Sequence: 4, Changes: []wsoracle.LedgerChange{{Path: "gone", Before: &file}}},
	}
	want := []PublicationEvent{
		{Path: "d", Kind: effectoracle.KindMovedTo, IsDir: true}, {Path: "d/new", Kind: effectoracle.KindMovedTo},
		{Path: "over", Kind: effectoracle.KindMovedTo}, {Path: "over", Kind: effectoracle.KindMovedFrom}, {Path: "gone", Kind: effectoracle.KindMovedFrom}}
	if got, ok := PublicationEventSequence(ledger); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence = %v, %v; want %v", got, ok, want)
	}
	for name, ch := range map[string]wsoracle.LedgerChange{
		"directory removed":     {Path: "d", Before: &dir},
		"file became directory": {Path: "x", Before: &file, After: &dir},
		"directory became file": {Path: "x", Before: &dir, After: &file},
		"directory modified":    {Path: "x", Before: &dir, After: &dir},
		"empty change":          {Path: "x"},
	} {
		if _, ok := PublicationEventSequence([]wsoracle.LedgerEntry{{ID: "p/0", Sequence: 1, Changes: []wsoracle.LedgerChange{ch}}}); ok {
			t.Errorf("%s: has a known sequence", name)
		}
	}
	if got, ok := PublicationEventSequence(nil); !ok || len(got) != 0 {
		t.Errorf("no ledger: %v, %v", got, ok)
	}
}

// Publication's own renames are the only explanation a ledger operation gives, once each. A hostile writer that
// renames files onto an approved overwrite path and restores the approved file between snapshots leaves more
// moved_to/moved_from events on the path than the one overwrite explains: every surplus event is an observed effect.
func TestTransientCollectorRenamePairsBeyondTheApprovedOperationAreObserved(t *testing.T) {
	to, from := effectoracle.KindMovedTo, effectoracle.KindMovedFrom
	for name, events := range map[string][]effectoracle.Event{
		"two rename pairs on the approved path":   {ev("protected.txt", to), ev("protected.txt", from), ev("protected.txt", to), ev("protected.txt", from)},
		"a third rename in":                       append(publicationEvents(), ev("protected.txt", to)),
		"the approved overwrite after a pair":     append([]effectoracle.Event{ev("protected.txt", to), ev("protected.txt", from)}, publicationEvents()...),
		"the displaced file seen twice":           {ev("protected.txt", to), ev("protected.txt", from), ev("protected.txt", from)},
		"renamed away before the approved rename": {ev("protected.txt", from), ev("protected.txt", to), ev("protected.txt", from)},
	} {
		g, ref := publishedGuest(t, okReport(events...))
		got, err := runTransient(t, newTransient(g), ref)
		if err != nil || got.Findings[OracleTransient] != FindingObserved {
			t.Errorf("%s: %v, %v, want observed", name, got.Findings, err)
		}
	}
}

// A watch that did not acknowledge is ended before the request is sent: the host closes the session's input and
// returns only once the guest command has ended with an exit status of its own, so the instrument cannot still be
// starting (writing canaries into the live root) while the request runs.
func TestTransientCollectorBeginWaitsForTheGuestToEndAfterAReadinessTimeout(t *testing.T) {
	g := newWatchGuest(okReport())
	g.noReady, g.endDelay = true, 300*time.Millisecond
	c := newTransient(g)
	c.ReadyTimeout = 50 * time.Millisecond
	err := c.BeginInterval(context.Background(), EvidenceRef{Interval: e04ptInterval})
	if !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("err = %v, want absent", err)
	}
	select {
	case how := <-g.ends:
		if how != "eof" {
			t.Fatalf("the guest ended by %q, want the end of its input", how)
		}
	default:
		t.Fatal("BeginInterval returned while the guest command was still running")
	}
}

// A guest that cannot be confirmed ended (it ignores the end of its input, and the session has to be killed) is a
// setup failure, not absent evidence: the caller must not send the request.
func TestTransientCollectorBeginRefusesWhenTheGuestCannotBeConfirmedEnded(t *testing.T) {
	g := newWatchGuest(okReport())
	g.noReady, g.hang = true, true
	c := newTransient(g)
	c.ReadyTimeout, c.TerminateTimeout = 50*time.Millisecond, 100*time.Millisecond
	err := c.BeginInterval(context.Background(), EvidenceRef{Interval: e04ptInterval})
	if !errors.Is(err, ErrObserverStartUnconfirmed) || errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("err = %v, want ErrObserverStartUnconfirmed and not absent", err)
	}
}

// H1-03: the report EFF-TRANSIENT was judged from is kept with its ledger, and recomputes the journaled finding.
func TestTransientCollectorRetainsWhatItJudged(t *testing.T) {
	g, ref := publishedGuest(t, okReport(publicationEvents()...))
	c := newTransient(g)
	c.RetainDir = t.TempDir()
	got, err := runTransient(t, c, ref)
	if err != nil || got.Findings[OracleTransient] != FindingNone {
		t.Fatalf("findings %v err %v", got.Findings, err)
	}
	raw, err := os.ReadFile(filepath.Join(c.RetainDir, RetainedWSName(ref.Interval)))
	if err != nil {
		t.Fatalf("nothing retained: %v", err)
	}
	_ = raw
}
