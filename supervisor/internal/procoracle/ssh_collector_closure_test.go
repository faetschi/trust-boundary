package procoracle

import (
	"context"
	"errors"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/runner"
)

// D49 tests: the PROC monitor ends on proof of completion and otherwise keeps its full horizon.

var closureRef = runner.EvidenceRef{Interval: sshRef.Interval, RunID: "run-1"}

const testDrain = 7 * time.Second

func newEarlyCollector(g *fakeGuest) (*SSHCollector, *[]time.Time) {
	c, waited := newSSHCollector(g)
	c.DrainGrace = testDrain
	c.RetainDir, _ = os.MkdirTemp("", "d49-proc")
	return c, waited
}

// runProc runs Proc under a bound so that a collector that wrongly waits for the (never released) monitor fails the test.
func runProc(t *testing.T, c *SSHCollector, ref runner.EvidenceRef) (runner.ObserverEvidence, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Proc(ctx, ref)
}

func begin(t *testing.T, c *SSHCollector) {
	t.Helper()
	if err := c.BeginInterval(context.Background(), closureRef); err != nil {
		t.Fatal(err)
	}
}

func wantAll(t *testing.T, ev runner.ObserverEvidence, f runner.Finding) {
	t.Helper()
	for _, k := range []string{runner.OracleProc01, runner.OracleProc02, runner.OracleProc03} {
		if ev.Findings[k] != f {
			t.Errorf("%s = %q, want %q", k, ev.Findings[k], f)
		}
	}
}

func TestEarlyCloseCleanSettledIntervalReturnsBeforeTheMonitorTimer(t *testing.T) {
	g := &fakeGuest{cell: sshCell, release: make(chan struct{})} // the monitor's own timer never fires
	c, waited := newEarlyCollector(g)
	begin(t, c)
	ev, err := runProc(t, c, closureRef)
	if err != nil {
		t.Fatalf("Proc did not return on proof: %v", err)
	}
	wantAll(t, ev, runner.FindingNone)
	for _, k := range []string{runner.OracleProc01, runner.OracleProc02, runner.OracleProc03} {
		if r := ev.Refs[k]; r.Version != runner.ProcOracleVersion || len(r.Report) != 64 {
			t.Errorf("%s ref = %+v", k, r)
		}
	}
	// The teardown deadline is the interval start plus the grace, as before; then the drain grace after the proof.
	if len(*waited) != 2 || !(*waited)[0].Equal(sshT0.Add(time.Second)) || !(*waited)[1].Equal(sshT0.Add(testDrain)) {
		t.Fatalf("waits = %v, want deadline then drain end", *waited)
	}
	var kinds []string
	for _, a := range g.argvs() {
		kinds = append(kinds, a[0])
	}
	if got := strings.Join(kinds, " "); got != "proc-cell proc-monitor proc-capture proc-capture proc-closure" {
		t.Fatalf("calls = %s", got)
	}
	if stops := g.stopLines(); len(stops) != 1 || !strings.HasPrefix(stops[0], "stop closed-on-proof ") || !strings.HasSuffix(stops[0], " run-1\n") {
		t.Fatalf("stop lines = %q", stops)
	}
	if v := c.Closure(context.Background(), closureRef); !v.Proven || !v.DrainUntil.Equal(sshT0.Add(testDrain)) ||
		!v.Horizon.Equal(sshT0.Add(30*time.Second)) || len(v.Digest) != 64 {
		t.Fatalf("closure = %+v", v)
	}
}

func TestEarlyCloseStopsOnlyAfterTheDrainGrace(t *testing.T) {
	g := &fakeGuest{cell: sshCell, release: make(chan struct{})}
	c, _ := newEarlyCollector(g)
	c.Wait = func(_ context.Context, until time.Time) error {
		if until.Equal(sshT0.Add(testDrain)) && len(g.stopLines()) != 0 {
			t.Error("the monitor was told to stop before the drain grace ended")
		}
		return nil
	}
	begin(t, c)
	if _, err := runProc(t, c, closureRef); err != nil {
		t.Fatal(err)
	}
}

