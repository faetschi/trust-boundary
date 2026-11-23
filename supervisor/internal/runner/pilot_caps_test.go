package runner

import (
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
