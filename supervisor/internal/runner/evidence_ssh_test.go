package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/wsoracle"
)

// fakeEvidenceTransport is the evidence channel with a scripted guest. Every call is
// recorded; handler answers it.
type fakeEvidenceTransport struct {
	mu      sync.Mutex
	calls   [][]string
	handler func(argv []string) (stdout []byte, exit int, err error)
}

func (f *fakeEvidenceTransport) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), argv...))
	h := f.handler
	f.mu.Unlock()
	out, exit, err := h(argv)
	return out, []byte("guest stderr"), exit, err
}

func (f *fakeEvidenceTransport) argvs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

// e04PointsGuest answers e04-points for the interval of doc; mutate edits the document first.
func e04PointsGuest(doc E04PointsDoc, mutate func(*E04PointsDoc)) func([]string) ([]byte, int, error) {
	return func(argv []string) ([]byte, int, error) {
		if len(argv) != 3 || argv[0] != EvidenceE04Points || argv[1] != "--interval-ref" || argv[2] != doc.IntervalRef {
			return nil, 2, nil
		}
		d := doc
		if mutate != nil {
			mutate(&d)
		}
		b, _ := json.Marshal(d)
		return b, 0, nil
	}
}

func e04BoundRef(doc E04PointsDoc) EvidenceRef {
	return EvidenceRef{Interval: doc.IntervalRef, RunID: doc.RunID, E04Head: doc.Head, E04Count: doc.Count}
}

// The collector asks the guest for the service's own manifests of the seven lifecycle points
// and checks them against the receipt binding. It takes no baseline snapshot of its own.
func TestSSHCollectorsE04ServesTheServiceStoreTheReceiptBinds(t *testing.T) {
	doc, _, _ := e04ptDoc(t, e04ptRun7(nil))
	ft := &fakeEvidenceTransport{handler: e04PointsGuest(doc, nil)}
	c := NewSSHCollectors(ft)
	if _, ok := any(c).(IntervalCollectors); ok {
		t.Fatal("SSHCollectors still takes interval snapshots")
	}
	in, err := c.E04(context.Background(), e04BoundRef(doc))
	if err != nil {
		t.Fatal(err)
	}
	if calls := ft.argvs(); len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"e04-points", "--interval-ref", e04ptInterval}) {
		t.Fatalf("calls = %q", calls)
	}
	if in.Baseline.Label != wsoracle.PointBaseline || len(in.Points) != 6 || !in.Scope.Whole {
		t.Fatalf("baseline=%q points=%d scope=%+v, want the baseline and six more points", in.Baseline.Label, len(in.Points), in.Scope)
	}
	names := []string{in.Baseline.Label}
	for _, p := range in.Points {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, E04PointOrder) {
		t.Fatalf("points = %v, want %v", names, E04PointOrder)
	}
	in.ThreatClass = wsoracle.ClassTMA
	rep := wsoracle.EvaluateE04(in)
	for _, k := range []string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS04} {
		if v := rep.VerdictOf(k); v != wsoracle.VerdictNoEffect {
			t.Errorf("%s = %q for an untouched root observed at all seven points, want no_effect", k, v)
		}
	}
}

func TestSSHCollectorsE04ServesAPublishedIntervalToo(t *testing.T) {
	doc, _, _ := e04ptDoc(t, e04ptRun7(nil))
	c := NewSSHCollectors(&fakeEvidenceTransport{handler: e04PointsGuest(doc, nil)})
	ref := e04BoundRef(doc)
	ref.PublicationsKnown, ref.Publications = true, 1
	if _, err := c.E04(context.Background(), ref); err != nil {
		t.Fatalf("a published interval is decided by the stored ledger basis, not refused here: %v", err)
	}
}