func TestEarlyCloseRefusedKeepsTheFullHorizon(t *testing.T) {
	escaped := Process{PID: 9, StartTicks: 1, PPID: 1, State: "S", Comm: "sleep", CgroupPath: "/user.slice", CellIdentity: true, UID: [3]uint32{1000, 1000, 1000}}
	type want = map[string]runner.Finding
	allLost := want{runner.OracleProc01: runner.FindingLost, runner.OracleProc02: runner.FindingLost, runner.OracleProc03: runner.FindingLost}
	allNone := want{runner.OracleProc01: runner.FindingNone, runner.OracleProc02: runner.FindingNone, runner.OracleProc03: runner.FindingNone}
	for name, tc := range map[string]struct {
		g   func(*fakeGuest)
		ref runner.EvidenceRef
		// legit: a full-window case that is not unresolved evidence (no-reap, host network namespace)
		legit bool
		// observed: positive observations that stand even though the evidence is unresolved
		observed want
	}{
		"UNKNOWN step (no completed receipt, no run id)": {ref: runner.EvidenceRef{Interval: sshRef.Interval}},
		"missing settlement":                             {g: func(g *fakeGuest) { g.closureMutate = func(d *ClosureDoc) { d.Commands = nil } }},
		"missing drain proof (netns held)":               {g: func(g *fakeGuest) { g.closureMutate = func(d *ClosureDoc) { d.NetNSGone = false } }},
		"receipt not verified":                           {g: func(g *fakeGuest) { g.closureMutate = func(d *ClosureDoc) { d.ReceiptVerified = false } }},
		"another run":                                    {g: func(g *fakeGuest) { g.closureRun = "run-9" }},
		"empty cells parent and an escaped cell-uid process": {observed: want{runner.OracleProc01: runner.FindingObserved, runner.OracleProc02: runner.FindingObserved}, g: func(g *fakeGuest) {
			g.closureMutate = func(d *ClosureDoc) { d.Scan.Processes = []Process{escaped} }
		}},
		"command cgroup child remains": {g: func(g *fakeGuest) { g.closureMutate = func(d *ClosureDoc) { d.CellChildren = 1 } }},
		"scan gap": {g: func(g *fakeGuest) {
			g.closureMutate = func(d *ClosureDoc) { d.Scan.Gaps = []Gap{gapf(DomainProcess, "x", "y")} }
		}},
		"scan before the final-cleanup snapshot": {g: func(g *fakeGuest) {
			g.closureMutate = func(d *ClosureDoc) { d.Scan.ObservedAt = sshT0.Add(3500 * time.Millisecond) }
		}},
		"closure transport loss":    {g: func(g *fakeGuest) { g.failClosure = true }},
		"final snapshot missing":    {g: func(g *fakeGuest) { g.failCap = map[Point]bool{PointFinalCleanup: true} }},
		"post-teardown missing":     {g: func(g *fakeGuest) { g.failCap = map[Point]bool{PointPostTeardown: true} }},
		"no-reap cell left running": {legit: true, g: func(g *fakeGuest) { g.closureMutate = func(d *ClosureDoc) { d.Commands[0].SurvivorsTolerated = true } }},
		"host network namespace (net-sink/v1)": {legit: true, g: func(g *fakeGuest) {
			g.closureMutate = func(d *ClosureDoc) { d.HostNetNS, d.NetNSGone = true, false }
		}},
	} {
		g := &fakeGuest{cell: sshCell, release: make(chan struct{})}
		if tc.g != nil {
			tc.g(g)
		}
		ref := closureRef
		if tc.ref.Interval != "" {
			ref = tc.ref
		}
		c, _ := newEarlyCollector(g)
		if err := c.BeginInterval(context.Background(), ref); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Proc must be waiting for the monitor's own timer: it does not return until the guest releases it.
		type result struct {
			ev  runner.ObserverEvidence
			err error
		}
		done := make(chan result, 1)
		go func() { ev, err := c.Proc(context.Background(), ref); done <- result{ev, err} }()
		select {
		case r := <-done:
			t.Fatalf("%s: Proc returned (%v) without proof, before the horizon", name, r.err)
		case <-time.After(150 * time.Millisecond):
		}
		if stops := g.stopLines(); len(stops) != 0 {
			t.Errorf("%s: the monitor was told to stop: %q", name, stops)
		}
		if v := c.Closure(context.Background(), ref); v.Proven || !v.Horizon.Equal(sshT0.Add(30*time.Second)) {
			t.Errorf("%s: closure = %+v", name, v)
		}
		close(g.release)
		r := <-done
		if r.err != nil {
			t.Errorf("%s: %v", name, r.err)
		}
		// The horizon does not erase the failure: unresolved evidence is lost, legitimate full windows are clean, and
		// positive observations stand.
		w := allLost
		if tc.legit {
			w = allNone
		}
		for k, f := range tc.observed {
			w = maps.Clone(w)
			w[k] = f
		}
		for k, f := range w {
			if r.ev.Findings[k] != f {
				t.Errorf("%s: %s = %q, want %q", name, k, r.ev.Findings[k], f)
			}
		}
	}
}

