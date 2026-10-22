package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/usdledger"
)

func TestRunCompletesAndCountsUnits(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	j, rep := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	if !rep.Finished || rep.Stop != "" || rep.Remaining != 0 {
		t.Fatalf("report %+v", rep)
	}
	if rep.Executed != 89 || rep.UnitsUsed != 89 || rep.PlannedUnits != 89 || len(fake.Calls()) != 89 {
		t.Fatalf("report %+v calls %d", rep, len(fake.Calls()))
	}
	if rep.PlanDigest != s.Digest {
		t.Fatal("report does not name the plan digest")
	}
	// Execution follows the plan order.
	for i, c := range fake.Calls() {
		if c.Index != i || c.Attempt != 1 {
			t.Fatalf("call %d executed step %d attempt %d", i, c.Index, c.Attempt)
		}
	}
	// A second run does nothing.
	rep2, err := mustRunner(t, s, j, fake, Options{}).Run(context.Background())
	if err != nil || rep2.Executed != 0 || rep2.AlreadyDone != 89 || !rep2.Finished || len(fake.Calls()) != 89 {
		t.Fatalf("second run: %+v %v", rep2, err)
	}
}

// crash is the panic value of a simulated process death.
type crash struct{}

func TestResumeAfterCrashMidRun(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	const dieAt = 30

	first := NewFakeExecutor(s.Config)
	first.Hook = func(st Step, _ *Outcome) error {
		if st.Index == dieAt {
			panic(crash{})
		}
		return nil
	}
	var dead *Journal
	func() {
		defer func() {
			// A dead process releases its file handles. Without this, the handle stays open
			// and native Windows cannot remove the temp dir. The journal code does not leak.
			if dead != nil {
				_ = dead.Close()
			}
			if v := recover(); v == nil {
				t.Fatal("the simulated crash did not happen")
			} else if _, ok := v.(crash); !ok {
				panic(v)
			}
		}()
		dead = openJournal(t, dir, s) // not closed by the run: the process "died"
		_, _ = mustRunner(t, s, dead, first, Options{}).Run(context.Background())
	}()
	if len(first.Calls()) != dieAt+1 {
		t.Fatalf("first process executed %d steps", len(first.Calls()))
	}

	// Fresh process: reopen the journal from disk.
	second := NewFakeExecutor(s.Config)
	j := openJournal(t, dir, s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, second, Options{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Finished || rep.Abandoned != 1 || rep.AlreadyDone != dieAt {
		t.Fatalf("report %+v", rep)
	}
	for _, c := range second.Calls() {
		if c.Index <= dieAt {
			t.Fatalf("resume re-executed step %d", c.Index)
		}
	}
	if len(second.Calls()) != 89-dieAt-1 {
		t.Fatalf("resume executed %d steps, want %d", len(second.Calls()), 89-dieAt-1)
	}
	// The interrupted step consumed its unit and has no outcome; it is not retried.
	if rep.UnitsUsed != 89 {
		t.Fatalf("units used %d, want 89", rep.UnitsUsed)
	}
	var abandoned []Entry
	for _, e := range j.Entries() {
		if e.Kind == EntryAbandoned {
			abandoned = append(abandoned, e)
		}
	}
	if len(abandoned) != 1 || abandoned[0].Index != dieAt || abandoned[0].Outcome != nil {
		t.Fatalf("abandoned entries: %+v", abandoned)
	}
}

func TestJournalTornTailIsRepaired(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	j := openJournal(t, dir, s)
	rep, err := mustRunner(t, s, j, NewFakeExecutor(s.Config), Options{}).Run(context.Background())
	if err != nil || !rep.Finished {
		t.Fatal(err, rep)
	}
	n := len(j.Entries())
	_ = j.Close()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":999,"kind":"do`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	j2, err := OpenJournal(path, s)
	if err != nil {
		t.Fatalf("torn tail refused: %v", err)
	}
	defer j2.Close()
	if len(j2.Entries()) != n {
		t.Fatalf("entries %d, want %d", len(j2.Entries()), n)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `"seq":999`) {
		t.Fatal("torn tail was not removed")
	}
}

// Two runners must never own one journal: the second open is refused while the
// first handle is live, before it can read, repair or write the file. Only after
// the first closes may a new owner resume and see every record the first wrote.
func TestJournalRefusesASecondOwnerOfTheSameTrialStep(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	first := openJournal(t, dir, s)

	var second *Journal
	var secondErr error
	checked := false
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(Step, *Outcome) error {
		if checked {
			return nil
		}
		checked = true
		// A second owner tries to open the same journal to execute the same
		// trial step while the first is mid-step.
		second, secondErr = OpenJournal(path, s)
		return nil
	}
	rep, err := mustRunner(t, s, first, fake, Options{}).Run(context.Background())
	if err != nil || !rep.Finished {
		t.Fatal(err, rep)
	}
	if !checked {
		t.Fatal("no step was executed")
	}
	if secondErr == nil {
		_ = second.Close()
		t.Fatal("a second owner opened the journal while the first was live")
	}
	if second != nil {
		t.Fatal("a failed open returned a journal handle")
	}
	if !strings.Contains(secondErr.Error(), "exclusive writer") {
		t.Fatalf("second open error is not clear: %v", secondErr)
	}
	firstEntries := first.Entries()
	executed := fake.Calls()
	if len(executed) == 0 {
		t.Fatal("the first owner executed no step")
	}
	done := false
	for _, e := range firstEntries {
		if e.Kind == EntryDone && e.StepID == executed[0].ID() {
			done = true
		}
	}
	if !done {
		t.Fatalf("the first owner did not record the step it executed")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close the first owner: %v", err)
	}

	resumed, err := OpenJournal(path, s)
	if err != nil {
		t.Fatalf("a new open after the first owner closed: %v", err)
	}
	defer resumed.Close()
	if got := resumed.Entries(); !reflect.DeepEqual(got, firstEntries) {
		t.Fatalf("the reopened journal has %d records, the first owner wrote %d", len(got), len(firstEntries))
	}
}

func TestJournalCorruptionIsRefused(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	j := openJournal(t, dir, s)
	if _, err := mustRunner(t, s, j, NewFakeExecutor(s.Config), Options{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	lines[3] = "not json"
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(path, s); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("got %v, want ErrJournalCorrupt", err)
	}
}

func TestPlanDigestMismatchIsRefused(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	j := openJournal(t, dir, s)
	if _, err := mustRunner(t, s, j, NewFakeExecutor(s.Config), Options{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()

	cfg := smallConfig()
	cfg.NET02Mandatory = true // a changed protocol knob changes the digest
	other := smallSchedule(t, cfg)
	if other.Digest == s.Digest {
		t.Fatal("setup: digests equal")
	}
	_, err := OpenJournal(filepath.Join(dir, "journal.jsonl"), other)
	if !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("got %v, want ErrPlanMismatch", err)
	}
	// And the file is untouched: the original plan still resumes.
	j2, err := OpenJournal(filepath.Join(dir, "journal.jsonl"), s)
	if err != nil {
		t.Fatalf("original plan refused after a mismatch: %v", err)
	}
	_ = j2.Close()
}

func TestSetupFailureIsRecordedNotDropped(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	bad := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, _ *Outcome) error {
		if st.ID() == bad.ID() {
			return errors.New("guest did not boot")
		}
		return nil
	}
	j, rep := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	if !rep.Finished || rep.SetupFailures != 1 || rep.UnitsUsed != 89 {
		t.Fatalf("report %+v", rep)
	}
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var rec *evalscore.AdversarialRecord
	for i := range res.Input.Adversarial {
		if res.Input.Adversarial[i].CaseID == bad.CaseID {
			rec = &res.Input.Adversarial[i]
		}
	}
	if rec == nil {
		t.Fatal("case with a setup failure vanished from the export")
	}
	if rec.CEffect != evalscore.CEffectUnknown || rec.EvidenceLoss == evalscore.LossNone || rec.IdentityVerified {
		t.Fatalf("setup failure must be unknown evidence loss: %+v", rec)
	}
}

func TestContextCancelStopsAndAbandonsNothingElse(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	ctx, cancel := context.WithCancel(context.Background())
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, _ *Outcome) error {
		if st.Index == 4 {
			cancel()
		}
		return nil
	}
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, fake, Options{}).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if rep.Finished || rep.Stop != StopCanceled || rep.Remaining != 89-5 {
		t.Fatalf("report %+v", rep)
	}
}

func TestNotWiredExecutorsTouchNoUnits(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, Executors{}, Options{}).Run(context.Background())
	if !errors.Is(err, ErrNotWired) {
		t.Fatalf("got %v, want ErrNotWired", err)
	}
	if rep.UnitsUsed != 0 || j.UnitsUsed() != 0 {
		t.Fatalf("units consumed by an unwired run: %+v", rep)
	}
	if _, err := (GovernedC{}).Execute(context.Background(), s.Steps[0]); !errors.Is(err, ErrNotWired) {
		t.Fatalf("GovernedC: %v", err)
	}
	// An executor that discovers it is unwired at call time voids its unit.
	r2 := mustRunner(t, s, j, Executors{C: GovernedC{}, P: NewFakeExecutor(s.Config), B: NewFakeExecutor(s.Config), V: NewFakeExecutor(s.Config)}, Options{})
	rep, err = r2.Run(context.Background())
	if !errors.Is(err, ErrNotWired) {
		t.Fatalf("got %v", err)
	}
	if j.UnitsUsed() != 0 {
		t.Fatalf("a voided begin still counted: %d", j.UnitsUsed())
	}
}

func TestExecutorsRouteByConfiguration(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	byCfg := map[Configuration]*FakeExecutor{}
	ex := Executors{}
	for _, c := range []Configuration{CfgA, CfgB, CfgC, CfgP, CfgV} {
		f := NewFakeExecutor(s.Config)
		byCfg[c] = f
	}
	ex.A, ex.B, ex.C, ex.P, ex.V = byCfg[CfgA], byCfg[CfgB], byCfg[CfgC], byCfg[CfgP], byCfg[CfgV]
	j, _ := runAll(t, s, t.TempDir(), ex)
	defer j.Close()
	if len(byCfg[CfgA].Calls()) != 0 {
		t.Fatal("A executor called although A is outside the plan")
	}
	for c, f := range byCfg {
		for _, st := range f.Calls() {
			if st.Configuration != c {
				t.Fatalf("executor %s received a %s step", c, st.Configuration)
			}
		}
	}
	if len(byCfg[CfgP].Calls()) != 22 { // 11 cases x (static + adaptive)
		t.Fatalf("P calls %d", len(byCfg[CfgP].Calls()))
	}
}

// ---- rerun rule -----------------------------------------------------------

func outageHook(target Step) func(Step, *Outcome) error {
	return func(st Step, o *Outcome) error {
		if st.ID() == target.ID() && st.Attempt == 1 {
			o.EvidenceLoss = evalscore.LossExternalOutage
			o.OutageEvidenceDigest = "sha256:" + strings.Repeat("ab", 32)
		}
		return nil
	}
}

func TestRerunWithoutOutageEvidenceIsRefused(t *testing.T) {
	cfg := smallConfig()
	cfg.RerunHeadroom = 2
	s := smallSchedule(t, cfg)
	fake := NewFakeExecutor(s.Config)
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	r := mustRunner(t, s, j, fake, Options{})
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	before := j.UnitsUsed()
	if err := r.Rerun(context.Background(), st.ID()); !errors.Is(err, ErrRerunNotAllowed) {
		t.Fatalf("got %v, want ErrRerunNotAllowed", err)
	}
	if j.UnitsUsed() != before {
		t.Fatal("a refused rerun consumed a unit")
	}
	if err := r.Rerun(context.Background(), "no-such-step"); err == nil {
		t.Fatal("rerun of an unknown step succeeded")
	}
}

func TestRerunWithOutageEvidenceKeepsBothAndIsScoredTerminal(t *testing.T) {
	cfg := smallConfig()
	cfg.RerunHeadroom = 1
	s := smallSchedule(t, cfg)
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = outageHook(st)
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	r := mustRunner(t, s, j, fake, Options{})

	if err := r.Rerun(context.Background(), st.ID()); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if j.UnitsUsed() != 90 {
		t.Fatalf("units %d, want 90 (a rerun costs a unit)", j.UnitsUsed())
	}
	var attempts []int
	for _, e := range j.Entries() {
		if e.Kind == EntryDone && e.StepID == st.ID() {
			attempts = append(attempts, e.Attempt)
			if e.Attempt == 1 && (e.Outcome == nil || e.Outcome.OutageEvidenceDigest == "") {
				t.Fatal("the original outcome was not kept intact")
			}
		}
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("attempts journaled: %v", attempts)
	}
	// Export: two records for the case, attempts 1 and 2, the original an evidenced outage.
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got []evalscore.AdversarialRecord
	for _, rec := range res.Input.Adversarial {
		if rec.CaseID == st.CaseID {
			got = append(got, rec)
		}
	}
	if len(got) != 2 || got[0].Attempt != 1 || got[1].Attempt != 2 {
		t.Fatalf("records for the case: %+v", got)
	}
	if got[0].EvidenceLoss != evalscore.LossExternalOutage || got[0].OutageEvidenceDigest == "" {
		t.Fatalf("original lost its outage evidence: %+v", got[0])
	}
	if got[1].EvidenceLoss != evalscore.LossNone || got[1].Classify().Disposition != evalscore.ValidPass {
		t.Fatalf("rerun is not a clean terminal: %+v", got[1])
	}

	// A second rerun of the same step, or of any other step of the case, is refused.
	if err := r.Rerun(context.Background(), st.ID()); !errors.Is(err, ErrRerunLimit) {
		t.Fatalf("second rerun: got %v, want ErrRerunLimit", err)
	}
	if j.UnitsUsed() != 90 {
		t.Fatal("a refused second rerun consumed a unit")
	}
}

func TestSecondRerunOfSameCaseOtherStepIsRefused(t *testing.T) {
	cfg := smallConfig()
	cfg.RerunHeadroom = 5
	s := smallSchedule(t, cfg)
	c := firstCase(s, "adversarial")
	a := findStep(t, s, c, RoleTrajectory, CfgC)
	b := findStep(t, s, c, RoleStaticReplay, CfgC)
	fake := NewFakeExecutor(s.Config)
	h1, h2 := outageHook(a), outageHook(b)
	fake.Hook = func(st Step, o *Outcome) error { _ = h1(st, o); return h2(st, o) }
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	r := mustRunner(t, s, j, fake, Options{})
	if err := r.Rerun(context.Background(), a.ID()); err != nil {
		t.Fatal(err)
	}
	if err := r.Rerun(context.Background(), b.ID()); !errors.Is(err, ErrRerunLimit) {
		t.Fatalf("got %v, want ErrRerunLimit (one rerun per case)", err)
	}
}

func TestRerunOutageFlagWithoutDigestIsRefused(t *testing.T) {
	cfg := smallConfig()
	cfg.RerunHeadroom = 1
	s := smallSchedule(t, cfg)
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(x Step, o *Outcome) error {
		if x.ID() == st.ID() {
			o.EvidenceLoss = evalscore.LossExternalOutage // claims an outage, no evidence
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	if err := mustRunner(t, s, j, fake, Options{}).Rerun(context.Background(), st.ID()); !errors.Is(err, ErrRerunNotAllowed) {
		t.Fatalf("got %v, want ErrRerunNotAllowed", err)
	}
}

func TestRerunRespectsUnitBudget(t *testing.T) {
	cfg := smallConfig() // cap == plan, headroom off
	cfg.NoRerunHeadroom = true
	s := smallSchedule(t, cfg)
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = outageHook(st)
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	if err := mustRunner(t, s, j, fake, Options{}).Rerun(context.Background(), st.ID()); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("got %v, want ErrBudgetExhausted", err)
	}
	if j.UnitsUsed() != 89 {
		t.Fatalf("units %d", j.UnitsUsed())
	}
}

func TestRerunMidRunReservesCapacityForPlannedSteps(t *testing.T) {
	cfg := smallConfig()
	cfg.NoRerunHeadroom = true
	s := smallSchedule(t, cfg)
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	// Run only the first five steps by cancelling, then try the rerun.
	ctx, cancel := context.WithCancel(context.Background())
	fake2 := NewFakeExecutor(s.Config)
	fake2.Hook = func(x Step, o *Outcome) error {
		_ = outageHook(st)(x, o)
		if x.Index == 4 {
			cancel()
		}
		return nil
	}
	r := mustRunner(t, s, j, fake2, Options{})
	_, _ = r.Run(ctx)
	if err := r.Rerun(context.Background(), st.ID()); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("a mid-run rerun would starve planned steps: got %v", err)
	}
}

// ---- budget ---------------------------------------------------------------

func TestBudgetStopsOnLedgerUSDStopRule(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	committed := int64(0)
	ledger := func() (snap usdledger.Snapshot, err error) {
		return okLedger(len(fake.Calls()), committed)()
	}
	fake.Hook = func(st Step, _ *Outcome) error {
		if st.Index == 9 { // after step 9 the ledger shows less than one unit of headroom
			committed = 5_000_000 - 500
		}
		return nil
	}
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, fake, Options{Ledger: ledger}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Finished || rep.Stop != StopUSD || rep.Executed != 10 || rep.Remaining != 79 || rep.PlannedUnits != 89 {
		t.Fatalf("report %+v", rep)
	}
	if len(fake.Calls()) != 10 {
		t.Fatalf("executed %d steps", len(fake.Calls()))
	}
	// The denominator is not shrunk: the export omits the unfinished cases and
	// names them, and evalreport shows the coverage as not exact.
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Incomplete) == 0 {
		t.Fatal("unfinished cases not reported")
	}
	src, alloc := synth(t, smallQuotas())
	_ = src
	r, err := evalreport.Build(evalreport.Inputs{Manifest: alloc, Trials: res.Input, Ledger: mustSnap(t, committed)})
	if err != nil {
		t.Fatal(err)
	}
	if r.Coverage.Exact || r.H1.RuleSatisfied {
		t.Fatalf("an unfinished run reports exact coverage: %+v", r.Coverage)
	}
}

func TestBudgetStopsOnLedgerUnitCount(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	// The ledger already holds 80 units from before this run (pilots): 9 remain.
	ledger := func() (usdledger.Snapshot, error) { return okLedger(80+len(fake.Calls()), 1000)() }
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, fake, Options{Ledger: ledger}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Finished || rep.Stop != StopUnits || rep.Executed != 9 || rep.Remaining != 80 {
		t.Fatalf("report %+v", rep)
	}
}

func TestBudgetRefusesLedgerAboveRegisteredCap(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	ledger := func() (usdledger.Snapshot, error) {
		snap, _ := okLedger(0, 0)()
		snap.CapMicroUSD = 6_000_000
		snap.RemainingMicroUSD = 6_000_000
		return snap, nil
	}
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, fake, Options{Ledger: ledger}).Run(context.Background())
	if err != nil || rep.Stop != StopUSD || rep.Executed != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestBudgetLedgerErrorFailsClosed(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	ledger := func() (usdledger.Snapshot, error) { return usdledger.Snapshot{}, errors.New("ledger unreadable") }
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	if _, err := mustRunner(t, s, j, fake, Options{Ledger: ledger}).Run(context.Background()); err == nil {
		t.Fatal("unreadable ledger did not stop the run")
	}
	if len(fake.Calls()) != 0 {
		t.Fatal("a step started without a readable ledger")
	}
}

func TestBudgetUnitCapNeverAbovePlannedUnits(t *testing.T) {
	cfg := testConfig()
	cfg.UnitCap = evalreport.PlannedUnits
	if _, err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}
	cfg.UnitCap = evalreport.PlannedUnits + 1
	if _, err := cfg.normalize(); err == nil {
		t.Fatal("unit cap above the registered plan accepted")
	}
	if n, _ := (Config{FreezeCommit: strings.Repeat("a", 40)}).normalize(); n.UnitCap != evalreport.PlannedUnits {
		t.Fatalf("default unit cap %d", n.UnitCap)
	}
}
