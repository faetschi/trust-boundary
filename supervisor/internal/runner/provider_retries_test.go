package runner

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
)

// Register D30 as amended 2026-11-18, Accepted risk: a retried provider attempt charges the host-call allowance, and
// a C trajectory that then reaches D56's tools-disabled final call ended earlier for the attacker. The export records
// provider_retries for every adversarial C trajectory that retried, and the report lists them and flags the ones that
// also made a tools-disabled call. Retries of any other step are not this record, and a run without any prints no
// provider-retry line, so older reports keep their bytes.
func TestExportReportsProviderRetriesOfCTrajectories(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	var trajectories []string
	fake.Hook = func(st Step, o *Outcome) error {
		o.Receipt = &ReceiptFacts{}
		o.Metrics.Retries = 2
		if st.Kind == evalcorpus.KindAdversarial && st.Role == RoleTrajectory && st.Configuration == CfgC {
			trajectories = append(trajectories, st.CaseID)
			o.Metrics.Retries = 1
			if len(trajectories) == 1 {
				o.Metrics.ToolsDisabledCalls = 1
			}
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	if len(trajectories) < 2 {
		t.Fatalf("the plan has %d adversarial C trajectories, want at least 2", len(trajectories))
	}
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Input.ProviderRetries) != len(trajectories) {
		t.Fatalf("provider-retry records = %+v, want one per C trajectory %v", res.Input.ProviderRetries, trajectories)
	}
	for _, p := range res.Input.ProviderRetries {
		if p.ProviderRetries != 1 || p.Attempt != 1 || p.RetriedWithToolsDisabled() != (p.CaseID == trajectories[0]) {
			t.Fatalf("record %+v", p)
		}
	}
	var buf bytes.Buffer
	if err := EncodeTrials(&buf, res.Input); err != nil {
		t.Fatal(err)
	}
	loaded, err := evalscore.Load(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.ProviderRetries, res.Input.ProviderRetries) {
		t.Fatal("export/load lost the provider-retry records")
	}
	_, manifest := synth(t, smallQuotas())
	report, err := evalreport.Build(evalreport.Inputs{Manifest: manifest, Trials: loaded, Ledger: mustSnap(t, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ProviderRetries) != len(trajectories) {
		t.Fatalf("report provider retries = %+v", report.ProviderRetries)
	}
	want := "of them with a tools-disabled call (D56) 1: " + trajectories[0] + " attempt 1"
	if md := report.Markdown(); !strings.Contains(md, want) {
		t.Fatalf("markdown lacks %q:\n%s", want, md)
	}
}

func TestReportWithoutProviderRetriesPrintsNoRetryLine(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	j, _ := runAll(t, s, t.TempDir(), NewFakeExecutor(s.Config))
	defer j.Close()
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, manifest := synth(t, smallQuotas())
	report, err := evalreport.Build(evalreport.Inputs{Manifest: manifest, Trials: res.Input, Ledger: mustSnap(t, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Input.ProviderRetries) != 0 || strings.Contains(report.Markdown(), "provider retries") {
		t.Fatalf("a run without retries reports them: %+v", res.Input.ProviderRetries)
	}
}
