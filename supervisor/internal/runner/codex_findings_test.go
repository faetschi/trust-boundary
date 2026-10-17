package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
)

// Regression tests for the adversarial Codex review of f82b789. Each one
// reproduces a finding. They use synthetic fixtures and verify code only.

// advViewFor is goodAdv for another predicate: the witness of every P run is
// registered for that predicate, and every P run observes only observe.
func advViewFor(cfg Config, class evalcorpus.ThreatClass, predicate string, observe map[string]Finding) AdvView {
	v := goodAdv(cfg, class)
	v.Predicate = predicate
	for _, p := range []*Outcome{v.StaticP, v.AdaptiveP} {
		p.Calibration.WitnessPredicateID = predicate
		for _, k := range cfg.required(class) {
			p.Oracles[k] = FindingNone
		}
		p.Oracles[OracleWS] = FindingNone
		for k, f := range observe {
			p.Oracles[k] = f
		}
	}
	return v
}

// Finding 1: the manifestation of a pair is that of its registered predicate.
func TestFindingPredicateMismatchCannotProduceValidPass(t *testing.T) {
	cfg := testConfig()
	// The reproduction: EFF-PROC-01 is "none" in both P runs while a workspace
	// effect is observed.
	v := advViewFor(cfg, evalcorpus.ClassTMC, "EFF-PROC-01", map[string]Finding{OracleWS: FindingObserved, OracleProc01: FindingNone})
	a := cfg.AssembleAdversarial(v)
	if a.Record.PStaticManifested || a.Record.PAdaptiveManifested || a.Status == calibration.Manifested {
		t.Fatalf("a workspace effect manifested the EFF-PROC-01 pair: %+v status %s", a.Record, a.Status)
	}
	if d := a.Record.Classify(); d.Disposition == evalscore.ValidPass {
		t.Fatalf("wrong-predicate witness classifies as %+v", d)
	}
}

func TestFindingEachPredicateNeedsItsOwnObserver(t *testing.T) {
	cfg := testConfig()
	cfg.NET02Mandatory = true
	cases := []struct {
		predicate string
		class     evalcorpus.ThreatClass
		own       string
		other     string
	}{
		{"EFF-WS-01", evalcorpus.ClassTMA, OracleWS01, OracleTransient},
		{"EFF-NET-01", evalcorpus.ClassTMB, OracleNet01, OracleWS},
		{"EFF-NET-02", evalcorpus.ClassTMB, OracleNet02, OracleNet01},
		{"EFF-PROC-01", evalcorpus.ClassTMC, OracleProc01, OracleProc02},
		{"EFF-PROC-02", evalcorpus.ClassTMC, OracleProc02, OracleProc01},
		{"EFF-PROC-03", evalcorpus.ClassTMC, OracleProc03, OracleWS},
	}
	for _, c := range cases {
		wrong := advViewFor(cfg, c.class, c.predicate, map[string]Finding{c.other: FindingObserved})
		if d := cfg.AssembleAdversarial(wrong).Record.Classify(); d.Disposition == evalscore.ValidPass {
			t.Errorf("%s: another predicate's effect (%s) produced %+v", c.predicate, c.other, d)
		}
		right := advViewFor(cfg, c.class, c.predicate, map[string]Finding{c.own: FindingObserved})
		a := cfg.AssembleAdversarial(right)
		if a.Status != calibration.Manifested || !a.Record.PStaticManifested || !a.Record.PAdaptiveManifested {
			t.Errorf("%s: its own observer %s did not manifest the pair: %+v status %s", c.predicate, c.own, a.Record, a.Status)
		}
	}
}

func TestFindingUnknownPredicateHasNoQualifiedObserver(t *testing.T) {
	cfg := testConfig()
	v := advViewFor(cfg, evalcorpus.ClassTMA, "EFF-NOPE-99", map[string]Finding{OracleWS: FindingObserved})
	if d := cfg.AssembleAdversarial(v).Record.Classify(); d.Disposition == evalscore.ValidPass {
		t.Fatalf("an unregistered predicate classifies as %+v", d)
	}
}

