package evalreport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/usdledger"
)

// Every fixture here is synthetic. The tests check code paths only and do not
// verify, support, or refute G1, H1, or any other thesis claim.

const synthHead = "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"

func okLedger() usdledger.Snapshot {
	return usdledger.Snapshot{
		LedgerID:                    "research-synthetic",
		CapMicroUSD:                 RegisteredCapMicroUSD,
		MinPerUnitWorstCaseMicroUSD: 2_583,
		SettledMicroUSD:             1_250_000,
		CommittedMicroUSD:           1_250_000,
		RemainingMicroUSD:           RegisteredCapMicroUSD - 1_250_000,
		SettledUnits:                400,
		Records:                     401,
		HeadSHA256:                  synthHead,
	}
}

func advCase(id string, class evalcorpus.ThreatClass, part evalcorpus.Partition) evalcorpus.Case {
	return evalcorpus.Case{
		CaseID: id, Kind: evalcorpus.KindAdversarial,
		Adversarial: &evalcorpus.Adversarial{ThreatClass: class, Partition: part},
	}
}

func cmpCase(id string, tool evalcorpus.Tool) evalcorpus.Case {
	return evalcorpus.Case{
		CaseID: id, Kind: evalcorpus.KindCompliant,
		Compliant: &evalcorpus.Compliant{PrimaryTool: tool},
	}
}

func ambCase(id string) evalcorpus.Case {
	return evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindAmbiguous, Ambiguous: &evalcorpus.Ambiguous{}}
}

func cleanAdv(id string) evalscore.AdversarialRecord {
	return evalscore.AdversarialRecord{
		CaseID: id, Attempt: 1, Channel: "tool_call",
		IdentityVerified: true, ObserverCoverageComplete: true,
		EvidenceLoss: evalscore.LossNone, CEffect: evalscore.CEffectNone,
		PStaticManifested: true, PAdaptiveManifested: true,
		CMatchedDelivery: true, PCEvidenceComplete: true,
	}
}

func cleanCmp(id string) evalscore.CompliantRecord {
	return evalscore.CompliantRecord{
		TaskID: id, Attempt: 1, IdentityVerified: true, ObserverCoverageComplete: true,
		EvidenceLoss: evalscore.LossNone, BSuccess: true, CSuccess: true, VSuccess: true,
		Attribution: evalscore.AttributionNotApplicable,
	}
}

// smallInputs: 3 adversarial cases in 3 strata, 2 compliant tasks, 1 ambiguous
// case, and one clean attempt per scored case.
func smallInputs() Inputs {
	return Inputs{
		Manifest: evalcorpus.Manifest{
			SchemaVersion: evalcorpus.SchemaVersion,
			SplitSeed:     "synthetic-seed",
			Cases: []evalcorpus.Case{
				advCase("A-1", evalcorpus.ClassTMA, evalcorpus.PartitionCold),
				advCase("A-2", evalcorpus.ClassTMA, evalcorpus.PartitionNonCold),
				advCase("B-1", evalcorpus.ClassTMB, evalcorpus.PartitionNonCold),
				cmpCase("C-1", evalcorpus.ToolRead),
				cmpCase("C-2", evalcorpus.ToolBash),
				ambCase("M-1"),
			},
		},
		Trials: evalscore.Input{
			Schema: evalscore.SchemaVersion, EVQPassed: true,
			Adversarial: []evalscore.AdversarialRecord{cleanAdv("A-1"), cleanAdv("A-2"), cleanAdv("B-1")},
			Compliant:   []evalscore.CompliantRecord{cleanCmp("C-1"), cleanCmp("C-2")},
		},
		Ledger: okLedger(),
	}
}

// fullInputs is the registered size: 299 adversarial cases and 100 compliant
// tasks, all clean, which satisfies every evalscore H1 condition.
func fullInputs() Inputs {
	classes := []evalcorpus.ThreatClass{evalcorpus.ClassTMA, evalcorpus.ClassTMB, evalcorpus.ClassTMC}
	tools := []evalcorpus.Tool{evalcorpus.ToolRead, evalcorpus.ToolWrite, evalcorpus.ToolEdit, evalcorpus.ToolBash}
	in := Inputs{
		Manifest: evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: "synthetic-seed"},
		Trials:   evalscore.Input{Schema: evalscore.SchemaVersion, EVQPassed: true},
		Ledger:   okLedger(),
	}
	for i := 0; i < evalscore.RegisteredAdversarialCases; i++ {
		id := fmt.Sprintf("adv-%03d", i)
		part := evalcorpus.PartitionNonCold
		if i%5 == 0 {
			part = evalcorpus.PartitionCold
		}
		in.Manifest.Cases = append(in.Manifest.Cases, advCase(id, classes[i%3], part))
		in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv(id))
	}
	for i := 0; i < evalscore.RegisteredCompliantTasks; i++ {
		id := fmt.Sprintf("task-%03d", i)
		in.Manifest.Cases = append(in.Manifest.Cases, cmpCase(id, tools[i%4]))
		in.Trials.Compliant = append(in.Trials.Compliant, cleanCmp(id))
	}
	return in
}

func mustBuild(t *testing.T, in Inputs) Report {
	t.Helper()
	r, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return r
}

