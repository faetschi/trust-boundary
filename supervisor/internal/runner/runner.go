package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/usdledger"
)

// StopReason says why a run ended before the plan was finished.
type StopReason string

const (
	StopNone     StopReason = ""
	StopUnits    StopReason = "units_exhausted"
	StopUSD      StopReason = "usd_cap"
	StopCanceled StopReason = "canceled"
)

// Options are the run-time inputs that are not part of the frozen protocol.
type Options struct {
	// Ledger supplies a usdledger snapshot before every step. If nil, only the
	// unit budget is enforced. A ledger that cannot be read stops the run with
	// an error, because the cap cannot then be checked.
	Ledger func() (usdledger.Snapshot, error)
}

// RunReport describes what a Run call did and what is still undone. The
// denominator, PlannedUnits, is the size of the plan and never shrinks.
type RunReport struct {
	PlanDigest    string
	PlannedUnits  int
	UnitsUsed     int // units spent in the journal, reruns included
	Executed      int // steps run by this call
	AlreadyDone   int // steps that already had a terminal record
	Abandoned     int // interrupted steps closed by this call
	SetupFailures int // setup failures in the journal
	Remaining     int // plan steps without a terminal record
	Finished      bool
	Stop          StopReason
	SpentMicroUSD int64 // cost reported by the outcomes in the journal
}

// Runner executes a Schedule against an Executor and records every step in a
// Journal.
type Runner struct {
	sched Schedule
	j     *Journal
	exec  Executor
	opt   Options
}

// NewRunner returns a Runner. The journal must have been opened for s.
func NewRunner(s Schedule, j *Journal, e Executor, opt Options) (*Runner, error) {
	if j == nil || e == nil {
		return nil, errors.New("runner: nil journal or executor")
	}
	if j.sched.Digest != s.Digest {
		return nil, ErrPlanMismatch
	}
	return &Runner{sched: s, j: j, exec: e, opt: opt}, nil
}

// Run executes every step that has no terminal record, in plan order, and
// stops cleanly when the budget is exhausted or ctx ends. The report states
// whether the plan is finished; a run that is not finished is unfinished, and
// the plan is never reduced.
//
// A step that was begun in an earlier process but has no terminal record is
// closed as abandoned and not run again: its unit is spent and its provider
// spend is unknown.
func (r *Runner) Run(ctx context.Context) (RunReport, error) {
	if c, ok := r.exec.(interface{ Check(Schedule) error }); ok {
		if err := c.Check(r.sched); err != nil {
			return r.report(RunReport{}), err
		}
	}
	var rep RunReport
	for _, st := range r.sched.Steps { // a rerun interrupted in an earlier process
		if r.j.begunOf(st.ID(), 2) && r.j.terminalOf(st.ID(), 2) == nil {
			if err := r.j.append(Entry{Kind: EntryAbandoned, StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 2,
				Reason: "rerun begun in an earlier process and never finished; not retried"}); err != nil {
				return r.report(rep), err
			}
			rep.Abandoned++
		}
	}
	for _, st := range r.sched.Steps {
		if t := r.j.terminalOf(st.ID(), 1); t != nil {
			rep.AlreadyDone++
			continue
		}
		if r.j.begunOf(st.ID(), 1) {
			if err := r.j.append(Entry{Kind: EntryAbandoned, StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 1,
				Reason: "begun in an earlier process and never finished; not retried"}); err != nil {
				return r.report(rep), err
			}
			rep.Abandoned++
			continue
		}
		if err := ctx.Err(); err != nil {
			rep.Stop = StopCanceled
			return r.report(rep), err
		}
		stop, err := r.admit(false)
		if err != nil {
			return r.report(rep), err
		}
		if stop != StopNone {
			rep.Stop = stop
			return r.report(rep), nil
		}
		if err := r.execute(ctx, st, 1); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				rep.Stop = StopCanceled
			}
			return r.report(rep), err
		}
		rep.Executed++
	}
	return r.report(rep), nil
}

