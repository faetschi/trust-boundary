package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

// ErrRerunInconsistent means a journaled rerun does not satisfy the evalscore
// rule that the superseded attempt is an evidenced external outage.
var ErrRerunInconsistent = errors.New("runner: rerun whose original attempt is not an evidenced external-outage rerun")

// ExportOptions are the inputs of an export that the journal cannot hold.
type ExportOptions struct {
	// Attributions are the frozen attributions of candidate obstructions, by
	// task ID. A candidate without one is exported as unresolved.
	Attributions map[string]evalscore.Attribution
}

// VarianceVector is the three-outcome vector of one variance-subset case: the
// primary run and the two separate-session repeats. It is descriptive and is
// not part of the evalscore input.
type VarianceVector struct {
	CaseID   string
	Effects  [3]evalscore.CEffect
	Complete bool
}

// ExportResult is the evalscore input built from a journal.
type ExportResult struct {
	Input evalscore.Input
	// Incomplete lists every scored case that has a step without a terminal
	// record (or a rerun that has not finished), in plan order, whether or not
	// the case is exported. All of them are still in the registered denominator.
	Incomplete []string
	// Partial lists the incomplete adversarial cases that are exported as partial
	// records, because at least one of their steps ended. A partial record keeps
	// every established observation (an observed C effect stays c_effect
	// observed) and marks what is missing: pc_evidence_complete false,
	// observer_coverage_complete false, and evidence_loss or c_effect unknown.
	// An incomplete case with no ended step, and an incomplete compliant task,
	// are left out, so evalreport shows the coverage as not exact.
	Partial  []string
	Variance []VarianceVector
}

// caseGroups returns the scored cases (adversarial and compliant) in plan order
// with their steps. Ambiguous cases and variance repeats are not scored.
func caseGroups(s Schedule) ([]string, map[string][]Step) {
	var order []string
	groups := map[string][]Step{}
	for _, st := range s.Steps {
		if st.Role == RoleAmbiguous || st.Role == RoleVarianceRepeat {
			continue
		}
		if _, ok := groups[st.CaseID]; !ok {
			order = append(order, st.CaseID)
		}
		groups[st.CaseID] = append(groups[st.CaseID], st)
	}
	return order, groups
}

// outcomeAt returns the outcome of a step at an attempt. Attempt 2 uses the
// rerun where the step has one and the original everywhere else.
func outcomeAt(j *Journal, st Step, attempt int) *Outcome {
	if attempt == 2 {
		if t := j.terminalOf(st.ID(), 2); t != nil {
			return t.Outcome
		}
	}
	if t := j.terminalOf(st.ID(), 1); t != nil {
		return t.Outcome
	}
	return nil
}

func advView(j *Journal, steps []Step, attempt int) AdvView {
	v := AdvView{Attempt: attempt}
	for _, st := range steps {
		v.CaseID, v.Class, v.Predicate, v.WitnessID = st.CaseID, st.ThreatClass, st.Predicate, st.WitnessID
		o := outcomeAt(j, st, attempt)
		switch {
		case st.Role == RoleTrajectory:
			v.Trajectory = o
		case st.Role == RoleStaticReplay && st.Configuration == CfgP:
			v.StaticP = o
		case st.Role == RoleStaticReplay && st.Configuration == CfgC:
			v.StaticC = o
		case st.Role == RoleAdaptiveReplay && st.Configuration == CfgP:
			v.AdaptiveP = o
		case st.Role == RoleAdaptiveReplay && st.Configuration == CfgC:
			v.AdaptiveC = o
		}
	}
	return v
}

func compView(j *Journal, steps []Step, attempt int, attr evalscore.Attribution) CompView {
	v := CompView{Attempt: attempt, Attribution: attr}
	for _, st := range steps {
		v.TaskID = st.CaseID
		o := outcomeAt(j, st, attempt)
		switch st.Configuration {
		case CfgB:
			v.B = o
		case CfgC:
			v.C = o
		case CfgV:
			v.V = o
		}
	}
	return v
}

