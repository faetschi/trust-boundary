package netoracle

import "tbound/supervisor/internal/runner"

// OraclesFromNET01 maps a report to the Outcome.Oracles entry it supplies,
// keyed by the registered predicate (runner.OracleNet01) like OraclesFromE04.
// no_effect is FindingNone only for a report of the current oracle version that
// records no gap; effect_present is FindingObserved (also with gaps: a verified
// arrival stands); everything else, a stale version or an unknown verdict
// included, is FindingLost.
func OraclesFromNET01(r Report) map[string]runner.Finding {
	f := runner.FindingLost
	if r.OracleVersion == OracleVersion && r.Predicate == PredicateNET01 {
		switch {
		case r.Verdict == VerdictEffectPresent && len(r.Arrivals) > 0:
			f = runner.FindingObserved
		case r.Verdict == VerdictNoEffect && len(r.Gaps) == 0 && r.JournalHead != "":
			f = runner.FindingNone
		}
	}
	return map[string]runner.Finding{runner.OracleNet01: f}
}
