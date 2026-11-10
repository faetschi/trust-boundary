package runner

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
)

func thesisSchedule(t testing.TB) Schedule {
	t.Helper()
	src, alloc := synth(t, evalcorpus.ThesisQuotas())
	s, err := Plan(src, alloc, testConfig())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return s
}

func TestPlanThesisUnitCounts(t *testing.T) {
	s := thesisSchedule(t)
	if evalreport.PlannedUnits != 1935 {
		t.Fatalf("evalreport.PlannedUnits = %d, want 1935", evalreport.PlannedUnits)
	}
	if got := len(s.Steps); got != evalreport.PlannedUnits {
		t.Fatalf("units = %d, want %d", got, evalreport.PlannedUnits)
	}
	byRole := map[Role]int{}
	byCfg := map[Configuration]int{}
	ids := map[string]bool{}
	for i, st := range s.Steps {
		byRole[st.Role]++
		byCfg[st.Configuration]++
		if st.Index != i {
			t.Fatalf("step %d has index %d", i, st.Index)
		}
		if st.Attempt != 1 {
			t.Fatalf("step %d attempt %d", i, st.Attempt)
		}
		if ids[st.ID()] {
			t.Fatalf("duplicate step id %s", st.ID())
		}
		ids[st.ID()] = true
	}
	want := map[Role]int{
		RoleTrajectory: 299, RoleStaticReplay: 598, RoleAdaptiveReplay: 598,
		RoleCompliant: 300, RoleAmbiguous: 40, RoleVarianceRepeat: 100,
	}
	for role, n := range want {
		if byRole[role] != n {
			t.Errorf("role %s: %d steps, want %d", role, byRole[role], n)
		}
	}
	// 299 trajectories + 299*2 static/adaptive C replays + 100 compliant C + 20 ambiguous C + 100 repeats.
	wantCfg := map[Configuration]int{CfgC: 299 + 598 + 100 + 20 + 100, CfgP: 598, CfgB: 100 + 20, CfgV: 100}
	for c, n := range wantCfg {
		if byCfg[c] != n {
			t.Errorf("configuration %s: %d steps, want %d", c, byCfg[c], n)
		}
	}
	if byCfg[CfgA] != 0 {
		t.Errorf("configuration A is outside the 1,935 units but %d steps were planned", byCfg[CfgA])
	}
}

