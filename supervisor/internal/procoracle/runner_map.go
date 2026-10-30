package procoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"tbound/supervisor/internal/runner"
)

// ErrProvenance means a report cannot be bound to an outcome: it has no
// digest or another oracle version, or the outcome already holds an EFF-PROC
// finding with no report reference of its own.
var ErrProvenance = errors.New("procoracle: report cannot be bound to the outcome")

// keys maps each registered predicate to the runner's Outcome.Oracles key.
var keys = []struct{ predicate, key string }{
	{PredicateProc01, runner.OracleProc01},
	{PredicateProc02, runner.OracleProc02},
	{PredicateProc03, runner.OracleProc03},
}

// Findings maps a report to the runner's predicate-keyed findings, one per
// EFF-PROC predicate: no_effect is FindingNone, effect_present FindingObserved
// and evidence_lost FindingLost. Anything the report contradicts is FindingLost:
// another oracle version, a predicate with no result or with several, an unknown
// verdict, no_effect beside a finding or a gap, effect_present without a finding.
func Findings(r Report) map[string]runner.Finding {
	out := map[string]runner.Finding{}
	for _, k := range keys {
		out[k.key] = findingOf(r, k.predicate)
	}
	return out
}

func findingOf(r Report, predicate string) runner.Finding {
	if r.OracleVersion != OracleVersion {
		return runner.FindingLost
	}
	var res *Result
	for i := range r.Results {
		if r.Results[i].Predicate != predicate {
			continue
		}
		if res != nil {
			return runner.FindingLost
		}
		res = &r.Results[i]
	}
	if res == nil {
		return runner.FindingLost
	}
	switch res.Verdict {
	case VerdictNoEffect:
		if len(res.Findings) == 0 && len(res.Gaps) == 0 {
			return runner.FindingNone
		}
	case VerdictEffectPresent:
		if len(res.Findings) > 0 {
			return runner.FindingObserved
		}
	}
	return runner.FindingLost
}

// Refs binds each finding Findings supplies to the report: the oracle version
// and the report digest. It is empty if the report cannot be digested.
func Refs(r Report) map[string]runner.OracleRef {
	out := map[string]runner.OracleRef{}
	digest, err := r.Digest()
	if err != nil || digest == "" {
		return out
	}
	for _, k := range keys {
		out[k.key] = runner.OracleRef{Version: r.OracleVersion, Report: digest}
	}
	return out
}

// Apply merges the report's findings into o with their provenance (OracleRefs).
// An observed effect always stands; otherwise the weaker evidence wins. It does
// not touch Outcome.OracleVersion or Outcome.OracleReport, which belong to the
// workspace oracle. It returns ErrProvenance, and changes nothing, if the report
// is not of this version or has no digest, or if o already holds an EFF-PROC
// finding without a report reference.
func Apply(o *runner.Outcome, r Report) error {
	if o == nil || r.OracleVersion != OracleVersion {
		return ErrProvenance
	}
	refs := Refs(r)
	if len(refs) != len(keys) {
		return ErrProvenance
	}
	for _, k := range keys {
		if o.Oracles[k.key] != "" && o.OracleRefs[k.key].Report == "" {
			return ErrProvenance
		}
	}
	if o.Oracles == nil {
		o.Oracles = map[string]runner.Finding{}
	}
	if o.OracleRefs == nil {
		o.OracleRefs = map[string]runner.OracleRef{}
	}
	for key, f := range Findings(r) {
		o.Oracles[key] = merge(o.Oracles[key], f)
		ref, next := o.OracleRefs[key], refs[key]
		if ref.Report == "" {
			ref = next
		} else {
			if ref.Version != next.Version {
				ref.Version = "mixed"
			}
			sum := sha256.Sum256([]byte(ref.Report + "\n" + next.Report))
			ref.Report = hex.EncodeToString(sum[:])
		}
		o.OracleRefs[key] = ref
	}
	return nil
}

// merge keeps an observed effect, otherwise the weaker evidence (lost over
// none), as the runner does.
func merge(old, next runner.Finding) runner.Finding {
	switch {
	case old == runner.FindingObserved || next == runner.FindingObserved:
		return runner.FindingObserved
	case old == "" || (old == runner.FindingNone && next != runner.FindingNone):
		return next
	}
	return old
}
