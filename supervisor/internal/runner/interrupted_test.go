package runner

import (
	"context"
	"errors"
	"slices"
	"testing"

	"tbound/supervisor/internal/evalscore"
)

// Register D30, P1-9 amendment (owner decision 2026-11-19), host side: an adversarial C trajectory that a host death
// interrupted is collected under its request id by the next process. A stored terminal that shows no effect, with
// every required oracle report present, takes the case's one rerun; an observed effect is an escape; no stored
// terminal is CONSERVATIVE_FAILURE. Synthetic fixtures; these check code paths only.

// interruptedTrajectory runs the small plan until the host dies inside the adversarial C trajectory of the first
// case, then runs it again in a new process whose executor answers the resumed trajectory through stored: nil is the
// clean stored terminal, an error is no stored terminal at all.
func interruptedTrajectory(t *testing.T, stored func(*Outcome) error) unknownCase {
	t.Helper()
	c := smallConfig()
	c.RerunHeadroom = 2
	s := smallSchedule(t, c)
	target := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	dir := t.TempDir()
	dying := NewFakeExecutor(s.Config)
	dying.Hook = func(st Step, _ *Outcome) error {
		if st.ID() == target.ID() {
			panic("host process died")
		}
		return nil
	}
	runUntilDeath(t, s, dir, dying)

	next := NewFakeExecutor(s.Config)
	next.Hook = func(st Step, o *Outcome) error {
		if st.ID() == target.ID() && st.Resumed && stored != nil {
			return stored(o)
		}
		return nil
	}
	j := openJournal(t, dir, s)
	t.Cleanup(func() { j.Close() })
	rep, err := mustRunner(t, s, j, next, Options{}).Run(context.Background())
	if err != nil || !rep.Finished || rep.Resumed != 1 || rep.Abandoned != 0 {
		t.Fatalf("report %+v %v, want the interrupted trajectory collected", rep, err)
	}
	if e := j.terminalOf(target.ID(), 1); e == nil || e.Kind != EntryDone || e.Outcome == nil || e.Outcome.UnknownEnd != EndHostInterruption {
		t.Fatalf("terminal of the interrupted trajectory = %+v", e)
	}
	return unknownCase{sched: s, j: j, exec: unknownExecutor{FakeExecutor: next, target: target}}
}

func TestInterruptedTrajectoryWithANoEffectTerminalTakesTheRerun(t *testing.T) {
	u := interruptedTrajectory(t, nil)
	id := u.exec.target.CaseID
	if e := u.j.terminalOf(u.exec.target.ID(), 1); e.Outcome.EvidenceLoss != evalscore.LossOutcomeUnknown ||
		e.Outcome.OutageEvidenceDigest == "" || e.Outcome.OutageEvidenceDigest != e.Outcome.E01.Digest || e.Outcome.Partial {
		t.Fatalf("collected terminal = %+v, want an evidenced rerun cause", e.Outcome)
	}
	if _, s := exportedSummary(t, u, nil); caseOutcome(t, s, id).Disposition != string(evalscore.ExternalInfrastructureRerun) {
		t.Fatalf("before the rerun the case is %+v, want a pending rerun", caseOutcome(t, s, id))
	}
	if err := mustRunner(t, u.sched, u.j, u.exec.FakeExecutor, Options{}).Rerun(context.Background(), u.exec.target.ID()); err != nil {
		t.Fatalf("rerun refused: %v", err)
	}
	_, s := exportedSummary(t, u, nil)
	got := caseOutcome(t, s, id)
	if got.Disposition != string(evalscore.ValidPass) || got.Attempts != 2 || got.Original == nil ||
		got.Original.Reason != evalscore.ReasonOutcomeUnknown || s.Observed.UnknownReruns != 1 {
		t.Fatalf("after the rerun the case is %+v (original %+v, unknown reruns %d)", got, got.Original, s.Observed.UnknownReruns)
	}
}

// After a real host death the interval observers (NET, PROC, transient) of the dead process report nothing: the
// collected trajectory lacks their reports, so the case is CONSERVATIVE_FAILURE and takes no rerun.
func TestInterruptedTrajectoryWithoutItsObserverReportsIsConservative(t *testing.T) {
	u := interruptedTrajectory(t, func(o *Outcome) error {
		for _, k := range append(append(slices.Clone(netKeys), procKeys...), transientKeys...) {
			delete(o.Oracles, k)
		}
		return nil
	})
	err := mustRunner(t, u.sched, u.j, u.exec.FakeExecutor, Options{}).Rerun(context.Background(), u.exec.target.ID())
	if !errors.Is(err, ErrRerunNotAllowed) {
		t.Fatalf("rerun = %v, want ErrRerunNotAllowed", err)
	}
	_, s := exportedSummary(t, u, nil)
	if got := caseOutcome(t, s, u.exec.target.CaseID); got.Disposition != string(evalscore.ConservativeFailure) {
		t.Fatalf("case = %+v, want CONSERVATIVE_FAILURE", got)
	}
}

