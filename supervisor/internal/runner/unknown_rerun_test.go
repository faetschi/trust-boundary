package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalscore"
)

// Register D30 as amended 2026-11-18 (owner decision A2), host side: GovernedC types an UNKNOWN answer as a rerun
// cause, the runner reruns it once only when the case's attempt 1 shows no effect and no gap, and the export keeps
// both records. Synthetic fixtures; these check code paths only.

var outageRecordHash = strings.Repeat("e3", 32)

func respondWithOutage(f *governedFixture, digest string) {
	line, err := json.Marshal(map[string]any{"schema_version": "tbound-pi-service-response/v1", "request_id": f.req.RequestID,
		"status": "unknown", "error_code": "provider-outage", "outage_evidence_digest": digest})
	if err != nil {
		panic(err)
	}
	f.tr.stdout, f.tr.code = append(line, '\n'), 2
}

func TestGovernedCTypesAnUnknownAnswerAsARerunCause(t *testing.T) {
	f := newGovernedFixture(t)
	if f.step.Role != RoleTrajectory {
		t.Fatalf("fixture step %s is not the C trajectory", f.step.ID())
	}
	respondWithOutage(f, outageRecordHash)
	o, err := f.g.Execute(context.Background(), f.step)
	if !errors.Is(err, ErrGovernedUnknown) {
		t.Fatalf("err = %v, want ErrGovernedUnknown", err)
	}
	if o.EvidenceLoss != evalscore.LossExternalOutage || o.OutageEvidenceDigest != "provider-journal:"+outageRecordHash ||
		o.UnknownEnd != "provider-outage" {
		t.Fatalf("outage answer = loss %q digest %q end %q", o.EvidenceLoss, o.OutageEvidenceDigest, o.UnknownEnd)
	}

	respondWithOutage(f, "not-a-digest")
	if o, _ = f.g.Execute(context.Background(), f.step); o.EvidenceLoss != evalscore.LossOutcomeUnknown || o.OutageEvidenceDigest != o.E01.Digest {
		t.Fatalf("an outage code without a valid record hash must stay a plain UNKNOWN: %+v", o)
	}

	f.respond("unknown", "task-outcome-unknown", nil, 2)
	if o, _ = f.g.Execute(context.Background(), f.step); o.EvidenceLoss != evalscore.LossOutcomeUnknown || o.UnknownEnd != "task-outcome-unknown" ||
		o.OutageEvidenceDigest == "" || o.OutageEvidenceDigest != o.E01.Digest {
		t.Fatalf("plain UNKNOWN = %+v", o)
	}

	f.respond("unknown", "task-outcome-unknown", nil, 0)
	if o, _ = f.g.Execute(context.Background(), f.step); o.UnknownEnd != "" || o.EvidenceLoss != "" {
		t.Fatalf("an UNKNOWN beside a second failure (exit code) must not be typed: %+v", o)
	}
}

func TestGovernedCLeavesUnknownReplaysUntyped(t *testing.T) {
	f := newGovernedFixture(t)
	s := smallSchedule(t, smallConfig())
	f.step = findStep(t, s, f.step.CaseID, RoleStaticReplay, CfgC)
	var err error
	if f.req, err = f.g.Request(f.step); err != nil {
		t.Fatal(err)
	}
	respondWithOutage(f, outageRecordHash)
	if o, _ := f.g.Execute(context.Background(), f.step); o.UnknownEnd != "" || o.EvidenceLoss != "" {
		t.Fatalf("a replay UNKNOWN is outside the rerun scope: %+v", o)
	}
}

// unknownExecutor ends the target step's attempt 1 the way GovernedC ends a clean UNKNOWN answer: no receipt, no
// verified identity, no task result, the oracles the collectors gave, a typed rerun cause, and ErrGovernedUnknown.
type unknownExecutor struct {
	*FakeExecutor
	target Step
	loss   evalscore.EvidenceLoss
	edit   func(*Outcome)
	cancel context.CancelFunc
}

func (u unknownExecutor) Execute(ctx context.Context, s Step) (Outcome, error) {
	o, err := u.FakeExecutor.Execute(ctx, s)
	if err != nil || s.ID() != u.target.ID() || s.Attempt != 1 {
		return o, err
	}
	o.Receipt, o.E01.Verified, o.Task = nil, false, ""
	o.UnknownEnd, o.EvidenceLoss, o.OutageEvidenceDigest = "task-outcome-unknown", u.loss, "sha256:"+strings.Repeat("9f", 32)
	if u.edit != nil {
		u.edit(&o)
	}
	if u.cancel != nil {
		u.cancel()
	}
	return o, ErrGovernedUnknown
}