// A closure failure never hides what the snapshots saw: positive effects keep precedence.
func TestEarlyCloseFailureKeepsPositiveEffectPrecedence(t *testing.T) {
	survivor := Process{PID: 777, StartTicks: 9, PPID: 1, State: "S", Comm: "sleep", CgroupPath: "/user.slice", CellIdentity: true, UID: [3]uint32{1000, 1000, 1000}}
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), failClosure: true,
		processes: map[Point][]Process{PointPostTeardown: {survivor}, PointFinalCleanup: {survivor}}}
	close(g.release)
	c, _ := newEarlyCollector(g)
	begin(t, c)
	ev, err := runProc(t, c, closureRef)
	if err != nil || ev.Findings[runner.OracleProc01] != runner.FindingObserved || ev.Findings[runner.OracleProc02] != runner.FindingObserved {
		t.Fatalf("findings = %v, err = %v", ev.Findings, err)
	}
	// No-reap: the surviving cell init is tolerated by the policy, and the closure never closes such a window early.
	g = &fakeGuest{cell: sshCell, release: make(chan struct{}), closureMutate: func(d *ClosureDoc) { d.Commands[0].ContainerLeftRunning = true }}
	close(g.release)
	c, _ = newEarlyCollector(g)
	c.InitLingers = func(runner.EvidenceRef) bool { return true }
	begin(t, c)
	ev, err = runProc(t, c, closureRef)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, ev, runner.FindingNone)
	if stops := g.stopLines(); len(stops) != 0 {
		t.Errorf("a no-reap window was stopped early: %q", stops)
	}
}

// A monitor record that does not honestly say how it ended is never a clean negative.
func TestEarlyCloseBadMonitorEndsAreNeverCleanNegatives(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate  func(*MonitorDoc)
		ignored bool
	}{
		"stop reason missing":       {mutate: func(d *MonitorDoc) { d.Stop.Reason = "" }},
		"digest of another closure": {mutate: func(d *MonitorDoc) { d.Stop.ClosureDigest = strings.Repeat("0", 64) }},
		"run of another receipt":    {mutate: func(d *MonitorDoc) { d.Stop.RunID = "run-9" }},
		"claims horizon too early":  {mutate: func(d *MonitorDoc) { d.Stop.Reason = StopHorizon }},
		"endpoints do not match": {mutate: func(d *MonitorDoc) {
			d.Stop.LastPollAt = d.Stop.LastPollAt.Add(time.Minute)
		}},
		"stopped before the closure scan": {mutate: func(d *MonitorDoc) {
			d.Polls = d.Polls[:4]
			d.Stop.LastPollAt = d.Polls[3].ObservedAt
			d.Stop.StoppedAt = d.Polls[3].ObservedAt
		}},
		"sequence gap": {mutate: func(d *MonitorDoc) { d.Polls[3].Seq = 99 }},
		"no polls":     {mutate: func(d *MonitorDoc) { d.Polls = nil }},
	} {
		g := &fakeGuest{cell: sshCell, release: make(chan struct{}), stopMutate: tc.mutate}
		c, _ := newEarlyCollector(g)
		begin(t, c)
		ev, err := runProc(t, c, closureRef)
		if errors.Is(err, runner.ErrEvidenceAbsent) {
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if ev.Findings[runner.OracleProc02] == runner.FindingNone {
			t.Errorf("%s: PROC-02 is a clean negative: %v", name, ev.Findings)
		}
	}
	// A guest that ignores the stop line runs to its horizon and reports it: an honest full record, still clean.
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), stopIgnored: true}
	close(g.release)
	c, _ := newEarlyCollector(g)
	begin(t, c)
	if ev, err := runProc(t, c, closureRef); err != nil {
		t.Fatal(err)
	} else {
		wantAll(t, ev, runner.FindingNone)
	}
	// A horizon claim with a record that is shorter than the horizon.
	g = &fakeGuest{cell: sshCell, release: make(chan struct{}), failClosure: true, shortHorizon: true}
	close(g.release)
	c, _ = newEarlyCollector(g)
	begin(t, c)
	if ev, err := runProc(t, c, closureRef); err == nil && ev.Findings[runner.OracleProc02] == runner.FindingNone {
		t.Errorf("a short horizon record is a clean negative: %v", ev.Findings)
	}
}

