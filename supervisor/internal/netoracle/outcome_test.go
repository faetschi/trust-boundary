package netoracle

import (
	"errors"
	"testing"

	"tbound/supervisor/internal/runner"
)

func rep(v Verdict) Report {
	r := Report{OracleVersion: OracleVersion, Predicate: "EFF-NET-01", Scope: ScopeUnitCheck, Interval: "i", Verdict: v, JournalHead: "h", Records: 2}
	if v == VerdictEffectPresent {
		r.Arrivals = []ArrivalRef{{Seq: 2, Reason: ReasonTagged}}
	}
	return r
}

// An effect_present report without an arrival contradicts itself.
func TestEffectPresentWithoutArrivalIsLost(t *testing.T) {
	r := rep(VerdictEffectPresent)
	r.Arrivals = nil
	if got := OraclesFromNET01(r); got[runner.OracleNet01] != runner.FindingLost {
		t.Fatalf("got %v", got)
	}
}

func TestOraclesFromNET01(t *testing.T) {
	stale := rep(VerdictNoEffect)
	stale.OracleVersion = "netoracle/v0"
	gap := rep(VerdictNoEffect)
	gap.Gaps = []string{GapEndCanaryMissing}
	staleHit := rep(VerdictEffectPresent)
	staleHit.OracleVersion = "netoracle/v0"
	cases := map[string]struct {
		r    Report
		want runner.Finding
	}{
		"no_effect":            {rep(VerdictNoEffect), runner.FindingNone},
		"effect_present":       {rep(VerdictEffectPresent), runner.FindingObserved},
		"evidence_lost":        {rep(VerdictEvidenceLost), runner.FindingLost},
		"unknown verdict":      {rep("maybe"), runner.FindingLost},
		"empty verdict":        {rep(""), runner.FindingLost},
		"no_effect with a gap": {gap, runner.FindingLost},
		"stale version":        {stale, runner.FindingLost},
		"stale effect":         {staleHit, runner.FindingLost},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := OraclesFromNET01(c.r)
			if len(got) != 1 || got[runner.OracleNet01] != c.want {
				t.Fatalf("got %v, want %s under %s", got, c.want, runner.OracleNet01)
			}
		})
	}
}

func TestOraclesFromNET02(t *testing.T) {
	mk := func(v Verdict) NET02Report {
		return NET02Report{OracleVersion: OracleVersion, Predicate: "EFF-NET-02", Interval: "i", Verdict: v}
	}
	if got := OraclesFromNET02(mk(VerdictNotRegistered)); len(got) != 0 {
		t.Fatalf("not_registered must supply no finding, got %v", got)
	}
	for v, want := range map[Verdict]runner.Finding{
		VerdictNoEffect: runner.FindingNone, VerdictEffectPresent: runner.FindingObserved, VerdictEvidenceLost: runner.FindingLost, "x": runner.FindingLost,
	} {
		if got := OraclesFromNET02(mk(v)); got[runner.OracleNet02] != want || len(got) != 1 {
			t.Fatalf("%s: got %v want %s", v, got, want)
		}
	}
}

func TestRecordNET01BindsProvenance(t *testing.T) {
	var o runner.Outcome
	r := rep(VerdictNoEffect)
	if err := RecordNET01(&o, r); err != nil {
		t.Fatal(err)
	}
	d, err := r.Digest()
	if err != nil || d == "" {
		t.Fatalf("digest %q %v", d, err)
	}
	ref := o.OracleRefs[runner.OracleNet01]
	if o.Oracles[runner.OracleNet01] != runner.FindingNone || ref.Version != OracleVersion || ref.Report != d {
		t.Fatalf("outcome = %+v", o)
	}
	// The outcome-level fields carry the wsoracle version (runner.oracleVersionOK);
	// the NET recorder must not overwrite them.
	if o.OracleVersion != "" || o.OracleReport != "" {
		t.Fatalf("outcome-level provenance touched: %+v", o)
	}
	// An observed effect always wins; the second report folds into the reference.
	hit := rep(VerdictEffectPresent)
	hit.Records = 9
	if err := RecordNET01(&o, hit); err != nil {
		t.Fatal(err)
	}
	if o.Oracles[runner.OracleNet01] != runner.FindingObserved || o.OracleRefs[runner.OracleNet01].Report == d {
		t.Fatalf("merged = %+v", o)
	}
	if err := RecordNET01(&o, rep(VerdictNoEffect)); err != nil || o.Oracles[runner.OracleNet01] != runner.FindingObserved {
		t.Fatalf("an observed effect must stand: %v %+v", err, o)
	}
}

func TestRecordNET01WeakerEvidenceMerges(t *testing.T) {
	var o runner.Outcome
	_ = RecordNET01(&o, rep(VerdictNoEffect))
	_ = RecordNET01(&o, rep(VerdictEvidenceLost))
	if o.Oracles[runner.OracleNet01] != runner.FindingLost {
		t.Fatalf("lost must weaken none: %+v", o)
	}
}

func TestRecordKeepsExistingWSProvenance(t *testing.T) {
	o := runner.Outcome{
		Oracles:       map[string]runner.Finding{runner.OracleWS01: runner.FindingNone},
		OracleVersion: "wsoracle/vX", OracleReport: "abc",
		OracleRefs: map[string]runner.OracleRef{runner.OracleWS01: {Version: "wsoracle/vX", Report: "abc"}},
	}
	if err := RecordNET01(&o, rep(VerdictNoEffect)); err != nil {
		t.Fatal(err)
	}
	if o.OracleVersion != "wsoracle/vX" || o.OracleReport != "abc" || o.OracleRefs[runner.OracleWS01].Report != "abc" {
		t.Fatalf("WS provenance changed: %+v", o)
	}
}

func TestRecordRefusesBadReports(t *testing.T) {
	var o runner.Outcome
	bad := rep(VerdictNoEffect)
	bad.Predicate = "EFF-WS-01"
	if err := RecordNET01(&o, bad); !errors.Is(err, runner.ErrOracleProvenance) {
		t.Fatalf("wrong predicate: %v", err)
	}
	// A NET finding that has no report reference of its own is never extended.
	o = runner.Outcome{Oracles: map[string]runner.Finding{runner.OracleNet01: runner.FindingNone}}
	if err := RecordNET01(&o, rep(VerdictNoEffect)); !errors.Is(err, runner.ErrOracleProvenance) {
		t.Fatalf("a NET finding with no reference must be refused: %v", err)
	}
	if o.OracleRefs != nil {
		t.Fatalf("outcome changed on refusal: %+v", o)
	}
}

func TestRecordNET02(t *testing.T) {
	var o runner.Outcome
	if err := RecordNET02(&o, EvaluateNET02(NET02Input{Interval: "i"})); err != nil || len(o.Oracles) != 0 {
		t.Fatalf("not_registered must record nothing: %v %+v", err, o)
	}
	r := EvaluateNET02(NET02Input{Interval: "i", Registered: true, Capture: fakeCapture{v: VerdictNoEffect}})
	if err := RecordNET02(&o, r); err != nil {
		t.Fatal(err)
	}
	d, _ := r.Digest()
	if o.Oracles[runner.OracleNet02] != runner.FindingNone || o.OracleRefs[runner.OracleNet02] != (runner.OracleRef{Version: OracleVersion, Report: d}) {
		t.Fatalf("outcome = %+v", o)
	}
}
