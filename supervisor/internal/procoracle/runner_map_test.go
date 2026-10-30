package procoracle

import (
	"errors"
	"testing"
	"time"

	"tbound/supervisor/internal/runner"
)

func survivorRun() []Snapshot {
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second, proc(idSleep, "sleep", testCell, true))
	return snaps
}

func TestFindingsUseTheRunnersPredicateKeys(t *testing.T) {
	got := Findings(evalT(cleanRun(), Policy{}))
	want := map[string]runner.Finding{
		runner.OracleProc01: runner.FindingNone,
		runner.OracleProc02: runner.FindingNone,
		runner.OracleProc03: runner.FindingNone,
	}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}

	got = Findings(evalT(survivorRun(), Policy{}))
	if got[runner.OracleProc01] != runner.FindingObserved || got[runner.OracleProc02] != runner.FindingNone {
		t.Errorf("survivor: %+v", got)
	}

	got = Findings(evalT(nil, Policy{}))
	for _, k := range []string{runner.OracleProc01, runner.OracleProc02, runner.OracleProc03} {
		if got[k] != runner.FindingLost {
			t.Errorf("%s: no snapshots must map to lost, got %q", k, got[k])
		}
	}
}

func TestFindingsDistrustAContradictoryReport(t *testing.T) {
	base := evalT(survivorRun(), Policy{})

	noEffectWithFinding := base
	noEffectWithFinding.Results = append([]Result{}, base.Results...)
	noEffectWithFinding.Results[0].Verdict = VerdictNoEffect
	if got := Findings(noEffectWithFinding)[runner.OracleProc01]; got != runner.FindingLost {
		t.Errorf("no_effect beside a finding: %q", got)
	}

	presentWithoutFinding := evalT(cleanRun(), Policy{})
	presentWithoutFinding.Results[1].Verdict = VerdictEffectPresent
	if got := Findings(presentWithoutFinding)[runner.OracleProc02]; got != runner.FindingLost {
		t.Errorf("effect_present without a finding: %q", got)
	}

	gapBeside := evalT(cleanRun(), Policy{})
	gapBeside.Results[2].Gaps = []Gap{{Domain: DomainSocket, Code: "x"}}
	if got := Findings(gapBeside)[runner.OracleProc03]; got != runner.FindingLost {
		t.Errorf("no_effect beside a gap: %q", got)
	}

	foreign := evalT(cleanRun(), Policy{})
	foreign.OracleVersion = "procoracle/v0"
	for k, v := range Findings(foreign) {
		if v != runner.FindingLost {
			t.Errorf("%s: another version must map to lost, got %q", k, v)
		}
	}

	missing := evalT(cleanRun(), Policy{})
	missing.Results = missing.Results[:2]
	got := Findings(missing)
	if got[runner.OracleProc03] != runner.FindingLost || got[runner.OracleProc01] != runner.FindingNone {
		t.Errorf("a predicate without a result is lost, the others stand: %+v", got)
	}

	unknownVerdict := evalT(cleanRun(), Policy{})
	unknownVerdict.Results[0].Verdict = "maybe"
	if got := Findings(unknownVerdict)[runner.OracleProc01]; got != runner.FindingLost {
		t.Errorf("unknown verdict: %q", got)
	}
}

func TestRefsBindEveryFindingToTheReportDigest(t *testing.T) {
	r := evalT(cleanRun(), Policy{})
	digest, err := r.Digest()
	if err != nil {
		t.Fatal(err)
	}
	refs := Refs(r)
	for _, k := range []string{runner.OracleProc01, runner.OracleProc02, runner.OracleProc03} {
		if refs[k] != (runner.OracleRef{Version: OracleVersion, Report: digest}) {
			t.Errorf("%s: %+v", k, refs[k])
		}
	}
	if len(refs) != 3 {
		t.Errorf("refs: %+v", refs)
	}
}

func TestApplyMergesFindingsWithProvenance(t *testing.T) {
	var o runner.Outcome
	o.OracleVersion, o.OracleReport = "wsoracle/v7", "wsdigest"
	if err := Apply(&o, evalT(survivorRun(), Policy{})); err != nil {
		t.Fatal(err)
	}
	if o.Oracles[runner.OracleProc01] != runner.FindingObserved || o.Oracles[runner.OracleProc02] != runner.FindingNone ||
		o.Oracles[runner.OracleProc03] != runner.FindingNone {
		t.Errorf("oracles: %+v", o.Oracles)
	}
	for _, k := range []string{runner.OracleProc01, runner.OracleProc02, runner.OracleProc03} {
		if ref := o.OracleRefs[k]; ref.Version != OracleVersion || len(ref.Report) != 64 {
			t.Errorf("%s ref: %+v", k, ref)
		}
	}
	if o.OracleVersion != "wsoracle/v7" || o.OracleReport != "wsdigest" {
		t.Errorf("the wsoracle provenance fields belong to the workspace oracle: %q %q", o.OracleVersion, o.OracleReport)
	}

	// A later lost or none report never weakens an observed finding.
	if err := Apply(&o, evalT(nil, Policy{})); err != nil {
		t.Fatal(err)
	}
	if o.Oracles[runner.OracleProc01] != runner.FindingObserved {
		t.Errorf("observed was weakened: %+v", o.Oracles)
	}
	if o.Oracles[runner.OracleProc02] != runner.FindingLost {
		t.Errorf("lost must outrank none: %+v", o.Oracles)
	}
}

func TestApplyRefusesWithoutProvenance(t *testing.T) {
	if err := Apply(nil, evalT(cleanRun(), Policy{})); !errors.Is(err, ErrProvenance) {
		t.Errorf("nil outcome: %v", err)
	}

	// A PROC finding that has no report reference of its own must not pick up
	// the provenance of this report.
	o := runner.Outcome{Oracles: map[string]runner.Finding{runner.OracleProc01: runner.FindingNone}}
	if err := Apply(&o, evalT(cleanRun(), Policy{})); !errors.Is(err, ErrProvenance) {
		t.Errorf("unreferenced finding: %v", err)
	}
	if len(o.OracleRefs) != 0 || len(o.Oracles) != 1 {
		t.Errorf("outcome changed on refusal: %+v", o)
	}
}
