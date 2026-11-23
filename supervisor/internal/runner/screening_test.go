package runner

import (
	"errors"
	"slices"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

func screeningQuotas() evalcorpus.Quotas {
	return evalcorpus.Quotas{
		Adversarial: map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 0, evalcorpus.ClassTMB: 0, evalcorpus.ClassTMC: 0},
		Compliant:   map[evalcorpus.Tool]int{evalcorpus.ToolRead: 3, evalcorpus.ToolWrite: 3, evalcorpus.ToolEdit: 3, evalcorpus.ToolBash: 3},
		Variance:    map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 0, evalcorpus.ClassTMB: 0, evalcorpus.ClassTMC: 0},
		ColdPercent: 20,
	}
}

func screeningSchedule(t testing.TB, only ...string) Schedule {
	t.Helper()
	src, alloc := synth(t, screeningQuotas())
	cfg := testConfig()
	cfg.Purpose, cfg.OnlyCases = PurposeScreening, only
	s, err := Plan(src, alloc, cfg)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return s
}

func TestScreeningPlansOneBStepPerCompliantTask(t *testing.T) {
	src, alloc := synth(t, screeningQuotas())
	s := screeningSchedule(t)
	cmp := src.CaseIDs(evalcorpus.KindCompliant)
	if !s.Subset || s.Config.Purpose != PurposeScreening || len(s.Steps) != len(cmp) {
		t.Fatalf("subset %t, purpose %q, %d steps; want %d", s.Subset, s.Config.Purpose, len(s.Steps), len(cmp))
	}
	var order []string
	for i, st := range s.Steps {
		if st.Index != i || st.Kind != evalcorpus.KindCompliant || st.Configuration != CfgB || st.Role != RoleCompliant {
			t.Fatalf("step %d is %s/%s/%s, want a B compliant step", i, st.CaseID, st.Configuration, st.Role)
		}
		order = append(order, st.CaseID)
	}
	if want := rankOrder("runner-test-seed", "order/compliant", cmp); !slices.Equal(order, want) {
		t.Fatalf("order %v, want the seeded compliant rank order %v", order, want)
	}
	study, err := Plan(src, alloc, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if s.Digest == study.Digest {
		t.Fatal("a screening plan has the study's plan digest")
	}
}

func TestScreeningOnlyCasesSelectsASubset(t *testing.T) {
	all := screeningSchedule(t)
	pick := []string{all.Steps[1].CaseID, all.Steps[4].CaseID}
	s := screeningSchedule(t, pick...)
	if len(s.Steps) != 2 || s.Steps[0].CaseID != all.Steps[1].CaseID || s.Steps[1].CaseID != all.Steps[4].CaseID {
		t.Fatalf("steps %+v, want %v in rank order", s.Steps, pick)
	}
	if s.Digest == all.Digest {
		t.Fatal("a subset keeps the full plan digest")
	}
}

func TestScreeningRefusals(t *testing.T) {
	mixed, mixedAlloc := synth(t, smallQuotas())
	cfg := testConfig()
	cfg.Purpose = PurposeScreening
	if _, err := Plan(mixed, mixedAlloc, cfg); err == nil {
		t.Error("a manifest with adversarial and ambiguous cases was planned")
	}
	cfg.CDH = testCDH(t)
	if _, err := Plan(mixed, mixedAlloc, cfg); err == nil {
		t.Error("a C-DH screening run was planned")
	}
	src, alloc := synth(t, screeningQuotas())
	cfg = testConfig()
	cfg.Purpose, cfg.OnlyCases = PurposeScreening, []string{"unknown-case"}
	if _, err := Plan(src, alloc, cfg); err == nil {
		t.Error("OnlyCases naming an unknown case was planned")
	}
}

func TestScreeningHasNoExportCalibrationOrRerun(t *testing.T) {
	s := screeningSchedule(t)
	j, _ := finishedJournal(t, s, NewFakeExecutor(s.Config))
	if _, err := Export(s, j, ExportOptions{}); !errors.Is(err, ErrNonConfirmatoryPurpose) {
		t.Fatalf("Export: %v", err)
	}
	if _, err := Calibrations(s, j); !errors.Is(err, ErrNonConfirmatoryPurpose) {
		t.Fatalf("Calibrations: %v", err)
	}
	if err := rerunEligible(s, j, s.Steps[0], ""); err != nil {
		t.Fatalf("rerunEligible: %v", err)
	}
}

func TestScreeningScheduleRefusesAnotherStep(t *testing.T) {
	s := screeningSchedule(t)
	s.Steps[0].Configuration = CfgC
	if err := s.validatePurposeSteps(); err == nil {
		t.Fatal("a C step passed the screening purpose check")
	}
}
