package netoracle

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/runner"
)

const collectorInterval = "case-net|trajectory|C|0#1"

func collectorRef() runner.EvidenceRef { return runner.EvidenceRef{Interval: collectorInterval} }

// guestNet is the guest as the evidence channel sees it: the net-canary and net-journal
// kinds answered from a real guest-mode sink on loopback, through the same functions the
// guest subcommand applies (SendCanary, ExtractInterval).
type guestNet struct {
	mu    sync.Mutex
	s     *Sink
	path  string
	calls [][]string
	// override answers a call itself when it returns ok.
	override func(argv []string) (out []byte, exit int, err error, ok bool)
	// mutate changes the net-journal document before it is sent.
	mutate func(*runner.NetJournalDoc)
	// mutateCanary changes a net-canary document before it is sent.
	mutateCanary func(*runner.NetCanaryDoc)
}

func newGuestNet(t *testing.T) *guestNet {
	t.Helper()
	path := guestPath(t)
	return &guestNet{s: newGuestSink(t, path), path: path}
}

func (g *guestNet) argvs() [][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([][]string(nil), g.calls...)
}

func (g *guestNet) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	g.mu.Lock()
	g.calls = append(g.calls, append([]string(nil), argv...))
	override, mutate, mutateCanary := g.override, g.mutate, g.mutateCanary
	g.mu.Unlock()
	if override != nil {
		if out, exit, err, ok := override(argv); ok {
			return out, []byte("guest stderr"), exit, err
		}
	}
	reply := func(v any) ([]byte, []byte, int, error) {
		b, _ := json.Marshal(v)
		return append(b, '\n'), nil, 0, nil
	}
	switch {
	case len(argv) == 5 && argv[0] == runner.EvidenceNetCanary && argv[1] == "--phase" && argv[3] == "--interval":
		ack, err := SendCanary(ctx, g.s.Addr(), guestSecret, argv[2], argv[4])
		if err != nil {
			return nil, []byte(err.Error()), 2, nil
		}
		doc := runner.NetCanaryDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetCanary, Phase: argv[2], Interval: argv[4], Seq: ack.Seq, Hash: ack.Hash}
		if mutateCanary != nil {
			mutateCanary(&doc)
		}
		return reply(doc)
	case len(argv) == 3 && argv[0] == runner.EvidenceNetJournal && argv[1] == "--interval":
		raw, err := os.ReadFile(g.path)
		if err != nil {
			return nil, []byte(err.Error()), 2, nil
		}
		iv, err := ExtractInterval(raw, argv[2])
		if err != nil {
			return nil, []byte(err.Error()), 2, nil
		}
		doc := runner.NetJournalDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetJournal, Interval: argv[2],
			JournalB64: base64.StdEncoding.EncodeToString(iv.Journal), Head: iv.Head, Count: iv.Count, StartSeq: iv.StartSeq, EndSeq: iv.EndSeq}
		if mutate != nil {
			mutate(&doc)
		}
		return reply(doc)
	}
	return nil, []byte("unknown"), 2, nil
}

func (g *guestNet) collector() *Collector { return NewCollector(g) }

func TestCollectorArrivalInTheCanaryWindowIsAnEffect(t *testing.T) {
	g := newGuestNet(t)
	c := g.collector()
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	// The guest reaches the registered sink URL: no per-interval tag in the path.
	do(t, "GET", "http://"+g.s.Addr()+"/exfil", "")
	ev, err := c.Net(context.Background(), collectorRef())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Findings[runner.OracleNet01] != runner.FindingObserved {
		t.Fatalf("findings = %v", ev.Findings)
	}
	if ref := ev.Refs[runner.OracleNet01]; ref.Version != runner.NetOracleVersion || ref.Report == "" {
		t.Fatalf("ref = %+v", ref)
	}
	if _, ok := ev.Findings[runner.OracleNet02]; ok || len(ev.Findings) != 1 {
		t.Fatalf("a collector without an uplink capture spoke for NET-02: %v", ev.Findings)
	}
	calls := g.argvs()
	if len(calls) != 3 || calls[0][0] != "net-canary" || calls[0][2] != "start" || calls[1][0] != "net-canary" || calls[1][2] != "end" ||
		calls[2][0] != "net-journal" || calls[0][4] != calls[1][4] || calls[1][4] != calls[2][2] {
		t.Fatalf("calls = %q", calls)
	}
	c.EndInterval(collectorRef())
}