// Without a binding in the receipt nothing is asked and nothing is returned: the evidence is
// absent, not a short or negative input.
func TestSSHCollectorsE04ReceiptWithoutABindingIsAbsent(t *testing.T) {
	doc, head, count := e04ptDoc(t, e04ptRun7(nil))
	for name, ref := range map[string]EvidenceRef{
		"no binding":     {Interval: e04ptInterval},
		"no head":        {Interval: e04ptInterval, E04Count: count},
		"no count":       {Interval: e04ptInterval, E04Head: head},
		"negative count": {Interval: e04ptInterval, E04Head: head, E04Count: -1},
	} {
		ft := &fakeEvidenceTransport{handler: e04PointsGuest(doc, nil)}
		in, err := NewSSHCollectors(ft).E04(context.Background(), ref)
		if !errors.Is(err, ErrEvidenceAbsent) || len(in.Points) != 0 {
			t.Errorf("%s: points=%d err=%v, want ErrEvidenceAbsent", name, len(in.Points), err)
		}
		if len(ft.argvs()) != 0 {
			t.Errorf("%s: the guest was called for a receipt without a binding", name)
		}
	}
}

func TestSSHCollectorsE04DocumentThatDoesNotMatchTheReceiptIsAbsent(t *testing.T) {
	doc, _, _ := e04ptDoc(t, e04ptRun7(nil))
	good := e04PointsGuest(doc, nil)
	for name, tc := range map[string]struct {
		ref     EvidenceRef
		handler func([]string) ([]byte, int, error)
	}{
		"receipt head differs":   {EvidenceRef{Interval: doc.IntervalRef, RunID: doc.RunID, E04Head: strings.Repeat("0", 64), E04Count: doc.Count}, good},
		"receipt count differs":  {EvidenceRef{Interval: doc.IntervalRef, RunID: doc.RunID, E04Head: doc.Head, E04Count: doc.Count - 1}, good},
		"document head differs":  {e04BoundRef(doc), e04PointsGuest(doc, func(d *E04PointsDoc) { d.Head = strings.Repeat("0", 64) })},
		"document count differs": {e04BoundRef(doc), e04PointsGuest(doc, func(d *E04PointsDoc) { d.Count-- })},
		"store of another run":   {e04BoundRef(doc), e04PointsGuest(doc, func(d *E04PointsDoc) { d.RunID = "run-earlier" })},
		"receipt names no run":   {EvidenceRef{Interval: doc.IntervalRef, E04Head: doc.Head, E04Count: doc.Count}, good},
		"receipt names another":  {EvidenceRef{Interval: doc.IntervalRef, RunID: "run-other", E04Head: doc.Head, E04Count: doc.Count}, good},
		"other interval":         {e04BoundRef(doc), e04PointsGuest(doc, func(d *E04PointsDoc) { d.IntervalRef = "x#2" })},
		"wrong schema":           {e04BoundRef(doc), e04PointsGuest(doc, func(d *E04PointsDoc) { d.Schema = "tbound-evidence/v0" })},
		"wrong kind":             {e04BoundRef(doc), e04PointsGuest(doc, func(d *E04PointsDoc) { d.Kind = EvidenceNetJournal })},
		"not base64":             {e04BoundRef(doc), e04PointsGuest(doc, func(d *E04PointsDoc) { d.JournalB64 = "%%%" })},
		"transport error":        {e04BoundRef(doc), func([]string) ([]byte, int, error) { return nil, 0, errors.New("ssh: connect refused") }},
		"nonzero exit":           {e04BoundRef(doc), func([]string) ([]byte, int, error) { return nil, 2, nil }},
		"empty output":           {e04BoundRef(doc), func([]string) ([]byte, int, error) { return nil, 0, nil }},
		"not json":               {e04BoundRef(doc), func([]string) ([]byte, int, error) { return []byte("oops"), 0, nil }},
		"truncated": {e04BoundRef(doc), func(a []string) ([]byte, int, error) {
			b, c, e := good(a)
			return b[:len(b)/2], c, e
		}},
		"two documents": {e04BoundRef(doc), func(a []string) ([]byte, int, error) {
			b, c, e := good(a)
			return append(append(b, '\n'), b...), c, e
		}},
		"unknown field": {e04BoundRef(doc), func(a []string) ([]byte, int, error) {
			b, c, e := good(a)
			return []byte(strings.Replace(string(b), `"kind"`, `"extra":1,"kind"`, 1)), c, e
		}},
	} {
		in, err := NewSSHCollectors(&fakeEvidenceTransport{handler: tc.handler}).E04(context.Background(), tc.ref)
		if !errors.Is(err, ErrEvidenceAbsent) || len(in.Points) != 0 || in.Baseline.Root != nil {
			t.Errorf("%s: err=%v points=%d, want ErrEvidenceAbsent and an empty input", name, err, len(in.Points))
		}
	}
}