func stratum(t *testing.T, r Report, key string) Stratum {
	t.Helper()
	for _, s := range r.Strata {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no stratum %q in %v", key, r.Strata)
	return Stratum{}
}

func hasFlag(r Report, f string) bool { return slices.Contains(r.Flags, f) }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHappyPathFullCorpus(t *testing.T) {
	in := fullInputs()
	r := mustBuild(t, in)

	if r.Schema != Schema {
		t.Errorf("schema %q", r.Schema)
	}
	if !r.Coverage.Exact || r.Coverage.Adversarial.Scored != 299 || r.Coverage.Compliant.Scored != 100 {
		t.Errorf("coverage %+v", r.Coverage)
	}
	if !r.H1.RuleSatisfied || !r.Clauses.Security.Passed || !r.Clauses.Friction.Passed {
		t.Errorf("want H1 rule satisfied; h1=%+v clauses=%+v", r.H1, r.Clauses)
	}
	if !r.Spend.WithinBudget || len(r.Flags) != 0 {
		t.Errorf("spend %+v flags %v", r.Spend, r.Flags)
	}
	if !strings.HasPrefix(r.Digest, "sha256:") || len(r.Digest) != len("sha256:")+64 {
		t.Errorf("digest %q", r.Digest)
	}

	// The scoring fields are exactly evalscore.Summarize output, not a re-computation.
	trials := in.Trials
	trials.Registered = &evalscore.RegisteredIDs{
		AdversarialIDs: in.Manifest.CaseIDs(evalcorpus.KindAdversarial),
		CompliantIDs:   in.Manifest.CaseIDs(evalcorpus.KindCompliant),
	}
	want, err := evalscore.Summarize(trials)
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]any{
		"clauses": {r.Clauses, want.Clauses}, "h1": {r.H1, want.H1}, "metrics": {r.Metrics, want.Metrics},
		"configurations": {r.Configurations, want.Configurations}, "observed": {r.Observed, want.Observed},
		"invalid": {r.InvalidRecords, want.InvalidRecords},
	} {
		if g, w := mustJSON(t, pair[0]), mustJSON(t, pair[1]); g != w {
			t.Errorf("%s differs from evalscore:\n got %s\nwant %s", name, g, w)
		}
	}

	// Strata partition the registered cases.
	adv, cmp := 0, 0
	for _, s := range r.Strata {
		if strings.HasPrefix(s.Key, "adversarial/") {
			adv += s.Scored
		} else {
			cmp += s.Scored
		}
		if s.Dispositions.ValidPass != s.Scored {
			t.Errorf("stratum %s: %d passes of %d", s.Key, s.Dispositions.ValidPass, s.Scored)
		}
	}
	if adv != 299 || cmp != 100 || len(r.Strata) != 6+4 {
		t.Errorf("strata adv=%d cmp=%d n=%d", adv, cmp, len(r.Strata))
	}
	if r.Calibration.Pairs != 299 || r.Calibration.StaticManifested != 299 ||
		r.Calibration.AdaptiveManifested != 299 || r.Calibration.NonDiscriminative != 0 {
		t.Errorf("calibration %+v", r.Calibration)
	}
}

func TestSmallCorpusStrataAndDispositions(t *testing.T) {
	in := smallInputs()
	in.Trials.Adversarial[0].CEffect = evalscore.CEffectObserved // A-1 escapes
	in.Trials.Compliant[1].CSuccess = false                      // C-2 blocked
	in.Trials.Compliant[1].Attribution = evalscore.AttributionSupported
	r := mustBuild(t, in)

	if r.Coverage.UnscoredAmbiguous != 1 || !r.Coverage.Exact {
		t.Errorf("coverage %+v", r.Coverage)
	}
	a1 := stratum(t, r, "adversarial/TM-A/cold")
	if a1.Registered != 1 || a1.Scored != 1 || a1.Dispositions.ValidSecurityFailure != 1 || a1.Dispositions.ValidPass != 0 {
		t.Errorf("A cold %+v", a1)
	}
	if s := stratum(t, r, "adversarial/TM-A/non_cold"); s.Dispositions.ValidPass != 1 {
		t.Errorf("A non_cold %+v", s)
	}
	if s := stratum(t, r, "compliant/bash"); s.Dispositions.ValidFrictionObstruction != 1 {
		t.Errorf("bash %+v", s)
	}
	if s := stratum(t, r, "compliant/read"); s.Dispositions.ValidPass != 1 {
		t.Errorf("read %+v", s)
	}
	if r.Configurations.C.Escapes != 1 || r.Configurations.C.Blocked != 1 {
		t.Errorf("config C %+v", r.Configurations.C)
	}
	// 3 and 2 cases are not the registered 299 and 100, so the rule cannot hold.
	if r.H1.RuleSatisfied {
		t.Error("rule satisfied for a 3+2 corpus")
	}
	if !r.H1.ObservedSecurityFailure {
		t.Error("escape not reported")
	}
}

func TestMissingCase(t *testing.T) {
	in := smallInputs()
	in.Trials.Compliant = in.Trials.Compliant[:1] // drop C-2
	r := mustBuild(t, in)
	if r.Coverage.Exact || !slices.Equal(r.Coverage.Compliant.Missing, []string{"C-2"}) {
		t.Errorf("coverage %+v", r.Coverage)
	}
	if r.Coverage.Compliant.Scored != 1 || r.Coverage.Compliant.Registered != 2 {
		t.Errorf("compliant coverage %+v", r.Coverage.Compliant)
	}
	if s := stratum(t, r, "compliant/bash"); s.Missing != 1 || s.Scored != 0 || s.Registered != 1 {
		t.Errorf("bash %+v", s)
	}
	if !hasFlag(r, "coverage_not_exact") {
		t.Errorf("flags %v", r.Flags)
	}
	if r.Coverage.Adversarial.Missing == nil || r.Coverage.Compliant.ManifestDuplicate == nil {
		t.Error("empty lists must be non-nil so JSON has [] not null")
	}
}

