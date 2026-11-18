package runner

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
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
	Resumed       int // started steps of an earlier process whose result this call collected
	SetupFailures int // setup failures in the journal (no outcome; the unit is spent)
	ExecFailures  int // steps that failed after execution began and kept a partial outcome
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
	if j.sched.Digest != s.Digest || j.sched.RunBinding != s.RunBinding {
		return nil, ErrPlanMismatch
	}
	if h, max := s.Config.RerunHeadroom, RerunHeadroomFor(s.PlannedUnits); h < 0 || h > max || (s.Config.NoRerunHeadroom && h != 0) {
		return nil, fmt.Errorf("runner: rerun headroom %d is outside the owner ceiling %d", h, max)
	}
	if err := s.validateWitnessPurpose(); err != nil {
		return nil, err
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
// spend is unknown. The exception is a started step that resumable admits: its
// executor is called again with Step.Resumed set, collects the result of the
// request the earlier process sent, and the step ends with that result. A started
// adversarial step is abandoned after its executor sent its request again with
// Step.Resumed until the guest settled it or the resend window ended; that answer is not scored.
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
				Reason: abandonReason(r.j.startedOf(st.ID(), 2), "rerun ")}); err != nil {
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
		if r.j.begunOf(st.ID(), 1) && r.j.startedOf(st.ID(), 1) && resumable(st) {
			if err := ctx.Err(); err != nil {
				rep.Stop = StopCanceled
				return r.report(rep), err
			}
			run := st
			run.Attempt, run.Resumed = 1, true
			if err := r.dispatch(ctx, run); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					rep.Stop = StopCanceled
				}
				return r.report(rep), err
			}
			rep.Resumed++
			continue
		}
		if r.j.begunOf(st.ID(), 1) {
			reason := abandonReason(r.j.startedOf(st.ID(), 1), "")
			if r.j.startedOf(st.ID(), 1) && st.Kind == evalcorpus.KindAdversarial && ctx.Err() == nil {
				// A guest service that restarted mid-request keeps the request open until its id is sent again, and an
				// open request blocks every later reset and begin (SB-04). The answer is not scored: the step's observers
				// died with the earlier process.
				run := st
				run.Attempt, run.Resumed = 1, true
				_, _ = r.exec.Execute(ctx, run)
				reason += "; its request id was sent again until the guest settled it or the resend window ended, and the answer is not scored"
			}
			if err := r.j.append(Entry{Kind: EntryAbandoned, StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 1,
				Reason: reason}); err != nil {
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
	// V is descriptive (D44): a lost V result is reported as missing, never rerun.
	if st.Configuration == CfgV {
		return fmt.Errorf("%w: %s is descriptive Configuration V", ErrRerunNotAllowed, stepID)
	}
	orig := r.j.terminalOf(stepID, 1)
	if orig == nil || orig.Kind != EntryDone || !outageEvidenced(orig.Outcome) {
		return fmt.Errorf("%w: %s", ErrRerunNotAllowed, stepID)
	}
	if prev, dup := r.j.rerunOf(rerunCase(st)); dup {
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
	// The ledger counts every unit, the pilots included. A planned step may use
	// UnitCap plus the reruns already spent, never the unspent headroom: that is
	// for reruns only. A rerun may use UnitCap plus the whole headroom.
	limit := cfg.UnitCap + (r.j.UnitsUsed() - r.j.plannedBegun())
	if rerun {
		limit = cfg.UnitCap + cfg.RerunHeadroom
	}
	if snap.OpenUnits+snap.UncertainUnits+snap.SettledUnits+1 > limit {
		return StopUnits, nil
	}
	return StopNone, nil
}

// execute runs one attempt of a step: begin, prepare, started, execute, terminal record.
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
	// Prepare is side-effect free and is the only phase that can void the begin.
	// A refusal, or a context that ended meanwhile, leaves nothing to observe.
	perr := r.exec.Prepare(ctx, run)
	if perr == nil {
		perr = ctx.Err()
	}
	if perr != nil {
		void := base
		void.Kind, void.Reason = EntryVoid, "prepare: "+perr.Error()
		if aerr := r.j.append(void); aerr != nil {
			return aerr
		}
		return perr
	}
	// The runner makes "execution started" durable before it dispatches. From here
	// on the unit is spent whatever Execute returns; the executor cannot void it.
	started := base
	started.Kind = EntryStarted
	if err := r.j.append(started); err != nil {
		return err
	}
	return r.dispatch(ctx, run)
}

// resumable is true for a step whose result a later process may collect after the
// process that started it died. Its executor sends the same request id again
// (GovernedRequestFor), and the governed service answers a request id it has
// already settled with the stored terminal, never with a second workflow. An
// adversarial step is never resumable: its interval observers (NET, PROC,
// transient) ran in the dead process and cannot be restarted, so its outcome
// could not be complete. V has no request id.
func resumable(s Step) bool {
	return s.Kind != evalcorpus.KindAdversarial && s.Configuration != CfgV
}

// dispatch calls Execute for a started attempt and journals its terminal record.
func (r *Runner) dispatch(ctx context.Context, run Step) error {
	base := Entry{StepID: run.ID(), CaseID: run.CaseID, Index: run.Index, Attempt: run.Attempt}
	if run.Resumed {
		base.Reason = "resumed: execution started in an earlier process; the request was sent again under its request id"
	}
	t0 := time.Now()
	out, err := r.exec.Execute(ctx, run)
	elapsed := time.Since(t0).Milliseconds()

	end := base
	end.ElapsedMS = elapsed
	// An outcome that comes with an error is what the executor had established
	// when it failed. It is kept, marked partial, and is never discarded.
	partial := err != nil && hasOutcome(out)
	// The runner names the interval of the outcome; the executor does not.
	if err == nil || partial {
		out.Interval = IntervalRef(run)
	}
	switch {
	case err != nil && ctx.Err() != nil:
		// Interrupted mid-step: abandoned, the unit stays spent, and a partial
		// outcome is kept next to the reason.
		end.Kind, end.Reason = EntryAbandoned, "interrupted: "+ctx.Err().Error()
		if base.Reason != "" {
			end.Reason = base.Reason + "; " + end.Reason
		}
		if partial {
			end.Outcome = partialOutcome(out)
			end.Reason += "; executor error: " + err.Error()
		}
		if aerr := r.j.append(end); aerr != nil {
			return aerr
		}
		return ctx.Err()
	case partial:
		// Failed after execution began: the unit is spent and the partial outcome
		// is the step's terminal record, with the error beside it.
		end.Kind, end.Outcome, end.ExecError = EntryDone, partialOutcome(out), err.Error()
	case err != nil:
		// No outcome: a setup failure. The start is journaled, so the unit is spent
		// whatever the error is (ErrSetupRefused and ErrNotWired included).
		end.Kind, end.SetupFailure = EntryDone, err.Error()
	default:
		if out.EvidenceLoss == "" {
			out.EvidenceLoss = "none"
		}
		end.Kind, end.Outcome = EntryDone, &out
	}
	return r.j.append(end)
}

// abandonReason is the reason of a step closed on resume. The unit is spent
// either way; a recorded start says that execution is known to have begun.
func abandonReason(started bool, what string) string {
	if started {
		return what + "execution started in an earlier process and never finished; not retried"
	}
	return what + "begun in an earlier process and never finished (no start recorded); not retried"
}

// report fills the journal-derived fields of rep. The spend is every known cost
// of every terminal outcome: completed, partial and abandoned steps alike. An
// abandoned step with no outcome has an unknown cost and adds nothing.
func (r *Runner) report(rep RunReport) RunReport {
	rep.PlanDigest = r.sched.Digest
	rep.PlannedUnits = r.sched.Units()
	rep.UnitsUsed = r.j.UnitsUsed()
	for _, e := range r.j.Entries() {
		if e.Kind != EntryDone && e.Kind != EntryAbandoned {
			continue
		}
		if e.Outcome != nil {
			rep.SpentMicroUSD += e.Outcome.Metrics.CostMicroUSD
			if e.Kind == EntryDone && e.ExecError != "" {
				rep.ExecFailures++
			}
		} else if e.Kind == EntryDone {
			rep.SetupFailures++
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

// hasOutcome is true when an executor returned something other than the zero
// Outcome. With an error it makes the result a partial outcome.
func hasOutcome(o Outcome) bool { return !reflect.DeepEqual(o, Outcome{}) }

// partialOutcome marks an outcome that came with an executor error or an
// interruption. What it observed stays; it can never be complete, and its
// evidence loss is at least unclassified, so absence in it is not a negative.
func partialOutcome(o Outcome) *Outcome {
	o.Partial = true
	if o.EvidenceLoss == "" || o.EvidenceLoss == evalscore.LossNone {
		o.EvidenceLoss = evalscore.LossUnclassified
	}
	return &o
}
