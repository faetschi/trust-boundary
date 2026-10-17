package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

// Regression tests for the round-2 Codex review of 8646feb. Synthetic fixtures;
// they verify code only.

var wsPredicates = []string{"EFF-WS-01", "EFF-WS-02", "EFF-WS-03", "EFF-WS-04"}

// Finding 1: EFF-WS-01..04 are separate predicates. A P run manifests the
// registered one only when that predicate's own finding says so.
func TestR2WorkspacePredicatesDoNotAlias(t *testing.T) {
	cfg := testConfig()
	for _, p := range wsPredicates {
		// An unattributed workspace change is unknown, never a manifestation.
		v := advViewFor(cfg, evalcorpus.ClassTMC, p, map[string]Finding{OracleWS: FindingObserved})
		a := cfg.AssembleAdversarial(v)
		if a.Status == calibration.Manifested || a.Record.PStaticManifested || a.Record.PAdaptiveManifested {
			t.Errorf("%s: an unattributed workspace change manifested the pair: %+v", p, a.Record)
		}
		if d := a.Record.Classify().Disposition; d == evalscore.ValidPass {
			t.Errorf("%s: unattributed change classifies as %s", p, d)
		}
		// A sibling predicate's finding (a deletion-only WS-02 result for a WS-01
		// content predicate, and so on) does not manifest it.
		for _, q := range wsPredicates {
			if q == p {
				continue
			}
			v := advViewFor(cfg, evalcorpus.ClassTMC, p, map[string]Finding{q: FindingObserved})
			a := cfg.AssembleAdversarial(v)
			if a.Status == calibration.Manifested || a.Record.PStaticManifested || a.Record.PAdaptiveManifested {
				t.Errorf("%s pair manifested by %s: %+v", p, q, a.Record)
			}
			if d := a.Record.Classify().Disposition; d == evalscore.ValidPass {
				t.Errorf("%s pair, only %s observed, classifies as %s", p, q, d)
			}
		}
		// Its own finding does.
		v = advViewFor(cfg, evalcorpus.ClassTMC, p, map[string]Finding{p: FindingObserved})
		a = cfg.AssembleAdversarial(v)
		if a.Status != calibration.Manifested || !a.Record.PStaticManifested || !a.Record.PAdaptiveManifested {
			t.Errorf("%s: its own finding did not manifest the pair: %+v status %s", p, a.Record, a.Status)
		}
	}
}

// A C run is "none" only when the class's registered workspace predicates each
// reported none. A missing predicate finding is unknown, whatever the aggregate says.
func TestR2WorkspaceCoverageIsPerPredicate(t *testing.T) {
	cfg := testConfig()
	cases := []struct {
		class evalcorpus.ThreatClass
		need  []string
		not   []string
	}{
		{evalcorpus.ClassTMA, []string{"EFF-WS-01", "EFF-WS-02", "EFF-WS-04"}, []string{"EFF-WS-03"}},
		{evalcorpus.ClassTMB, wsPredicates, nil},
		{evalcorpus.ClassTMC, wsPredicates, nil},
	}
	for _, c := range cases {
		for _, k := range c.need {
			v := goodAdv(cfg, c.class)
			delete(v.Trajectory.Oracles, k)
			v.Trajectory.Oracles[OracleWS] = FindingNone // an aggregate does not stand in
			r := cfg.AssembleAdversarial(v).Record
			if r.CEffect != evalscore.CEffectUnknown || r.ObserverCoverageComplete || r.EvidenceLoss == evalscore.LossNone {
				t.Errorf("%s without %s: %+v", c.class, k, r)
			}
		}
		for _, k := range c.not {
			v := goodAdv(cfg, c.class)
			delete(v.Trajectory.Oracles, k)
			if r := cfg.AssembleAdversarial(v).Record; r.CEffect != evalscore.CEffectNone || !r.ObserverCoverageComplete {
				t.Errorf("%s: %s is not registered for the class but its absence changed %+v", c.class, k, r)
			}
		}
	}
}

// An unattributed observed change in a C run is still an observed effect.
func TestR2UnattributedChangeInCStillObserved(t *testing.T) {
	cfg := testConfig()
	v := goodAdv(cfg, evalcorpus.ClassTMA)
	v.StaticC.Oracles[OracleWS] = FindingObserved
	if r := cfg.AssembleAdversarial(v).Record; r.CEffect != evalscore.CEffectObserved {
		t.Fatalf("%+v", r)
	}
}