// A report with a missing case is produced, but it can never satisfy the rule.
func TestMissingCaseNeverSatisfiesRule(t *testing.T) {
	for name, drop := range map[string]func(in *Inputs){
		"adversarial": func(in *Inputs) { in.Trials.Adversarial = in.Trials.Adversarial[1:] },
		"compliant":   func(in *Inputs) { in.Trials.Compliant = in.Trials.Compliant[1:] },
	} {
		in := fullInputs()
		drop(&in)
		r := mustBuild(t, in)
		if r.Coverage.Exact || r.H1.RuleSatisfied || !hasFlag(r, "coverage_not_exact") {
			t.Errorf("%s: exact=%v rule_satisfied=%v flags=%v", name, r.Coverage.Exact, r.H1.RuleSatisfied, r.Flags)
		}
	}
}

// The guard holds even if evalscore's own value were true.
func TestRequireExactCoverageOverridesScoreValue(t *testing.T) {
	h := evalscore.H1Summary{RuleSatisfied: true, PVRUpperWithinLimit: true, Blockers: []string{"b"}}
	got := requireExactCoverage(h, false)
	if got.RuleSatisfied {
		t.Error("rule_satisfied survived inexact coverage")
	}
	if !got.PVRUpperWithinLimit || !slices.Equal(got.Blockers, []string{"b"}) {
		t.Errorf("guard changed more than rule_satisfied: %+v", got)
	}
	if !requireExactCoverage(h, true).RuleSatisfied {
		t.Error("guard must not change an exact-coverage value")
	}
	if !h.RuleSatisfied {
		t.Error("guard modified its argument")
	}
}

// inputErr asserts that err is the sentinel with exactly the listed IDs.
func inputErr(t *testing.T, err, sentinel error, adv, cmp []string) {
	t.Helper()
	var ie *InputError
	if !errors.Is(err, sentinel) || !errors.As(err, &ie) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if !slices.Equal(ie.Adversarial, adv) || !slices.Equal(ie.Compliant, cmp) {
		t.Errorf("IDs adversarial=%v compliant=%v, want %v and %v", ie.Adversarial, ie.Compliant, adv, cmp)
	}
	for _, id := range append(slices.Clone(adv), cmp...) {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error text lacks %q: %v", id, err)
		}
	}
}

func TestRepeatedPrimaryFailsClosed(t *testing.T) {
	esc := func(r evalscore.AdversarialRecord) evalscore.AdversarialRecord {
		r.CEffect = evalscore.CEffectObserved
		return r
	}
	second := func(r evalscore.AdversarialRecord) evalscore.AdversarialRecord { r.Attempt = 2; return r }
	cases := map[string]struct {
		mutate   func(in *Inputs)
		adv, cmp []string
	}{
		"same attempt twice": {func(in *Inputs) {
			in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("A-2"))
		}, []string{"A-2"}, nil},
		"second attempt after a non-rerun result": {func(in *Inputs) {
			in.Trials.Adversarial = append(in.Trials.Adversarial, second(cleanAdv("A-2")))
		}, []string{"A-2"}, nil},
		"observed escape then a clean retry": {func(in *Inputs) {
			in.Trials.Adversarial[1] = esc(cleanAdv("A-2"))
			in.Trials.Adversarial = append(in.Trials.Adversarial, second(cleanAdv("A-2")))
		}, []string{"A-2"}, nil},
		"duplicate registered record keeps its observed escape": {func(in *Inputs) {
			in.Trials.Adversarial[1] = esc(cleanAdv("A-2"))
			in.Trials.Adversarial = append(in.Trials.Adversarial, in.Trials.Adversarial[1])
		}, []string{"A-2"}, nil},
		"duplicated compliant task": {func(in *Inputs) {
			in.Trials.Compliant = append(in.Trials.Compliant, cleanCmp("C-1"))
		}, nil, []string{"C-1"}},
		"both kinds, only the rejected cases are named": {func(in *Inputs) {
			in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("B-1"))
			in.Trials.Compliant = append(in.Trials.Compliant, cleanCmp("C-2"))
			rerun := cleanAdv("A-1") // a legitimate rerun pair stays unnamed
			rerun.EvidenceLoss = evalscore.LossExternalOutage
			rerun.OutageEvidenceDigest = "sha256:" + strings.Repeat("0", 64)
			in.Trials.Adversarial[0] = rerun
			in.Trials.Adversarial = append(in.Trials.Adversarial, second(cleanAdv("A-1")))
		}, []string{"B-1"}, []string{"C-2"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := smallInputs()
			tc.mutate(&in)
			_, err := Build(in)
			inputErr(t, err, ErrDuplicatePrimary, tc.adv, tc.cmp)
			if errors.Is(err, ErrUnregisteredRecord) {
				t.Error("duplicate misreported as unregistered")
			}
		})
	}
}

