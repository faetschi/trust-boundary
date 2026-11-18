package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Regression tests for the third adversarial Codex round, finding 1: start
// accounting. Every dispatched attempt is spent by default; only a refusal in the
// side-effect-free Prepare phase, which the runner controls, can void it.

// silentRefuser models the Codex counterexample: it makes a visible effect, never
// cooperates in any start accounting, and returns an empty outcome with a
// refusal error. visible counts the effects; startedBefore records whether the
// runner had journaled a durable started entry for the step at that moment.
type silentRefuser struct {
	Executor
	j             *Journal
	target        Step
	err           error
	visible       int
	startedBefore bool
}

func (p *silentRefuser) Execute(ctx context.Context, s Step) (Outcome, error) {
	if s.ID() != p.target.ID() {
		return p.Executor.Execute(ctx, s)
	}
	p.visible++ // the visible effect (a write to the workspace)
	for _, e := range p.j.Entries() {
		if e.StepID == s.ID() && e.Kind == EntryStarted {
			p.startedBefore = true
		}
	}
	return Outcome{}, p.err
}

func TestR3VisibleEffectThenSetupRefusedSpendsTheUnit(t *testing.T) {
	for _, refusal := range []error{ErrSetupRefused, ErrNotWired, fmt.Errorf("%w: admission denied", ErrSetupRefused)} {
		s := smallSchedule(t, smallConfig())
		traj := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
		dir := t.TempDir()
		j := openJournal(t, dir, s)
		ex := &silentRefuser{Executor: NewFakeExecutor(s.Config), j: j, target: traj, err: refusal}
		rep, err := mustRunner(t, s, j, ex, Options{}).Run(context.Background())
		if err != nil || !rep.Finished || rep.UnitsUsed != 89 || j.UnitsUsed() != 89 || rep.SetupFailures != 1 {
			t.Fatalf("%v: a refusal after a visible effect did not spend the unit: %+v %v", refusal, rep, err)
		}
		if ex.visible != 1 || !ex.startedBefore {
			t.Fatalf("%v: visible %d, started journaled before the effect %v", refusal, ex.visible, ex.startedBefore)
		}
		for _, e := range j.Entries() {
			if e.StepID == traj.ID() && e.Kind == EntryVoid {
				t.Fatalf("%v: the attempt was voided", refusal)
			}
		}
		// Codex ran it twice and saw two writes: a second run, in a fresh process,
		// does not dispatch the step again.
		_ = j.Close()
		j = openJournal(t, dir, s)
		ex2 := &silentRefuser{Executor: NewFakeExecutor(s.Config), j: j, target: traj, err: refusal}
		rep, err = mustRunner(t, s, j, ex2, Options{}).Run(context.Background())
		if err != nil || ex2.visible != 0 || rep.UnitsUsed != 89 || rep.AlreadyDone != 89 {
			t.Fatalf("%v: second run: visible %d %+v %v", refusal, ex2.visible, rep, err)
		}
		_ = j.Close()
	}
}

// preparing records Prepare and Execute calls of one executor.
type preparing struct {
	Executor
	prepared, executed []string
	refuse             func(Step) error
}

func (p *preparing) Prepare(ctx context.Context, s Step) error {
	p.prepared = append(p.prepared, s.ID())
	if p.refuse != nil {
		if err := p.refuse(s); err != nil {
			return err
		}
	}
	return p.Executor.Prepare(ctx, s)
}

func (p *preparing) Execute(ctx context.Context, s Step) (Outcome, error) {
	if !s.Resumed { // a resumed call collects or settles an attempt already sent; it is not a dispatch
		p.executed = append(p.executed, s.ID())
	}
	return p.Executor.Execute(ctx, s)
}

func TestR3PrepareRefusalVoidsTheAttempt(t *testing.T) {
	for _, refusal := range []error{ErrSetupRefused, ErrNotWired, errors.New("registration missing")} {
		s := smallSchedule(t, smallConfig())
		traj := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
		ex := &preparing{Executor: NewFakeExecutor(s.Config), refuse: func(st Step) error {
			if st.ID() == traj.ID() {
				return refusal
			}
			return nil
		}}
		j := openJournal(t, t.TempDir(), s)
		rep, err := mustRunner(t, s, j, ex, Options{}).Run(context.Background())
		if !errors.Is(err, refusal) || rep.UnitsUsed != 0 || j.UnitsUsed() != 0 || rep.Finished || rep.Remaining != 89 {
			t.Fatalf("%v: %+v %v", refusal, rep, err)
		}
		if slices.Contains(ex.executed, traj.ID()) {
			t.Fatalf("%v: Execute was called after a Prepare refusal", refusal)
		}
		kinds := []EntryKind{}
		for _, e := range j.Entries() {
			if e.StepID == traj.ID() {
				kinds = append(kinds, e.Kind)
			}
		}
		if !slices.Equal(kinds, []EntryKind{EntryBegin, EntryVoid}) {
			t.Fatalf("%v: journal entries of the step %v, want begin, void", refusal, kinds)
		}
		_ = j.Close()
	}
}