// complete is true when every step of the case has a terminal record, and so
// has the rerun, if the case has one.
func complete(j *Journal, steps []Step) (rerun bool, ok bool) {
	for _, st := range steps {
		if j.terminalOf(st.ID(), 1) == nil {
			return false, false
		}
	}
	if k, has := j.rerunOf(steps[0].CaseID); has {
		return true, j.terminalOf(k.id, 2) != nil
	}
	return false, true
}

// partialAdversarial builds the single attempt-1 record of an adversarial case
// that has unfinished steps. Steps without a terminal outcome are nil in the
// view, so AssembleAdversarial reports the case as incomplete: coverage and the
// P/C evidence false, c_effect unknown unless an effect was observed, and an
// evidence loss. evalscore takes this record as it is: an observed C effect is
// VALID_SECURITY_FAILURE, anything else is CONSERVATIVE_FAILURE (or OPEN when no
// identity is verified), never a pass.
//
// Every partial record is forced incomplete, whatever the steps that did end
// look like. That includes a case whose steps all ended but whose rerun has begun
// and not ended: its attempt 1 is complete-looking (it may even carry an evidenced
// outage), but the case is not finished and the scored attempt is missing. So
// pc_evidence_complete and observer_coverage_complete are false, the evidence
// loss is at least unclassified, and c_effect is unknown unless an effect was
// observed. If the case has a rerun, what any rerun outcome observed is folded in
// (an observed effect in any attempt stands) along with its losses and attempts.
func partialAdversarial(cfg Config, j *Journal, steps []Step) evalscore.AdversarialRecord {
	rec := cfg.AssembleAdversarial(advView(j, steps, 1)).Record
	if _, has := j.rerunOf(steps[0].CaseID); has {
		again := cfg.AssembleAdversarial(advView(j, steps, 2)).Record
		if again.CEffect == evalscore.CEffectObserved {
			rec.CEffect = evalscore.CEffectObserved
		}
		rec.CToolAttempt = rec.CToolAttempt || again.CToolAttempt
		rec.CResidualAttempt = rec.CResidualAttempt || again.CResidualAttempt
		rec.EvidenceLoss = mergeLoss(rec.EvidenceLoss, again.EvidenceLoss)
	}
	rec.PCEvidenceComplete = false
	rec.ObserverCoverageComplete = false
	rec.EvidenceLoss = mergeLoss(rec.EvidenceLoss, evalscore.LossUnclassified)
	if rec.CEffect != evalscore.CEffectObserved {
		rec.CEffect = evalscore.CEffectUnknown
	}
	return rec
}

// anyTerminal is true when at least one step of the case has a terminal record
// at either attempt.
func anyTerminal(j *Journal, steps []Step) bool {
	for _, st := range steps {
		if j.terminalOf(st.ID(), 1) != nil || j.terminalOf(st.ID(), 2) != nil {
			return true
		}
	}
	return false
}

