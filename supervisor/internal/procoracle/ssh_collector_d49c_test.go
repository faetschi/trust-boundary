package procoracle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/runner"
)

// Review round 2: NET consumes the FINALIZED PROC decision, and a PROC that never began is unresolved for NET too.
// netoracle.Collector.EndDecision is the NET side of the end canary (the canary and journal need a Linux sink).

func netSide(c *SSHCollector) (*netoracle.Collector, *[]time.Time) {
	var waited []time.Time
	n := &netoracle.Collector{Gate: c, Horizon: 30 * time.Second, Now: func() time.Time { return sshT0 },
		Wait: func(_ context.Context, until time.Time) error { waited = append(waited, until); return nil }}
	return n, &waited
}

// netNegative is what the NET collector would record for a window with no arrival.
func netNegative(cl *runner.IntervalClosure) runner.Finding {
	f, _ := netoracle.BindClosure(map[string]runner.Finding{runner.OracleNet01: runner.FindingNone}, "d", cl)
	return f[runner.OracleNet01]
}

func TestNetClosesOnlyOnAFinalizedProcDecision(t *testing.T) {
	g := &fakeGuest{cell: sshCell, release: make(chan struct{})}
	c, _ := newEarlyCollector(g)
	begin(t, c)
	n, waited := netSide(c)
	cl, err := n.EndDecision(context.Background(), closureRef) // NET runs before PROC
	if err != nil || !cl.Proven || netNegative(cl) != runner.FindingNone {
		t.Fatalf("clean interval: %+v %v", cl, err)
	}
	// The monitor had already ended on the stop line and been validated when NET was told it may close.
	if stops := g.stopLines(); len(stops) != 1 {
		t.Fatalf("NET closed before the PROC monitor was stopped: %q", stops)
	}
	if len(*waited) == 0 || !(*waited)[len(*waited)-1].Equal(cl.DrainUntil) {
		t.Fatalf("waits = %v", *waited)
	}
	ev, err := runProc(t, c, closureRef)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, ev, runner.FindingNone)
}

func TestNetAndProcAgreeOnACoverageFailure(t *testing.T) {
	for name, g := range map[string]*fakeGuest{
		"timing gap (polls at 1, 2, 3 and 12 s)": {cell: sshCell, release: make(chan struct{}), stopPollSecs: []int{1, 2, 3, 12}},
		"a poll with recorded scan gaps":         {cell: sshCell, release: make(chan struct{}), pollGaps: map[int][]Gap{4: {gapf(DomainProcess, "unreadable", "pid 77")}}},
	} {
		c, _ := newEarlyCollector(g)
		begin(t, c)
		n, waited := netSide(c)
		cl, err := n.EndDecision(context.Background(), closureRef)
		if err != nil || cl.Proven || len(cl.Unresolved) == 0 {
			t.Fatalf("%s: NET was allowed to close: %+v %v", name, cl, err)
		}
		if !(*waited)[len(*waited)-1].Equal(cl.Horizon) {
			t.Errorf("%s: NET did not keep its horizon: %v", name, *waited)
		}
		if got := netNegative(cl); got != runner.FindingLost {
			t.Errorf("%s: NET negative = %q, want lost", name, got)
		}
		ev, err := runProc(t, c, closureRef)
		if err != nil || ev.Findings[runner.OracleProc02] != runner.FindingLost {
			t.Errorf("%s: PROC = %v %v", name, ev.Findings, err)
		}
	}
}

func TestMissingProcIntervalIsUnresolvedForNet(t *testing.T) {
	// proc-cell fails: BeginInterval is absent (GovernedC sends the request and marks the outcome partial).
	c, _ := newEarlyCollector(&fakeGuest{cell: sshCell, failCell: true})
	if err := c.BeginInterval(context.Background(), closureRef); !isAbsent(err) {
		t.Fatalf("Begin = %v", err)
	}
	n, waited := netSide(c)
	cl, err := n.EndDecision(context.Background(), closureRef)
	if err != nil || cl.Proven || len(cl.Unresolved) == 0 {
		t.Fatalf("closure = %+v %v", cl, err)
	}
	// NET keeps a horizon of its own, independent of PROC's startup, and its negative is lost.
	if len(*waited) != 1 || !(*waited)[0].Equal(sshT0.Add(30*time.Second)) {
		t.Fatalf("NET waited %v, want its own horizon", *waited)
	}
	if got := netNegative(cl); got != runner.FindingLost {
		t.Fatalf("NET negative = %q, want lost", got)
	}
}

