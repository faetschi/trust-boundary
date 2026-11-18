package runner

import (
	"context"
	"fmt"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

// typedEndOutcome runs one compliant C step through GovernedC and the acceptance oracle the way the runner
// does: an executor error makes the outcome partial (Runner.execute, partialOutcome).
func typedEndOutcome(t *testing.T, status string, receipt map[string]any, exit int, accepted bool) Outcome {
	t.Helper()
	cfg := smallConfig()
	s := smallSchedule(t, cfg)
	var step Step
	for _, st := range s.Steps {
		if st.Configuration == CfgC && st.Kind == evalcorpus.KindCompliant {
			step = st
			break
		}
	}
	if step.CaseID == "" {
		t.Fatal("no C compliant step in the plan")
	}
	service := &fakeTransport{}
	g := GovernedC{PlanDigest: s.Digest, Transport: service}
	f := &governedFixture{step: step, tr: service, g: g}
	var err error
	if f.req, err = g.Request(step); err != nil {
		t.Fatal(err)
	}
	if receipt != nil {
		for key, value := range f.receipt(true) {
			if _, set := receipt[key]; !set {
				receipt[key] = value
			}
		}
	}
	code := ""
	if status == "unknown" {
		code = "task-outcome-unknown"
	}
	f.respond(status, code, receipt, exit)
	guest := &scriptedTransport{reply: func([]string) ([]byte, []byte, int, error) {
		return acceptJSON(t, step.CaseID, accepted), nil, 0, nil
	}}
	out, err := AcceptExecutor{Inner: g, Transport: guest}.Execute(context.Background(), step)
	if err != nil {
		return *partialOutcome(out)
	}
	return out
}

// The typed budget end: a compliant run whose last provider call went out with tools disabled completes with a
// receipt like any other run. The executor carries the receipt's tools-disabled and refused-call counts, the
// acceptance oracle judges the run, the task assembles to a decided record without evidence loss, and with
// every other task complete the friction clause stays evaluable. The cap-exhausted UNKNOWN it replaces is a
// typed UNKNOWN that takes the registered rerun (D30 as amended 2026-11-18); until the rerun ends the clause
// is not evaluable.
func TestTypedBudgetEndIsJudgedAndKeepsFrictionEvaluable(t *testing.T) {
	cfg := testConfig()
	typed := typedEndOutcome(t, "completed", map[string]any{"tools_disabled_calls": 1, "refused_tool_calls": 1}, 0, true)
	if typed.Partial || typed.Task != TaskPass || typed.Metrics.ToolsDisabledCalls != 1 || typed.Metrics.RefusedToolCalls != 1 {
		t.Fatalf("typed end outcome = %+v", typed)
	}
	unknown := typedEndOutcome(t, "unknown", nil, 2, true)
	if !unknown.Partial || unknown.EvidenceLoss != evalscore.LossOutcomeUnknown || unknown.UnknownEnd != "task-outcome-unknown" ||
		unknown.OutageEvidenceDigest != unknown.E01.Digest {
		t.Fatalf("cap-exhausted UNKNOWN outcome = %+v", unknown)
	}

	// Only what the executor determines replaces the attested defaults: the task, partial, loss and metrics.
	withC := func(id string, c *Outcome) evalscore.CompliantRecord {
		v := goodComp(cfg)
		v.TaskID = id
		if c != nil {
			v.C.Task, v.C.Partial, v.C.EvidenceLoss, v.C.Metrics = c.Task, c.Partial, c.EvidenceLoss, c.Metrics
			v.C.UnknownEnd, v.C.OutageEvidenceDigest, v.C.Receipt = c.UnknownEnd, c.OutageEvidenceDigest, c.Receipt
		}
		return cfg.AssembleCompliant(v)
	}
	summarize := func(first evalscore.CompliantRecord) evalscore.Summary {
		in := evalscore.Input{Schema: evalscore.SchemaVersion, EVQPassed: true, Registered: &evalscore.RegisteredIDs{}}
		for i := 0; i < evalscore.RegisteredCompliantTasks; i++ {
			r := withC(fmt.Sprintf("task-%03d", i), nil)
			if i == 0 {
				r = first
			}
			in.Compliant = append(in.Compliant, r)
			in.Registered.CompliantIDs = append(in.Registered.CompliantIDs, r.TaskID)
		}
		s, err := evalscore.Summarize(in)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	record := withC("task-000", &typed)
	if got := record.Classify(); record.EvidenceLoss != evalscore.LossNone || got.Open || got.Disposition != evalscore.ValidPass {
		t.Fatalf("typed end record %+v classifies as %+v, want VALID_PASS", record, got)
	}
	if s := summarize(record); !s.Clauses.Friction.Evaluable {
		t.Fatalf("friction not evaluable with the typed end: %+v", s.Clauses.Friction)
	}

	failed := typedEndOutcome(t, "completed", map[string]any{"tools_disabled_calls": 1}, 0, false)
	record = withC("task-000", &failed)
	if got := record.Classify(); failed.Partial || failed.Task != TaskFail || record.EvidenceLoss != evalscore.LossNone ||
		!record.ObserverCoverageComplete || got.Disposition == evalscore.ConservativeFailure {
		t.Fatalf("a judged typed-end failure %+v classifies as %+v, want a decided C failure without evidence loss", record, got)
	}

	// B succeeded, so the C UNKNOWN is a candidate obstruction: without its frozen attribution it stays
	// UNSUPPORTED, and only an unsupported attribution (no denial caused it) lets it take the rerun.
	record = withC("task-000", &unknown)
	if got := record.Classify(); got.Disposition != evalscore.Unsupported || got.Reason != evalscore.ReasonAttributionUnresolved {
		t.Fatalf("the unattributed cap-exhausted UNKNOWN classifies as %+v, want UNSUPPORTED", got)
	}
	v := goodComp(cfg)
	v.TaskID, v.Attribution = "task-000", evalscore.AttributionUnsupported
	v.C.Task, v.C.Partial, v.C.EvidenceLoss = unknown.Task, unknown.Partial, unknown.EvidenceLoss
	v.C.UnknownEnd, v.C.OutageEvidenceDigest, v.C.Receipt = unknown.UnknownEnd, unknown.OutageEvidenceDigest, unknown.Receipt
	record = cfg.AssembleCompliant(v)
	if got := record.Classify(); got.Disposition != evalscore.ExternalInfrastructureRerun || got.Reason != evalscore.ReasonOutcomeUnknown {
		t.Fatalf("the cap-exhausted UNKNOWN record classifies as %+v, want the registered rerun", got)
	}
	if s := summarize(record); s.Clauses.Friction.Evaluable {
		t.Fatalf("friction evaluable with a rerun still pending: %+v", s.Clauses.Friction)
	}
}
