package runner

import (
	"context"
	"testing"

	"tbound/supervisor/internal/calibration"
)

func TestPilotCasesReadsEveryAdaptivePairInPlanOrder(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	missed := firstCase(s, "adversarial")
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, o *Outcome) error {
		if st.CaseID == missed && st.Role == RoleAdaptiveReplay && st.Configuration == CfgP {
			for k := range o.Oracles {
				o.Oracles[k] = FindingNone
			}
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()

	var want []string
	for _, st := range s.Steps {
		if st.Role == RoleAdaptiveReplay && st.Configuration == CfgP {
			want = append(want, st.CaseID)
		}
	}
	got := PilotCases(s, j)
	if len(got) != len(want) || len(got) < 2 {
		t.Fatalf("got %d designated cases, want the %d adaptive P cases (at least 2)", len(got), len(want))
	}
	for i, c := range got {
		wantStatus := calibration.Manifested
		if c.CaseID == missed {
			wantStatus = calibration.ChannelNonDiscriminative
		}
		if c.CaseID != want[i] || c.Status != wantStatus || c.Adaptive.Attempts != 3 {
			t.Fatalf("case %d = %+v; want %s, status %s, 3 adaptive attempts", i, c, want[i], wantStatus)
		}
	}
}

func TestPilotCasesReadsARerunCaseAtItsScoredAttempt(t *testing.T) {
	s, j, caseID := adaptiveBudgetJournal(t, nil, consistentReceipts)
	for _, c := range PilotCases(s, j) {
		if c.CaseID != caseID {
			continue
		}
		if c.Status != calibration.Manifested || c.Adaptive.ToolCalls != 3 || c.Adaptive.WallMS != 300 {
			t.Fatalf("rerun case %+v; want manifested with the P run's cumulative 3 tool calls and 300 ms", c)
		}
		return
	}
	t.Fatalf("case %s is not a designated case", caseID)
}

func TestPilotCasesMarksAnUnfinishedCaseNotRun(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	stop := findStep(t, s, firstCase(s, "adversarial"), RoleAdaptiveReplay, CfgP)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, o *Outcome) error {
		if st.ID() == stop.ID() {
			cancel()
		}
		return nil
	}
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	_, _ = mustRunner(t, s, j, fake, Options{}).Run(ctx)
	got := PilotCases(s, j)
	last := got[len(got)-1]
	if last.Status != calibration.NotRun || last.Reason == "" {
		t.Fatalf("the last case of a run stopped early is %+v; want not_run with a reason", last)
	}
	if _, err := calibration.DerivePilotCaps(got, 5, true, 1_200_000); err == nil {
		t.Fatal("a pilot with an unfinished case derived caps")
	}
}
