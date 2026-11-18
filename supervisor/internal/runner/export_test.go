package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
)

func loadFile(t testing.TB, path string) evalscore.Input {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	in, err := evalscore.Load(f)
	if err != nil {
		t.Fatalf("evalscore.Load: %v", err)
	}
	return in
}

func TestExportLoadsThroughEvalscoreAndBuilds(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	j, rep := runAll(t, s, t.TempDir(), NewFakeExecutor(s.Config))
	defer j.Close()
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Incomplete) != 0 || !res.Input.EVQPassed {
		t.Fatalf("incomplete %v evq %v", res.Incomplete, res.Input.EVQPassed)
	}
	if len(res.Input.Adversarial) != 11 || len(res.Input.Compliant) != 8 {
		t.Fatalf("records %d/%d, want 11/8 (ambiguous and variance repeats are not scored)", len(res.Input.Adversarial), len(res.Input.Compliant))
	}
	path := filepath.Join(t.TempDir(), "trials.json")
	if err := WriteTrialsFile(path, res.Input); err != nil {
		t.Fatal(err)
	}
	in := loadFile(t, path)
	if in.Schema != evalscore.SchemaVersion || len(in.Adversarial) != 11 || len(in.Compliant) != 8 {
		t.Fatalf("loaded %+v", in)
	}
	_, alloc := synth(t, smallQuotas())
	r, err := evalreport.Build(evalreport.Inputs{Manifest: alloc, Trials: in, Ledger: mustSnap(t, rep.SpentMicroUSD)})
	if err != nil {
		t.Fatalf("evalreport.Build: %v", err)
	}
	if !r.Coverage.Exact || r.Coverage.Adversarial.Scored != 11 || r.Coverage.Compliant.Scored != 8 || r.Coverage.UnscoredAmbiguous != 2 {
		t.Fatalf("coverage %+v", r.Coverage)
	}
	if r.Observed.InvalidRecords != 0 || len(r.InvalidRecords) != 0 || r.Observed.PendingExternalReruns != 0 {
		t.Fatalf("a clean fake run produced invalid or pending records: %+v", r.Observed)
	}
}

func TestThesisSizeRunMatchesEvalreportAccounting(t *testing.T) {
	src, alloc := synth(t, legacyThesisQuotas())
	s, err := Plan(src, alloc, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	fake := NewFakeExecutor(s.Config)
	j, rep := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	if !rep.Finished || rep.UnitsUsed != evalreport.LegacyPlannedUnits || rep.PlannedUnits != evalreport.LegacyPlannedUnits {
		t.Fatalf("report %+v", rep)
	}
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Input.Adversarial) != 299 || len(res.Input.Compliant) != 100 {
		t.Fatalf("records %d/%d", len(res.Input.Adversarial), len(res.Input.Compliant))
	}
	if len(res.Variance) != 50 {
		t.Fatalf("variance vectors %d, want 50", len(res.Variance))
	}
	for _, v := range res.Variance {
		if !v.Complete {
			t.Fatalf("incomplete vector %+v", v)
		}
	}
	snap := mustSnap(t, rep.SpentMicroUSD)
	snap.SettledUnits = rep.UnitsUsed
	r, err := evalreport.Build(evalreport.Inputs{Manifest: alloc, Trials: res.Input, Ledger: snap})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !r.Coverage.Exact || r.Spend.PlannedUnits != 1935 || r.Spend.TotalUnits != 1935 || r.Spend.UnitsOverPlanned || !r.Spend.WithinBudget {
		t.Fatalf("coverage %+v spend %+v", r.Coverage, r.Spend)
	}
	if rep.SpentMicroUSD >= evalreport.RegisteredCapMicroUSD {
		t.Fatalf("fake run spend %d", rep.SpentMicroUSD)
	}
}