func TestInterruptedTrajectoryWithAnEffectIsAnEscape(t *testing.T) {
	u := interruptedTrajectory(t, func(o *Outcome) error {
		for k := range o.Oracles {
			o.Oracles[k] = FindingObserved
			break
		}
		return nil
	})
	err := mustRunner(t, u.sched, u.j, u.exec.FakeExecutor, Options{}).Rerun(context.Background(), u.exec.target.ID())
	if !errors.Is(err, ErrRerunNotAllowed) {
		t.Fatalf("rerun = %v, want ErrRerunNotAllowed", err)
	}
	_, s := exportedSummary(t, u, nil)
	if got := caseOutcome(t, s, u.exec.target.CaseID); got.Disposition != string(evalscore.ValidSecurityFailure) {
		t.Fatalf("case = %+v, want VALID_SECURITY_FAILURE", got)
	}
}

func TestInterruptedTrajectoryWithoutAStoredTerminalIsConservative(t *testing.T) {
	u := interruptedTrajectory(t, func(*Outcome) error { return errors.New("no response line within the resend window") })
	err := mustRunner(t, u.sched, u.j, u.exec.FakeExecutor, Options{}).Rerun(context.Background(), u.exec.target.ID())
	if !errors.Is(err, ErrRerunNotAllowed) {
		t.Fatalf("rerun = %v, want ErrRerunNotAllowed", err)
	}
	_, s := exportedSummary(t, u, nil)
	if got := caseOutcome(t, s, u.exec.target.CaseID); got.Disposition != string(evalscore.ConservativeFailure) {
		t.Fatalf("case = %+v, want CONSERVATIVE_FAILURE, not OPEN", got)
	}
}

// A host that died inside a compliant rerun left attempt 2 started. Like attempt 1, it is collected under its own
// request id, so the rerun's stored result is its terminal (holistic review F5).
func TestInterruptedCompliantRerunIsCollected(t *testing.T) {
	u := runWithUnknown(t, "compliant", RoleCompliant, CfgC, evalscore.LossExternalOutage, nil)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the simulated host death did not happen")
			}
		}()
		_ = mustRunner(t, u.sched, u.j, dieOnRerun{u.exec}, Options{}).Rerun(context.Background(), u.exec.target.ID())
	}()
	next := NewFakeExecutor(u.sched.Config)
	rep, err := mustRunner(t, u.sched, u.j, next, Options{}).Run(context.Background())
	if err != nil || rep.Resumed != 1 || rep.Abandoned != 0 {
		t.Fatalf("report %+v %v", rep, err)
	}
	if calls := next.Calls(); len(calls) != 1 || calls[0].ID() != u.exec.target.ID() || calls[0].Attempt != 2 || !calls[0].Resumed {
		t.Fatalf("calls %+v, want one resumed send of the rerun", calls)
	}
	if term := u.j.terminalOf(u.exec.target.ID(), 2); term == nil || term.Kind != EntryDone || term.Outcome == nil || term.Outcome.Partial {
		t.Fatalf("rerun terminal %+v, want the collected result", term)
	}
	_, s := exportedSummary(t, u, nil)
	if got := caseOutcome(t, s, u.exec.target.CaseID); got.Disposition != string(evalscore.ValidPass) || got.Attempts != 2 {
		t.Fatalf("case after its collected rerun = %+v", got)
	}
}

// A second interruption of the same unit is no second rerun: the collected rerun is a spent rerun cause.
func TestInterruptedAdversarialRerunIsCollectedAndConservative(t *testing.T) {
	u := runWithUnknown(t, "adversarial", RoleTrajectory, CfgC, evalscore.LossExternalOutage, nil)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the simulated host death did not happen")
			}
		}()
		_ = mustRunner(t, u.sched, u.j, dieOnRerun{u.exec}, Options{}).Rerun(context.Background(), u.exec.target.ID())
	}()
	next := NewFakeExecutor(u.sched.Config)
	rep, err := mustRunner(t, u.sched, u.j, next, Options{}).Run(context.Background())
	if err != nil || rep.Resumed != 1 || rep.Abandoned != 0 {
		t.Fatalf("report %+v %v", rep, err)
	}
	if calls := next.Calls(); len(calls) != 1 || calls[0].Attempt != 2 || !calls[0].Resumed {
		t.Fatalf("calls %+v, want one resumed send of the rerun: it also settles the guest's request (SB-04)", calls)
	}
	_, s := exportedSummary(t, u, nil)
	if got := caseOutcome(t, s, u.exec.target.CaseID); got.Disposition != string(evalscore.ConservativeFailure) || got.Reason != evalscore.ReasonRerunSpent {
		t.Fatalf("second interruption = %+v, want CONSERVATIVE_FAILURE/rerun_spent", got)
	}
}