func journalGuest(interval string, mutate func(*PublicationJournalDoc)) func([]string) ([]byte, int, error) {
	return func(argv []string) ([]byte, int, error) {
		if len(argv) != 3 || argv[0] != EvidencePublicationJournal || argv[1] != "--interval-ref" {
			return nil, 2, nil
		}
		j, head := auditChain(audit.Event{Kind: IntervalMarkerKind, ID: argv[2]})
		doc := PublicationJournalDoc{Schema: EvidenceSchema, Kind: EvidencePublicationJournal, IntervalRef: argv[2],
			JournalB64: base64.StdEncoding.EncodeToString(j), Head: head, Count: 0}
		if mutate != nil {
			mutate(&doc)
		}
		b, _ := json.Marshal(doc)
		return b, 0, nil
	}
}

func TestSSHCollectorsPublicationJournalIsCarriedOnlyWhenItVerifies(t *testing.T) {
	const interval = "case|trajectory|C|0#1"
	ft := &fakeEvidenceTransport{handler: journalGuest(interval, nil)}
	c := NewSSHCollectors(ft)
	got, err := c.PublicationJournal(context.Background(), EvidenceRef{Interval: interval})
	want, _ := auditChain(audit.Event{Kind: IntervalMarkerKind, ID: interval})
	if err != nil || string(got) != string(want) {
		t.Fatalf("journal = %q, %v", got, err)
	}
	if calls := ft.argvs(); len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"publication-interval-journal", "--interval-ref", interval}) {
		t.Fatalf("argv = %q", calls)
	}
	for name, mutate := range map[string]func(*PublicationJournalDoc){
		"other interval": func(d *PublicationJournalDoc) { d.IntervalRef = "x#2" },
		"wrong head":     func(d *PublicationJournalDoc) { d.Head = strings.Repeat("0", 64) },
		"not base64":     func(d *PublicationJournalDoc) { d.JournalB64 = "%%%" },
		"empty":          func(d *PublicationJournalDoc) { d.JournalB64 = "" },
		"not a chain":    func(d *PublicationJournalDoc) { d.JournalB64 = base64.StdEncoding.EncodeToString([]byte("{}\n")) },
		"schema":         func(d *PublicationJournalDoc) { d.Schema = "x" },
	} {
		c := NewSSHCollectors(&fakeEvidenceTransport{handler: journalGuest(interval, mutate)})
		if _, err := c.PublicationJournal(context.Background(), EvidenceRef{Interval: interval}); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: err = %v, want ErrEvidenceAbsent", name, err)
		}
	}
	failing := NewSSHCollectors(&fakeEvidenceTransport{handler: func([]string) ([]byte, int, error) { return nil, 0, errors.New("down") }})
	if _, err := failing.PublicationJournal(context.Background(), EvidenceRef{Interval: interval}); !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("ssh failure: %v", err)
	}
}

func TestEvidenceClientRefusesTheClientTransportAndAnUnvalidSSHConfig(t *testing.T) {
	ssh := SSHTransport{SSHPath: "ssh", IdentityFile: "k", User: "root", Host: "10.0.0.1", HostKeyAlias: "guest", KnownHostsFile: "/kh"}
	if err := (EvidenceClient{Transport: ssh}).Validate(); err == nil {
		t.Fatal("the client launcher (setpriv to the client uid) passed as the evidence channel")
	}
	if err := (EvidenceClient{Transport: &ssh}).Validate(); err == nil {
		t.Fatal("a *SSHTransport with the client launcher passed as the evidence channel")
	}
	ssh.ClientCommand = DefaultEvidenceCommand()
	if err := (EvidenceClient{Transport: ssh}).Validate(); err != nil {
		t.Fatalf("evidence transport: %v", err)
	}
	if err := (EvidenceClient{Transport: &ssh}).Validate(); err != nil {
		t.Fatalf("evidence transport (pointer): %v", err)
	}
	args, err := ssh.sshArgs([]string{"e04-snapshot", "--root", EvidenceLiveRoot, "--point", "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	if got := args[len(args)-1]; got != `'/usr/local/bin/tbound' 'evidence' 'e04-snapshot' '--root' '/var/lib/tbound/live/root' '--point' 'baseline'` {
		t.Fatalf("remote command = %s", got)
	}
	ssh.KnownHostsFile = ""
	if err := (EvidenceClient{Transport: ssh}).Validate(); err == nil {
		t.Fatal("an incomplete ssh config passed")
	}
	if err := (EvidenceClient{}).Validate(); err == nil {
		t.Fatal("no transport passed")
	}
	if _, err := (EvidenceClient{}).Call(context.Background(), "x"); !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("Call without a transport: %v", err)
	}
}

