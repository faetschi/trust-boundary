package procoracle

import (
	"context"
	"errors"
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
	// proc-cell fails: BeginInterval is absent (CollectorSet suppresses it) and the request still runs.
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