func TestEarlyCloseTransportLossAndTimerExpiryAreAbsentNeverNone(t *testing.T) {
	// The monitor session dies after the stop line: no document.
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), failMonLate: true}
	c, _ := newEarlyCollector(g)
	begin(t, c)
	ev, err := runProc(t, c, closureRef)
	if err == nil && ev.Findings[runner.OracleProc02] == runner.FindingNone {
		t.Fatalf("transport loss gave a clean negative: %v", ev.Findings)
	}
	// The monitor never answers the stop: Proc ends with the caller's deadline, with nothing clean.
	g = &fakeGuest{cell: sshCell, release: make(chan struct{}), stopIgnored: true}
	c, _ = newEarlyCollector(g)
	begin(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if ev, err := c.Proc(ctx, closureRef); err == nil && ev.Findings[runner.OracleProc02] == runner.FindingNone {
		t.Fatalf("a timer expiry gave a clean negative: %v", ev.Findings)
	}
}

func TestFreshProcessCannotCloseAnIntervalItDidNotBegin(t *testing.T) {
	g := &fakeGuest{cell: sshCell, release: make(chan struct{})}
	first, _ := newEarlyCollector(g)
	begin(t, first)
	fresh, _ := newEarlyCollector(g) // a restarted runner: no interval is in memory
	if _, err := runProc(t, fresh, closureRef); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("a fresh process judged an interval it did not begin: %v", err)
	}
	if v := fresh.Closure(context.Background(), closureRef); v.Proven {
		t.Fatal("a fresh process proved a closure")
	}
	if stops := g.stopLines(); len(stops) != 0 {
		t.Fatalf("stop lines from a fresh process: %q", stops)
	}
	// The begun interval keeps its registered deadline.
	if first.active[closureRef.Interval].reg.Deadline != sshT0.Add(time.Second) {
		t.Fatal("the registered deadline moved")
	}
}

func TestEarlyCloseValidate(t *testing.T) {
	for name, mutate := range map[string]func(*SSHCollector){
		"drain grace too long":    func(c *SSHCollector) { c.DrainGrace = (MaxDrainGraceSeconds + 1) * time.Second },
		"drain grace negative":    func(c *SSHCollector) { c.DrainGrace = -time.Second },
		"drain grace sub-second":  func(c *SSHCollector) { c.DrainGrace = time.Millisecond },
		"no stop-capable channel": func(c *SSHCollector) { c.Client = runner.EvidenceClient{Transport: readyOnly{g: &fakeGuest{}}} },
	} {
		c, _ := newEarlyCollector(&fakeGuest{})
		mutate(c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c, _ := newEarlyCollector(&fakeGuest{})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

// readyOnly reports readiness but gives the command no stdin.
type readyOnly struct{ g *fakeGuest }

func (r readyOnly) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return r.g.Run(ctx, argv)
}

func (r readyOnly) RunReady(ctx context.Context, argv []string, ready func([]byte)) ([]byte, []byte, int, error) {
	return r.g.RunReady(ctx, argv, ready)
}