type unknownCase struct {
	sched Schedule
	j     *Journal
	exec  unknownExecutor
}

func runWithUnknown(t *testing.T, kind string, role Role, cfg Configuration, loss evalscore.EvidenceLoss, edit func(*Outcome)) unknownCase {
	t.Helper()
	c := smallConfig()
	c.RerunHeadroom = 2
	s := smallSchedule(t, c)
	target := findStep(t, s, firstCase(s, kind), role, cfg)
	exec := unknownExecutor{FakeExecutor: NewFakeExecutor(s.Config), target: target, loss: loss, edit: edit}
	j, _ := runAll(t, s, t.TempDir(), exec)
	t.Cleanup(func() { j.Close() })
	if e := j.terminalOf(target.ID(), 1); e == nil || e.Outcome == nil || !e.Outcome.Partial || e.Outcome.UnknownEnd == "" {
		t.Fatalf("the target did not end as a clean UNKNOWN: %+v", e)
	}
	return unknownCase{sched: s, j: j, exec: exec}
}

func exportedSummary(t *testing.T, u unknownCase, attr map[string]evalscore.Attribution) (ExportResult, evalscore.Summary) {
	t.Helper()
	res, err := Export(u.sched, u.j, ExportOptions{Attributions: attr})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	s, err := evalscore.Summarize(res.Input)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	return res, s
}

func caseOutcome(t *testing.T, s evalscore.Summary, id string) evalscore.CaseOutcome {
	t.Helper()
	for _, c := range s.Cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no case %s", id)
	return evalscore.CaseOutcome{}
}

func TestOutageTypedUnknownTrajectoryTakesTheRerun(t *testing.T) {
	for _, loss := range []evalscore.EvidenceLoss{evalscore.LossExternalOutage, evalscore.LossOutcomeUnknown} {
		u := runWithUnknown(t, "adversarial", RoleTrajectory, CfgC, loss, nil)
		id := u.exec.target.CaseID
		res, s := exportedSummary(t, u, nil)
		if got := caseOutcome(t, s, id); got.Disposition != string(evalscore.ExternalInfrastructureRerun) || got.Attempts != 1 {
			t.Fatalf("%s: before the rerun the case is %+v, want a pending rerun", loss, got)
		}
		if res.Input.Adversarial[0].IdentityVerified != true {
			t.Fatalf("%s: the UNKNOWN trajectory has no receipt, but its identity gap must not make the case OPEN", loss)
		}
		used := u.j.UnitsUsed()
		if err := mustRunner(t, u.sched, u.j, u.exec, Options{}).Rerun(context.Background(), u.exec.target.ID()); err != nil {
			t.Fatalf("%s: rerun refused: %v", loss, err)
		}
		if u.j.UnitsUsed() != used+1 {
			t.Fatalf("%s: the rerun is one unit", loss)
		}
		_, s = exportedSummary(t, u, nil)
		got := caseOutcome(t, s, id)
		if got.Disposition != string(evalscore.ValidPass) || got.Attempts != 2 || got.Original == nil ||
			got.Original.Reason != string(loss) || s.Observed.SupersededAttempts != 1 {
			t.Fatalf("%s: after the rerun the case is %+v (original %+v)", loss, got, got.Original)
		}
	}
}

func TestUnknownTrajectoryWithAnEffectOrAGapTakesNoRerun(t *testing.T) {
	for name, edit := range map[string]func(*Outcome){
		"observed effect": func(o *Outcome) {
			for k := range o.Oracles {
				o.Oracles[k] = FindingObserved
				break
			}
		},
		"missing oracle": func(o *Outcome) { delete(o.Oracles, OracleWS01) },
	} {
		u := runWithUnknown(t, "adversarial", RoleTrajectory, CfgC, evalscore.LossOutcomeUnknown, edit)
		used := u.j.UnitsUsed()
		err := mustRunner(t, u.sched, u.j, u.exec, Options{}).Rerun(context.Background(), u.exec.target.ID())
		if !errors.Is(err, ErrRerunNotAllowed) || u.j.UnitsUsed() != used {
			t.Fatalf("%s: rerun = %v, units %d -> %d; want ErrRerunNotAllowed and no unit spent", name, err, used, u.j.UnitsUsed())
		}
		_, s := exportedSummary(t, u, nil)
		if got := caseOutcome(t, s, u.exec.target.CaseID); got.Disposition == string(evalscore.ExternalInfrastructureRerun) || got.Disposition == string(evalscore.ValidPass) {
			t.Fatalf("%s: case = %+v", name, got)
		}
	}
}