func isAbsent(err error) bool { return err != nil && errors.Is(err, runner.ErrEvidenceAbsent) }

// Review round 3. NET's finalizer applies the same coverage validation as the evaluator, so a record the evaluator
// rejects cannot let NET close; a survivor seen after the closure scan revokes the proof; residual cell-uid sockets,
// even ones the policy authorizes for scoring, never authorize an early close.

func assertNetKeepsHorizon(t *testing.T, name string, g *fakeGuest, policy func(*SSHCollector)) (*SSHCollector, *runner.IntervalClosure) {
	t.Helper()
	c, _ := newEarlyCollector(g)
	if policy != nil {
		policy(c)
	}
	begin(t, c)
	n, waited := netSide(c)
	cl, err := n.EndDecision(context.Background(), closureRef)
	if err != nil || cl.Proven || len(cl.Unresolved) == 0 {
		t.Fatalf("%s: NET was allowed to close: %+v %v", name, cl, err)
	}
	if !(*waited)[len(*waited)-1].Equal(cl.Horizon) {
		t.Errorf("%s: NET did not keep its horizon: %v", name, *waited)
	}
	if got := netNegative(cl); got != runner.FindingLost {
		t.Errorf("%s: NET negative = %q, want lost", name, got)
	}
	return c, cl
}

func TestNetSideUsesTheEvaluatorsCoverageValidation(t *testing.T) {
	for name, mutate := range map[string]func(*MonitorDoc){
		"poll sequence gap":       func(d *MonitorDoc) { d.Polls[3].Seq = 99 },
		"poll of another cell":    func(d *MonitorDoc) { d.Polls[3].CellInode++ },
		"poll of another reg":     func(d *MonitorDoc) { d.Polls[3].RegistrationDigest = "00" },
		"first poll after MaxGap": func(d *MonitorDoc) { d.Polls = d.Polls[3:]; d.Stop.FirstPollAt = d.Polls[0].ObservedAt },
	} {
		g := &fakeGuest{cell: sshCell, release: make(chan struct{}), stopMutate: mutate}
		c, _ := assertNetKeepsHorizon(t, name, g, nil)
		if ev, err := runProc(t, c, closureRef); err != nil || ev.Findings[runner.OracleProc02] != runner.FindingLost {
			t.Errorf("%s: PROC = %v %v", name, ev.Findings, err)
		}
	}
}

func TestSurvivorSeenAfterTheClosureScanRevokesTheProof(t *testing.T) {
	// The scan at 5 s was clean; the healthy poll at 7 s sees a live escaped cell-identity process.
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), pollProcs: map[int][]Process{7: {escapedProc}}}
	c, _ := assertNetKeepsHorizon(t, "survivor after the scan", g, nil)
	ev, err := runProc(t, c, closureRef)
	if err != nil || ev.Findings[runner.OracleProc02] != runner.FindingObserved {
		t.Fatalf("the positive PROC observation was lost: %v %v", ev.Findings, err)
	}
	// An in-cell survivor of a post-scan poll is a PROC-01 observation as well.
	inCell := Process{PID: 11, StartTicks: 2, PPID: 1, State: "S", Comm: "sleep", InCell: true, CellIdentity: true, UID: [3]uint32{1000, 1000, 1000}}
	g = &fakeGuest{cell: sshCell, release: make(chan struct{}), pollProcs: map[int][]Process{7: {inCell}},
		stopMutate: func(d *MonitorDoc) { d.Polls[6].Populated = true }}
	c, _ = assertNetKeepsHorizon(t, "in-cell survivor after the scan", g, nil)
	if ev, err := runProc(t, c, closureRef); err != nil || ev.Findings[runner.OracleProc01] != runner.FindingObserved {
		t.Fatalf("PROC-01 = %v %v", ev.Findings, err)
	}
}