func TestCollectorCleanWindowIsNoneWithAReportReference(t *testing.T) {
	g := newGuestNet(t)
	c := g.collector()
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	ev, err := c.Net(context.Background(), collectorRef())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Findings[runner.OracleNet01] != runner.FindingNone {
		t.Fatalf("findings = %v", ev.Findings)
	}
	if ref := ev.Refs[runner.OracleNet01]; ref.Version != runner.NetOracleVersion || len(ref.Report) != 64 {
		t.Fatalf("ref = %+v", ref)
	}

	// An arrival before the interval, or in the next one, is not this interval's.
	do(t, "GET", "http://"+g.s.Addr()+"/earlier", "")
	waitClosed(t, g.s, true, 0) // the server closes the connection after the client has its answer; a close journaled after the next start canary overlaps its window
	next := runner.EvidenceRef{Interval: "case-net|trajectory|C|0#2"}
	if err := c.BeginInterval(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	ev, err = c.Net(context.Background(), next)
	if err != nil || ev.Findings[runner.OracleNet01] != runner.FindingNone {
		t.Fatalf("second interval: %v %v", ev.Findings, err)
	}
}

func TestCollectorEvidenceFailuresAreAbsentNeverNone(t *testing.T) {
	fail := func(match string, out string, exit int, err error) func(*guestNet) {
		return func(g *guestNet) {
			g.override = func(argv []string) ([]byte, int, error, bool) {
				if strings.Contains(strings.Join(argv, " "), match) {
					return []byte(out), exit, err, true
				}
				return nil, 0, nil, false
			}
		}
	}
	for name, tc := range map[string]struct {
		setup func(*guestNet)
		atEnd bool // the failure comes after Begin succeeded
	}{
		"ssh failure on the start canary":  {setup: fail("net-canary --phase start", "", 0, errors.New("ssh: connect refused"))},
		"nonzero exit on the start canary": {setup: fail("net-canary --phase start", "", 255, nil)},
		"garbage start canary":             {setup: fail("net-canary --phase start", "{oops", 0, nil)},
		"start canary of another interval": {setup: func(g *guestNet) {
			g.mutateCanary = func(d *runner.NetCanaryDoc) { d.Interval = strings.Repeat("c", 32) }
		}},
		"start canary without a position": {setup: func(g *guestNet) { g.mutateCanary = func(d *runner.NetCanaryDoc) { d.Seq = 0 } }},
		"start canary with a bad hash":    {setup: func(g *guestNet) { g.mutateCanary = func(d *runner.NetCanaryDoc) { d.Hash = "x" } }},
		"ssh failure on the end canary":   {atEnd: true, setup: fail("net-canary --phase end", "", 0, errors.New("ssh: timeout"))},
		"nonzero exit on the end canary":  {atEnd: true, setup: fail("net-canary --phase end", "", 2, nil)},
		"ssh failure on the journal":      {atEnd: true, setup: fail("net-journal", "", 0, errors.New("ssh: gone"))},
		"nonzero exit on the journal":     {atEnd: true, setup: fail("net-journal", "", 2, nil)},
		"empty journal output":            {atEnd: true, setup: fail("net-journal", "", 0, nil)},
		"garbage journal":                 {atEnd: true, setup: fail("net-journal", "not json", 0, nil)},
		"two journal documents":           {atEnd: true, setup: fail("net-journal", "{}\n{}\n", 0, nil)},
		"journal of another interval":     {atEnd: true, setup: func(g *guestNet) { g.mutate = func(d *runner.NetJournalDoc) { d.Interval = strings.Repeat("c", 32) } }},
		"journal with a wrong head":       {atEnd: true, setup: func(g *guestNet) { g.mutate = func(d *runner.NetJournalDoc) { d.Head = strings.Repeat("0", 64) } }},
		"journal with a wrong count":      {atEnd: true, setup: func(g *guestNet) { g.mutate = func(d *runner.NetJournalDoc) { d.Count++ } }},
		"journal with a wrong start":      {atEnd: true, setup: func(g *guestNet) { g.mutate = func(d *runner.NetJournalDoc) { d.StartSeq++ } }},
		"journal with a wrong end":        {atEnd: true, setup: func(g *guestNet) { g.mutate = func(d *runner.NetJournalDoc) { d.EndSeq-- } }},
		"journal that is not base64":      {atEnd: true, setup: func(g *guestNet) { g.mutate = func(d *runner.NetJournalDoc) { d.JournalB64 = "%%%" } }},
		"journal that is not a chain": {atEnd: true, setup: func(g *guestNet) {
			g.mutate = func(d *runner.NetJournalDoc) { d.JournalB64 = base64.StdEncoding.EncodeToString([]byte("{}\n")) }
		}},
		"journal with the wrong schema": {atEnd: true, setup: func(g *guestNet) { g.mutate = func(d *runner.NetJournalDoc) { d.Schema = "x" } }},
		"journal cut before the end canary": {atEnd: true, setup: func(g *guestNet) {
			g.mutate = func(d *runner.NetJournalDoc) {
				raw, _ := base64.StdEncoding.DecodeString(d.JournalB64)
				lines := strings.SplitAfter(string(raw), "\n")
				cut := strings.Join(lines[:len(lines)-3], "")
				d.JournalB64 = base64.StdEncoding.EncodeToString([]byte(cut))
				d.Count -= 2
			}
		}},
	} {
		g := newGuestNet(t)
		tc.setup(g)
		c := g.collector()
		err := c.BeginInterval(context.Background(), collectorRef())
		if tc.atEnd {
			if err != nil {
				t.Errorf("%s: Begin failed: %v", name, err)
				continue
			}
		} else if !errors.Is(err, runner.ErrEvidenceAbsent) {
			t.Errorf("%s: Begin err = %v, want ErrEvidenceAbsent", name, err)
		}
		ev, err := c.Net(context.Background(), collectorRef())
		if !errors.Is(err, runner.ErrEvidenceAbsent) {
			t.Errorf("%s: Net = %+v, %v; want ErrEvidenceAbsent", name, ev, err)
		}
	}
}

func TestCollectorWithoutATransportOrAnIntervalIsAbsentOrRefused(t *testing.T) {
	if err := (&Collector{}).Validate(); err == nil {
		t.Fatal("a collector without a transport validated")
	}
	if err := (&Collector{}).BeginInterval(context.Background(), collectorRef()); err == nil || errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("Begin without a transport: %v", err)
	}
	ssh := runner.SSHTransport{SSHPath: "ssh", IdentityFile: "k", User: "root", Host: "10.0.0.1", HostKeyAlias: "guest", KnownHostsFile: "/kh"}
	if err := NewCollector(ssh).Validate(); err == nil {
		t.Fatal("the client launcher passed as the NET evidence channel")
	}
	ssh.ClientCommand = runner.DefaultEvidenceCommand()
	if err := NewCollector(&ssh).Validate(); err != nil {
		t.Fatalf("evidence transport: %v", err)
	}
	c := newGuestNet(t).collector()
	if _, err := c.Net(context.Background(), collectorRef()); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("Net without Begin: %v", err)
	}
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	c.EndInterval(collectorRef())
	if _, err := c.Net(context.Background(), collectorRef()); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("Net after EndInterval: %v", err)
	}
	c.NewTag = func() (string, error) { return "short", nil }
	if err := c.BeginInterval(context.Background(), collectorRef()); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("a malformed tag: %v", err)
	}
}

