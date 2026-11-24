package runner

import (
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
)

func planWithV(t testing.TB, planV bool) Schedule {
	t.Helper()
	src, alloc := synth(t, legacyThesisQuotas())
	cfg := testConfig()
	cfg.PlanV = planV
	s, err := Plan(src, alloc, cfg)
	if err != nil {
		t.Fatalf("Plan(PlanV=%t): %v", planV, err)
	}
	return s
}

func compliantByCase(s Schedule) (order []string, configs map[string][]Configuration) {
	configs = map[string][]Configuration{}
	for _, st := range s.Steps {
		if st.Role != RoleCompliant || st.Kind != evalcorpus.KindCompliant {
			continue
		}
		if _, seen := configs[st.CaseID]; !seen {
			order = append(order, st.CaseID)
		}
		configs[st.CaseID] = append(configs[st.CaseID], st.Configuration)
	}
	return order, configs
}

func TestPlanVOffKeepsTheVFreePlan(t *testing.T) {
	s := planWithV(t, false)
	if s.Digest != thesisSchedule(t).Digest {
		t.Fatalf("PlanV off changed the plan digest to %s", s.Digest)
	}
	if got := len(s.Steps); got != 1835 || s.PlannedUnits != 1835 || s.Config.RerunHeadroom != 92 {
		t.Fatalf("units %d, planned %d, headroom %d; want 1835, 1835, 92", got, s.PlannedUnits, s.Config.RerunHeadroom)
	}
	order, configs := compliantByCase(s)
	bFirst := 0
	for i, id := range order {
		want := []Configuration{CfgB, CfgC}
		if i%2 == 1 {
			want = []Configuration{CfgC, CfgB}
		} else {
			bFirst++
		}
		if !slices.Equal(configs[id], want) {
			t.Fatalf("task %d %s runs %v, want %v", i, id, configs[id], want)
		}
	}
	if len(order) != 100 || bFirst != 50 {
		t.Fatalf("%d compliant tasks, B first in %d; want 100 and 50", len(order), bFirst)
	}
}

func TestPlanVOnPlansTheSixSeededOrders(t *testing.T) {
	off := planWithV(t, false)
	on := planWithV(t, true)
	if on.Digest == off.Digest {
		t.Fatal("PlanV on keeps the V-free plan digest")
	}
	if got := len(on.Steps); got != 1935 || on.PlannedUnits != 1935 || on.Config.RerunHeadroom != 97 || !on.Config.PlanV {
		t.Fatalf("units %d, planned %d, headroom %d, PlanV %t; want 1935, 1935, 97, true", got, on.PlannedUnits, on.Config.RerunHeadroom, on.Config.PlanV)
	}
	offOrder, _ := compliantByCase(off)
	onOrder, configs := compliantByCase(on)
	if !slices.Equal(onOrder, offOrder) {
		t.Fatal("PlanV on changes the seeded task order")
	}
	f32 := [6][3]Configuration{
		{CfgB, CfgC, CfgV}, {CfgB, CfgV, CfgC}, {CfgC, CfgB, CfgV},
		{CfgC, CfgV, CfgB}, {CfgV, CfgB, CfgC}, {CfgV, CfgC, CfgB},
	}
	bBeforeC, vBeforeB := 0, 0
	for i, id := range onOrder {
		got := configs[id]
		if !slices.Equal(got, f32[i%6][:]) {
			t.Fatalf("task %d %s runs %v, want %v", i, id, got, f32[i%6])
		}
		if slices.Index(got, CfgB) < slices.Index(got, CfgC) {
			bBeforeC++
		}
		if slices.Index(got, CfgV) < slices.Index(got, CfgB) {
			vBeforeB++
		}
	}
	if len(onOrder) != 100 || bBeforeC != 50 || vBeforeB != 49 {
		t.Fatalf("%d tasks, B before C in %d, V before B in %d; want 100, 50, 49", len(onOrder), bBeforeC, vBeforeB)
	}
	for _, st := range off.Steps {
		if st.Configuration == CfgV {
			t.Fatalf("PlanV off planned V step %s", st.ID())
		}
	}
}

func TestPlanVIsRefusedWithTheScreeningPurpose(t *testing.T) {
	src, alloc := synth(t, smallQuotas())
	cfg := testConfig()
	cfg.Purpose, cfg.PlanV = PurposeScreening, true
	if _, err := Plan(src, alloc, cfg); err == nil {
		t.Fatal("a compliant-screening plan accepted PlanV")
	}
}

func TestPlannedUnitsFollowThePlanV(t *testing.T) {
	src, alloc := synth(t, legacyThesisQuotas())
	if off, on := evalreport.PlannedUnits(alloc, false), evalreport.PlannedUnits(alloc, true); off != 1835 || on != 1935 {
		t.Fatalf("legacy PlannedUnits off %d, on %d; want 1835, 1935", off, on)
	}
	for _, m := range []*evalcorpus.Manifest{&src, &alloc} {
		m.Pins.DrawDigest = digestOf("draw")
	}
	for _, planV := range []bool{false, true} {
		cfg := testConfig()
		cfg.PlanV = planV
		s, err := Plan(src, alloc, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if want := evalreport.PlannedUnits(alloc, planV); s.Units() != want || s.PlannedUnits != want {
			t.Fatalf("PlanV %t: plan holds %d units, PlannedUnits %d", planV, s.Units(), want)
		}
	}
}

func TestPlanVIsRefusedWhereNoCompliantTaskIsPlanned(t *testing.T) {
	src, alloc := synth(t, legacyThesisQuotas())
	witness := testConfig()
	witness.Purpose, witness.PlanV = PurposeConstructWitness, true
	if _, err := Plan(src, alloc, witness); err == nil || !strings.Contains(err.Error(), "PlanV") {
		t.Errorf("a construct-witness run with PlanV: %v, want the PlanV refusal", err)
	}
	cdh := testConfig()
	cdh.PlanV, cdh.CDH = true, &CDHConfig{}
	if _, err := Plan(src, alloc, cdh); err == nil || !strings.Contains(err.Error(), "PlanV") {
		t.Errorf("a C-DH run with PlanV: %v, want the PlanV refusal", err)
	}
}