func TestUnknownCompliantRunNeedsAnUnsupportedAttributionForItsRerun(t *testing.T) {
	u := runWithUnknown(t, "compliant", RoleCompliant, CfgC, evalscore.LossExternalOutage, nil)
	id := u.exec.target.CaseID
	if err := mustRunner(t, u.sched, u.j, u.exec, Options{}).Rerun(context.Background(), u.exec.target.ID()); !errors.Is(err, ErrRerunNotAllowed) {
		t.Fatalf("an unattributed candidate took a rerun: %v", err)
	}
	for _, attr := range []evalscore.Attribution{evalscore.AttributionSupported, evalscore.AttributionUnresolved} {
		err := mustRunner(t, u.sched, u.j, u.exec, Options{Attributions: map[string]evalscore.Attribution{id: attr}}).Rerun(context.Background(), u.exec.target.ID())
		if !errors.Is(err, ErrRerunNotAllowed) {
			t.Fatalf("a %s candidate took a rerun: %v", attr, err)
		}
	}
	attrs := map[string]evalscore.Attribution{id: evalscore.AttributionUnsupported}
	if err := mustRunner(t, u.sched, u.j, u.exec, Options{Attributions: attrs}).Rerun(context.Background(), u.exec.target.ID()); err != nil {
		t.Fatalf("rerun refused: %v", err)
	}
	_, s := exportedSummary(t, u, attrs)
	if got := caseOutcome(t, s, id); got.Disposition != string(evalscore.ValidPass) || got.Original == nil || got.Original.Reason != evalscore.ReasonExternalOutage {
		t.Fatalf("compliant case after its rerun = %+v", got)
	}
}

func TestSecondUnknownOfAUnitIsConservativeFailure(t *testing.T) {
	u := runWithUnknown(t, "adversarial", RoleTrajectory, CfgC, evalscore.LossOutcomeUnknown, nil)
	again := u.exec
	again.target.Attempt = 0 // the rerun ends UNKNOWN as well
	rerunUnknown := secondUnknown{unknownExecutor: again}
	if err := mustRunner(t, u.sched, u.j, rerunUnknown, Options{}).Rerun(context.Background(), u.exec.target.ID()); err != nil {
		t.Fatal(err)
	}
	_, s := exportedSummary(t, u, nil)
	if got := caseOutcome(t, s, u.exec.target.CaseID); got.Disposition != string(evalscore.ConservativeFailure) || got.Reason != evalscore.ReasonRerunSpent {
		t.Fatalf("second UNKNOWN = %+v, want CONSERVATIVE_FAILURE/rerun_spent", got)
	}
	if err := mustRunner(t, u.sched, u.j, rerunUnknown, Options{}).Rerun(context.Background(), u.exec.target.ID()); !errors.Is(err, ErrRerunLimit) {
		t.Fatalf("a third attempt: %v, want ErrRerunLimit", err)
	}
}

// secondUnknown ends the target's rerun (attempt 2) as a clean UNKNOWN too.
type secondUnknown struct{ unknownExecutor }

func (u secondUnknown) Execute(ctx context.Context, s Step) (Outcome, error) {
	if s.ID() == u.unknownExecutor.target.ID() && s.Attempt == 2 {
		s.Attempt = 1
		o, err := u.unknownExecutor.Execute(ctx, s)
		return o, err
	}
	return u.unknownExecutor.Execute(ctx, s)
}

func TestInterruptedUnknownIsNoRerunCause(t *testing.T) {
	c := smallConfig()
	c.RerunHeadroom = 2
	s := smallSchedule(t, c)
	target := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := unknownExecutor{FakeExecutor: NewFakeExecutor(s.Config), target: target, loss: evalscore.LossOutcomeUnknown, cancel: cancel}
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	if _, err := mustRunner(t, s, j, exec, Options{}).Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run = %v, want the interruption", err)
	}
	e := j.terminalOf(target.ID(), 1)
	if e == nil || e.Kind != EntryAbandoned || e.Outcome == nil || e.Outcome.UnknownEnd != "" || e.Outcome.EvidenceLoss != evalscore.LossUnclassified {
		t.Fatalf("interrupted UNKNOWN = %+v", e)
	}
}
