package netoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"tbound/supervisor/internal/runner"
)

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

// OraclesFromNET02 maps a NET-02 report. not_registered supplies no finding: the
// route is a diagnostic, and the runner requires the key only when the plan
// declares NET02Mandatory.
func OraclesFromNET02(r NET02Report) map[string]runner.Finding {
	if r.Verdict == VerdictNotRegistered && r.OracleVersion == OracleVersion && r.Predicate == PredicateNET02 {
		return map[string]runner.Finding{}
	}
	f := runner.FindingLost
	if r.OracleVersion == OracleVersion && r.Predicate == PredicateNET02 {
		switch r.Verdict {
		case VerdictEffectPresent:
			f = runner.FindingObserved
		case VerdictNoEffect:
			if len(r.Gaps) == 0 {
				f = runner.FindingNone
			}
		}
	}
	return map[string]runner.Finding{runner.OracleNet02: f}
}

// RecordNET01 stores the report's finding in o and binds it to this report in
// o.OracleRefs (version and report digest), as RecordE04 does for WS findings.
// It does not touch o.OracleVersion or o.OracleReport: the runner checks those
// against the wsoracle version, so they stay with the WS findings.
func RecordNET01(o *runner.Outcome, r Report) error {
	d, _ := r.Digest()
	return record(o, OraclesFromNET01(r), PredicateNET01, r.Predicate, r.OracleVersion, d)
}

// RecordNET02 is RecordNET01 for EFF-NET-02. A not_registered report records
// nothing.
func RecordNET02(o *runner.Outcome, r NET02Report) error {
	d, _ := r.Digest()
	return record(o, OraclesFromNET02(r), PredicateNET02, r.Predicate, r.OracleVersion, d)
}

func record(o *runner.Outcome, found map[string]runner.Finding, want, got, version, report string) error {
	if got != want {
		return fmt.Errorf("%w: report is for %q, not %s", runner.ErrOracleProvenance, got, want)
	}
	if len(found) == 0 {
		return nil
	}
	if report == "" {
		return fmt.Errorf("%w: the report has no digest", runner.ErrOracleProvenance)
	}
	for k := range found {
		if o.Oracles[k] != "" && o.OracleRefs[k].Report == "" {
			return fmt.Errorf("%w: %s holds a finding with no report reference", runner.ErrOracleProvenance, k)
		}
	}
	if o.Oracles == nil {
		o.Oracles = map[string]runner.Finding{}
	}
	if o.OracleRefs == nil {
		o.OracleRefs = map[string]runner.OracleRef{}
	}
	for k, f := range found {
		o.Oracles[k] = merge(o.Oracles[k], f)
		ref := o.OracleRefs[k]
		if ref.Report == "" {
			ref = runner.OracleRef{Version: version, Report: report}
		} else {
			if ref.Version != version {
				ref.Version = "mixed"
			}
			sum := sha256.Sum256([]byte(ref.Report + "\n" + report))
			ref.Report = hex.EncodeToString(sum[:])
		}
		o.OracleRefs[k] = ref
	}
	return nil
}

// merge is the runner's rule: an observed effect always stands, otherwise the
// weaker evidence (none, indeterminate, lost) wins.
func merge(old, new runner.Finding) runner.Finding {
	rank := func(f runner.Finding) int {
		switch f {
		case runner.FindingNone:
			return 1
		case runner.FindingIndeterminate:
			return 2
		case runner.FindingLost:
			return 3
		}
		return 0
	}
	switch {
	case old == runner.FindingObserved || new == runner.FindingObserved:
		return runner.FindingObserved
	case old == "":
		return new
	case rank(new) > rank(old):
		return new
	}
	return old
}
