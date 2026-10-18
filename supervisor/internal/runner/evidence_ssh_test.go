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

func evidenceManifest(label string) wsoracle.Manifest {
	root := &wsoracle.Entry{Type: wsoracle.TypeDir, Mode: 0o700, UID: 995, GID: 986, Nlink: 2, Ino: 7}
	return wsoracle.Manifest{Label: label, Complete: true, Root: root, Dev: 9,
		Entries: []wsoracle.Entry{wsFile("a.txt", 11, "X")}}
}

// e04Guest answers e04-snapshot for every point with the same tree.
func e04Guest(mutate func(*E04SnapshotDoc)) func([]string) ([]byte, int, error) {
	return func(argv []string) ([]byte, int, error) {
		if len(argv) != 5 || argv[0] != EvidenceE04Snapshot || argv[1] != "--root" || argv[3] != "--point" {
			return nil, 2, nil
		}
		doc := E04SnapshotDoc{Schema: EvidenceSchema, Kind: EvidenceE04Snapshot, Point: argv[4], Root: argv[2],
			Manifest: evidenceManifest(argv[4])}
		if mutate != nil {
			mutate(&doc)
		}
		b, _ := json.Marshal(doc)
		return b, 0, nil
	}
}

// The collector sits outside the service and can observe only the baseline (before the
// request) and the end state (after the answer). It must not label end-state snapshots
// with the six internal E04 points: a change made and undone between them would then
// read as no_effect. The internal points stay absent, so every WS predicate is
// evidence_lost until service-side lifecycle manifests exist.
func TestSSHCollectorsE04ObservesOnlyTheBaselineAndTheEndState(t *testing.T) {
	ft := &fakeEvidenceTransport{handler: e04Guest(nil)}
	c := NewSSHCollectors(ft)
	ref := EvidenceRef{Interval: "case|trajectory|C|0#1", PublicationsKnown: true}
	if err := c.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if got := ft.argvs(); len(got) != 1 || !reflect.DeepEqual(got[0], []string{"e04-snapshot", "--root", EvidenceLiveRoot, "--point", "baseline"}) {
		t.Fatalf("calls after Begin = %q", got)
	}
	in, err := c.E04(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	calls := ft.argvs()
	if len(calls) != 2 || len(in.Points) != 1 {
		t.Fatalf("calls=%d points=%d, want 2 and 1", len(calls), len(in.Points))
	}
	if p := in.Points[0]; p.Name != wsoracle.PointFinalCleanup || calls[1][4] != wsoracle.PointFinalCleanup {
		t.Fatalf("end state labelled %q (call %q), want final_cleanup", p.Name, calls[1])
	}
	if in.Points[0].LedgerPrefixDigest != wsoracle.LedgerPrefixDigest(nil, 0) || in.Points[0].LedgerApplied != 0 {
		t.Error("the end state carries a ledger prefix")
	}
	if in.Baseline.Label != wsoracle.PointBaseline || !in.Scope.Whole || len(in.Ledger) != 0 {
		t.Fatalf("baseline/scope/ledger = %q %+v %v", in.Baseline.Label, in.Scope, in.Ledger)
	}
	in.ThreatClass = "TM-C"
	rep := wsoracle.EvaluateE04(in)
	for _, k := range []string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS03, wsoracle.PredicateWS04} {
		if v := rep.VerdictOf(k); v != wsoracle.VerdictEvidenceLost {
			t.Errorf("%s = %q on an unchanged end state, want evidence_lost (internal points were not observed)", k, v)
		}
	}
}

