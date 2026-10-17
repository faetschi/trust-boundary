package evalreport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
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
	if r.Coverage.Adversarial.Missing == nil || r.Coverage.Adversarial.Extra == nil || r.Coverage.Adversarial.Duplicate == nil {
		t.Error("empty lists must be non-nil so JSON has [] not null")
	}
}

func TestDuplicateCaseIsReportedNotDropped(t *testing.T) {
	cases := map[string]func(in *Inputs){
		"same attempt twice": func(in *Inputs) {
			in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("A-2"))
		},
		"second attempt after a non-rerun result": func(in *Inputs) {
			second := cleanAdv("A-2")
			second.Attempt = 2
			in.Trials.Adversarial = append(in.Trials.Adversarial, second)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := smallInputs()
			mutate(&in)
			r := mustBuild(t, in)
			if r.Coverage.Exact || !slices.Equal(r.Coverage.Adversarial.Duplicate, []string{"A-2"}) {
				t.Fatalf("coverage %+v", r.Coverage)
			}
			if r.Coverage.WithheldRecords != 2 {
				t.Errorf("withheld %d, want 2", r.Coverage.WithheldRecords)
			}
			if len(r.Coverage.Adversarial.Missing) != 0 {
				t.Errorf("duplicate also reported missing: %v", r.Coverage.Adversarial.Missing)
			}
			if s := stratum(t, r, "adversarial/TM-A/non_cold"); s.Duplicate != 1 || s.Scored != 0 {
				t.Errorf("stratum %+v", s)
			}
			if r.H1.RuleSatisfied || !hasFlag(r, "coverage_not_exact") {
				t.Errorf("h1 %+v flags %v", r.H1, r.Flags)
			}
		})
	}
}

func TestEvidencedRerunIsOneScoredTrial(t *testing.T) {
	in := smallInputs()
	first := cleanAdv("A-1")
	first.EvidenceLoss = evalscore.LossExternalOutage
	first.OutageEvidenceDigest = "sha256:" + strings.Repeat("0", 64)
	second := cleanAdv("A-1")
	second.Attempt = 2
	in.Trials.Adversarial[0] = first
	in.Trials.Adversarial = append(in.Trials.Adversarial, second)
	r := mustBuild(t, in)
	if !r.Coverage.Exact || r.Observed.SupersededAttempts != 1 || r.Coverage.WithheldRecords != 0 {
		t.Errorf("coverage %+v observed %+v", r.Coverage, r.Observed)
	}
	if s := stratum(t, r, "adversarial/TM-A/cold"); s.Scored != 1 || s.Dispositions.ValidPass != 1 {
		t.Errorf("stratum %+v", s)
	}
}

func TestExtraCase(t *testing.T) {
	in := smallInputs()
	in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("ghost"))
	in.Trials.Compliant = append(in.Trials.Compliant, cleanCmp("M-1")) // an ambiguous case has no trial record kind
	r := mustBuild(t, in)
	if r.Coverage.Exact {
		t.Error("extra records must break exact coverage")
	}
	if !slices.Equal(r.Coverage.Adversarial.Extra, []string{"ghost"}) || !slices.Equal(r.Coverage.Compliant.Extra, []string{"M-1"}) {
		t.Errorf("coverage %+v", r.Coverage)
	}
	if s := stratum(t, r, "adversarial/unregistered"); s.Scored != 1 || s.Registered != 0 {
		t.Errorf("unregistered %+v", s)
	}
	if r.Coverage.Adversarial.Scored != 3 || r.Coverage.Compliant.Scored != 2 {
		t.Errorf("registered scored counts must exclude extras: %+v", r.Coverage)
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

func TestSpendFlags(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *usdledger.Snapshot)
		flag   string
		within bool
	}{
		{"over cap", func(s *usdledger.Snapshot) {
			s.SettledMicroUSD, s.CommittedMicroUSD, s.RemainingMicroUSD = 5_000_001, 5_000_001, -1
		}, "spend_over_cap", false},
		{"uncertain counts against cap", func(s *usdledger.Snapshot) {
			s.UncertainMicroUSD, s.CommittedMicroUSD, s.RemainingMicroUSD = 4_000_000, 5_250_000, -250_000
		}, "spend_over_cap", false},
		{"cap differs from registered", func(s *usdledger.Snapshot) {
			s.CapMicroUSD, s.RemainingMicroUSD = 10_000_000, 10_000_000-s.CommittedMicroUSD
		}, "spend_cap_mismatch", false},
		{"overspent", func(s *usdledger.Snapshot) { s.Overspent = true }, "ledger_overspent", false},
		{"stop rule", func(s *usdledger.Snapshot) { s.StopRule = true }, "ledger_stop_rule", true},
		{"units over planned", func(s *usdledger.Snapshot) { s.SettledUnits = PlannedUnits + 1 }, "units_over_planned", true},
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
	for name, mutate := range map[string]func(s *usdledger.Snapshot){
		"committed": func(s *usdledger.Snapshot) { s.CommittedMicroUSD++ },
		"remaining": func(s *usdledger.Snapshot) { s.RemainingMicroUSD++ },
	} {
		in := smallInputs()
		mutate(&in.Ledger)
		if _, err := Build(in); err == nil {
			t.Errorf("inconsistent snapshot (%s) accepted", name)
		}
	}
	in := smallInputs()
	in.Manifest.Cases = append(in.Manifest.Cases, evalcorpus.Case{CaseID: "Z-1", Kind: "mystery"})
	if _, err := Build(in); err == nil {
		t.Error("unknown case kind accepted")
	}
}

func TestBuildDoesNotMutateInputs(t *testing.T) {
	in := smallInputs()
	in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("A-2")) // forces withholding
	want := smallInputs()
	want.Trials.Adversarial = append(want.Trials.Adversarial, cleanAdv("A-2"))
	mustBuild(t, in)
	if !reflect.DeepEqual(in, want) {
		t.Error("Build modified its inputs")
	}
	if in.Trials.Registered != nil {
		t.Error("Build set Trials.Registered on the caller's value")
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

func TestDigestChangesWithEveryInput(t *testing.T) {
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
		"record added":                   func(in *Inputs) { in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("ghost")) },
		"record removed":                 func(in *Inputs) { in.Trials.Compliant = in.Trials.Compliant[:1] },
		"ledger head": func(in *Inputs) {
			in.Ledger.HeadSHA256 = strings.Repeat("cd", 32)
		},
		"ledger id":      func(in *Inputs) { in.Ledger.LedgerID = "other" },
		"ledger records": func(in *Inputs) { in.Ledger.Records++ },
		"ledger spend": func(in *Inputs) {
			in.Ledger.SettledMicroUSD++
			in.Ledger.CommittedMicroUSD++
			in.Ledger.RemainingMicroUSD--
		},
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
	// The trials digest covers the raw record set, including records that scoring withholds.
	in2 := smallInputs()
	in2.Trials.Adversarial = append(in2.Trials.Adversarial, cleanAdv("A-2"))
	if mustBuild(t, in2).Bindings.TrialsDigest == r.Bindings.TrialsDigest {
		t.Error("trials digest ignores a withheld duplicate record")
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