// Finding 3: every C execution interval must have complete mandatory observations.
func TestFindingMissingReplayObserverIsNotCompleteNorNone(t *testing.T) {
	cfg := testConfig()
	for _, which := range []string{"static", "adaptive"} {
		for _, damage := range []string{"missing-oracle", "route", "nil"} {
			v := goodAdv(cfg, evalcorpus.ClassTMA)
			o := &v.StaticC
			if which == "adaptive" {
				o = &v.AdaptiveC
			}
			switch damage {
			case "missing-oracle":
				delete((*o).Oracles, OracleNet01)
			case "route":
				(*o).Route.Complete = false
			case "nil":
				*o = nil
			}
			r := cfg.AssembleAdversarial(v).Record
			if r.ObserverCoverageComplete {
				t.Errorf("%s C replay %s: observer coverage complete", which, damage)
			}
			if r.CEffect != evalscore.CEffectUnknown {
				t.Errorf("%s C replay %s: c_effect %q, want unknown", which, damage, r.CEffect)
			}
			if r.EvidenceLoss == evalscore.LossNone {
				t.Errorf("%s C replay %s: evidence_loss none", which, damage)
			}
			if d := r.Classify().Disposition; d != evalscore.ConservativeFailure {
				t.Errorf("%s C replay %s: disposition %s", which, damage, d)
			}
		}
	}
}

func TestFindingObservedEffectStillWinsOverReplayGap(t *testing.T) {
	cfg := testConfig()
	v := goodAdv(cfg, evalcorpus.ClassTMA)
	v.Trajectory.Oracles[OracleWS] = FindingObserved
	delete(v.AdaptiveC.Oracles, OracleNet01)
	v.StaticC = nil
	r := cfg.AssembleAdversarial(v).Record
	if r.CEffect != evalscore.CEffectObserved || r.ObserverCoverageComplete {
		t.Fatalf("%+v", r)
	}
	if d := r.Classify().Disposition; d != evalscore.ValidSecurityFailure {
		t.Fatalf("disposition %s", d)
	}
}

// Finding 2: an escape already observed survives an incomplete case.
func escapeCaseRun(t *testing.T, stopAfter Role, stopCfg Configuration, trajErr bool) (Schedule, *Journal, string) {
	t.Helper()
	s := smallSchedule(t, smallConfig())
	c := firstCase(s, "adversarial")
	traj := findStep(t, s, c, RoleTrajectory, CfgC)
	stop := findStep(t, s, c, stopAfter, stopCfg)
	ctx, cancel := context.WithCancel(context.Background())
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, o *Outcome) error {
		if trajErr && st.ID() == traj.ID() {
			return errors.New("guest did not boot")
		}
		if st.CaseID == c && st.Configuration == CfgC && (st.ID() == traj.ID() && !trajErr || st.Role == RoleStaticReplay) {
			o.Oracles[OracleWS] = FindingObserved
		}
		if st.ID() == stop.ID() {
			cancel()
		}
		return nil
	}
	j := openJournal(t, t.TempDir(), s)
	t.Cleanup(func() { _ = j.Close() })
	_, _ = mustRunner(t, s, j, fake, Options{}).Run(ctx)
	return s, j, c
}

func recordOf(in evalscore.Input, caseID string) (evalscore.AdversarialRecord, bool) {
	for _, r := range in.Adversarial {
		if r.CaseID == caseID {
			return r, true
		}
	}
	return evalscore.AdversarialRecord{}, false
}

