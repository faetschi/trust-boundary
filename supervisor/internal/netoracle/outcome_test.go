package netoracle

import (
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