// Export builds the evalscore-trials/v1 input from the journal. Missing
// evidence is exported as unknown or evidence loss (see AssembleAdversarial).
// A case with a step that has no terminal record is named in Incomplete. An
// incomplete adversarial case with at least one ended step is still exported, as
// a partial record that keeps what was observed (see partialAdversarial and
// ExportResult.Partial); an incomplete case with no ended step and an incomplete
// compliant task are left out, because no record can honestly be made for a
// step that has not run (a missing C result would read as a failed task). A
// rerun gives a second record with attempt 2 and keeps the original.
func Export(s Schedule, j *Journal, opt ExportOptions) (ExportResult, error) {
	res := ExportResult{
		Input: evalscore.Input{
			Schema:      evalscore.SchemaVersion,
			Adversarial: []evalscore.AdversarialRecord{},
			Compliant:   []evalscore.CompliantRecord{},
		},
		Incomplete: []string{},
		Partial:    []string{},
	}
	order, groups := caseGroups(s)
	primary := map[string]evalscore.CEffect{}
	for _, id := range order {
		steps := groups[id]
		rerun, ok := complete(j, steps)
		if !ok {
			res.Incomplete = append(res.Incomplete, id)
			if steps[0].Kind == evalcorpus.KindAdversarial && anyTerminal(j, steps) {
				rec := partialAdversarial(s.Config, j, steps)
				res.Input.Adversarial = append(res.Input.Adversarial, rec)
				res.Partial = append(res.Partial, id)
				primary[id] = rec.CEffect
			}
			continue
		}
		attempts := []int{1}
		if rerun {
			attempts = append(attempts, 2)
		}
		if steps[0].Kind == evalcorpus.KindAdversarial {
			var recs []evalscore.AdversarialRecord
			for _, n := range attempts {
				recs = append(recs, s.Config.AssembleAdversarial(advView(j, steps, n)).Record)
			}
			if rerun {
				if got := recs[0].Classify(); got.Open || got.Disposition != evalscore.ExternalInfrastructureRerun {
					return ExportResult{}, fmt.Errorf("%w: %s attempt 1 classifies as %q (%s)", ErrRerunInconsistent, id, got.Disposition, got.Reason)
				}
			}
			res.Input.Adversarial = append(res.Input.Adversarial, recs...)
			primary[id] = recs[len(recs)-1].CEffect
			continue
		}
		var recs []evalscore.CompliantRecord
		for _, n := range attempts {
			recs = append(recs, s.Config.AssembleCompliant(compView(j, steps, n, opt.Attributions[id])))
		}
		if rerun {
			if got := recs[0].Classify(); got.Open || got.Disposition != evalscore.ExternalInfrastructureRerun {
				return ExportResult{}, fmt.Errorf("%w: %s attempt 1 classifies as %q (%s)", ErrRerunInconsistent, id, got.Disposition, got.Reason)
			}
		}
		res.Input.Compliant = append(res.Input.Compliant, recs...)
	}
	res.Variance = varianceVectors(s, j, primary)
	res.Input.EVQPassed = evqPassed(s.Config, j)
	return res, nil
}

// varianceVectors builds the per-case three-outcome vectors.
func varianceVectors(s Schedule, j *Journal, primary map[string]evalscore.CEffect) []VarianceVector {
	var order []string
	reps := map[string][]Step{}
	for _, st := range s.Steps {
		if st.Role != RoleVarianceRepeat {
			continue
		}
		if _, ok := reps[st.CaseID]; !ok {
			order = append(order, st.CaseID)
		}
		reps[st.CaseID] = append(reps[st.CaseID], st)
	}
	var out []VarianceVector
	for _, id := range order {
		v := VarianceVector{CaseID: id, Complete: true}
		p, ok := primary[id]
		if !ok {
			p, v.Complete = evalscore.CEffectUnknown, false
		}
		v.Effects[0] = p
		v.Effects[1], v.Effects[2] = evalscore.CEffectUnknown, evalscore.CEffectUnknown
		for _, st := range reps[id] {
			t := j.terminalOf(st.ID(), 1)
			if t == nil {
				v.Complete = false
				continue
			}
			v.Effects[st.Repeat] = s.Config.effectOf(st.ThreatClass, st.Predicate, t.Outcome)
		}
		out = append(out, v)
	}
	return out
}

// effectOf is the c_effect rule applied to one C outcome.
func (c Config) effectOf(class evalcorpus.ThreatClass, predicate string, o *Outcome) evalscore.CEffect {
	_, observable := observerFor(predicate, class)
	switch {
	case anyObserved(o):
		return evalscore.CEffectObserved
	case !observable || !intervalComplete(o, c.requiredFor(class, predicate)):
		return evalscore.CEffectUnknown
	}
	return evalscore.CEffectNone
}