func TestResidualCellUIDSocketNeverAuthorizesAnEarlyClose(t *testing.T) {
	sock := Socket{Proto: "tcp", Local: "127.0.0.1:9", UID: 1000, Role: RoleConnected, State: "FIN_WAIT2"}
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), closureMutate: func(d *ClosureDoc) { d.Scan.Sockets = []Socket{sock} }}
	close(g.release) // unproven: the monitor runs to its own horizon
	// The same socket is authorized for EFF-PROC-03 scoring; that exemption must not license the NET close.
	assertNetKeepsHorizon(t, "ownerless socket", g, func(c *SSHCollector) { c.Policy.AuthorizedSockets = []string{"tcp 127.0.0.1:9"} })
}

// Review round 4.

func lingeringInit(pid int) Process {
	return Process{PID: pid, StartTicks: 3, PPID: 900, State: "S", Comm: CellInitComm, CgroupPath: sshCell.CellCgroupPath, InCell: true,
		CellIdentity: true, UID: [3]uint32{1000, 1000, 1000}, NSPid: []int{pid, 1}}
}

// A no-reap interval keeps its full horizon as a legitimate reason: the init that lingers by design is neither an
// unresolved survivor nor a revocation, so NET and PROC stay clean.
func TestNoReapLingeringInitIsLegitimateNotUnresolved(t *testing.T) {
	procs := map[int][]Process{}
	for i := 6; i <= 30; i++ {
		procs[i] = []Process{lingeringInit(50)}
	}
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), pollProcs: procs,
		closureMutate: func(d *ClosureDoc) { d.Commands[0].SurvivorsTolerated = true }}
	close(g.release)
	c, _ := newEarlyCollector(g)
	c.InitLingers = func(runner.EvidenceRef) bool { return true }
	begin(t, c)
	n, _ := netSide(c)
	cl, err := n.EndDecision(context.Background(), closureRef)
	if err != nil || cl.Proven || len(cl.Unresolved) != 0 || len(cl.Reasons) == 0 || netNegative(cl) != runner.FindingNone {
		t.Fatalf("no-reap closure: %+v %v", cl, err)
	}
	ev, err := runProc(t, c, closureRef)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, ev, runner.FindingNone)
}

// One reason per surviving process, not one per poll; and the retained record carries the effective decision.
func TestRevocationIsOneReasonPerProcessAndRecordedInTheTermination(t *testing.T) {
	procs := map[int][]Process{}
	for i := 7; i <= 13; i++ {
		procs[i] = []Process{escapedProc}
	}
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), pollProcs: procs}
	c, cl := assertNetKeepsHorizon(t, "survivor in seven polls", g, nil)
	if len(cl.Unresolved) != 1 {
		t.Fatalf("%d reasons for one process: %q", len(cl.Unresolved), cl.Unresolved)
	}
	if _, err := runProc(t, c, closureRef); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(c.RetainDir, RetainedName(closureRef.Interval)))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ParseRetained(raw)
	if err != nil || rec.Proven || len(rec.Unresolved) != 1 || len(rec.Reasons) == 0 {
		t.Fatalf("the termination record does not carry the revoked decision: %+v %v", rec, err)
	}
}

// The monitor must have observed through the drain grace: a stop whose record ends at the scan proves nothing.
func TestStopWithoutObservationThroughTheDrainGraceIsNotProven(t *testing.T) {
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), stopPollSecs: []int{1, 2, 3, 4, 5}}
	assertNetKeepsHorizon(t, "no polls inside the grace", g, nil)
	// The last poll is far from the stop instant.
	g = &fakeGuest{cell: sshCell, release: make(chan struct{}), stopMutate: func(d *MonitorDoc) { d.Stop.StoppedAt = d.Stop.StoppedAt.Add(5 * time.Second) }}
	assertNetKeepsHorizon(t, "stopped long after the last poll", g, nil)
}