// An invalid earlier attempt is not withheld: evalscore poisons the case and the
// report keeps both the invalid record and the case's UNSUPPORTED outcome.
func TestInvalidRecordInRepeatedCaseIsRetained(t *testing.T) {
	in := smallInputs()
	bad := cleanAdv("A-2")
	bad.CEffect = "bogus"
	bad.OutageEvidenceDigest = "sha256:" + strings.Repeat("0", 64)
	later := cleanAdv("A-2")
	later.Attempt = 2
	in.Trials.Adversarial[1] = bad
	in.Trials.Adversarial = append(in.Trials.Adversarial, later)
	r := mustBuild(t, in)
	if len(r.InvalidRecords) != 1 || r.InvalidRecords[0].ID != "A-2" {
		t.Errorf("invalid records %+v", r.InvalidRecords)
	}
	if s := stratum(t, r, "adversarial/TM-A/non_cold"); s.InvalidRecords != 1 || s.Scored != 1 || s.Dispositions.Unsupported != 1 {
		t.Errorf("stratum %+v", s)
	}
}

func TestEvidencedRerunIsOneScoredTrial(t *testing.T) {
	in := smallInputs()
	outageRerunPair(t, &in, "A-1")
	r := mustBuild(t, in)
	if !r.Coverage.Exact || r.Observed.SupersededAttempts != 1 {
		t.Errorf("coverage %+v observed %+v", r.Coverage, r.Observed)
	}
	if s := stratum(t, r, "adversarial/TM-A/cold"); s.Scored != 1 || s.Dispositions.ValidPass != 1 {
		t.Errorf("stratum %+v", s)
	}
}

func TestUnregisteredRecordFailsClosed(t *testing.T) {
	ghost := cleanAdv("ghost")
	ghost.CEffect = evalscore.CEffectObserved
	cases := map[string]struct {
		mutate   func(in *Inputs)
		adv, cmp []string
	}{
		"unknown adversarial ID": {func(in *Inputs) {
			in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("ghost"))
		}, []string{"ghost"}, nil},
		"ambiguous case has no record kind": {func(in *Inputs) {
			in.Trials.Compliant = append(in.Trials.Compliant, cleanCmp("M-1"))
		}, nil, []string{"M-1"}},
		"compliant ID under the adversarial kind": {func(in *Inputs) {
			in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("C-1"))
		}, []string{"C-1"}, nil},
		"repeated unknown IDs are listed once": {func(in *Inputs) {
			in.Trials.Adversarial = append(in.Trials.Adversarial, ghost, ghost, cleanAdv("zeta"))
		}, []string{"ghost", "zeta"}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := smallInputs()
			tc.mutate(&in)
			_, err := Build(in)
			inputErr(t, err, ErrUnregisteredRecord, tc.adv, tc.cmp)
		})
	}
}

func TestManifestDuplicateIDIsReported(t *testing.T) {
	in := smallInputs()
	in.Manifest.Cases = append(in.Manifest.Cases, advCase("A-1", evalcorpus.ClassTMA, evalcorpus.PartitionCold))
	r := mustBuild(t, in)
	if r.Coverage.Exact || !slices.Equal(r.Coverage.Adversarial.ManifestDuplicate, []string{"A-1"}) {
		t.Errorf("coverage %+v", r.Coverage)
	}
	if r.Coverage.Adversarial.Registered != 3 {
		t.Errorf("registered counts distinct IDs, got %d", r.Coverage.Adversarial.Registered)
	}
}

func TestInvalidRecordsAreCounted(t *testing.T) {
	in := smallInputs()
	in.Trials.Adversarial[1].CEffect = "bogus"
	in.Trials.Compliant[0].Attribution = "bogus"
	r := mustBuild(t, in)
	if len(r.InvalidRecords) != 2 || r.Observed.InvalidRecords != 2 {
		t.Fatalf("invalid %v observed %+v", r.InvalidRecords, r.Observed)
	}
	if s := stratum(t, r, "adversarial/TM-A/non_cold"); s.InvalidRecords != 1 || s.Dispositions.Unsupported != 1 {
		t.Errorf("A non_cold %+v", s)
	}
	if s := stratum(t, r, "compliant/read"); s.InvalidRecords != 1 {
		t.Errorf("read %+v", s)
	}
	if s := stratum(t, r, "adversarial/TM-A/cold"); s.InvalidRecords != 0 {
		t.Errorf("A cold %+v", s)
	}
	if r.H1.RuleSatisfied {
		t.Error("invalid records must block the rule")
	}
}

// withMoney sets the committed total and derives remaining and the stop-rule
// flag from it exactly as usdledger.Snapshot does.
func withMoney(s *usdledger.Snapshot, open, uncertain, settled int64) {
	s.OpenMicroUSD, s.UncertainMicroUSD, s.SettledMicroUSD = open, uncertain, settled
	s.CommittedMicroUSD = open + uncertain + settled
	s.RemainingMicroUSD = s.CapMicroUSD - s.CommittedMicroUSD
	s.StopRule = s.RemainingMicroUSD < s.MinPerUnitWorstCaseMicroUSD
}

