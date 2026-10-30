package runner

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
)

func TestExportPostDenialTrials(t *testing.T) {
	for _, rerunCfg := range []Configuration{CfgB, CfgC, CfgV} {
		t.Run(string(rerunCfg), func(t *testing.T) {
			cfg := smallConfig()
			cfg.RerunHeadroom = 1
			s := smallSchedule(t, cfg)
			id := firstCase(s, "compliant")
			rerun := findStep(t, s, id, RoleCompliant, rerunCfg)
			proposals := []ProposalFact{
				{Tool: "write", Verdict: "DENY", ReasonCode: "scope_protected_path"},
				{Tool: "bash", Verdict: "ALLOW", ReasonCode: "policy_rule_allow", Status: "command_failed"},
				{Tool: "read", Verdict: "ALLOW", ReasonCode: "policy_rule_allow"},
			}
			fake := NewFakeExecutor(s.Config)
			fake.Hook = func(st Step, o *Outcome) error {
				o.Receipt = &ReceiptFacts{Proposals: proposals}
				return outageHook(rerun)(st, o)
			}
			j, _ := runAll(t, s, t.TempDir(), fake)
			defer j.Close()
			if err := mustRunner(t, s, j, fake, Options{}).Rerun(context.Background(), rerun.ID()); err != nil {
				t.Fatal(err)
			}
			res, err := Export(s, j, ExportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			wantTrials := 8
			if rerunCfg == CfgC {
				wantTrials++
			}
			if len(res.Input.PostDenialTrials) != wantTrials {
				t.Fatalf("trials = %d, want %d", len(res.Input.PostDenialTrials), wantTrials)
			}
			for _, trial := range res.Input.PostDenialTrials {
				if !reflect.DeepEqual(trial.Proposals, proposals) {
					t.Fatalf("proposal order/status lost: %+v", trial)
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
			if !reflect.DeepEqual(loaded.PostDenialTrials, res.Input.PostDenialTrials) {
				t.Fatal("export/load lost descriptive evidence")
			}
			_, manifest := synth(t, smallQuotas())
			report, err := evalreport.Build(evalreport.Inputs{Manifest: manifest, Trials: loaded, Ledger: mustSnap(t, 0)})
			if err != nil {
				t.Fatal(err)
			}
			if got := report.PostDenialContinuation.C; got.TrialsWithDenial != wantTrials || got.RetryDenied != wantTrials || got.AlternativeAllowed != wantTrials {
				t.Fatalf("report = %+v", got)
			}
		})
	}
}
