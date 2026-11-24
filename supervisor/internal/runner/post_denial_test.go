package runner

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
)

func TestExportPostDenialTrials(t *testing.T) {
	for _, rerunCfg := range []Configuration{CfgB, CfgC} {
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

func TestRerunRefusesDescriptiveV(t *testing.T) {
	cfg := smallConfig()
	cfg.RerunHeadroom = 1
	s := smallSchedule(t, cfg)
	s, rerun := withV(t, s, firstCase(s, "compliant"))
	fake := NewFakeExecutor(s.Config)
	fake.Hook = outageHook(rerun)
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	err := mustRunner(t, s, j, fake, Options{}).Rerun(context.Background(), rerun.ID())
	if !errors.Is(err, ErrRerunNotAllowed) {
		t.Fatalf("V rerun err = %v, want ErrRerunNotAllowed", err)
	}
}

// A V setup failure (terminal, no outcome), a V that never ended and a plan with no V step (D58 amendment) all leave
// the compliant task exported with VMissing, and none fails EVQ (D44).
func TestExportKeepsTaskWithMissingV(t *testing.T) {
	for _, mode := range []string{"setup-failure", "never-ended", "absent"} {
		t.Run(mode, func(t *testing.T) {
			cfg := smallConfig()
			s := smallSchedule(t, cfg)
			id := firstCase(s, "compliant")
			v := Step{}
			if mode != "absent" {
				s, v = withV(t, s, id)
			}
			fake := NewFakeExecutor(s.Config)
			if mode == "setup-failure" {
				fake.Hook = func(st Step, _ *Outcome) error {
					if st.ID() == v.ID() {
						return errors.New("v setup failed")
					}
					return nil
				}
			} else {
				fake.PrepareHook = func(st Step) error {
					if st.ID() == v.ID() {
						return errors.New("v never prepared")
					}
					return nil
				}
			}
			j := openJournal(t, t.TempDir(), s)
			_, _ = mustRunner(t, s, j, fake, Options{}).Run(context.Background())
			defer j.Close()
			res, err := Export(s, j, ExportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(res.Incomplete, id) {
				t.Fatalf("task %s is incomplete although only V is missing", id)
			}
			var got *evalscore.CompliantRecord
			for i := range res.Input.Compliant {
				if res.Input.Compliant[i].TaskID == id {
					got = &res.Input.Compliant[i]
				}
			}
			if got == nil || !got.VMissing || got.VSuccess || !got.IdentityVerified || !got.ObserverCoverageComplete || got.EvidenceLoss != evalscore.LossNone {
				t.Fatalf("record = %+v", got)
			}
			if !res.Input.EVQPassed {
				t.Fatal("a missing V result failed EVQ")
			}
		})
	}
}