func TestSSHCollectorsE04ChangedEndStateIsNeverNoEffect(t *testing.T) {
	changed := e04Guest(func(d *E04SnapshotDoc) {
		if d.Point != wsoracle.PointBaseline {
			d.Manifest.Entries = []wsoracle.Entry{wsFile("a.txt", 11, "Y")}
		}
	})
	c := NewSSHCollectors(&fakeEvidenceTransport{handler: changed})
	ref := EvidenceRef{Interval: "i#1", PublicationsKnown: true}
	if err := c.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	in, err := c.E04(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	in.ThreatClass = "TM-A"
	rep := wsoracle.EvaluateE04(in)
	if v := rep.VerdictOf(wsoracle.PredicateWS01); v == wsoracle.VerdictNoEffect {
		t.Fatalf("EFF-WS-01 = %q for a changed file", v)
	}
	if len(rep.Findings) == 0 {
		t.Fatal("the end-state change is not reported as a finding")
	}
}

func TestSSHCollectorsE04WithoutALedgerServesOnlyAKnownPublicationFreeInterval(t *testing.T) {
	for name, ref := range map[string]EvidenceRef{
		"publication":        {Interval: "i#1", PublicationsKnown: true, Publications: 1},
		"anchor not known":   {Interval: "i#1"},
		"begin not observed": {Interval: "other#1", PublicationsKnown: true},
	} {
		ft := &fakeEvidenceTransport{handler: e04Guest(nil)}
		c := NewSSHCollectors(ft)
		if err := c.BeginInterval(context.Background(), EvidenceRef{Interval: "i#1"}); err != nil {
			t.Fatal(err)
		}
		before := len(ft.argvs())
		if _, err := c.E04(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: err = %v, want ErrEvidenceAbsent", name, err)
		}
		if name != "begin not observed" && len(ft.argvs()) != before {
			t.Errorf("%s: snapshots were taken although E04 cannot be served", name)
		}
	}
}

func TestSSHCollectorsEvidenceFailuresAreAbsentNeverNone(t *testing.T) {
	good := e04Guest(nil)
	cases := map[string]func([]string) ([]byte, int, error){
		"transport error": func([]string) ([]byte, int, error) { return nil, 0, errors.New("ssh: connect refused") },
		"nonzero exit":    func([]string) ([]byte, int, error) { return nil, 2, nil },
		"empty output":    func([]string) ([]byte, int, error) { return nil, 0, nil },
		"not json":        func([]string) ([]byte, int, error) { return []byte("oops"), 0, nil },
		"truncated": func(a []string) ([]byte, int, error) {
			b, c, e := good(a)
			return b[:len(b)/2], c, e
		},
		"two documents": func(a []string) ([]byte, int, error) {
			b, c, e := good(a)
			return append(append(b, '\n'), b...), c, e
		},
		"unknown field": func(a []string) ([]byte, int, error) {
			b, c, e := good(a)
			return []byte(strings.Replace(string(b), `"kind"`, `"extra":1,"kind"`, 1)), c, e
		},
		"wrong schema": e04Guest(func(d *E04SnapshotDoc) { d.Schema = "tbound-evidence/v0" }),
		"wrong kind":   e04Guest(func(d *E04SnapshotDoc) { d.Kind = EvidenceProcCapture }),
		"other point":  e04Guest(func(d *E04SnapshotDoc) { d.Point = wsoracle.PointFinalCleanup }),
		"other label":  e04Guest(func(d *E04SnapshotDoc) { d.Manifest.Label = "other" }),
		"other root":   e04Guest(func(d *E04SnapshotDoc) { d.Root = "/etc" }),
		"no root meta": e04Guest(func(d *E04SnapshotDoc) { d.Manifest.Root = nil }),
	}
	for name, h := range cases {
		c := NewSSHCollectors(&fakeEvidenceTransport{handler: h})
		ref := EvidenceRef{Interval: "i#1", PublicationsKnown: true}
		if err := c.BeginInterval(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: Begin err = %v, want ErrEvidenceAbsent", name, err)
		}
		if _, err := c.E04(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: E04 err = %v, want ErrEvidenceAbsent", name, err)
		}
	}
	// The end-state snapshot failing makes the whole input absent, not a short one.
	calls := 0
	c := NewSSHCollectors(&fakeEvidenceTransport{handler: func(a []string) ([]byte, int, error) {
		calls++
		if calls == 2 {
			return nil, 255, nil
		}
		return e04Guest(nil)(a)
	}})
	ref := EvidenceRef{Interval: "i#1", PublicationsKnown: true}
	if err := c.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if in, err := c.E04(context.Background(), ref); !errors.Is(err, ErrEvidenceAbsent) || len(in.Points) != 0 {
		t.Fatalf("partial points: %d, %v", len(in.Points), err)
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
	if got := args[len(args)-1]; got != `'/usr/local/bin/tbound' 'evidence' 'e04-snapshot' '--root' '/var/lib/tbound/live' '--point' 'baseline'` {
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
	if _, err := f.g.Execute(context.Background(), f.step); err != nil {
		t.Fatal(err)
	}
	if want := []string{"begin", "request", "e04", "end"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("order = %v, want %v", events, want)
	}
	if rc.begin[0].Interval != IntervalRef(f.step) || rc.begin[0].Request != f.req {
		t.Fatalf("begin ref = %+v", rc.begin[0])
	}
	if e := rc.e04[0]; !e.PublicationsKnown || e.Publications != 0 || e.RunID != "production-run-abc" {
		t.Fatalf("E04 ref = %+v", e)
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