func TestSpendFlags(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *usdledger.Snapshot)
		flag   string
		within bool
	}{
		{"over cap", func(s *usdledger.Snapshot) { withMoney(s, 0, 0, 5_000_001) }, "spend_over_cap", false},
		{"uncertain counts against cap", func(s *usdledger.Snapshot) { withMoney(s, 0, 4_000_000, 1_250_000) }, "spend_over_cap", false},
		{"cap differs from registered", func(s *usdledger.Snapshot) {
			s.CapMicroUSD = 10_000_000
			withMoney(s, 0, 0, s.SettledMicroUSD)
		}, "spend_cap_mismatch", false},
		{"overspent", func(s *usdledger.Snapshot) { s.Overspent = true }, "ledger_overspent", false},
		{"stop rule", func(s *usdledger.Snapshot) { withMoney(s, 0, 0, 4_999_000) }, "ledger_stop_rule", true},
		{"units over planned", func(s *usdledger.Snapshot) { s.SettledUnits = LegacyPlannedUnits + 1 }, "units_over_planned", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := smallInputs()
			tc.mutate(&in.Ledger)
			r := mustBuild(t, in)
			if !hasFlag(r, tc.flag) {
				t.Errorf("flags %v, want %s", r.Flags, tc.flag)
			}
			if r.Spend.WithinBudget != tc.within {
				t.Errorf("within_budget %v, want %v (%+v)", r.Spend.WithinBudget, tc.within, r.Spend)
			}
		})
	}
	r := mustBuild(t, smallInputs())
	s := r.Spend
	if s.TotalUnits != 400 || s.PlannedUnits != 1935 || s.RegisteredCapMicroUSD != 5_000_000 ||
		s.CommittedMicroUSD != 1_250_000 || !s.CapMatchesRegistered || s.OverCap || !s.WithinBudget {
		t.Errorf("spend %+v", s)
	}
}

func TestBuildRejectsBadLedgerHeadAndKind(t *testing.T) {
	for _, head := range []string{"", "xyz", strings.ToUpper(synthHead), synthHead + "00"} {
		in := smallInputs()
		in.Ledger.HeadSHA256 = head
		if _, err := Build(in); err == nil {
			t.Errorf("head %q accepted", head)
		}
	}
	in := smallInputs()
	in.Manifest.Cases = append(in.Manifest.Cases, evalcorpus.Case{CaseID: "Z-1", Kind: "mystery"})
	if _, err := Build(in); err == nil {
		t.Error("unknown case kind accepted")
	}
}

// Every malformed snapshot is an error, never a report.
func TestBuildRejectsMalformedSnapshot(t *testing.T) {
	const maxI = math.MaxInt64
	cases := map[string]func(s *usdledger.Snapshot){
		"committed mismatch":       func(s *usdledger.Snapshot) { s.CommittedMicroUSD++ },
		"remaining mismatch":       func(s *usdledger.Snapshot) { s.RemainingMicroUSD++ },
		"negative open":            func(s *usdledger.Snapshot) { withMoney(s, -1, 0, 1_250_001) },
		"negative uncertain":       func(s *usdledger.Snapshot) { withMoney(s, 0, -1, 1_250_001) },
		"negative settled":         func(s *usdledger.Snapshot) { withMoney(s, 1_250_001, 0, -1) },
		"negative committed":       func(s *usdledger.Snapshot) { s.CommittedMicroUSD, s.RemainingMicroUSD = -1, s.CapMicroUSD+1 },
		"negative cap":             func(s *usdledger.Snapshot) { s.CapMicroUSD = -RegisteredCapMicroUSD },
		"zero cap":                 func(s *usdledger.Snapshot) { s.CapMicroUSD = 0 },
		"negative open units":      func(s *usdledger.Snapshot) { s.OpenUnits = -1 },
		"negative uncertain units": func(s *usdledger.Snapshot) { s.UncertainUnits = -1 },
		"negative settled units":   func(s *usdledger.Snapshot) { s.SettledUnits = -1 },
		"zero threshold":           func(s *usdledger.Snapshot) { s.MinPerUnitWorstCaseMicroUSD = 0 },
		"negative threshold":       func(s *usdledger.Snapshot) { s.MinPerUnitWorstCaseMicroUSD = -1 },
		"threshold above cap":      func(s *usdledger.Snapshot) { s.MinPerUnitWorstCaseMicroUSD = s.CapMicroUSD + 1 },
		"stop rule set, not due":   func(s *usdledger.Snapshot) { s.StopRule = true },
		"stop rule clear, due":     func(s *usdledger.Snapshot) { withMoney(s, 0, 0, 4_999_000); s.StopRule = false },
		// Wraps to committed 0, remaining = cap: only the overflow check rejects it.
		"addition overflow": func(s *usdledger.Snapshot) {
			s.OpenMicroUSD, s.UncertainMicroUSD, s.SettledMicroUSD = maxI, maxI, 2
			s.CommittedMicroUSD, s.RemainingMicroUSD = 0, s.CapMicroUSD
		},
		"unit count overflow": func(s *usdledger.Snapshot) {
			s.OpenUnits, s.SettledUnits = math.MaxInt, 1
		},
		"settled 6 open -2": func(s *usdledger.Snapshot) {
			s.SettledMicroUSD, s.OpenMicroUSD, s.UncertainMicroUSD = 6_000_000, -2_000_000, 0
			s.CommittedMicroUSD, s.RemainingMicroUSD = 4_000_000, 1_000_000
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := smallInputs()
			mutate(&in.Ledger)
			if r, err := Build(in); err == nil {
				t.Errorf("malformed snapshot accepted: %+v", r.Spend)
			}
		})
	}
	// Valid edge cases stay valid: a recorded overage (negative remaining) and a
	// threshold equal to the cap.
	over := smallInputs()
	withMoney(&over.Ledger, 0, 0, 5_000_001)
	over.Ledger.Overspent = true
	if r := mustBuild(t, over); r.Spend.RemainingMicroUSD != -1 || r.Spend.WithinBudget {
		t.Errorf("overage spend %+v", r.Spend)
	}
	edge := smallInputs()
	edge.Ledger.MinPerUnitWorstCaseMicroUSD = edge.Ledger.CapMicroUSD
	withMoney(&edge.Ledger, 0, 0, 1_250_000)
	mustBuild(t, edge)
}

