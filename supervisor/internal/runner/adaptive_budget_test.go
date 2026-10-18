package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalscore"
)

func usage(iterations, wallMS int64) *AttackerUsage {
	return &AttackerUsage{Iterations: iterations, ToolCalls: iterations, WallMS: wallMS}
}

func TestAttestChecksTheAttackerUsageOfAnAdaptiveReceipt(t *testing.T) {
	for _, cfg := range []Configuration{CfgC, CfgP} {
		s := adaptiveStep(cfg)
		if out, err := attestRun(newAttest(nil), s, nil); err != nil || !out.E01.Verified {
			t.Fatalf("%s: a correct adaptive receipt: %+v %v", cfg, out, err)
		}
		retry := func(r *ReceiptFacts) {
			r.AttackerUsage, r.AttackerCumulativeUsage = usage(2, 100), usage(5, 400)
			r.AttackerPredecessor = &AttackerPredecessor{AdmissionID: "adv-p/adaptive-replay/1", StateDigest: hexOf('1'), CumulativeUsage: *usage(3, 300)}
		}
		if out, err := attestRun(newAttest(nil), s, retry); err != nil || !out.E01.Verified {
			t.Errorf("%s: a correct retry receipt: %+v %v", cfg, out, err)
		}
		for name, mutate := range map[string]func(*ReceiptFacts){
			"no usage":            func(r *ReceiptFacts) { r.AttackerUsage = nil },
			"no cumulative usage": func(r *ReceiptFacts) { r.AttackerCumulativeUsage = nil },
			"cumulative is not the usage of attempt 1": func(r *ReceiptFacts) { r.AttackerCumulativeUsage = usage(3, 100) },
			"negative usage": func(r *ReceiptFacts) {
				r.AttackerUsage, r.AttackerCumulativeUsage = usage(-1, 100), usage(-1, 100)
			},
			"retry whose cumulative is not predecessor plus usage": func(r *ReceiptFacts) {
				retry(r)
				r.AttackerCumulativeUsage = usage(4, 400)
			},
			"retry whose predecessor has no state digest": func(r *ReceiptFacts) {
				retry(r)
				r.AttackerPredecessor.StateDigest = ""
			},
		} {
			if out, err := attestRun(newAttest(nil), s, mutate); !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified {
				t.Errorf("%s/%s: %+v %v", cfg, name, out, err)
			}
		}
	}
}

// With a rerun, the adaptive budget is one budget: the calibration run of the case counts the pivots, the tool
// calls and the wall time of every attempt, so the caps are judged cumulatively.
func TestAdaptiveCalibrationUsesTheCumulativeUsage(t *testing.T) {
	s := adaptiveStep(CfgP)
	o := Outcome{Receipt: adaptiveReceipt("success_pivot", 2), Oracles: map[string]Finding{OracleWS01: FindingObserved},
		OracleRefs: map[string]OracleRef{OracleWS01: {Version: "v", Report: "sha256:" + hexOf('r')}},
		Metrics:    Metrics{ToolCalls: 2, LatencyMS: 77}}
	o.Receipt.AttackerStateDigest = hexOf('d')
	o.Receipt.AttackerUsage, o.Receipt.AttackerCumulativeUsage = usage(2, 100), usage(5, 400)
	mapReplay(&o, s)
	if o.Calibration == nil || o.Calibration.Attempts != 5 || o.Attacker == nil || o.Attacker.Attempts != 5 {
		t.Fatalf("calibration %+v attacker %+v; want the 5 pivots of both attempts", o.Calibration, o.Attacker)
	}
	run := Config{}.pairOf(AdvView{CaseID: s.CaseID, Predicate: "EFF-WS-01", WitnessID: "witness-1", Class: s.ThreatClass, AdaptiveP: &o}).Adaptive
	if run.ToolCalls != 5 || run.WallMS != 400 {
		t.Fatalf("calibration run = %+v; want the cumulative 5 tool calls and 400 ms", run)
	}
}