// Rerun runs the one permitted outage rerun of the step. It is allowed only
// when the original outcome reports an external outage with its evidence
// digest (ErrRerunNotAllowed otherwise), only once per case (ErrRerunLimit),
// and only if the budget has slack beyond the planned steps still to run
// (ErrBudgetExhausted). The original record stays in the journal and the rerun
// is the scored terminal. A rerun costs a unit.
func (r *Runner) Rerun(ctx context.Context, stepID string) error {
	st, ok := r.sched.Lookup(stepID)
	if !ok {
		return fmt.Errorf("runner: unknown step %q", stepID)
	}
	orig := r.j.terminalOf(stepID, 1)
	if orig == nil || orig.Kind != EntryDone || !outageEvidenced(orig.Outcome) {
		return fmt.Errorf("%w: %s", ErrRerunNotAllowed, stepID)
	}
	if prev, dup := r.j.rerunOf(st.CaseID); dup {
		return fmt.Errorf("%w: %s already rerun as %s", ErrRerunLimit, st.CaseID, prev.id)
	}
	stop, err := r.admit(true)
	if err != nil {
		return err
	}
	if stop != StopNone {
		return fmt.Errorf("%w: %s", ErrBudgetExhausted, stop)
	}
	return r.execute(ctx, st, 2)
}

// admit decides whether one more unit may be spent. Planned steps are
// guaranteed their units: a rerun is admitted only if the units used, the
// planned steps not yet begun and the rerun itself fit within UnitCap plus
// RerunHeadroom. A ledger snapshot, if supplied, can only tighten this.
func (r *Runner) admit(rerun bool) (StopReason, error) {
	cfg := r.sched.Config
	need := r.j.UnitsUsed() + (r.sched.Units() - r.j.plannedBegun())
	if rerun {
		need++
	}
	if need > cfg.UnitCap+cfg.RerunHeadroom {
		return StopUnits, nil
	}
	if r.opt.Ledger == nil {
		return StopNone, nil
	}
	snap, err := r.opt.Ledger()
	if err != nil {
		return StopNone, fmt.Errorf("runner: ledger snapshot: %w", err)
	}
	if snap.CapMicroUSD > evalreport.RegisteredCapMicroUSD || snap.CommittedMicroUSD > evalreport.RegisteredCapMicroUSD ||
		snap.StopRule || snap.Overspent || snap.RemainingMicroUSD < snap.MinPerUnitWorstCaseMicroUSD ||
		evalreport.RegisteredCapMicroUSD-snap.CommittedMicroUSD < snap.MinPerUnitWorstCaseMicroUSD {
		return StopUSD, nil
	}
	if snap.OpenUnits+snap.UncertainUnits+snap.SettledUnits+1 > cfg.UnitCap+cfg.RerunHeadroom {
		return StopUnits, nil
	}
	return StopNone, nil
}

// execute runs one attempt of a step: begin, execute, terminal record.
func (r *Runner) execute(ctx context.Context, st Step, attempt int) error {
	id := st.ID()
	base := Entry{StepID: id, CaseID: st.CaseID, Index: st.Index, Attempt: attempt}
	begin := base
	begin.Kind = EntryBegin
	if err := r.j.append(begin); err != nil {
		return err
	}
	run := st
	run.Attempt = attempt
	t0 := time.Now()
	out, err := r.exec.Execute(ctx, run)
	elapsed := time.Since(t0).Milliseconds()

	end := base
	end.ElapsedMS = elapsed
	switch {
	case errors.Is(err, ErrNotWired):
		end.Kind, end.Reason = EntryVoid, err.Error()
		if aerr := r.j.append(end); aerr != nil {
			return aerr
		}
		return err
	case err != nil && ctx.Err() != nil:
		end.Kind, end.Reason = EntryAbandoned, "interrupted: "+ctx.Err().Error()
		if aerr := r.j.append(end); aerr != nil {
			return aerr
		}
		return ctx.Err()
	case err != nil:
		end.Kind, end.SetupFailure = EntryDone, err.Error()
	default:
		if out.EvidenceLoss == "" {
			out.EvidenceLoss = "none"
		}
		end.Kind, end.Outcome = EntryDone, &out
	}
	return r.j.append(end)
}

// report fills the journal-derived fields of rep.
func (r *Runner) report(rep RunReport) RunReport {
	rep.PlanDigest = r.sched.Digest
	rep.PlannedUnits = r.sched.Units()
	rep.UnitsUsed = r.j.UnitsUsed()
	for _, e := range r.j.Entries() {
		if e.Kind == EntryDone {
			if e.Outcome != nil {
				rep.SpentMicroUSD += e.Outcome.Metrics.CostMicroUSD
			} else {
				rep.SetupFailures++
			}
		}
	}
	for _, st := range r.sched.Steps {
		if r.j.terminalOf(st.ID(), 1) == nil {
			rep.Remaining++
		}
	}
	rep.Finished = rep.Remaining == 0
	return rep
}