func TestBuildDoesNotMutateInputs(t *testing.T) {
	rerun := func() Inputs {
		in := smallInputs()
		outageRerunPair(t, &in, "A-1")
		return in
	}
	in, want := rerun(), rerun()
	mustBuild(t, in)
	if !reflect.DeepEqual(in, want) {
		t.Error("Build modified its inputs")
	}
	if in.Trials.Registered != nil {
		t.Error("Build set Trials.Registered on the caller's value")
	}
	// A rejected input is not modified either.
	bad, wantBad := smallInputs(), smallInputs()
	bad.Trials.Adversarial = append(bad.Trials.Adversarial, cleanAdv("A-2"))
	wantBad.Trials.Adversarial = append(wantBad.Trials.Adversarial, cleanAdv("A-2"))
	if _, err := Build(bad); err == nil {
		t.Fatal("duplicate accepted")
	}
	if !reflect.DeepEqual(bad, wantBad) {
		t.Error("Build modified a rejected input")
	}
}

// shuffled returns a copy of in with every record list in a different order.
func shuffled(in Inputs) Inputs {
	out := in
	out.Trials.Adversarial = slices.Clone(in.Trials.Adversarial)
	out.Trials.Compliant = slices.Clone(in.Trials.Compliant)
	slices.Reverse(out.Trials.Adversarial)
	slices.Reverse(out.Trials.Compliant)
	if n := len(out.Trials.Adversarial); n > 2 {
		out.Trials.Adversarial = append(out.Trials.Adversarial[n/2:], out.Trials.Adversarial[:n/2]...)
	}
	return out
}

func TestDigestAndOutputStableAcrossRecordOrder(t *testing.T) {
	for name, in := range map[string]Inputs{"small": smallInputs(), "full": fullInputs()} {
		t.Run(name, func(t *testing.T) {
			a, b := mustBuild(t, in), mustBuild(t, shuffled(in))
			if a.Digest != b.Digest {
				t.Errorf("digest depends on record order: %s vs %s", a.Digest, b.Digest)
			}
			ja, _ := a.CanonicalJSON()
			jb, _ := b.CanonicalJSON()
			if !bytes.Equal(ja, jb) {
				t.Error("canonical JSON depends on record order")
			}
			if a.Markdown() != b.Markdown() {
				t.Error("Markdown depends on record order")
			}
		})
	}
}

// fill prepares v for the mutation tests. It gives every slice one element and
// every pointer a target, so no field class is skipped. scalarPaths then lists
// every scalar reachable from v. Fields tagged json:"-" are not part of any digest
// and are skipped.
// rawJSON is the free-form JSON member type (a script turn's arguments): one scalar leaf of the digest.
var rawJSON = reflect.TypeOf(json.RawMessage(nil))

func fill(v reflect.Value) {
	if v.Type() == rawJSON {
		v.Set(reflect.ValueOf(json.RawMessage(`{"a":"b"}`)))
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fill(v.Elem())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).Tag.Get("json") == "-" {
				continue
			}
			fill(v.Field(i))
		}
	case reflect.Slice:
		if v.Len() == 0 {
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		}
		for i := 0; i < v.Len(); i++ {
			fill(v.Index(i))
		}
	case reflect.Map:
		if v.Len() == 0 {
			v.Set(reflect.MakeMap(v.Type()))
			entry := reflect.New(v.Type().Elem()).Elem()
			fill(entry)
			v.SetMapIndex(reflect.ValueOf("k").Convert(v.Type().Key()), entry)
		}
	}
}

func scalarPaths(v reflect.Value, path string, out *[]string) {
	if v.Type() == rawJSON {
		*out = append(*out, path)
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			scalarPaths(v.Elem(), path, out)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.Tag.Get("json") == "-" {
				continue
			}
			if !f.IsExported() {
				panic("unexported field " + path + "." + f.Name + " is invisible to the digest")
			}
			scalarPaths(v.Field(i), path+"."+f.Name, out)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			scalarPaths(v.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		for _, k := range keys {
			scalarPaths(v.MapIndex(k), fmt.Sprintf("%s[%s]", path, k.String()), out)
		}
	default:
		*out = append(*out, path)
	}
}

// mutateAt changes the scalar at path (as produced by scalarPaths) so that its
// canonical JSON differs.
func mutateAt(t *testing.T, root reflect.Value, path string) {
	t.Helper()
	var walk func(v reflect.Value, p string) bool
	walk = func(v reflect.Value, p string) bool {
		if v.Type() == rawJSON {
			if p != path {
				return false
			}
			v.Set(reflect.ValueOf(json.RawMessage(`{"a":"c"}`)))
			return true
		}
		switch v.Kind() {
		case reflect.Pointer:
			return !v.IsNil() && walk(v.Elem(), p)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				f := v.Type().Field(i)
				if f.Tag.Get("json") == "-" {
					continue
				}
				if walk(v.Field(i), p+"."+f.Name) {
					return true
				}
			}
			return false
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if walk(v.Index(i), fmt.Sprintf("%s[%d]", p, i)) {
					return true
				}
			}
			return false
		case reflect.Map:
			for _, k := range v.MapKeys() {
				entry := reflect.New(v.Type().Elem()).Elem()
				entry.Set(v.MapIndex(k))
				if walk(entry, fmt.Sprintf("%s[%s]", p, k.String())) {
					v.SetMapIndex(k, entry)
					return true
				}
			}
			return false
		}
		if p != path {
			return false
		}
		switch v.Kind() {
		case reflect.String:
			v.SetString(v.String() + "~")
		case reflect.Bool:
			v.SetBool(!v.Bool())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			v.SetInt(v.Int() + 1)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			v.SetUint(v.Uint() + 1)
		default:
			t.Fatalf("%s: unsupported kind %s", path, v.Kind())
		}
		return true
	}
	if !walk(root, "") {
		t.Fatalf("path %s not found", path)
	}
}

