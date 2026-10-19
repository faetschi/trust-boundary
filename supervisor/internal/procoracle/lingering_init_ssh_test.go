package procoracle

import (
	"context"
	"testing"

	"tbound/supervisor/internal/runner"
)

// The collector decides per interval whether the run is a no-reap one: InitLingers gets the step's
// EvidenceRef. Nothing else turns the exclusion on.
func TestSSHCollectorExcludesTheCellInitOnlyWhenTheIntervalIsNoReap(t *testing.T) {
	init := initProc(initPID)
	sleeper := descendant(5100, initPID, "sleep")
	cases := map[string]struct {
		lingers func(runner.EvidenceRef) bool
		procs   []Process
		want    runner.Finding
	}{
		"no-reap, only the init":       {func(runner.EvidenceRef) bool { return true }, []Process{init}, runner.FindingNone},
		"no-reap, a detached sleep":    {func(runner.EvidenceRef) bool { return true }, []Process{init, sleeper}, runner.FindingObserved},
		"not no-reap, only the init":   {func(runner.EvidenceRef) bool { return false }, []Process{init}, runner.FindingObserved},
		"no hook configured, the init": {nil, []Process{init}, runner.FindingObserved},
	}
	for name, c := range cases {
		g := &fakeGuest{cell: sshCell, processes: map[Point][]Process{PointPostTeardown: c.procs, PointFinalCleanup: c.procs}}
		col, _ := newSSHCollector(g)
		col.InitLingers = c.lingers
		if err := col.BeginInterval(context.Background(), sshRef); err != nil {
			t.Fatal(err)
		}
		ev, err := col.Proc(context.Background(), sshRef)
		if err != nil {
			t.Fatal(err)
		}
		if got := ev.Findings[runner.OracleProc01]; got != c.want {
			t.Errorf("%s: PROC-01 = %q, want %q", name, got, c.want)
		}
	}
}

func TestSSHCollectorAsksInitLingersAboutTheIntervalsStep(t *testing.T) {
	g := &fakeGuest{cell: sshCell}
	col, _ := newSSHCollector(g)
	var asked []runner.EvidenceRef
	col.InitLingers = func(ref runner.EvidenceRef) bool { asked = append(asked, ref); return false }
	ref := runner.EvidenceRef{Interval: sshRef.Interval, Step: runner.Step{CaseID: "case-7", Configuration: runner.CfgP}}
	if err := col.BeginInterval(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := col.Proc(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0].Step.CaseID != "case-7" || asked[0].Step.Configuration != runner.CfgP {
		t.Fatalf("asked %+v", asked)
	}
}