func TestExportExportsUnfinishedAdversarialCaseAsPartialAndNamesIt(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	ctx, cancel := context.WithCancel(context.Background())
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, _ *Outcome) error {
		if st.Index == 7 {
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
	// 8 steps done: case 1 complete (5 steps), case 2 has 3 of 5 and is exported as
	// a partial record. The cases that were not started are left out.
	if len(res.Input.Adversarial) != 2 || res.Input.Compliant == nil || res.Input.Adversarial == nil || len(res.Partial) != 1 {
		t.Fatalf("records %d, partial %v, nil compliant? %v", len(res.Input.Adversarial), res.Partial, res.Input.Compliant == nil)
	}
	if p := res.Input.Adversarial[1]; p.CEffect == evalscore.CEffectNone || p.PCEvidenceComplete || p.ObserverCoverageComplete || p.Classify().Disposition == evalscore.ValidPass {
		t.Fatalf("partial record reads as clean: %+v", p)
	}
	if len(res.Incomplete) != 11-1+8+0 {
		t.Fatalf("incomplete %d: %v", len(res.Incomplete), res.Incomplete)
	}
	if res.Input.EVQPassed != true {
		t.Fatal("evq should reflect only what ran")
	}
}

func TestExportRerunBuildsInEvalreport(t *testing.T) {
	cfg := smallConfig()
	cfg.RerunHeadroom = 1
	s := smallSchedule(t, cfg)
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = outageHook(st)
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	if err := mustRunner(t, s, j, fake, Options{}).Rerun(context.Background(), st.ID()); err != nil {
		t.Fatal(err)
	}
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Input.Adversarial) != 12 {
		t.Fatalf("records %d, want 11 cases + 1 extra attempt", len(res.Input.Adversarial))
	}
	_, alloc := synth(t, smallQuotas())
	r, err := evalreport.Build(evalreport.Inputs{Manifest: alloc, Trials: res.Input, Ledger: mustSnap(t, 0)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !r.Coverage.Exact {
		t.Fatalf("%+v", r.Coverage)
	}
}

func TestExportRefusesRerunWhoseOriginalIsNotAnEvidencedOutage(t *testing.T) {
	// A journal can only contain such a rerun if it was edited; Export must not
	// pass it on to evalscore as if it were valid.
	cfg := smallConfig()
	cfg.RerunHeadroom = 1
	s := smallSchedule(t, cfg)
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(x Step, o *Outcome) error {
		if x.ID() == st.ID() && x.Attempt == 1 {
			o.EvidenceLoss = evalscore.LossExternalOutage
			o.OutageEvidenceDigest = "sha256:" + strings.Repeat("cd", 32)
			delete(o.Oracles, OracleWS01) // the outage also destroyed an oracle: not a clean rerun candidate
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	if err := mustRunner(t, s, j, fake, Options{}).Rerun(context.Background(), st.ID()); !errors.Is(err, ErrRerunNotAllowed) {
		t.Fatalf("rerun after a destroyed oracle: got %v, want ErrRerunNotAllowed", err)
	}
	// Journaled by hand, as an edited journal would hold it.
	base := Entry{StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 2}
	for _, kind := range []EntryKind{EntryBegin, EntryStarted} {
		e := base
		e.Kind = kind
		if err := j.append(e); err != nil {
			t.Fatal(err)
		}
	}
	done := base
	done.Kind, done.Outcome = EntryDone, j.terminalOf(st.ID(), 1).Outcome
	if err := j.append(done); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(s, j, ExportOptions{}); !errors.Is(err, ErrRerunInconsistent) {
		t.Fatalf("Export of a rerun whose original does not classify as EXTERNAL_INFRASTRUCTURE_RERUN: %v", err)
	}
}

func TestExportEVQ(t *testing.T) {
	run := func(hook func(Step, *Outcome) error) evalscore.Input {
		s := smallSchedule(t, smallConfig())
		fake := NewFakeExecutor(s.Config)
		fake.Hook = hook
		j, _ := runAll(t, s, t.TempDir(), fake)
		defer j.Close()
		res, err := Export(s, j, ExportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return res.Input
	}
	if !run(nil).EVQPassed {
		t.Fatal("all obligations passed but evq_passed is false")
	}
	if run(func(st Step, o *Outcome) error {
		if st.Index == 3 {
			o.EVQ[1].Passed = false
		}
		return nil
	}).EVQPassed {
		t.Fatal("a failed obligation left evq_passed true")
	}
	if run(func(st Step, o *Outcome) error {
		if st.Index == 3 {
			o.EVQ = o.EVQ[:1] // one registered obligation not reported
		}
		return nil
	}).EVQPassed {
		t.Fatal("an unreported obligation left evq_passed true")
	}
}

func TestExportAttributionOption(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	var task string
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, o *Outcome) error {
		if st.Role == RoleCompliant && st.Configuration == CfgC && task == "" {
			task = st.CaseID
		}
		if st.Role == RoleCompliant && st.Configuration == CfgC && st.CaseID == task {
			o.Task = TaskFail
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	find := func(in evalscore.Input) evalscore.CompliantRecord {
		for _, r := range in.Compliant {
			if r.TaskID == task {
				return r
			}
		}
		t.Fatal("task missing")
		return evalscore.CompliantRecord{}
	}
	res, _ := Export(s, j, ExportOptions{})
	if r := find(res.Input); r.Attribution != evalscore.AttributionUnresolved {
		t.Fatalf("default attribution %q", r.Attribution)
	}
	res, _ = Export(s, j, ExportOptions{Attributions: map[string]evalscore.Attribution{task: evalscore.AttributionSupported}})
	if r := find(res.Input); r.Attribution != evalscore.AttributionSupported {
		t.Fatalf("attribution %q", r.Attribution)
	}
}

func TestWriteTrialsEmptyInputStillLoads(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeTrials(&buf, evalscore.Input{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"adversarial":[]`) || !strings.Contains(buf.String(), `"compliant":[]`) {
		t.Fatalf("slices not encoded as arrays: %s", buf.String())
	}
	if _, err := evalscore.Load(&buf); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestRawJournalKeepsRunnerOnlyFields(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, o *Outcome) error {
		if st.Index == 2 {
			o.Metrics.Retries = 2
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	var done int
	for _, e := range j.Entries() {
		if e.Kind != EntryDone {
			continue
		}
		done++
		o := e.Outcome
		if o == nil || o.E01.Digest == "" || !o.E01.Verified || len(o.EVQ) != 2 || o.Metrics.LatencyMS == 0 ||
			o.Metrics.InTokens == 0 || o.Metrics.OutTokens == 0 || o.Metrics.CostMicroUSD == 0 {
			t.Fatalf("entry %d lacks raw fields: %+v", e.Index, o)
		}
		if e.StepID == "" || e.Attempt != 1 {
			t.Fatalf("entry %+v", e)
		}
		if e.Index == 2 && o.Metrics.Retries != 2 {
			t.Fatal("retries lost")
		}
	}
	if done != 89 {
		t.Fatalf("%d done entries", done)
	}
	// The scored file has none of those fields (evalscore.Load is strict).
	res, _ := Export(s, j, ExportOptions{})
	var buf bytes.Buffer
	_ = EncodeTrials(&buf, res.Input)
	for _, banned := range []string{"e01", "latency", "tokens", "cost", "retries", "evq\":", "index"} {
		if strings.Contains(buf.String(), banned) {
			t.Errorf("trial file leaks runner field %q", banned)
		}
	}
}

func TestCalibrationFeedsPrefixCurveAndDryRunGate(t *testing.T) {
	src, alloc := synth(t, smallQuotas())
	cfg := smallConfig()
	cfg.OnlyCases = []string{"adv-TM-A-001", "adv-TM-B-001"}
	s, err := Plan(src, alloc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fake := NewFakeExecutor(s.Config)
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	cc, err := Calibrations(s, j)
	if err != nil {
		t.Fatal(err)
	}
	if len(cc) != 2 || !DryRunPassed(cc) {
		t.Fatalf("calibrations %+v", cc)
	}
	var pairs []calibration.Pair
	for _, c := range cc {
		if c.Status != calibration.Manifested {
			t.Fatalf("%+v", c)
		}
		pairs = append(pairs, c.Pair)
	}
	curve, err := calibration.PrefixCurve(pairs, "EFF-WS-01", s.Config.AdaptiveCaps, 8)
	if err != nil {
		t.Fatal(err)
	}
	// The fake adaptive run manifests on attempt 3: 0 pairs at k=1,2 and 2 pairs from k=3.
	if curve[1].Numerator != 0 || curve[2].Numerator != 2 || curve[7].Denominator != 2 {
		t.Fatalf("curve %+v", curve)
	}

	// One pair that does not manifest fails the dry-run gate.
	fake2 := NewFakeExecutor(s.Config)
	fake2.Hook = func(st Step, o *Outcome) error {
		if st.CaseID == "adv-TM-B-001" && st.Role == RoleAdaptiveReplay && st.Configuration == CfgP {
			o.Oracles[OracleWS01] = FindingNone
		}
		return nil
	}
	j2, _ := runAll(t, s, t.TempDir(), fake2)
	defer j2.Close()
	cc, err = Calibrations(s, j2)
	if err != nil {
		t.Fatal(err)
	}
	if DryRunPassed(cc) {
		t.Fatalf("dry-run gate passed with a non-manifesting pair: %+v", cc)
	}
}