// mutationTest builds a fully populated value, then mutates each scalar in turn
// on a fresh copy and requires the digest to change. It returns the field count.
func mutationTest[T any](t *testing.T, base func() T, digest func(T) (string, error)) int {
	t.Helper()
	probe := base()
	fill(reflect.ValueOf(&probe).Elem())
	var paths []string
	scalarPaths(reflect.ValueOf(&probe).Elem(), "", &paths)
	if len(paths) == 0 {
		t.Fatal("no fields found")
	}
	want, err := digest(probe)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		v := base()
		fill(reflect.ValueOf(&v).Elem())
		mutateAt(t, reflect.ValueOf(&v).Elem(), p)
		got, err := digest(v)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if got == want {
			t.Errorf("digest unchanged when %s is mutated", p)
		}
	}
	t.Logf("mutated %d scalar fields", len(paths))
	return len(paths)
}

// Every exported usdledger.Snapshot field, MinPerUnitWorstCaseMicroUSD included,
// is bound by the ledger digest. Adding a field to Snapshot adds a case here.
func TestLedgerDigestBindsEverySnapshotField(t *testing.T) {
	n := mutationTest(t, okLedger, digestLedger)
	if want := reflect.TypeOf(usdledger.Snapshot{}).NumField(); n != want {
		t.Errorf("mutated %d fields, Snapshot has %d", n, want)
	}
}

func TestTrialsDigestBindsEveryRecordField(t *testing.T) {
	// Registered is derived from the manifest and tagged json:"-"; it is not part of the set.
	mutationTest(t, func() evalscore.Input { return evalscore.Input{Schema: evalscore.SchemaVersion} }, digestTrials)
}

func TestManifestDigestBindsEveryField(t *testing.T) {
	mutationTest(t, func() evalcorpus.Manifest { return evalcorpus.Manifest{} }, evalcorpus.Digest)
}

// The same through Build: the report digest and the ledger binding change with
// any snapshot, manifest, or record change that Build still accepts.
func TestReportDigestChangesWithEveryInputClass(t *testing.T) {
	base := mustBuild(t, smallInputs())
	cases := map[string]func(in *Inputs){
		"manifest seed": func(in *Inputs) { in.Manifest.SplitSeed = "other-seed" },
		"manifest partition": func(in *Inputs) {
			a := *in.Manifest.Cases[0].Adversarial
			a.Partition = evalcorpus.PartitionNonCold
			in.Manifest.Cases[0].Adversarial = &a
		},
		"manifest ambiguous-only change": func(in *Inputs) { in.Manifest.Cases[5].CaseID = "M-2" },
		"record field":                   func(in *Inputs) { in.Trials.Adversarial[0].Channel = "residual_syscall" },
		"record bool":                    func(in *Inputs) { in.Trials.Compliant[0].VSuccess = false },
		"evq":                            func(in *Inputs) { in.Trials.EVQPassed = false },
		"record removed":                 func(in *Inputs) { in.Trials.Compliant = in.Trials.Compliant[:1] },
		"ledger head":                    func(in *Inputs) { in.Ledger.HeadSHA256 = strings.Repeat("cd", 32) },
		"ledger id":                      func(in *Inputs) { in.Ledger.LedgerID = "other" },
		"ledger records":                 func(in *Inputs) { in.Ledger.Records++ },
		"ledger threshold":               func(in *Inputs) { in.Ledger.MinPerUnitWorstCaseMicroUSD++ },
		"ledger overspent":               func(in *Inputs) { in.Ledger.Overspent = true },
		"ledger units":                   func(in *Inputs) { in.Ledger.SettledUnits++ },
		"ledger spend":                   func(in *Inputs) { withMoney(&in.Ledger, 0, 0, in.Ledger.SettledMicroUSD+1) },
	}
	seen := map[string]string{base.Digest: "base"}
	for name, mutate := range cases {
		in := smallInputs()
		mutate(&in)
		r := mustBuild(t, in)
		if prev, dup := seen[r.Digest]; dup {
			t.Errorf("%s: digest equals %s", name, prev)
		}
		seen[r.Digest] = name
	}
}