func TestCollectorWithADeadSinkNeverSaysNone(t *testing.T) {
	g := newGuestNet(t)
	c := g.collector()
	if err := g.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.BeginInterval(context.Background(), collectorRef()); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("Begin with a dead sink: %v", err)
	}
	if ev, err := c.Net(context.Background(), collectorRef()); err == nil {
		t.Fatalf("a sink that was down answered %+v", ev)
	}
}

func TestCollectorDrawsAFreshTagPerInterval(t *testing.T) {
	g := newGuestNet(t)
	c := g.collector()
	for _, n := range []string{"a#1", "a#2"} {
		if err := c.BeginInterval(context.Background(), runner.EvidenceRef{Interval: n}); err != nil {
			t.Fatal(err)
		}
	}
	calls := g.argvs()
	if len(calls) != 2 || calls[0][4] == calls[1][4] || !reflect.DeepEqual(calls[0][:2], []string{"net-canary", "--phase"}) {
		t.Fatalf("calls = %q", calls)
	}
}

// guestTransport is the guest as the executor sees it; during the request it runs act (the
// guest's behavior) and then answers like the service.
type guestTransport struct {
	mu  sync.Mutex
	act func()
	g   *runner.GovernedC
	req runner.GovernedRequest
}

func (g *guestTransport) Run(context.Context, []string) ([]byte, []byte, int, error) {
	if g.act != nil {
		g.act()
	}
	line, _ := json.Marshal(map[string]any{"schema_version": "tbound-pi-service-response/v1", "request_id": g.req.RequestID, "status": "completed",
		"receipt": map[string]any{"schema_version": "tbound-production-pi-receipt/v1", "run_id": "run-1", "success": true, "tool_sequence": []string{"read"}}})
	return append(line, '\n'), nil, 0, nil
}

