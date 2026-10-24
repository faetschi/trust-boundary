package corpusadmit

import (
	"fmt"
	"slices"
	"strings"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/wsoracle"
)

// checkEffect exercises the existing pure predicate over a clean and a changed
// fixture snapshot. These synthetic snapshots prove satisfiability, not a P run.
func checkEffect(c evalcorpus.Case, fixture trialfix.Spec) ConstructCheck {
	r := ConstructCheck{Name: "effect-satisfiable", Status: "not-checked", Reason: "not an adversarial effect case"}
	if c.Adversarial == nil {
		return r
	}
	r.Status, r.Reason = "guest-only", "network/process/unsupported effect predicate requires guest evidence; no trial script executed locally"
	if c.Adversarial.OracleID != "wsoracle" {
		return r
	}
	if len(c.Adversarial.ForbiddenEffects) == 0 {
		r.Status, r.Reason = "fail", "no effect predicate supplied"
		return r
	}
	if c.Trial == nil || len(c.Trial.ProtectedPaths) == 0 {
		r.Status, r.Reason = "fail", "workspace predicate has no protected path"
		return r
	}
	base, err := fixture.Manifest(trialfix.Owner{UID: 1000, GID: 1000})
	if err != nil {
		r.Status, r.Reason = "fail", err.Error()
		return r
	}
	base.Label, base.Dev, base.Root.Ino = wsoracle.PointBaseline, 1, 1
	for i := range base.Entries {
		base.Entries[i].Ino = uint64(i + 2)
	}
	eval := func(m wsoracle.Manifest) wsoracle.E04Report {
		var points []wsoracle.E04Point
		for _, name := range []string{wsoracle.PointPostMaterialization, wsoracle.PointPostSettlement, wsoracle.PointPostTeardown, wsoracle.PointPrePublication, wsoracle.PointPostPublication, wsoracle.PointFinalCleanup} {
			copy := m
			copy.Label = name
			points = append(points, wsoracle.E04Point{Name: name, Manifest: copy, LedgerPrefixDigest: wsoracle.LedgerPrefixDigest(nil, 0)})
		}
		return wsoracle.EvaluateE04(wsoracle.EvaluateE04Input{ThreatClass: string(c.Adversarial.ThreatClass), Baseline: base, Points: points, Scope: wsoracle.E04Scope{Paths: c.Trial.ProtectedPaths}, NoCreditPaths: c.Trial.ProtectedPaths})
	}
	clean := eval(base)
	for _, predicate := range c.Adversarial.ForbiddenEffects {
		if !slices.Contains([]string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS03, wsoracle.PredicateWS04}, predicate) {
			return r
		}
		if clean.VerdictOf(predicate) != wsoracle.VerdictNoEffect {
			r.Status, r.Reason = "fail", fmt.Sprintf("clean fixture is not no_effect for %s: %s (gaps %v)", predicate, clean.VerdictOf(predicate), clean.Gaps)
			return r
		}
		changed := base
		changed.Entries = slices.Clone(base.Entries)
		index := slices.IndexFunc(changed.Entries, func(e wsoracle.Entry) bool { return e.Path == c.Trial.ProtectedPaths[0] })
		if index < 0 {
			r.Status, r.Reason = "fail", "protected target is absent from fixture"
			return r
		}
		switch predicate {
		case wsoracle.PredicateWS01:
			if changed.Entries[index].Type != wsoracle.TypeFile {
				r.Status, r.Reason = "guest-only", "content predicate target is not a regular file"
				return r
			}
			changed.Entries[index].SHA256 = strings.Repeat("0", 64)
			if changed.Entries[index].SHA256 == base.Entries[index].SHA256 {
				changed.Entries[index].SHA256 = strings.Repeat("1", 64)
			}
		case wsoracle.PredicateWS02:
			changed.Entries = slices.DeleteFunc(changed.Entries, func(e wsoracle.Entry) bool {
				p := c.Trial.ProtectedPaths[0]
				return e.Path == p || strings.HasPrefix(e.Path, p+"/")
			})
		case wsoracle.PredicateWS03:
			r.Status, r.Reason = "guest-only", "rename effect needs a case-specific destination and scope witness"
			return r
		case wsoracle.PredicateWS04:
			changed.Entries[index].Mode ^= 0o100
		}
		if got := eval(changed); got.VerdictOf(predicate) != wsoracle.VerdictEffectPresent {
			r.Status, r.Reason = "fail", fmt.Sprintf("mutation does not satisfy %s: %s (gaps %v)", predicate, got.VerdictOf(predicate), got.Gaps)
			return r
		}
	}
	r.Status, r.Reason = "pass", "existing workspace oracle returns no_effect for clean fixture and effect_present for a deterministic protected-path mutation; not guest calibration"
	return r
}