func TestPlanDeterministic(t *testing.T) {
	a, b := thesisSchedule(t), thesisSchedule(t)
	if a.Digest == "" || a.Digest != b.Digest {
		t.Fatalf("digests %q vs %q", a.Digest, b.Digest)
	}
	for i := range a.Steps {
		if !reflect.DeepEqual(a.Steps[i], b.Steps[i]) {
			t.Fatalf("step %d differs", i)
		}
	}
	// A different seed changes the order, hence the digest.
	src, alloc := synth(t, evalcorpus.ThesisQuotas())
	src.SplitSeed, alloc.SplitSeed = "other", "other"
	alloc, err := evalcorpus.Allocate(src, evalcorpus.ThesisQuotas())
	if err != nil {
		t.Fatal(err)
	}
	c, err := Plan(src, alloc, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if c.Digest == a.Digest {
		t.Fatal("different seed gave the same plan digest")
	}
	// A protocol knob is part of the digest.
	cfg := testConfig()
	cfg.NET02Mandatory = true
	src, alloc = synth(t, evalcorpus.ThesisQuotas())
	d, err := Plan(src, alloc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if d.Digest == a.Digest {
		t.Fatal("NET-02 flag is not bound by the plan digest")
	}
}

func TestPlanPerCaseOrdering(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	type want struct {
		role Role
		cfg  Configuration
	}
	seq := []want{
		{RoleTrajectory, CfgC},
		{RoleStaticReplay, CfgP}, {RoleStaticReplay, CfgC},
		{RoleAdaptiveReplay, CfgP}, {RoleAdaptiveReplay, CfgC},
	}
	i := 0
	for i < len(s.Steps) && s.Steps[i].Kind == evalcorpus.KindAdversarial && s.Steps[i].Role != RoleVarianceRepeat {
		for k, w := range seq {
			st := s.Steps[i+k]
			if st.CaseID != s.Steps[i].CaseID || st.Role != w.role || st.Configuration != w.cfg {
				t.Fatalf("step %d: got %s/%s/%s, want %s/%s/%s", i+k, st.CaseID, st.Role, st.Configuration, s.Steps[i].CaseID, w.role, w.cfg)
			}
			if st.Predicate != "EFF-WS-01" || st.WitnessID != "wit-"+st.CaseID {
				t.Fatalf("step %d lacks the pair identity: %+v", i+k, st)
			}
		}
		i += len(seq)
	}
	if i != 11*5 {
		t.Fatalf("adversarial block ends at %d, want %d", i, 11*5)
	}
	// Variance repeats come after every primary unit, two per variance case, in C.
	reps := 0
	for _, st := range s.Steps[i:] {
		if st.Role == RoleVarianceRepeat {
			reps++
			if st.Configuration != CfgC || st.Repeat < 1 || st.Repeat > 2 {
				t.Fatalf("bad variance step %+v", st)
			}
		}
	}
	if reps != 6 {
		t.Fatalf("variance repeats = %d, want 6", reps)
	}
	last := s.Steps[len(s.Steps)-6:]
	for _, st := range last {
		if st.Role != RoleVarianceRepeat {
			t.Fatalf("variance repeats are not the final block: %+v", st)
		}
	}
	// Every variance repeat belongs to a variance-subset case of the allocation.
	_, alloc := synth(t, smallQuotas())
	inVar := map[string]bool{}
	for _, c := range alloc.Cases {
		if c.Adversarial != nil && c.Adversarial.VarianceSubset {
			inVar[c.CaseID] = true
		}
	}
	for _, st := range last {
		if !inVar[st.CaseID] {
			t.Fatalf("%s is not in the variance subset", st.CaseID)
		}
	}
}

func TestPlanCompliantPairOrderBalanced(t *testing.T) {
	s := thesisSchedule(t)
	orders := map[string]int{}
	byTask := map[string]string{}
	for _, st := range s.Steps {
		if st.Role == RoleCompliant {
			byTask[st.CaseID] += string(st.Configuration)
		}
	}
	if len(byTask) != 100 {
		t.Fatalf("%d compliant tasks", len(byTask))
	}
	for _, o := range byTask {
		if len(o) != 3 || !strings.Contains(o, "B") || !strings.Contains(o, "C") || !strings.Contains(o, "V") {
			t.Fatalf("task order %q is not a permutation of B, C, V", o)
		}
		orders[o]++
	}
	if len(orders) != 6 {
		t.Fatalf("%d distinct orders, want all 6", len(orders))
	}
	for o, n := range orders {
		if n < 16 || n > 17 {
			t.Errorf("order %s used %d times, want 16 or 17", o, n)
		}
	}
}

func TestPlanRefusals(t *testing.T) {
	src, alloc := synth(t, smallQuotas())
	cases := map[string]func(*Config, *evalcorpus.Manifest){
		"empty freeze commit":   func(c *Config, _ *evalcorpus.Manifest) { c.FreezeCommit = "" },
		"non-hex freeze commit": func(c *Config, _ *evalcorpus.Manifest) { c.FreezeCommit = "main" },
		"unit cap above 1935":   func(c *Config, _ *evalcorpus.Manifest) { c.UnitCap = 1936 },
		"unknown only-case":     func(c *Config, _ *evalcorpus.Manifest) { c.OnlyCases = []string{"nope"} },
		"allocation differs": func(_ *Config, a *evalcorpus.Manifest) {
			a.Cases = a.Cases[1:]
		},
		"unallocated adversarial": func(_ *Config, a *evalcorpus.Manifest) {
			c := *a.Cases[0].Adversarial
			c.Partition = ""
			a.Cases[0].Adversarial = &c
		},
	}
	for name, mut := range cases {
		cfg := smallConfig()
		a := alloc
		a.Cases = append([]evalcorpus.Case(nil), alloc.Cases...)
		mut(&cfg, &a)
		if _, err := Plan(src, a, cfg); err == nil {
			t.Errorf("%s: Plan succeeded", name)
		}
	}
	cfg := smallConfig()
	cfg.UnitCap = 88
	if _, err := Plan(src, alloc, cfg); !errors.Is(err, ErrOverPlanned) {
		t.Errorf("unit cap below the plan: got %v, want ErrOverPlanned", err)
	}
}

func TestPlanSubsetForDryRun(t *testing.T) {
	src, alloc := synth(t, smallQuotas())
	cfg := smallConfig()
	cfg.OnlyCases = []string{"adv-TM-A-001", "adv-TM-C-002"}
	s, err := Plan(src, alloc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Subset || len(s.Steps) != 10 {
		t.Fatalf("subset=%v units=%d, want true/10", s.Subset, len(s.Steps))
	}
	full := smallSchedule(t, smallConfig())
	if s.Digest == full.Digest || full.Subset {
		t.Fatal("subset and full plans must be distinguishable")
	}
}

// The study's own plans keep their digests: an empty purpose is omitted from the digested config, so the bytes the
// registered digests cover do not change. The values were computed before the purpose member was added.
func TestNormalPlanDigestsAreUnchanged(t *testing.T) {
	if got, want := smallSchedule(t, smallConfig()).Digest, "sha256:55b45cbe07bc55f1525c8e9d00692688c418423022bbbadeeb7f0d8542776b8b"; got != want {
		t.Errorf("small plan digest %s, want %s", got, want)
	}
	if got, want := thesisSchedule(t).Digest, "sha256:37faf9c235a4c570d7af07724cc729f282b5486fd907b0e36977e18092ac7c4d"; got != want {
		t.Errorf("thesis plan digest %s, want %s", got, want)
	}
}

// A construct-witness plan holds exactly one static P replay per adversarial case of the admitted manifest, and nothing
// else: no trajectory, adaptive replay, compliant or ambiguous run, or variance repeat.
func TestConstructWitnessPlansOneStaticPPerAdversarialCase(t *testing.T) {
	src, alloc := synth(t, evalcorpus.ThesisQuotas())
	cfg := testConfig()
	cfg.Purpose = PurposeConstructWitness
	s, err := Plan(src, alloc, cfg)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	adv := src.CaseIDs(evalcorpus.KindAdversarial)
	if !s.Subset || s.Config.Purpose != PurposeConstructWitness || len(s.Steps) != len(adv) {
		t.Fatalf("subset %t, purpose %q, %d units; want a witness subset of %d units", s.Subset, s.Config.Purpose, len(s.Steps), len(adv))
	}
	planned := map[string]int{}
	for i, st := range s.Steps {
		if st.Index != i || st.Kind != evalcorpus.KindAdversarial || st.Configuration != CfgP || st.Role != RoleStaticReplay {
			t.Fatalf("step %d is %s/%s/%s, want a static P replay", i, st.CaseID, st.Role, st.Configuration)
		}
		planned[st.CaseID]++
	}
	for _, id := range adv {
		if planned[id] != 1 {
			t.Errorf("case %s has %d static P steps, want 1", id, planned[id])
		}
	}
	if s.Digest == thesisSchedule(t).Digest {
		t.Fatal("a witness run has the study's plan digest")
	}
}

// Same steps, only Config.Purpose differs: the purpose must change the digest on its own, not through the steps.
func TestPurposeAloneChangesThePlanDigest(t *testing.T) {
	w := witnessSchedule(t)
	noPurpose := w
	noPurpose.Config.Purpose = ""
	witnessDigest, err := planDigest(w)
	if err != nil {
		t.Fatalf("planDigest: %v", err)
	}
	noPurposeDigest, err := planDigest(noPurpose)
	if err != nil {
		t.Fatalf("planDigest: %v", err)
	}
	if witnessDigest == noPurposeDigest {
		t.Fatalf("clearing only the purpose keeps the plan digest %s", noPurposeDigest)
	}
}

// A construct-witness run is refused with the config it cannot honour: C-DH, a subset of cases, or a purpose it does
// not know. The planner refuses them, so no journal is written for them.
func TestConstructWitnessRefusals(t *testing.T) {
	src, alloc := synth(t, smallQuotas())
	cases := map[string]func(*Config){
		"subset":          func(c *Config) { c.Purpose = PurposeConstructWitness; c.OnlyCases = []string{"adv-TM-A-001"} },
		"C-DH":            func(c *Config) { c.Purpose = PurposeConstructWitness; c.CDH = testCDH(t) },
		"unknown purpose": func(c *Config) { c.Purpose = "study" },
	}
	for name, mut := range cases {
		cfg := testConfig()
		mut(&cfg)
		if _, err := Plan(src, alloc, cfg); err == nil {
			t.Errorf("%s: Plan succeeded", name)
		}
	}
}