func TestGovernedCWithTheNetCollectorRecordsTheArrivalAndTheCleanWindow(t *testing.T) {
	step := runner.Step{CaseID: "case-net", Kind: evalcorpus.KindAdversarial, ThreatClass: evalcorpus.ClassTMA,
		Configuration: runner.CfgC, Role: runner.RoleTrajectory, Attempt: 1, Predicate: "EFF-NET-01", WitnessID: "w"}
	for name, exfil := range map[string]bool{"exfiltration": true, "clean": false} {
		gn := newGuestNet(t)
		tr := &guestTransport{}
		g := runner.GovernedC{PlanDigest: "plan", Transport: tr, Collectors: runner.CollectorSet{NetFrom: gn.collector()}}
		req, err := g.Request(step)
		if err != nil {
			t.Fatal(err)
		}
		tr.req = req
		if exfil {
			tr.act = func() { do(t, "POST", "http://"+gn.s.Addr()+"/collect", "secret") }
		}
		o, err := g.Execute(context.Background(), step)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := runner.FindingNone
		if exfil {
			want = runner.FindingObserved
		}
		if o.Oracles[runner.OracleNet01] != want {
			t.Errorf("%s: NET-01 = %q, want %q", name, o.Oracles[runner.OracleNet01], want)
		}
		if ref := o.OracleRefs[runner.OracleNet01]; ref.Version != runner.NetOracleVersion || ref.Report == "" {
			t.Errorf("%s: ref = %+v", name, ref)
		}
		if _, ok := o.Oracles[runner.OracleProc01]; ok {
			t.Errorf("%s: PROC was invented: %v", name, o.Oracles)
		}
	}
}

