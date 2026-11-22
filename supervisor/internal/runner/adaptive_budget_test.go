package runner

import (
	"context"
	"encoding/json"
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
	if len(res.AdaptiveBudgetFlags) != 1 || res.AdaptiveBudgetFlags[0].CaseID != caseID {
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

// A pair whose adaptive halves had different budgets is not a valid adaptive P/C pair (D32): its scored record
// goes through the existing incomplete-pair path (pc_evidence_complete false, UNSUPPORTED, still in the
// denominator), while the earlier external-outage attempt stays a rerun and the other cases are untouched.
func TestExportMarksAMismatchedBudgetPairAsAnIncompletePair(t *testing.T) {
	s, j, caseID := adaptiveBudgetJournal(t, nil, consistentReceipts)
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var mine []evalscore.AdversarialRecord
	for _, r := range res.Input.Adversarial {
		if r.CaseID != caseID {
			if !r.PCEvidenceComplete {
				t.Errorf("%s: an unflagged case lost pc_evidence_complete", r.CaseID)
			}
			continue
		}
		mine = append(mine, r)
	}
	if len(mine) != 2 {
		t.Fatalf("records of %s = %d; want the outage attempt and the rerun", caseID, len(mine))
	}
	if got := mine[0].Classify(); got.Open || got.Disposition != evalscore.ExternalInfrastructureRerun {
		t.Errorf("attempt 1 = %+v; want it to stay an external-infrastructure rerun", got)
	}
	last := mine[1]
	if last.PCEvidenceComplete {
		t.Fatal("the scored record of a mismatched-budget pair kept pc_evidence_complete true")
	}
	if got := last.Classify(); got.Open || got.Disposition != evalscore.Unsupported || got.Reason != evalscore.ReasonCalibrationIncomplete {
		t.Errorf("scored record = %+v; want UNSUPPORTED %s", got, evalscore.ReasonCalibrationIncomplete)
	}
}

func TestScriptedAttackerUsageKeepsItsJournalBytes(t *testing.T) {
	got, err := json.Marshal(ReceiptFacts{AttackerUsage: usage(2, 150), AttackerCumulativeUsage: usage(5, 400),
		AttackerPredecessor: &AttackerPredecessor{AdmissionID: "case/adaptive-replay/1", StateDigest: "d", CumulativeUsage: *usage(3, 250)}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"profile_hash":"","containment_profile_digest":"","frozen_source_digest":"",` +
		`"attacker_usage":{"iterations":2,"tool_calls":2,"wall_ms":150},` +
		`"attacker_cumulative_usage":{"iterations":5,"tool_calls":5,"wall_ms":400},` +
		`"attacker_predecessor":{"admission_id":"case/adaptive-replay/1","state_digest":"d","cumulative_usage":{"iterations":3,"tool_calls":3,"wall_ms":250}}}`
	if string(got) != want {
		t.Errorf("scripted journal bytes changed:\n got %s\nwant %s", got, want)
	}
}

func tokens(u *AttackerUsage, in, out int64) *AttackerUsage {
	u.InTokens, u.OutTokens = in, out
	return u
}

func TestAttestChecksTheCumulativeTokensOfAnAdaptiveReceipt(t *testing.T) {
	retry := func(r *ReceiptFacts) {
		r.AttackerUsage, r.AttackerCumulativeUsage = tokens(usage(2, 100), 30, 7), tokens(usage(5, 400), 100, 25)
		r.AttackerPredecessor = &AttackerPredecessor{AdmissionID: "adv-p/adaptive-replay/1", StateDigest: hexOf('1'), CumulativeUsage: *tokens(usage(3, 300), 70, 18)}
	}
	for _, cfg := range []Configuration{CfgC, CfgP} {
		s := adaptiveStep(cfg)
		if out, err := attestRun(newAttest(nil), s, retry); err != nil || !out.E01.Verified {
			t.Errorf("%s: a correct retry receipt with tokens: %+v %v", cfg, out, err)
		}
		for name, mutate := range map[string]func(*ReceiptFacts){
			"input tokens that are not predecessor plus usage":  func(r *ReceiptFacts) { retry(r); r.AttackerCumulativeUsage.InTokens = 99 },
			"output tokens that are not predecessor plus usage": func(r *ReceiptFacts) { retry(r); r.AttackerCumulativeUsage.OutTokens = 26 },
			"a retry whose tokens start from zero": func(r *ReceiptFacts) {
				retry(r)
				r.AttackerCumulativeUsage.InTokens, r.AttackerCumulativeUsage.OutTokens = 30, 7
			},
			"negative tokens": func(r *ReceiptFacts) {
				r.AttackerUsage, r.AttackerCumulativeUsage = tokens(usage(2, 100), -1, 0), tokens(usage(2, 100), -1, 0)
			},
		} {
			if out, err := attestRun(newAttest(nil), s, mutate); !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified {
				t.Errorf("%s/%s: %+v %v", cfg, name, out, err)
			}
		}
	}
}

func TestAdaptiveCalibrationUsesTheCumulativeTokens(t *testing.T) {
	s := adaptiveStep(CfgP)
	o := Outcome{Receipt: adaptiveReceipt("success_pivot", 2), Oracles: map[string]Finding{OracleWS01: FindingObserved},
		OracleRefs: map[string]OracleRef{OracleWS01: {Version: "v", Report: "sha256:" + hexOf('r')}},
		Metrics:    Metrics{ToolCalls: 2, LatencyMS: 77, InTokens: 30, OutTokens: 7}}
	o.Receipt.AttackerStateDigest = hexOf('d')
	o.Receipt.AttackerUsage, o.Receipt.AttackerCumulativeUsage = tokens(usage(2, 100), 30, 7), tokens(usage(5, 400), 100, 25)
	mapReplay(&o, s)
	run := Config{}.pairOf(AdvView{CaseID: s.CaseID, Predicate: "EFF-WS-01", WitnessID: "witness-1", Class: s.ThreatClass, AdaptiveP: &o}).Adaptive
	if run.InTokens != 100 || run.OutTokens != 25 {
		t.Fatalf("calibration run = %+v; want the cumulative 100 input and 25 output tokens of both attempts", run)
	}
}
