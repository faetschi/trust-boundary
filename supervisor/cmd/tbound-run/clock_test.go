package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
)

// skewedGuest answers proc-cell and proc-capture like the guest evidence command, with its clock
// offset from the host's.
type skewedGuest struct {
	offset time.Duration
	calls  int
}

func (g *skewedGuest) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	g.calls++
	var doc any
	switch argv[0] {
	case runner.EvidenceProcCell:
		doc = procoracle.CellDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcCell, CellID: "/cell", CellCgroupPath: "/c",
			CellCgroupInode: 7, CellUID: 1000, CellGID: 1000}
	case runner.EvidenceProcCapture:
		reg := argv[slices.Index(argv, "--registration")+1]
		doc = procoracle.CaptureDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcCapture, Point: procoracle.PointPostTeardown,
			RegistrationDigest: reg, Snapshot: procoracle.Snapshot{Seq: 1, Point: procoracle.PointPostTeardown,
				ObservedAt: time.Now().Add(g.offset), RegistrationDigest: reg}}
	default:
		return nil, []byte("unexpected"), 2, nil
	}
	raw, err := json.Marshal(doc)
	return append(raw, '\n'), nil, 0, err
}

type countingPrepare struct {
	runner.Executor
	prepared int
}

func (c *countingPrepare) Prepare(context.Context, runner.Step) error {
	c.prepared++
	return nil
}

func guarded(g *skewedGuest, inner runner.Executor) clockGuard {
	ev := runner.EvidenceClient{Transport: g}
	return clockGuard{Executor: inner, sample: func(ctx context.Context) (clockSample, error) {
		return sampleGuestClock(ctx, ev, 1000, 1000)
	}}
}

func TestClockGuardRefusesAnAdversarialStepWhenTheGuestClockIsOff(t *testing.T) {
	adversarial := runner.Step{CaseID: "a", Kind: evalcorpus.KindAdversarial}
	for _, offset := range []time.Duration{700 * time.Millisecond, -700 * time.Millisecond} {
		inner := &countingPrepare{}
		err := guarded(&skewedGuest{offset: offset}, inner).Prepare(context.Background(), adversarial)
		if !errors.Is(err, runner.ErrSetupRefused) || !strings.Contains(err.Error(), "w32tm") || inner.prepared != 0 {
			t.Fatalf("offset %s: err %v, inner prepared %d; want a refusal that names the resync and stops before the step", offset, err, inner.prepared)
		}
	}
	inner := &countingPrepare{}
	if err := guarded(&skewedGuest{}, inner).Prepare(context.Background(), adversarial); err != nil || inner.prepared != 1 {
		t.Fatalf("an aligned clock: err %v, inner prepared %d", err, inner.prepared)
	}
	g := &skewedGuest{offset: time.Hour}
	if err := guarded(g, &countingPrepare{}).Prepare(context.Background(), runner.Step{CaseID: "c", Kind: evalcorpus.KindCompliant}); err != nil || g.calls != 0 {
		t.Fatalf("a compliant step, which PROC does not observe: err %v, %d clock reads", err, g.calls)
	}
}

func TestWiredAdversarialConfigurationsCheckTheClock(t *testing.T) {
	e := newEnv(t)
	e.write()
	ex, err := wireSSH(plan{cfg: mustConfig(t, e), sched: e.sched()})
	if err != nil {
		t.Fatal(err)
	}
	for name, x := range map[string]runner.Executor{"C": ex.C, "P": ex.P} {
		if _, ok := x.(clockGuard); !ok {
			t.Errorf("%s is %T, want the clock guard outermost", name, x)
		}
	}
}

// unguarded is the executor under the clock guard.
func unguarded(x runner.Executor) runner.Executor {
	if g, ok := x.(clockGuard); ok {
		return g.Executor
	}
	return x
}