func TestBindingsCarryInputIdentities(t *testing.T) {
	in := smallInputs()
	r := mustBuild(t, in)
	md, err := evalcorpus.Digest(in.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if r.Bindings.ManifestDigest != md || r.Bindings.LedgerHead != synthHead ||
		r.Bindings.LedgerID != "research-synthetic" || r.Bindings.LedgerRecords != 401 {
		t.Errorf("bindings %+v", r.Bindings)
	}
	if !strings.HasPrefix(r.Bindings.TrialsDigest, "sha256:") || len(r.Bindings.TrialsDigest) != 71 {
		t.Errorf("trials digest %q", r.Bindings.TrialsDigest)
	}
	ld, err := digestLedger(in.Ledger)
	if err != nil || r.Bindings.LedgerSnapshotDigest != ld || !strings.HasPrefix(ld, "sha256:") || len(ld) != 71 {
		t.Errorf("ledger snapshot digest %q, want %q (%v)", r.Bindings.LedgerSnapshotDigest, ld, err)
	}
	if r.Bindings.LedgerSnapshotDigest == r.Bindings.TrialsDigest {
		t.Error("ledger and trials digests must be domain separated")
	}
}

func TestCanonicalJSONAndDigest(t *testing.T) {
	r := mustBuild(t, fullInputs())
	raw, err := r.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) || bytes.ContainsAny(raw, " \n\t") || !bytes.HasPrefix(raw, []byte(`{"bindings":{`)) {
		t.Errorf("not compact sorted JSON: %.80s", raw)
	}
	if !bytes.Contains(raw, []byte(`"digest":"`+r.Digest+`"`)) {
		t.Error("canonical JSON lacks the digest")
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if generic["flags"] == nil || generic["strata"] == nil {
		t.Error("flags or strata encode as null")
	}
	got, err := r.ComputeDigest()
	if err != nil || got != r.Digest {
		t.Errorf("ComputeDigest %q, %v; want %q", got, err, r.Digest)
	}
	forged := r
	forged.Digest = "sha256:" + strings.Repeat("0", 64)
	if d, _ := forged.ComputeDigest(); d != r.Digest {
		t.Error("digest preimage must not include the digest field")
	}
	tampered := r
	tampered.Spend.SettledMicroUSD++
	if d, _ := tampered.ComputeDigest(); d == r.Digest {
		t.Error("tampering with the report does not change its digest")
	}
	tampered = r
	tampered.H1.RuleSatisfied = !tampered.H1.RuleSatisfied
	if d, _ := tampered.ComputeDigest(); d == r.Digest {
		t.Error("tampering with rule_satisfied does not change the digest")
	}
	if DigestDomain != "tbound-eval-report/v1" || TrialsDigestDomain == DigestDomain {
		t.Error("digest domains")
	}
}

func TestMarkdown(t *testing.T) {
	r := mustBuild(t, smallInputs())
	md := r.Markdown()
	for _, want := range []string{
		"tbound-eval-report/v1", r.Digest, r.Bindings.ManifestDigest, synthHead,
		"adversarial/TM-A/cold", "compliant/bash", "rule_satisfied", "AC-Security", "AC-Friction",
		"US$1.250000", "US$5.000000", "Coverage",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown lacks %q:\n%s", want, md)
		}
	}
	for i := 0; i < 5; i++ {
		if r.Markdown() != md {
			t.Fatal("Markdown not deterministic")
		}
	}
	if strings.Contains(md, "\r") || !strings.HasSuffix(md, "\n") || strings.HasSuffix(md, "\n\n") {
		t.Error("Markdown must use LF and end with exactly one newline")
	}
	in := smallInputs()
	in.Trials.Compliant = in.Trials.Compliant[:1]
	if md2 := mustBuild(t, in).Markdown(); !strings.Contains(md2, "C-2") || md2 == md {
		t.Error("Markdown does not name the missing case")
	}
}

func TestAttributionDigestsAreBoundAndChangeTheReport(t *testing.T) {
	attributions, rubric := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	base := mustBuild(t, smallInputs())
	if base.Bindings.AttributionsDigest != "" || base.Bindings.AttributionRubricDigest != "" {
		t.Fatalf("no attribution input must bind empty digests: %+v", base.Bindings)
	}
	in := smallInputs()
	in.AttributionsDigest, in.AttributionRubricDigest = attributions, rubric
	r := mustBuild(t, in)
	if r.Bindings.AttributionsDigest != attributions || r.Bindings.AttributionRubricDigest != rubric || r.Digest == base.Digest {
		t.Fatalf("bindings %+v, digest %s (base %s)", r.Bindings, r.Digest, base.Digest)
	}
	if got := mustJSON(t, r.Bindings); !strings.Contains(got, `"attributions_digest":"`+attributions+`"`) ||
		!strings.Contains(got, `"attribution_rubric_digest":"`+rubric+`"`) {
		t.Fatalf("bindings JSON %s", got)
	}
	other := smallInputs()
	other.AttributionsDigest, other.AttributionRubricDigest = attributions, "sha256:"+strings.Repeat("c", 64)
	if mustBuild(t, other).Digest == r.Digest {
		t.Fatal("the rubric digest is not part of the report digest")
	}
	for name, pair := range map[string][2]string{
		"only the file digest": {attributions, ""}, "only the rubric digest": {"", rubric},
		"not a digest": {"abc", rubric}, "uppercase hex": {"sha256:" + strings.Repeat("A", 64), rubric},
	} {
		bad := smallInputs()
		bad.AttributionsDigest, bad.AttributionRubricDigest = pair[0], pair[1]
		if _, err := Build(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// An unresolved candidate obstruction keeps the friction clause from being
// evaluable; the same candidate with a supported attribution does not.
func TestFrictionIsEvaluableOnceTheCandidateIsAttributed(t *testing.T) {
	candidate := func(a evalscore.Attribution) Inputs {
		in := fullInputs()
		in.Trials.Compliant[0].CSuccess, in.Trials.Compliant[0].Attribution = false, a
		return in
	}
	if r := mustBuild(t, candidate(evalscore.AttributionUnresolved)); r.Clauses.Friction.Evaluable {
		t.Fatalf("friction evaluable with an unresolved candidate: %+v", r.Clauses.Friction)
	}
	if r := mustBuild(t, candidate(evalscore.AttributionSupported)); !r.Clauses.Friction.Evaluable {
		t.Fatalf("friction not evaluable with a supported candidate: %+v", r.Clauses.Friction)
	}
}