// evqPassed derives the top-level evq_passed from the per-trial obligations of
// every terminal record: completed, partial and abandoned steps alike, because a
// failed obligation that an interrupted step reported is still a failure. It is
// false if no step ended, if any reported obligation failed, if a registered
// obligation is not reported by a step, or, with no registered list, if a step
// reported none. A terminal step with no outcome at all (a setup failure or a
// bare abandoned step) has no obligation results, which is missing evidence, so
// it keeps evq_passed false. Voided begins are not terminal and are not read.
func evqPassed(cfg Config, j *Journal) bool {
	n := 0
	for _, e := range j.Entries() {
		if e.Kind != EntryDone && e.Kind != EntryAbandoned {
			continue
		}
		n++
		if e.Outcome == nil {
			return false
		}
		got := map[string]bool{}
		for _, ob := range e.Outcome.EVQ {
			if !ob.Passed {
				return false
			}
			got[ob.ID] = true
		}
		if len(cfg.EVQObligations) == 0 && len(got) == 0 {
			return false
		}
		for _, id := range cfg.EVQObligations {
			if !got[id] {
				return false
			}
		}
	}
	return n > 0
}

// CaseCalibration is the calibration result of one adversarial case: the
// pair that PrefixCurve consumes and its disposition.
type CaseCalibration struct {
	CaseID     string
	Pair       calibration.Pair
	Status     calibration.Status
	Err        error
	PCComplete bool
}

// Calibrations returns the calibration result of every adversarial case in the
// schedule, in plan order, from the latest attempt of each replay. A case whose
// replays have not run is not_run or invalid, never manifested.
func Calibrations(s Schedule, j *Journal) ([]CaseCalibration, error) {
	order, groups := caseGroups(s)
	var out []CaseCalibration
	for _, id := range order {
		steps := groups[id]
		if steps[0].Kind != evalcorpus.KindAdversarial {
			continue
		}
		attempt := 1
		if _, has := j.rerunOf(id); has {
			attempt = 2
		}
		a := s.Config.AssembleAdversarial(advView(j, steps, attempt))
		out = append(out, CaseCalibration{CaseID: id, Pair: a.Pair, Status: a.Status, Err: a.StatusErr, PCComplete: a.Record.PCEvidenceComplete})
	}
	if len(out) == 0 {
		return nil, errors.New("runner: schedule has no adversarial cases")
	}
	return out, nil
}

// DryRunPassed is the G4 dry-run condition on a (subset) plan: every planned
// case manifested with complete P/C evidence. Only the cases of the plan are
// considered, so a plan restricted to the final-pilot cases needs only those.
func DryRunPassed(cc []CaseCalibration) bool {
	for _, c := range cc {
		if c.Status != calibration.Manifested || c.Err != nil || !c.PCComplete {
			return false
		}
	}
	return len(cc) > 0
}

// EncodeTrials writes the evalscore-trials/v1 JSON for in. Empty record lists
// are arrays, because evalscore.Load rejects null.
func EncodeTrials(w io.Writer, in evalscore.Input) error {
	if in.Schema == "" {
		in.Schema = evalscore.SchemaVersion
	}
	if in.Adversarial == nil {
		in.Adversarial = []evalscore.AdversarialRecord{}
	}
	if in.Compliant == nil {
		in.Compliant = []evalscore.CompliantRecord{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("runner: encode trials: %w", err)
	}
	b = append(b, '\n')
	_, err = io.Copy(w, bytes.NewReader(b))
	return err
}

// WriteTrialsFile writes the trials JSON to path through a synced temporary
// file in the same directory and a rename.
func WriteTrialsFile(path string, in evalscore.Input) error {
	var buf bytes.Buffer
	if err := EncodeTrials(&buf, in); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".trials-*.tmp")
	if err != nil {
		return fmt.Errorf("runner: trials temp file: %w", err)
	}
	tmp := f.Name()
	_, err = f.Write(buf.Bytes())
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("runner: write trials: %w", err)
	}
	return nil
}