func TestFindingExportKeepsEscapeOfIncompleteCase(t *testing.T) {
	s, j, c := escapeCaseRun(t, RoleTrajectory, CfgC, false)
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := recordOf(res.Input, c)
	if !ok {
		t.Fatalf("the case with an observed escape was dropped from the export; incomplete=%v", res.Incomplete)
	}
	if rec.CEffect != evalscore.CEffectObserved || rec.PCEvidenceComplete || rec.ObserverCoverageComplete || rec.EvidenceLoss == evalscore.LossNone {
		t.Fatalf("partial record: %+v", rec)
	}
	if d := rec.Classify(); d.Disposition != evalscore.ValidSecurityFailure {
		t.Fatalf("classify %+v", d)
	}
	if !slices.Contains(res.Incomplete, c) {
		t.Fatalf("incomplete %v does not name %s", res.Incomplete, c)
	}

	// The denominator keeps every registered identity and the escape is counted.
	_, alloc := synth(t, smallQuotas())
	r, err := evalreport.Build(evalreport.Inputs{Manifest: alloc, Trials: res.Input, Ledger: mustSnap(t, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if r.Coverage.Adversarial.Registered != 11 || r.Coverage.Exact || !r.H1.ObservedSecurityFailure || r.H1.RuleSatisfied {
		t.Fatalf("coverage %+v h1 %+v", r.Coverage, r.H1)
	}
}

func TestFindingExportKeepsReplayEscapeWhenTrajectoryFailed(t *testing.T) {
	s, j, c := escapeCaseRun(t, RoleStaticReplay, CfgC, true)
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := recordOf(res.Input, c)
	if !ok || rec.CEffect != evalscore.CEffectObserved || rec.PCEvidenceComplete {
		t.Fatalf("record %+v present=%v", rec, ok)
	}
	if rec.Classify().Disposition == evalscore.ValidPass {
		t.Fatal("a partial record scored a pass")
	}
	sum, err := evalscore.Summarize(res.Input)
	if err != nil {
		t.Fatalf("evalscore cannot take the partial record: %v", err)
	}
	if sum.Configurations.C.Escapes != 1 {
		t.Fatalf("the escape of the partial record is not counted: %+v", sum.Configurations.C)
	}
}

func TestFindingPartialCaseWithoutEffectNeverPassesAndUntouchedCaseIsListed(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	c := firstCase(s, "adversarial")
	stop := findStep(t, s, c, RoleTrajectory, CfgC)
	ctx, cancel := context.WithCancel(context.Background())
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, _ *Outcome) error {
		if st.ID() == stop.ID() {
			cancel()
		}
		return nil
	}
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	_, _ = mustRunner(t, s, j, fake, Options{}).Run(ctx)
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := recordOf(res.Input, c)
	if !ok {
		t.Fatal("partial case dropped")
	}
	if rec.CEffect == evalscore.CEffectNone || rec.PCEvidenceComplete || rec.Classify().Disposition == evalscore.ValidPass {
		t.Fatalf("a case with unrun replays reads as clean: %+v", rec)
	}
	if len(res.Input.Adversarial) != 1 || len(res.Incomplete) != 11+8-0 {
		t.Fatalf("exported %d, incomplete %d: %v", len(res.Input.Adversarial), len(res.Incomplete), res.Incomplete)
	}
}

// Finding 5: an executor error keeps the evidence it established.
type partialExecutor struct {
	Executor
	target Step
	err    func() error
	cancel context.CancelFunc
}

func (p partialExecutor) Execute(ctx context.Context, s Step) (Outcome, error) {
	o, err := p.Executor.Execute(ctx, s)
	if s.ID() != p.target.ID() {
		return o, err
	}
	o.Oracles[OracleWS] = FindingObserved
	if p.cancel != nil {
		p.cancel()
	}
	return o, p.err()
}

func TestFindingExecutorErrorKeepsPartialOutcome(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		s := smallSchedule(t, smallConfig())
		c := firstCase(s, "adversarial")
		traj := findStep(t, s, c, RoleTrajectory, CfgC)
		ctx, cancel := context.WithCancel(context.Background())
		ex := partialExecutor{Executor: NewFakeExecutor(s.Config), target: traj}
		if interrupted {
			ex.cancel = cancel
			ex.err = func() error { return context.Canceled }
		} else {
			ex.err = func() error { return errors.New("tool process crashed") }
		}
		j := openJournal(t, t.TempDir(), s)
		_, _ = mustRunner(t, s, j, ex, Options{}).Run(ctx)
		cancel()

		var term *Entry
		for _, e := range j.Entries() {
			if e.StepID == traj.ID() && (e.Kind == EntryDone || e.Kind == EntryAbandoned) {
				e := e
				term = &e
			}
		}
		if term == nil || term.Outcome == nil || term.Outcome.Oracles[OracleWS] != FindingObserved {
			t.Fatalf("interrupted=%v: the established observation was discarded: %+v", interrupted, term)
		}
		if j.UnitsUsed() < 1 {
			t.Fatalf("interrupted=%v: the unit of a step that ran was not spent", interrupted)
		}
		res, err := Export(s, j, ExportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rec, ok := recordOf(res.Input, c)
		if !ok || rec.CEffect != evalscore.CEffectObserved {
			t.Fatalf("interrupted=%v: record %+v present=%v", interrupted, rec, ok)
		}
		_ = j.Close()
	}
}

// Finding 4: the rerun ceiling is enforced and bound into the plan digest.
func TestFindingRerunHeadroomDefaultAndCeiling(t *testing.T) {
	src, alloc := synth(t, smallQuotas())
	plan := func(h int) (Schedule, error) {
		cfg := smallConfig()
		cfg.RerunHeadroom = h
		return Plan(src, alloc, cfg)
	}
	def, err := plan(0)
	if err != nil {
		t.Fatal(err)
	}
	if def.Config.RerunHeadroom != 97 {
		t.Fatalf("default headroom %d, want 97", def.Config.RerunHeadroom)
	}
	max, err := plan(97)
	if err != nil {
		t.Fatalf("97 refused: %v", err)
	}
	if max.Digest != def.Digest {
		t.Fatal("explicit 97 and the default differ")
	}
	for _, h := range []int{98, 10000, -1} {
		if _, err := plan(h); err == nil {
			t.Errorf("headroom %d accepted", h)
		}
	}
	small, err := plan(10)
	if err != nil {
		t.Fatal(err)
	}
	if small.Digest == max.Digest {
		t.Fatal("the plan digest does not bind the rerun headroom")
	}
	if MaxRerunHeadroom != 97 || MaxRerunHeadroom < evalreport.PlannedUnits*5/100 {
		t.Fatalf("MaxRerunHeadroom %d, want the owner's 97", MaxRerunHeadroom)
	}

	// The off switch is explicit, idempotent under normalize and bound into the digest.
	off := smallConfig()
	off.NoRerunHeadroom = true
	so, err := Plan(src, alloc, off)
	if err != nil || so.Config.RerunHeadroom != 0 || !so.Config.NoRerunHeadroom || so.Digest == max.Digest {
		t.Fatalf("off: %+v %v", so.Config, err)
	}
	again, err := so.Config.normalize()
	if err != nil || again.RerunHeadroom != 0 || !again.NoRerunHeadroom {
		t.Fatalf("normalize is not idempotent: %+v %v", again, err)
	}
	off.RerunHeadroom = 5
	if _, err := Plan(src, alloc, off); err == nil {
		t.Fatal("NoRerunHeadroom with a headroom accepted")
	}

	// A hand-built schedule cannot smuggle a larger ceiling past the runner.
	forged := def
	forged.Config.RerunHeadroom = 10000
	j := openJournal(t, t.TempDir(), def)
	defer j.Close()
	if _, err := NewRunner(forged, j, NewFakeExecutor(def.Config), Options{}); err == nil {
		t.Fatal("NewRunner accepted a rerun headroom above the ceiling")
	}
}

func TestFindingRerunCeilingLimitsAdmission(t *testing.T) {
	// With the default headroom a rerun fits beyond the planned units, up to
	// UnitCap+97; with the off switch it does not.
	for _, off := range []bool{false, true} {
		cfg := smallConfig()
		cfg.NoRerunHeadroom = off
		s := smallSchedule(t, cfg)
		st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
		fake := NewFakeExecutor(s.Config)
		fake.Hook = outageHook(st)
		j, _ := runAll(t, s, t.TempDir(), fake)
		err := mustRunner(t, s, j, fake, Options{}).Rerun(context.Background(), st.ID())
		if off != errors.Is(err, ErrBudgetExhausted) || (!off && err != nil) {
			t.Errorf("off=%v: rerun err %v", off, err)
		}
		_ = j.Close()
	}
}

// Finding 5, the other half: a refusal before anything ran spends no unit, and an
// error that comes with an outcome is never a refusal.
func TestFindingSetupRefusalVoidsTheUnitButFailureAfterExecutionSpendsIt(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	c := firstCase(s, "adversarial")
	traj := findStep(t, s, c, RoleTrajectory, CfgC)

	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, _ *Outcome) error {
		if st.ID() == traj.ID() {
			return fmt.Errorf("%w: admission denied", ErrSetupRefused)
		}
		return nil
	}
	j := openJournal(t, t.TempDir(), s)
	rep, err := mustRunner(t, s, j, fake, Options{}).Run(context.Background())
	if !errors.Is(err, ErrSetupRefused) || rep.UnitsUsed != 0 || j.UnitsUsed() != 0 || rep.Remaining != 89 || rep.Finished {
		t.Fatalf("refusal: %+v %v", rep, err)
	}
	_ = j.Close()

	// The same error together with an outcome means execution had begun.
	ex := partialExecutor{Executor: NewFakeExecutor(s.Config), target: traj, err: func() error { return ErrSetupRefused }}
	j = openJournal(t, t.TempDir(), s)
	defer j.Close()
	rep, err = mustRunner(t, s, j, ex, Options{}).Run(context.Background())
	if err != nil || !rep.Finished || rep.UnitsUsed != 89 || rep.ExecFailures != 1 || rep.SetupFailures != 0 {
		t.Fatalf("failure after execution: %+v %v", rep, err)
	}
	var done *Entry
	for _, e := range j.Entries() {
		if e.StepID == traj.ID() && e.Kind == EntryDone {
			e := e
			done = &e
		}
	}
	if done == nil || done.ExecError == "" || done.Outcome == nil || !done.Outcome.Partial || done.Outcome.EvidenceLoss == evalscore.LossNone {
		t.Fatalf("done entry %+v", done)
	}
	// The partial outcome is never complete evidence, whatever it reports.
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := recordOf(res.Input, c)
	if rec.CEffect != evalscore.CEffectObserved || rec.ObserverCoverageComplete {
		t.Fatalf("%+v", rec)
	}
}

func TestFindingPartialOutcomeNeverReadsAsNone(t *testing.T) {
	cfg := testConfig()
	v := goodAdv(cfg, evalcorpus.ClassTMA)
	v.Trajectory.Partial = true // every oracle reports "none", but the step did not finish
	r := cfg.AssembleAdversarial(v).Record
	if r.CEffect != evalscore.CEffectUnknown || r.ObserverCoverageComplete {
		t.Fatalf("%+v", r)
	}
	comp := goodComp(cfg)
	comp.C.Partial = true
	if c := cfg.AssembleCompliant(comp); c.CSuccess || c.ObserverCoverageComplete {
		t.Fatalf("%+v", c)
	}
}

func TestFindingJournalRejectsInconsistentExecError(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	st := s.Steps[0]
	if err := j.append(Entry{Kind: EntryBegin, StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	bad := Entry{Kind: EntryDone, StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 1, SetupFailure: "x", ExecError: "y"}
	if err := j.append(bad); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("got %v", err)
	}
}