func TestR3PrepareRunsBeforeStartedBeforeExecute(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	var order []string
	var j *Journal
	ex := &orderExecutor{Executor: NewFakeExecutor(s.Config), log: &order, j: &j}
	j = openJournal(t, t.TempDir(), s)
	defer j.Close()
	if _, err := mustRunner(t, s, j, ex, Options{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2*89 {
		t.Fatalf("%d phase events", len(order))
	}
	for i := 0; i < len(order); i += 2 {
		if !strings.HasPrefix(order[i], "prepare:") || order[i+1] != "execute:started=true" {
			t.Fatalf("phase order %v", order[i:i+2])
		}
	}
}

type orderExecutor struct {
	Executor
	log *[]string
	j   **Journal
}

func (p *orderExecutor) started(s Step) bool {
	for _, e := range (*p.j).Entries() {
		if e.StepID == s.ID() && e.Kind == EntryStarted {
			return true
		}
	}
	return false
}

func (p *orderExecutor) Prepare(ctx context.Context, s Step) error {
	if p.started(s) {
		*p.log = append(*p.log, "prepare:ALREADY-STARTED")
		return nil
	}
	*p.log = append(*p.log, "prepare:"+s.ID())
	return nil
}

func (p *orderExecutor) Execute(ctx context.Context, s Step) (Outcome, error) {
	*p.log = append(*p.log, fmt.Sprintf("execute:started=%v", p.started(s)))
	return p.Executor.Execute(ctx, s)
}

// crashing dies inside Execute, after the runner has journaled the start.
type crashing struct {
	Executor
	target Step
}

func (p crashing) Execute(ctx context.Context, s Step) (Outcome, error) {
	if s.ID() == p.target.ID() {
		panic("process died")
	}
	return p.Executor.Execute(ctx, s)
}

func TestR3CrashAfterStartedIsAbandonedAndSpentOnResume(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	st := s.Steps[3]
	j := openJournal(t, dir, s)
	func() {
		defer func() { _ = recover() }()
		_, _ = mustRunner(t, s, j, crashing{Executor: NewFakeExecutor(s.Config), target: st}, Options{}).Run(context.Background())
	}()
	_ = j.Close()

	j = openJournal(t, dir, s) // fresh process
	defer j.Close()
	if j.UnitsUsed() != 4 {
		t.Fatalf("units after the crash %d, want 4 (the crashed attempt is spent)", j.UnitsUsed())
	}
	ex := &preparing{Executor: NewFakeExecutor(s.Config)}
	rep, err := mustRunner(t, s, j, ex, Options{}).Run(context.Background())
	if err != nil || rep.Abandoned != 1 || rep.UnitsUsed != 89 || !rep.Finished {
		t.Fatalf("%+v %v", rep, err)
	}
	if slices.Contains(ex.prepared, st.ID()) || slices.Contains(ex.executed, st.ID()) {
		t.Fatal("the crashed step was dispatched again")
	}
	var reason string
	for _, e := range j.Entries() {
		if e.Kind == EntryAbandoned && e.StepID == st.ID() {
			reason = e.Reason
		}
		if e.Kind == EntryVoid && e.StepID == st.ID() {
			t.Fatal("the crashed attempt was voided")
		}
	}
	if !strings.Contains(reason, "started") {
		t.Fatalf("abandoned reason %q does not say that execution had started", reason)
	}
}

func TestR3RoutersAndSkeletonsRefuseInPrepare(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	if err := (Executors{}).Prepare(context.Background(), s.Steps[0]); !errors.Is(err, ErrNotWired) {
		t.Fatalf("Executors.Prepare: %v", err)
	}
	if err := (GovernedC{}).Prepare(context.Background(), s.Steps[0]); !errors.Is(err, ErrNotWired) {
		t.Fatalf("GovernedC.Prepare: %v", err)
	}
	f := NewFakeExecutor(s.Config)
	f.PrepareHook = func(Step) error { return ErrSetupRefused }
	if err := (Executors{A: f, B: f, C: f, P: f, V: f}).Prepare(context.Background(), s.Steps[0]); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("routed Prepare: %v", err)
	}
}