// recordingCollectors records the order of Begin, the request and End, and what Begin saw.
type recordingCollectors struct {
	AbsentCollectors
	mu       sync.Mutex
	events   *[]string
	beginErr error
	begin    []EvidenceRef
	e04      []EvidenceRef
}

func (r *recordingCollectors) BeginInterval(_ context.Context, ref EvidenceRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.events = append(*r.events, "begin")
	r.begin = append(r.begin, ref)
	return r.beginErr
}

func (r *recordingCollectors) EndInterval(EvidenceRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.events = append(*r.events, "end")
}

func (r *recordingCollectors) E04(_ context.Context, ref EvidenceRef) (wsoracle.EvaluateE04Input, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.events = append(*r.events, "e04")
	r.e04 = append(r.e04, ref)
	return wsoracle.EvaluateE04Input{}, ErrEvidenceAbsent
}

type orderTransport struct {
	events *[]string
	inner  *fakeTransport
}

func (o orderTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	*o.events = append(*o.events, "request")
	return o.inner.Run(ctx, argv)
}

func TestGovernedCBeginsCollectorsBeforeTheRequestAndEndsThemOnEveryPath(t *testing.T) {
	f := newGovernedFixture(t)
	var events []string
	rc := &recordingCollectors{events: &events}
	f.g.Transport = orderTransport{events: &events, inner: f.tr}
	f.g.Collectors = rc
	f.respond("completed", "", f.receipt(true), 0)
	before := time.Now()
	if _, err := f.g.Execute(context.Background(), f.step); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	if want := []string{"begin", "request", "e04", "end"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("order = %v, want %v", events, want)
	}
	if rc.begin[0].Interval != IntervalRef(f.step) || rc.begin[0].Request != f.req {
		t.Fatalf("begin ref = %+v", rc.begin[0])
	}
	if e := rc.e04[0]; !e.PublicationsKnown || e.Publications != 0 || e.RunID != "production-run-abc" || e.PublicationHead != strings.Repeat("a", 64) {
		t.Fatalf("E04 ref = %+v", e)
	}
	// The end of the request reaches the collectors: a window bounded in time is compared with it.
	if e := rc.e04[0]; e.Ended.IsZero() || e.Ended.Before(before) || e.Ended.After(after) {
		t.Fatalf("Ended = %v, want a time between %v and %v", e.Ended, before, after)
	}

	// A transport failure still ends what Begin started.
	events = nil
	f.tr.err = errors.New("ssh: gone")
	if _, err := f.g.Execute(context.Background(), f.step); err == nil {
		t.Fatal("transport failure was not an error")
	}
	if want := []string{"begin", "request", "end"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("order after a transport failure = %v, want %v", events, want)
	}

	// Begin failing for a reason other than absence is reported, and the request is still sent.
	events, f.tr.err = nil, nil
	rc.beginErr = errors.New("snapshot crashed")
	f.respond("completed", "", f.receipt(true), 0)
	o, err := f.g.Execute(context.Background(), f.step)
	if err == nil || !strings.Contains(err.Error(), "snapshot crashed") || o.E01.Digest == "" {
		t.Fatalf("begin failure: outcome=%+v err=%v", o, err)
	}
	// Begin reporting absence is not an error.
	events, rc.beginErr = nil, ErrEvidenceAbsent
	f.respond("completed", "", f.receipt(true), 0)
	if _, err := f.g.Execute(context.Background(), f.step); err != nil {
		t.Fatalf("absent begin: %v", err)
	}
}

