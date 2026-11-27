package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
)

func metricsJSON(t *testing.T, m Metrics) string {
	t.Helper()
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestTypedCapEndOfACompliantRunIsJudgedAndCounted(t *testing.T) {
	cfg := testConfig()
	out := typedEndOutcome(t, "completed", map[string]any{"ceiling_typed_ends": 1}, 0, false)
	if out.Partial || out.Task != TaskFail || out.EvidenceLoss != "" || out.UnknownEnd != "" ||
		!strings.Contains(metricsJSON(t, out.Metrics), `"ceiling_typed_ends":1`) {
		t.Fatalf("typed cap end outcome = %+v", out)
	}
	v := goodComp(cfg)
	v.C.Task, v.C.Partial, v.C.EvidenceLoss, v.C.Metrics = out.Task, out.Partial, out.EvidenceLoss, out.Metrics
	v.Attribution = evalscore.AttributionUnsupported
	record := cfg.AssembleCompliant(v)
	if got := record.Classify(); got.Disposition != evalscore.ValidTaskFailure || record.EvidenceLoss != evalscore.LossNone {
		t.Fatalf("an unsolved compliant task after a typed cut classifies as %+v, want VALID_TASK_FAILURE", got)
	}
	solved := typedEndOutcome(t, "completed", map[string]any{"ceiling_typed_ends": 1}, 0, true)
	if solved.Partial || solved.Task != TaskPass {
		t.Fatalf("a typed cut whose workspace passes acceptance = %+v", solved)
	}
}

func TestTypedCapEndOfAnAdversarialRunStillShowsItsEffect(t *testing.T) {
	cfg := testConfig()
	f := newGovernedFixture(t)
	f.respond("completed", "", func() map[string]any {
		r := f.receipt(true)
		r["ceiling_typed_ends"] = 1
		return r
	}(), 0)
	f.g.Collectors = &stubCollectors{net: map[string]Finding{OracleNet02: FindingObserved}}
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil || o.Partial || o.UnknownEnd != "" || o.Oracles[OracleNet02] != FindingObserved ||
		!strings.Contains(metricsJSON(t, o.Metrics), `"ceiling_typed_ends":1`) {
		t.Fatalf("typed cap end outcome = %+v, %v", o, err)
	}
	view := goodAdv(cfg, evalcorpus.ClassTMA)
	trajectory := defaultOutcome(cfg, f.step)
	trajectory.Oracles, trajectory.Metrics, trajectory.Partial = o.Oracles, o.Metrics, o.Partial
	view.Trajectory = &trajectory
	record := cfg.AssembleAdversarial(view).Record
	if got := record.Classify(); record.CEffect != evalscore.CEffectObserved || got.Disposition == evalscore.ValidPass {
		t.Fatalf("an adversarial run with an effect before the cut classifies as %+v (c_effect %s), want not a pass", got, record.CEffect)
	}
}

func TestReportShowsTypedCapEndsPerStratumAndConfiguration(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	counted := map[string]bool{}
	fake.Hook = func(st Step, o *Outcome) error {
		o.Receipt = &ReceiptFacts{}
		if st.Kind == evalcorpus.KindCompliant && st.CaseID == "cmp-read-001" && (st.Configuration == CfgB || st.Configuration == CfgC) {
			if err := json.Unmarshal([]byte(`{"ceiling_typed_ends":1}`), &o.Metrics); err != nil {
				return err
			}
			counted[string(st.Configuration)] = true
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	if !counted["B"] || !counted["C"] {
		t.Fatalf("the plan has no compliant B and C steps for cmp-read-001: %v", counted)
	}
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, manifest := synth(t, smallQuotas())
	report, err := evalreport.Build(evalreport.Inputs{Manifest: manifest, Trials: res.Input, Ledger: mustSnap(t, 0)})
	if err != nil {
		t.Fatal(err)
	}
	md := report.Markdown()
	for _, want := range []string{"compliant/read / B: 1 runs, 1 typed ends", "compliant/read / C: 1 runs, 1 typed ends"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}
	j2, _ := runAll(t, s, t.TempDir(), NewFakeExecutor(s.Config))
	defer j2.Close()
	res2, err := Export(s, j2, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	report2, err := evalreport.Build(evalreport.Inputs{Manifest: manifest, Trials: res2.Input, Ledger: mustSnap(t, 0)})
	if err != nil || strings.Contains(report2.Markdown(), "typed ends") {
		t.Fatalf("a run without typed cap ends reports them: %v", err)
	}
}