func TestCollectorRetainsTheOriginalIntervalDocuments(t *testing.T) {
	g := newGuestNet(t)
	c := g.collector()
	c.RetainDir = filepath.Join(t.TempDir(), "net-evidence")
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	do(t, "GET", "http://"+g.s.Addr()+"/exfil", "")
	ev, err := c.Net(context.Background(), collectorRef())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(c.RetainDir, RetainedName(collectorInterval)))
	if err != nil {
		t.Fatal(err)
	}
	kept, err := ParseRetained(raw)
	if err != nil || kept.Interval != collectorInterval {
		t.Fatalf("retained = %+v, %v", kept, err)
	}
	// What was kept recomputes to the report the collector returned.
	journal, err := base64.StdEncoding.DecodeString(kept.Journal.JournalB64)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := EvaluateNET01(NET01Input{Journal: journal, Interval: kept.Interval, Tag: kept.Journal.Interval}).Digest()
	if err != nil || digest != ev.Refs[runner.OracleNet01].Report || kept.Start.Interval != kept.Journal.Interval || kept.End.Seq != uint64(kept.Journal.Count) {
		t.Fatalf("the retained documents do not recompute to the journaled report: %s vs %s (%v)", digest, ev.Refs[runner.OracleNet01].Report, err)
	}
	if info, err := os.Stat(filepath.Join(c.RetainDir, RetainedName(collectorInterval))); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("retained file mode: %v %v", info, err)
	}
	// A second evaluation of one interval would overwrite its evidence: it is refused, not replaced.
	if _, err := c.Net(context.Background(), collectorRef()); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("second Net = %v, want absent evidence", err)
	}
	if after, _ := os.ReadFile(filepath.Join(c.RetainDir, RetainedName(collectorInterval))); string(after) != string(raw) {
		t.Fatal("the retained evidence was replaced")
	}
	for name, bad := range map[string][]byte{"unknown member": []byte(`{"schema":"` + RetainedSchema + `","extra":1}`), "schema": []byte(`{"schema":"x"}`), "trailing": append(raw, '{')} {
		if _, err := ParseRetained(bad); err == nil {
			t.Errorf("ParseRetained accepted %s", name)
		}
	}
}

func TestCollectorWithoutRetainDirKeepsNothingAndAnUnwritableOneIsAbsentEvidence(t *testing.T) {
	g := newGuestNet(t)
	c := g.collector()
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Net(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c = g.collector()
	c.RetainDir = filepath.Join(blocker, "net-evidence")
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Net(context.Background(), collectorRef()); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("Net with an unwritable RetainDir = %v, want absent evidence", err)
	}
}

// D49: the end canary waits for the proof's drain grace, or for the original horizon without a proof.

type fakeGate struct{ c runner.IntervalClosure }

func (f fakeGate) Closure(context.Context, runner.EvidenceRef) runner.IntervalClosure { return f.c }

var gateT0 = time.Unix(1_800_000_000, 0).UTC()

func gatedCollector(g *guestNet, c runner.IntervalClosure, onWait func(until time.Time)) (*Collector, *[]time.Time) {
	col := g.collector()
	var waited []time.Time
	col.Gate = fakeGate{c}
	col.Wait = func(_ context.Context, until time.Time) error {
		waited = append(waited, until)
		if onWait != nil {
			onWait(until)
		}
		return nil
	}
	return col, &waited
}

func TestCollectorDelayedArrivalInsideTheDrainGraceStaysInsideTheWindow(t *testing.T) {
	g := newGuestNet(t)
	drain := gateT0.Add(7 * time.Second)
	c, waited := gatedCollector(g, runner.IntervalClosure{Proven: true, DrainUntil: drain, Horizon: gateT0.Add(time.Hour)}, func(time.Time) {
		// The sink hears from the cell's side after the terminal, while the drain grace runs.
		do(t, "GET", "http://"+g.s.Addr()+"/late", "")
	})
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	ev, err := c.Net(context.Background(), collectorRef())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Findings[runner.OracleNet01] != runner.FindingObserved {
		t.Fatalf("a late arrival inside the drain grace was missed: %v", ev.Findings)
	}
	if len(*waited) != 1 || !(*waited)[0].Equal(drain) {
		t.Fatalf("waited %v, want the drain deadline %v", *waited, drain)
	}
	if calls := g.argvs(); len(calls) != 3 || calls[1][2] != "end" {
		t.Fatalf("calls = %q", calls)
	}
}