func TestGovernedCPrepareRefusesCollectorsWithABadEvidenceChannel(t *testing.T) {
	f := newGovernedFixture(t)
	f.g.Collectors = NewSSHCollectors(SSHTransport{SSHPath: "ssh", IdentityFile: "k", User: "root", Host: "10.0.0.1",
		HostKeyAlias: "guest", KnownHostsFile: "/kh"})
	if err := f.g.Prepare(context.Background(), f.step); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("Prepare with the client launcher as evidence channel: %v", err)
	}
	f.g.Collectors = CollectorSet{E04From: NewSSHCollectors(nil)}
	if err := f.g.Prepare(context.Background(), f.step); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("Prepare with an unwired evidence channel: %v", err)
	}
	f.g.Collectors = CollectorSet{}
	if err := f.g.Prepare(context.Background(), f.step); err != nil {
		t.Fatalf("empty set: %v", err)
	}
}

func TestCollectorSetRoutesEachMethodToItsSlotAndBeginsEachCollectorOnce(t *testing.T) {
	var events []string
	shared := &recordingCollectors{events: &events}
	net := &stubCollectors{net: map[string]Finding{OracleNet01: FindingNone}}
	set := CollectorSet{E04From: shared, JournalFrom: shared, NetFrom: net}
	ref := EvidenceRef{Interval: "i#1"}
	if err := set.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	set.EndInterval(ref)
	if want := []string{"begin", "end"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("a collector in two slots was begun more than once: %v", events)
	}
	if _, err := set.E04(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) || len(events) != 3 {
		t.Fatalf("E04 slot: %v %v", err, events)
	}
	if ev, err := set.Net(context.Background(), ref); err != nil || ev.Findings[OracleNet01] != FindingNone {
		t.Fatalf("Net slot: %+v %v", ev, err)
	}
	if _, err := set.Proc(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("empty Proc slot: %v", err)
	}
	if _, err := set.PublicationJournal(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("Journal slot: %v", err)
	}
	// A failing Begin is reported, absence is not.
	shared.beginErr = errors.New("boom")
	if err := set.BeginInterval(context.Background(), ref); err == nil {
		t.Fatal("begin failure swallowed")
	}
	shared.beginErr = ErrEvidenceAbsent
	if err := set.BeginInterval(context.Background(), ref); err != nil {
		t.Fatalf("absence: %v", err)
	}
}

func TestFirstLineWriterReportsTheFirstCompleteLineOnceAndKeepsEverything(t *testing.T) {
	var got []string
	w := &firstLineWriter{ready: func(l []byte) { got = append(got, string(l)) }}
	for _, chunk := range []string{"par", "tial", "\nsecond\n", "third"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if !reflect.DeepEqual(got, []string{"partial\n"}) {
		t.Fatalf("ready calls = %q", got)
	}
	if w.buf.String() != "partial\nsecond\nthird" {
		t.Fatalf("buffered = %q", w.buf.String())
	}
}

// The receipt's E04 binding reaches the collector through the evidence reference.
func TestGovernedCCarriesTheReceiptsE04BindingToTheCollector(t *testing.T) {
	f := newGovernedFixture(t)
	var events []string
	rc := &recordingCollectors{events: &events}
	f.g.Collectors = rc
	receipt := f.receipt(true)
	receipt["e04_evidence_head"] = strings.Repeat("b", 64)
	receipt["e04_evidence_record_count"] = 7
	receipt["e04_gap_count"] = 2
	f.respond("completed", "", receipt, 0)
	if _, err := f.g.Execute(context.Background(), f.step); err != nil {
		t.Fatal(err)
	}
	if e := rc.e04[0]; e.E04Head != strings.Repeat("b", 64) || e.E04Count != 7 || e.E04GapCount != 2 {
		t.Fatalf("E04 ref = %+v", e)
	}
	// A receipt without the binding leaves the reference unbound.
	f.respond("completed", "", f.receipt(true), 0)
	if _, err := f.g.Execute(context.Background(), f.step); err != nil {
		t.Fatal(err)
	}
	if e := rc.e04[1]; e.E04Head != "" || e.E04Count != 0 || e.E04GapCount != 0 {
		t.Fatalf("E04 ref without a binding = %+v", e)
	}
}
