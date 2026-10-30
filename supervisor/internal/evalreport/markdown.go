package evalreport

import (
	"fmt"
	"strings"

	"tbound/supervisor/internal/evalscore"
)

// Markdown renders a short deterministic summary of r. It contains no
// timestamps and uses LF line endings. The JSON form is the record of truth.
func (r Report) Markdown() string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("# TBound evaluation report")
	w("")
	w("- schema: %s", r.Schema)
	w("- digest: %s", r.Digest)
	w("- manifest digest: %s", r.Bindings.ManifestDigest)
	w("- trials digest: %s", r.Bindings.TrialsDigest)
	w("- ledger: %s, head %s, %d records", mdText(r.Bindings.LedgerID), r.Bindings.LedgerHead, r.Bindings.LedgerRecords)
	w("- ledger snapshot digest: %s", r.Bindings.LedgerSnapshotDigest)
	w("")

	w("## Coverage")
	w("")
	w("Exact: %t. Ambiguous cases not scored by evalscore: %d.", r.Coverage.Exact, r.Coverage.UnscoredAmbiguous)
	w("")
	w("| Kind | Registered | Scored | Missing | Manifest duplicate |")
	w("|---|---|---|---|---|")
	for _, k := range []struct {
		name string
		c    KindCoverage
	}{{kindAdversarial, r.Coverage.Adversarial}, {kindCompliant, r.Coverage.Compliant}} {
		w("| %s | %d | %d | %d | %d |", k.name, k.c.Registered, k.c.Scored,
			len(k.c.Missing), len(k.c.ManifestDuplicate))
	}
	for _, k := range []struct {
		name string
		c    KindCoverage
	}{{kindAdversarial, r.Coverage.Adversarial}, {kindCompliant, r.Coverage.Compliant}} {
		for _, l := range []struct {
			what string
			ids  []string
		}{{"missing", k.c.Missing}, {"manifest duplicate", k.c.ManifestDuplicate}} {
			if len(l.ids) > 0 {
				w("")
				w("%s %s: %s", k.name, l.what, mdList(l.ids))
			}
		}
	}
	w("")

	w("## Strata")
	w("")
	w("| Stratum | Registered | Scored | Missing | Invalid | VALID_PASS | VALID_SECURITY_FAILURE | VALID_FRICTION_OBSTRUCTION | CONSERVATIVE_FAILURE | EXTERNAL_INFRASTRUCTURE_RERUN | UNSUPPORTED | OPEN |")
	w("|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range r.Strata {
		d := s.Dispositions
		w("| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |", mdText(s.Key), s.Registered, s.Scored,
			s.Missing, s.InvalidRecords, d.ValidPass, d.ValidSecurityFailure, d.ValidFrictionObstruction,
			d.ConservativeFailure, d.ExternalInfrastructureRerun, d.Unsupported, d.Open)
	}
	w("")

	c := r.Configurations
	w("## Configurations")
	w("")
	w("- P (calibration pairs): %d; static manifested %d; adaptive manifested %d; both %d; non-discriminative %d",
		r.Calibration.Pairs, r.Calibration.StaticManifested, r.Calibration.AdaptiveManifested,
		r.Calibration.BothManifested, r.Calibration.NonDiscriminative)
	w("- C (adversarial): trajectories %d; escapes %d; tool attempts %d; residual attempts %d; union %d",
		c.C.Trajectories, c.C.Escapes, c.C.Attempts.Tool, c.C.Attempts.Residual, c.C.Attempts.Union)
	w("- C (compliant): tasks %d; success %d; failure %d; blocked %d; unresolved %d",
		c.C.Tasks, c.C.Success, c.C.Failure, c.C.Blocked, c.C.Unresolved)
	w("- B: tasks %d; success %d; failure %d", c.B.Tasks, c.B.Success, c.B.Failure)
	w("- V: tasks %d; success %d; failure %d", c.V.Tasks, c.V.Success, c.V.Failure)
	w("- records: %d observed adversarial, %d observed compliant, %d superseded attempts, %d pending external reruns, %d invalid",
		r.Observed.Adversarial, r.Observed.Compliant, r.Observed.SupersededAttempts,
		r.Observed.PendingExternalReruns, r.Observed.InvalidRecords)
	for _, ir := range r.InvalidRecords {
		w("  - invalid %s %s attempt %d: %s", ir.Kind, mdText(ir.ID), ir.Attempt, mdText(ir.Reason))
	}
	w("")

	w("## Acceptance (as computed by evalscore)")
	w("")
	w("- rule_satisfied: %t", r.H1.RuleSatisfied)
	w("- AC-Security: evaluable %t, passed %t, reason %q", r.Clauses.Security.Evaluable, r.Clauses.Security.Passed, r.Clauses.Security.Reason)
	w("- AC-Friction: evaluable %t, passed %t, reason %q; ORR within limit %t; B floor %t; C floor %t",
		r.Clauses.Friction.Evaluable, r.Clauses.Friction.Passed, r.Clauses.Friction.Reason,
		r.Clauses.Friction.ORRWithinLimit, r.Clauses.Friction.BFloorMet, r.Clauses.Friction.CFloorMet)
	w("- PVR: %s; one-sided 95%% upper %s; within limit %t", rateText(r.Metrics.PVR), f4(r.Metrics.PVR.OneSided95Upper), r.H1.PVRUpperWithinLimit)
	w("- ORR: %s; 95%% interval %s to %s", rateText(r.Metrics.ORR), f4(r.Metrics.ORR.CILow), f4(r.Metrics.ORR.CIHigh))
	w("- observed security failure: %t; friction failure: %t", r.H1.ObservedSecurityFailure, r.H1.FrictionFailure)
	w("- blockers: %s", mdList(r.H1.Blockers))
	w("")

	pd := r.PostDenialContinuation
	w("## Post-denial continuation (descriptive, not H1)")
	w("")
	w("- C (compliant): alternative_allowed %d; retry_denied %d; no_further_action %d",
		pd.C.AlternativeAllowed, pd.C.RetryDenied, pd.C.NoFurtherAction)
	w("- C task success among trials with at least one denial: %d/%d", pd.C.SuccessfulTrialsWithDenial, pd.C.TrialsWithDenial)
	w("- B: %s; V: %s; P: %s", pd.B, pd.V, pd.P)
	w("")

	for _, c := range r.CDH {
		w("## %s (descriptive only, outside H1)", mdText(c.Configuration))
		w("")
		w("- Manifest: %s", c.ManifestDigest)
		w("- Paired success: both success %d; both failure %d; C only %d; C-DH only %d; unresolved %d of %d", c.BothSuccess, c.BothFailure, c.COnlySuccess, c.CDHOnlySuccess, c.UnresolvedPairs, c.RegisteredCompliant)
		w("- Success difference (C-DH minus C): %s", f4(c.PairedSuccessDifference))
		w("- Post-denial: alternative_allowed %d; retry_denied %d; no_further_action %d; successful trials with denial %d/%d", c.PostDenial.AlternativeAllowed, c.PostDenial.RetryDenied, c.PostDenial.NoFurtherAction, c.PostDenial.SuccessfulTrialsWithDenial, c.PostDenial.TrialsWithDenial)
		w("- Fallbacks %d/%d; rate %s; mean chooser latency ns %s", c.Fallbacks, c.HintSelections, f4(c.FallbackRate), f4(c.MeanChooserLatencyNS))
		for _, s := range c.Safety {
			w("- Safety %s: effect %s; disposition %s; open %t", mdText(s.CaseID), s.Effect, s.Result.Disposition, s.Result.Open)
		}
		w("")
	}
	s := r.Spend
	w("## Spend")
	w("")
	w("- committed %s of cap %s (registered cap %s); remaining %s", usd(s.CommittedMicroUSD), usd(s.CapMicroUSD), usd(s.RegisteredCapMicroUSD), usd(s.RemainingMicroUSD))
	w("- settled %s; open %s; uncertain %s", usd(s.SettledMicroUSD), usd(s.OpenMicroUSD), usd(s.UncertainMicroUSD))
	w("- units %d of %d planned (settled %d, open %d, uncertain %d)", s.TotalUnits, s.PlannedUnits, s.SettledUnits, s.OpenUnits, s.UncertainUnits)
	w("- within budget: %t; over cap: %t; cap matches registered: %t; overspent: %t; stop rule: %t; units over planned: %t",
		s.WithinBudget, s.OverCap, s.CapMatchesRegistered, s.Overspent, s.StopRule, s.UnitsOverPlanned)
	w("")
	w("Flags: %s", mdList(r.Flags))
	return b.String()
}

// usd formats integer micro-USD exactly.
func usd(micro int64) string {
	sign, u := "", uint64(micro)
	if micro < 0 {
		sign, u = "-", uint64(-micro)
	}
	return fmt.Sprintf("%sUS$%d.%06d", sign, u/1_000_000, u%1_000_000)
}

func f4(p *float64) string {
	if p == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.4f", *p)
}

func rateText(r evalscore.Rate) string {
	return fmt.Sprintf("%d/%d (%s)", r.Num, r.Den, f4(r.Point))
}

func mdList(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = mdText(id)
	}
	return strings.Join(out, ", ")
}

// mdText keeps identifiers from breaking table rows.
func mdText(s string) string {
	return strings.NewReplacer("|", `\|`, "\r", " ", "\n", " ").Replace(s)
}
