package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/runner"
)

func usageOf(iterations, wallMS int64) *runner.AttackerUsage {
	return &runner.AttackerUsage{Iterations: iterations, ToolCalls: iterations, WallMS: wallMS}
}

func digitOf(attempt int) string { return strings.Repeat(string(rune('0'+attempt)), 64) }

// runWithAdaptiveReceipts runs the fixture with cumulative adaptive receipts and, when retry is set, an outage
// in the C adaptive replay of the first case with one, followed by its rerun: a one-sided retry, so the C half
// of that case continues a budget that the P half never spent.
func runWithAdaptiveReceipts(t *testing.T, e *env, retry bool) (caseID string) {
	t.Helper()
	var target runner.Step
	for _, st := range e.sched().Steps {
		if st.Role == runner.RoleAdaptiveReplay && st.Configuration == runner.CfgC && target.CaseID == "" {
			target = st
		}
	}
	if target.CaseID == "" {
		t.Fatal("fixture: no adaptive replay step")
	}
	f := newFake(e)
	f.Hook = func(st runner.Step, o *runner.Outcome) error {
		if st.Role != runner.RoleAdaptiveReplay {
			return nil
		}
		if retry && st.Index == target.Index && st.Attempt == 1 {
			o.EvidenceLoss, o.OutageEvidenceDigest = evalscore.LossExternalOutage, fixtureDigest("outage")
		}
		r := &runner.ReceiptFacts{AttackerStateDigest: digitOf(st.Attempt)}
		r.AttackerUsage, r.AttackerCumulativeUsage = usageOf(3, 300), usageOf(3, 300)
		if st.Configuration == runner.CfgC && st.Attempt == 2 {
			r.AttackerUsage, r.AttackerCumulativeUsage = usageOf(2, 100), usageOf(5, 400)
			r.AttackerPredecessor = &runner.AttackerPredecessor{AdmissionID: st.CaseID + "/adaptive-replay/1", StateDigest: digitOf(1), CumulativeUsage: *usageOf(3, 300)}
		}
		o.Receipt = r
		return nil
	}
	if _, err := e.cliCfg(fakeWire(f), "run"); err != nil {
		t.Fatal(err)
	}
	if retry {
		if _, err := e.cliCfg(fakeWire(f), "rerun", "--step", target.ID()); err != nil {
			t.Fatal(err)
		}
	}
	return target.CaseID
}

func TestOneSidedAdaptiveRetryIsDisclosedInExportAndReport(t *testing.T) {
	e := newEnv(t)
	caseID := runWithAdaptiveReceipts(t, e, true)

	trials := e.path("out/trials.json")
	if err := os.MkdirAll(filepath.Dir(trials), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := e.cliCfg(nil, "export", "--out", trials)
	if err != nil || line(out, "adaptive_budget_flags") != "1" || !strings.Contains(out, "adaptive_budget_flag "+caseID+":") {
		t.Fatalf("export: %v\n%s", err, out)
	}
	sibling, err := os.ReadFile(filepath.Join(filepath.Dir(trials), "adaptive-budget-flags.json"))
	if err != nil || !strings.Contains(string(sibling), caseID) {
		t.Fatalf("export wrote no adaptive-budget-flags.json for %s: %v\n%s", caseID, err, sibling)
	}
	f, err := os.Open(trials)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	in, err := evalscore.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	var last *evalscore.AdversarialRecord
	for i := range in.Adversarial {
		if in.Adversarial[i].CaseID == caseID {
			last = &in.Adversarial[i]
		}
	}
	if last == nil || last.PCEvidenceComplete {
		t.Fatalf("scored record of %s = %+v; want pc_evidence_complete false", caseID, last)
	}

	rdir := e.path("report")
	out, err = e.cliCfg(nil, "report", "--out", rdir)
	if err != nil || line(out, "adaptive_budget_flags") != "1" {
		t.Fatalf("report: %v\n%s", err, out)
	}
	file, err := os.ReadFile(filepath.Join(rdir, "adaptive-budget-flags.json"))
	if err != nil || string(file) != string(sibling) {
		t.Fatalf("report dir flags file differs from the export one: %v", err)
	}
	sum := sha256.Sum256(file)
	var meta struct {
		Flags  []string `json:"adaptive_budget_flags"`
		Digest string   `json:"adaptive_budget_flags_sha256"`
	}
	raw, _ := os.ReadFile(filepath.Join(rdir, "report.meta.json"))
	if err := json.Unmarshal(raw, &meta); err != nil || len(meta.Flags) != 1 || meta.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("meta %+v %v\n%s", meta, err, raw)
	}
	md, _ := os.ReadFile(filepath.Join(rdir, "report.md"))
	if !strings.Contains(string(md), "## Adaptive budget mismatch") || !strings.Contains(string(md), caseID) ||
		!strings.Contains(string(md), "UNSUPPORTED") {
		t.Fatalf("report.md does not disclose the flagged pair:\n%s", md)
	}
}

func TestNoAdaptiveBudgetFlagsLeavesOutputsUnchanged(t *testing.T) {
	e := newEnv(t)
	runWithAdaptiveReceipts(t, e, false)
	trials := e.path("trials.json")
	out, err := e.cliCfg(nil, "export", "--out", trials)
	if err != nil || strings.Contains(out, "adaptive_budget") {
		t.Fatalf("export: %v\n%s", err, out)
	}
	rdir := e.path("report")
	out, err = e.cliCfg(nil, "report", "--out", rdir)
	if err != nil || strings.Contains(out, "adaptive_budget") {
		t.Fatalf("report: %v\n%s", err, out)
	}
	for _, p := range []string{e.path("adaptive-budget-flags.json"), filepath.Join(rdir, "adaptive-budget-flags.json")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s exists without flags", p)
		}
	}
	for _, name := range []string{"report.meta.json", "report.md"} {
		raw, _ := os.ReadFile(filepath.Join(rdir, name))
		if strings.Contains(string(raw), "adaptive_budget") || strings.Contains(string(raw), "Adaptive budget") {
			t.Errorf("%s mentions adaptive budget flags without any:\n%s", name, raw)
		}
	}
}