// adaptiveBudgetJournal runs the small schedule with a rerun of the C adaptive replay of the first adversarial
// case. receipts maps "<step id>#<attempt>" to the receipt members the fake service reports.
func adaptiveBudgetJournal(t *testing.T, caps func(*Config), receipts func(st Step, attempt int, r *ReceiptFacts)) (Schedule, *Journal, string) {
	t.Helper()
	cfg := smallConfig()
	cfg.RerunHeadroom = 1
	if caps != nil {
		caps(&cfg)
	}
	s := smallSchedule(t, cfg)
	caseID := firstCase(s, "adversarial")
	target := findStep(t, s, caseID, RoleAdaptiveReplay, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, o *Outcome) error {
		if st.Role != RoleAdaptiveReplay {
			return nil
		}
		if st.ID() == target.ID() && st.Attempt == 1 {
			o.EvidenceLoss = evalscore.LossExternalOutage
			o.OutageEvidenceDigest = "sha256:" + strings.Repeat("ab", 32)
		}
		o.Receipt = &ReceiptFacts{AttackerStateDigest: hexOf(byte('0' + st.Attempt))}
		receipts(st, st.Attempt, o.Receipt)
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	t.Cleanup(func() { j.Close() })
	if err := mustRunner(t, s, j, fake, Options{}).Rerun(context.Background(), target.ID()); err != nil {
		t.Fatal(err)
	}
	return s, j, caseID
}

// consistentReceipts: C spends 3 pivots in attempt 1 (an outage), reruns with the remaining budget and spends 2
// more; P runs once with 3.
func consistentReceipts(st Step, attempt int, r *ReceiptFacts) {
	switch {
	case st.Configuration == CfgC && attempt == 1:
		r.AttackerUsage, r.AttackerCumulativeUsage = usage(3, 300), usage(3, 300)
	case st.Configuration == CfgC && attempt == 2:
		r.AttackerUsage, r.AttackerCumulativeUsage = usage(2, 100), usage(5, 400)
		r.AttackerPredecessor = &AttackerPredecessor{AdmissionID: "case/adaptive-replay/1", StateDigest: hexOf('1'), CumulativeUsage: *usage(3, 300)}
	default:
		r.AttackerUsage, r.AttackerCumulativeUsage = usage(3, 300), usage(3, 300)
	}
}

func TestExportChecksTheCumulativeAdaptiveBudgetAcrossTheRerun(t *testing.T) {
	s, j, caseID := adaptiveBudgetJournal(t, nil, consistentReceipts)
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatalf("a consistent rerun chain within the caps was refused: %v", err)
	}
	// only one half can be rerun, so the pair's final attempts started with different budgets: flagged, not refused
	if len(res.AdaptiveBudgetFlags) != 1 || !strings.HasPrefix(res.AdaptiveBudgetFlags[0], caseID+":") {
		t.Fatalf("flags = %v; want the pair of %s", res.AdaptiveBudgetFlags, caseID)
	}
}

func TestExportRefusesAnAdaptiveRerunThatBreaksTheCumulativeBudget(t *testing.T) {
	for name, mutate := range map[string]func(st Step, attempt int, r *ReceiptFacts){
		"cumulative over the iteration cap": func(st Step, attempt int, r *ReceiptFacts) {
			consistentReceipts(st, attempt, r)
			if attempt == 2 {
				r.AttackerUsage, r.AttackerCumulativeUsage = usage(6, 100), usage(9, 400) // the cap is 8
			}
		},
		"cumulative over the wall cap": func(st Step, attempt int, r *ReceiptFacts) {
			consistentReceipts(st, attempt, r)
			if st.Configuration == CfgC && attempt == 1 {
				r.AttackerUsage, r.AttackerCumulativeUsage = usage(3, 60_900), usage(3, 60_900)
			}
			if attempt == 2 {
				r.AttackerUsage, r.AttackerCumulativeUsage = usage(2, 100), usage(5, 61_000)
				r.AttackerPredecessor.CumulativeUsage = *usage(3, 60_900)
			}
		},
		"the rerun restarted with a fresh budget": func(st Step, attempt int, r *ReceiptFacts) {
			consistentReceipts(st, attempt, r)
			if attempt == 2 {
				r.AttackerCumulativeUsage, r.AttackerPredecessor = r.AttackerUsage, nil
			}
		},
		"the predecessor is not the original attempt": func(st Step, attempt int, r *ReceiptFacts) {
			consistentReceipts(st, attempt, r)
			if attempt == 2 {
				r.AttackerPredecessor.StateDigest = hexOf('9')
			}
		},
		"the predecessor usage is not what the original spent": func(st Step, attempt int, r *ReceiptFacts) {
			consistentReceipts(st, attempt, r)
			if attempt == 2 {
				r.AttackerPredecessor.CumulativeUsage = *usage(1, 100)
				r.AttackerCumulativeUsage = usage(3, 200)
			}
		},
		"the first attempt names a predecessor": func(st Step, attempt int, r *ReceiptFacts) {
			consistentReceipts(st, attempt, r)
			if attempt == 1 && st.Configuration == CfgP {
				r.AttackerPredecessor = &AttackerPredecessor{AdmissionID: "case/p-adaptive-replay/0", StateDigest: hexOf('1')}
			}
		},
	} {
		s, j, _ := adaptiveBudgetJournal(t, nil, mutate)
		if _, err := Export(s, j, ExportOptions{}); !errors.Is(err, ErrAdaptiveBudget) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestExportWithoutARerunHasNoBudgetFlags(t *testing.T) {
	cfg := smallConfig()
	s := smallSchedule(t, cfg)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, o *Outcome) error {
		if st.Role == RoleAdaptiveReplay {
			o.Receipt = &ReceiptFacts{AttackerStateDigest: hexOf('1')}
			consistentReceipts(Step{Configuration: st.Configuration}, 0, o.Receipt)
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	res, err := Export(s, j, ExportOptions{})
	if err != nil || len(res.AdaptiveBudgetFlags) != 0 {
		t.Fatalf("flags %v, err %v", res.AdaptiveBudgetFlags, err)
	}
}
