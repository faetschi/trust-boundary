package runner

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/usdledger"
)

// All fixtures here are synthetic. They exercise the runner's code only.

func smallQuotas() evalcorpus.Quotas {
	return evalcorpus.Quotas{
		Adversarial: map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 4, evalcorpus.ClassTMB: 4, evalcorpus.ClassTMC: 3},
		Compliant:   map[evalcorpus.Tool]int{evalcorpus.ToolRead: 2, evalcorpus.ToolWrite: 2, evalcorpus.ToolEdit: 2, evalcorpus.ToolBash: 2},
		Ambiguous:   2,
		Variance:    map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 1, evalcorpus.ClassTMB: 1, evalcorpus.ClassTMC: 1},
		ColdPercent: 20,
	}
}

func digestOf(label string) string {
	return fmt.Sprintf("sha256:%064x", len(label)) // shape only; never compared to anything
}

// synth builds an unallocated manifest and its seeded allocation for q.
func synth(t testing.TB, q evalcorpus.Quotas) (src, alloc evalcorpus.Manifest) {
	t.Helper()
	src = evalcorpus.Manifest{
		SchemaVersion: evalcorpus.SchemaVersion,
		SplitSeed:     "runner-test-seed",
		Pins: evalcorpus.Pins{
			EnvironmentDigest: digestOf("e"), ModelDigest: digestOf("m"),
			ProfileDigest: digestOf("pr"), PolicyDigest: digestOf("po"),
		},
	}
	for _, class := range []evalcorpus.ThreatClass{evalcorpus.ClassTMA, evalcorpus.ClassTMB, evalcorpus.ClassTMC} {
		for i := 1; i <= q.Adversarial[class]; i++ {
			id := fmt.Sprintf("adv-%s-%03d", class, i)
			src.Cases = append(src.Cases, evalcorpus.Case{
				CaseID: id, Kind: evalcorpus.KindAdversarial,
				Adversarial: &evalcorpus.Adversarial{
					ThreatClass: class, OracleID: "oracle", ForbiddenEffects: []string{"EFF-WS-01"},
					CalibrationWitnessID: "wit-" + id,
				},
			})
		}
	}
	for _, tool := range []evalcorpus.Tool{evalcorpus.ToolRead, evalcorpus.ToolWrite, evalcorpus.ToolEdit, evalcorpus.ToolBash} {
		for i := 1; i <= q.Compliant[tool]; i++ {
			src.Cases = append(src.Cases, evalcorpus.Case{
				CaseID: fmt.Sprintf("cmp-%s-%03d", tool, i), Kind: evalcorpus.KindCompliant,
				Compliant: &evalcorpus.Compliant{PrimaryTool: tool, OracleID: "task-oracle"},
			})
		}
	}
	for i := 1; i <= q.Ambiguous; i++ {
		src.Cases = append(src.Cases, evalcorpus.Case{
			CaseID: fmt.Sprintf("amb-%03d", i), Kind: evalcorpus.KindAmbiguous, Ambiguous: &evalcorpus.Ambiguous{},
		})
	}
	var err error
	alloc, err = evalcorpus.Allocate(src, q)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	return src, alloc
}

func testCaps() calibration.Caps {
	return calibration.Caps{Iterations: 8, ToolCalls: 50, WallMS: 60000, InTokens: 100000, OutTokens: 50000, MicroUSD: 10000}
}

func testConfig() Config {
	return Config{
		FreezeCommit:   strings.Repeat("a", 40),
		AdaptiveCaps:   testCaps(),
		EVQObligations: []string{"EVQ-1", "EVQ-2"},
	}
}

// smallSchedule plans the small synthetic corpus.
func smallSchedule(t testing.TB, cfg Config) Schedule {
	t.Helper()
	src, alloc := synth(t, smallQuotas())
	s, err := Plan(src, alloc, cfg)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return s
}

// smallConfig sizes the unit cap to the small corpus.
func smallConfig() Config {
	cfg := testConfig()
	cfg.UnitCap = 81
	return cfg
}

func openJournal(t testing.TB, dir string, s Schedule) *Journal {
	t.Helper()
	j, err := OpenJournal(filepath.Join(dir, "journal.jsonl"), s)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	return j
}

func mustRunner(t testing.TB, s Schedule, j *Journal, e Executor, opt Options) *Runner {
	t.Helper()
	r, err := NewRunner(s, j, e, opt)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

func runAll(t testing.TB, s Schedule, dir string, e Executor) (*Journal, RunReport) {
	t.Helper()
	j := openJournal(t, dir, s)
	rep, err := mustRunner(t, s, j, e, Options{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return j, rep
}

func findStep(t testing.TB, s Schedule, caseID string, role Role, cfg Configuration) Step {
	t.Helper()
	for _, st := range s.Steps {
		if st.CaseID == caseID && st.Role == role && st.Configuration == cfg && st.Repeat <= 1 {
			return st
		}
	}
	t.Fatalf("no step %s/%s/%s", caseID, role, cfg)
	return Step{}
}

func firstCase(s Schedule, kind string) string {
	for _, st := range s.Steps {
		if string(st.Kind) == kind {
			return st.CaseID
		}
	}
	return ""
}

func okLedger(settledUnits int, committed int64) func() (usdledger.Snapshot, error) {
	return func() (usdledger.Snapshot, error) {
		return usdledger.Snapshot{
			LedgerID: "L", CapMicroUSD: evalreport.RegisteredCapMicroUSD, MinPerUnitWorstCaseMicroUSD: 1000,
			SettledMicroUSD: committed, CommittedMicroUSD: committed, RemainingMicroUSD: evalreport.RegisteredCapMicroUSD - committed,
			StopRule:     evalreport.RegisteredCapMicroUSD-committed < 1000,
			SettledUnits: settledUnits, HeadSHA256: strings.Repeat("0", 64), Records: 1,
		}, nil
	}
}

func mustSnap(t testing.TB, committed int64) usdledger.Snapshot {
	t.Helper()
	snap, err := okLedger(0, committed)()
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// legacyThesisQuotas is the pre-D48 thesis shape (299 adversarial as 100/100/99, 100 compliant, 20 ambiguous,
// 50 variance as 17/17/16): 1,835 planned units. Its manifest pins no draw, so it keeps the legacy totals and the
// plan digests pinned in plan_test.go.
func legacyThesisQuotas() evalcorpus.Quotas {
	return evalcorpus.Quotas{
		Adversarial: map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 100, evalcorpus.ClassTMB: 100, evalcorpus.ClassTMC: 99},
		Compliant:   map[evalcorpus.Tool]int{evalcorpus.ToolRead: 25, evalcorpus.ToolWrite: 25, evalcorpus.ToolEdit: 25, evalcorpus.ToolBash: 25},
		Ambiguous:   20,
		Variance:    map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 17, evalcorpus.ClassTMB: 17, evalcorpus.ClassTMC: 16},
		ColdPercent: 20,
	}
}

func withV(t testing.TB, s Schedule, caseID string) (Schedule, Step) {
	t.Helper()
	v := findStep(t, s, caseID, RoleCompliant, CfgB)
	v.Configuration, v.Index = CfgV, len(s.Steps)
	s.Steps = append(slices.Clone(s.Steps), v)
	s.index = maps.Clone(s.index)
	s.index[v.ID()] = v.Index
	return s, v
}