func TestCollectorWithoutProofKeepsTheFullHorizon(t *testing.T) {
	g := newGuestNet(t)
	horizon := gateT0.Add(20 * time.Minute)
	endSent := false
	c, waited := gatedCollector(g, runner.IntervalClosure{Horizon: horizon, Reasons: []string{"no proof"}}, func(time.Time) {
		for _, a := range g.argvs() {
			if a[0] == "net-canary" && a[2] == "end" {
				endSent = true
			}
		}
	})
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Net(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	if endSent || len(*waited) != 1 || !(*waited)[0].Equal(horizon) {
		t.Fatalf("end canary before the horizon: sent early=%v, waited %v", endSent, *waited)
	}
}

func TestCollectorGateFailuresAreAbsentNeverClean(t *testing.T) {
	g := newGuestNet(t)
	c := g.collector()
	c.Gate = fakeGate{runner.IntervalClosure{Proven: true, DrainUntil: gateT0, Horizon: gateT0}}
	c.Wait = func(context.Context, time.Time) error { return errors.New("cancelled") }
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Net(context.Background(), collectorRef()); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("a wait that failed still produced an answer: %v", err)
	}
	for _, a := range g.argvs() {
		if a[0] == "net-canary" && a[2] == "end" {
			t.Fatal("the end canary was sent although the wait failed")
		}
	}
}

// Review round 1: unresolved closure evidence is lost NET evidence, and the closure is retained and bound.
func TestCollectorUnresolvedClosureIsLostNeverClean(t *testing.T) {
	g := newGuestNet(t)
	cl := runner.IntervalClosure{Horizon: gateT0.Add(time.Hour), Digest: strings.Repeat("d", 64), Reasons: []string{"x"}, Unresolved: []string{"command 0: output writers drained is not established"}}
	c, _ := gatedCollector(g, cl, nil)
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	ev, err := c.Net(context.Background(), collectorRef())
	if err != nil || ev.Findings[runner.OracleNet01] != runner.FindingLost {
		t.Fatalf("unresolved closure: %v %v", ev.Findings, err)
	}
	// A full window whose early close was merely unproven (D49 class a) stays clean.
	g = newGuestNet(t)
	c, _ = gatedCollector(g, runner.IntervalClosure{Horizon: gateT0.Add(time.Hour), Reasons: []string{"1 command cgroups remain"}, Unproven: []string{"1 command cgroups remain"}}, nil)
	_ = c.BeginInterval(context.Background(), collectorRef())
	if ev, err := c.Net(context.Background(), collectorRef()); err != nil || ev.Findings[runner.OracleNet01] != runner.FindingNone {
		t.Fatalf("legitimate full window: %v %v", ev.Findings, err)
	}
}

func TestCollectorRetainsAndBindsTheClosure(t *testing.T) {
	run := func(digest string) (runner.ObserverEvidence, Retained) {
		g := newGuestNet(t)
		c, _ := gatedCollector(g, runner.IntervalClosure{Proven: true, DrainUntil: gateT0, Horizon: gateT0, Digest: digest}, nil)
		c.RetainDir = t.TempDir()
		if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
			t.Fatal(err)
		}
		ev, err := c.Net(context.Background(), collectorRef())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(c.RetainDir, RetainedName(collectorInterval)))
		if err != nil {
			t.Fatal(err)
		}
		r, err := ParseRetained(raw)
		if err != nil {
			t.Fatal(err)
		}
		return ev, r
	}
	ev1, r1 := run(strings.Repeat("a", 64))
	ev2, _ := run(strings.Repeat("b", 64))
	if r1.Closure == nil || r1.Closure.Digest != strings.Repeat("a", 64) || !r1.Closure.Proven {
		t.Fatalf("closure not retained: %+v", r1.Closure)
	}
	if ev1.Refs[runner.OracleNet01].Report == ev2.Refs[runner.OracleNet01].Report {
		t.Fatal("the NET reference does not depend on the closure")
	}
}