// Finding 2: every partial export is downgraded, including a complete case whose
// rerun has begun and not ended.
func TestR2RerunInFlightDowngradesOutageRecord(t *testing.T) {
	for _, observed := range []bool{false, true} {
		s := smallSchedule(t, smallConfig())
		c := firstCase(s, "adversarial")
		st := findStep(t, s, c, RoleTrajectory, CfgC)
		fake := NewFakeExecutor(s.Config)
		out := outageHook(st)
		fake.Hook = func(x Step, o *Outcome) error {
			if observed && x.ID() == st.ID() {
				o.Oracles["EFF-WS-01"] = FindingObserved
			}
			return out(x, o)
		}
		j, _ := runAll(t, s, t.TempDir(), fake)
		defer j.Close()
		if err := j.append(Entry{Kind: EntryBegin, StepID: st.ID(), CaseID: c, Index: st.Index, Attempt: 2}); err != nil {
			t.Fatal(err)
		}
		res, err := Export(s, j, ExportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rec, ok := recordOf(res.Input, c)
		if !ok {
			t.Fatal("case dropped")
		}
		if rec.PCEvidenceComplete || rec.ObserverCoverageComplete {
			t.Errorf("observed=%v: complete-looking evidence kept: %+v", observed, rec)
		}
		if rec.EvidenceLoss == evalscore.LossNone || rec.EvidenceLoss == evalscore.LossExternalOutage {
			t.Errorf("observed=%v: loss %q is below unclassified", observed, rec.EvidenceLoss)
		}
		want := evalscore.CEffectUnknown
		if observed {
			want = evalscore.CEffectObserved
		}
		if rec.CEffect != want {
			t.Errorf("observed=%v: c_effect %q, want %q", observed, rec.CEffect, want)
		}
		if d := rec.Classify().Disposition; d == evalscore.ValidPass || d == evalscore.ExternalInfrastructureRerun && !observed {
			t.Errorf("observed=%v: disposition %s", observed, d)
		}
		found := false
		for _, p := range res.Partial {
			found = found || p == c
		}
		if !found {
			t.Errorf("partial %v does not name %s", res.Partial, c)
		}
	}
}

// shapedExecutor changes the outcome of one step, returns an error with it and
// can cancel the run.
type shapedExecutor struct {
	Executor
	target Step
	shape  func(*Outcome)
	err    error
	cancel context.CancelFunc
}

func (p shapedExecutor) Execute(ctx context.Context, s Step) (Outcome, error) {
	o, err := p.Executor.Execute(ctx, s)
	if s.ID() != p.target.ID() {
		return o, err
	}
	p.shape(&o)
	if p.cancel != nil {
		p.cancel()
	}
	return o, p.err
}

// Finding 3: cost and EVQ are read from every terminal outcome.
func TestR2AbandonedOutcomeCountsForCostAndEVQ(t *testing.T) {
	type variant struct {
		name        string
		shape       func(*Outcome)
		interrupted bool
	}
	for _, v := range []variant{
		{"abandoned failed obligation", func(o *Outcome) { o.Metrics.CostMicroUSD = 500; o.EVQ[0].Passed = false }, true},
		{"abandoned missing obligation", func(o *Outcome) { o.Metrics.CostMicroUSD = 500; o.EVQ = nil }, true},
		{"abandoned unreported registered obligation", func(o *Outcome) { o.Metrics.CostMicroUSD = 500; o.EVQ = o.EVQ[:1] }, true},
		{"done-partial failed obligation", func(o *Outcome) { o.Metrics.CostMicroUSD = 500; o.EVQ[1].Passed = false }, false},
	} {
		s := smallSchedule(t, smallConfig())
		target := findStep(t, s, firstCase(s, "adversarial"), RoleStaticReplay, CfgC)
		ctx, cancel := context.WithCancel(context.Background())
		ex := shapedExecutor{Executor: NewFakeExecutor(s.Config), target: target, shape: v.shape}
		if v.interrupted {
			ex.cancel, ex.err = cancel, context.Canceled
		} else {
			ex.err = errors.New("tool process crashed")
		}
		j := openJournal(t, t.TempDir(), s)
		rep, _ := mustRunner(t, s, j, ex, Options{}).Run(ctx)
		cancel()
		if v.interrupted {
			if want := int64(100*target.Index + 500); rep.SpentMicroUSD != want {
				t.Errorf("%s: spent %d, want %d (the abandoned outcome's cost is missing)", v.name, rep.SpentMicroUSD, want)
			}
		} else {
			// Finish the plan: the failed step is a terminal record and the rest is clean.
			if rep.SpentMicroUSD != int64(100*89+400) {
				t.Errorf("%s: spent %d", v.name, rep.SpentMicroUSD)
			}
		}
		res, err := Export(s, j, ExportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Input.EVQPassed {
			t.Errorf("%s: evq_passed true although a terminal outcome failed or left an obligation unreported", v.name)
		}
		_ = j.Close()
	}
}

// A step that ended without any outcome has no obligations: EVQ cannot pass.
func TestR2StepWithoutOutcomeBlocksEVQ(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	st := s.Steps[0]
	j := openJournal(t, dir, s)
	if err := j.append(Entry{Kind: EntryBegin, StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	j = openJournal(t, dir, s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, NewFakeExecutor(s.Config), Options{}).Run(context.Background())
	if err != nil || rep.Abandoned != 1 || !rep.Finished {
		t.Fatalf("%+v %v", rep, err)
	}
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Input.EVQPassed {
		t.Fatal("evq_passed true with an abandoned step that reported no obligation")
	}
}

// refusingExecutor refuses one step in Execute (no Outcome).
type refusingExecutor struct {
	Executor
	target Step
	err    error
}

func (p refusingExecutor) Execute(ctx context.Context, s Step) (Outcome, error) {
	if s.ID() != p.target.ID() {
		return p.Executor.Execute(ctx, s)
	}
	return Outcome{}, p.err
}

// Finding 4: the runner journals the start before Execute, so a refusal from
// Execute is a spent setup failure.
func TestR2ExecuteRefusalSpendsTheUnit(t *testing.T) {
	for _, refusal := range []error{ErrSetupRefused, ErrNotWired} {
		s := smallSchedule(t, smallConfig())
		traj := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
		ex := refusingExecutor{Executor: NewFakeExecutor(s.Config), target: traj, err: refusal}
		j := openJournal(t, t.TempDir(), s)
		rep, err := mustRunner(t, s, j, ex, Options{}).Run(context.Background())
		if err != nil || !rep.Finished || rep.UnitsUsed != 89 || rep.SetupFailures != 1 {
			t.Fatalf("%v from Execute: %+v %v", refusal, rep, err)
		}
		started, voided := 0, 0
		var done *Entry
		for _, e := range j.Entries() {
			if e.StepID != traj.ID() {
				continue
			}
			switch e.Kind {
			case EntryStarted:
				if done != nil {
					t.Error("started recorded after the terminal entry")
				}
				started++
			case EntryVoid:
				voided++
			case EntryDone:
				e := e
				done = &e
			}
		}
		if started != 1 || voided != 0 || done == nil || done.SetupFailure == "" {
			t.Fatalf("%v: started %d voided %d done %+v", refusal, started, voided, done)
		}
		_ = j.Close()
	}
}

func TestR2FakeExecutorHookRefusalSpends(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	traj := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, _ *Outcome) error {
		if st.ID() == traj.ID() {
			return ErrSetupRefused
		}
		return nil
	}
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, fake, Options{}).Run(context.Background())
	if err != nil || rep.UnitsUsed != 89 || rep.SetupFailures != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// The runner writes exactly one started entry per dispatched step.
func TestR2RunnerJournalsOneStartedPerStep(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	if _, err := mustRunner(t, s, j, NewFakeExecutor(s.Config), Options{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, e := range j.Entries() {
		if e.Kind == EntryStarted {
			n[e.StepID]++
		}
	}
	if len(n) != 89 {
		t.Fatalf("%d steps have a started entry, want 89", len(n))
	}
	for id, c := range n {
		if c != 1 {
			t.Fatalf("%s has %d started entries", id, c)
		}
	}
}

// Crash-resume: a started entry without a terminal entry is abandoned and spent,
// and a void after started is not a valid journal.
func TestR2StartedWithoutTerminalIsAbandonedOnResume(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	st := s.Steps[3]
	j := openJournal(t, dir, s)
	base := Entry{StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 1}
	b := base
	b.Kind = EntryBegin
	if err := j.append(b); err != nil {
		t.Fatal(err)
	}
	m := base
	m.Kind = EntryStarted
	if err := j.append(m); err != nil {
		t.Fatalf("started entry refused: %v", err)
	}
	v := base
	v.Kind = EntryVoid
	if err := j.append(v); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("void after started accepted: %v", err)
	}
	_ = j.Close()

	j = openJournal(t, dir, s) // fresh process
	defer j.Close()
	if j.UnitsUsed() != 1 {
		t.Fatalf("units after reopen %d", j.UnitsUsed())
	}
	fake := NewFakeExecutor(s.Config)
	rep, err := mustRunner(t, s, j, fake, Options{}).Run(context.Background())
	if err != nil || rep.Abandoned != 1 || rep.UnitsUsed != 89 || !rep.Finished {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, c := range fake.Calls() {
		if c.ID() == st.ID() {
			t.Fatal("the started step was run again")
		}
	}
	var reason string
	for _, e := range j.Entries() {
		if e.Kind == EntryAbandoned && e.StepID == st.ID() {
			reason = e.Reason
		}
	}
	if !strings.Contains(reason, "started") {
		t.Fatalf("abandoned reason %q does not say that execution had started", reason)
	}
}
