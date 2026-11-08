package runner

import (
	"tbound/supervisor/internal/evalcorpus"
)

// The receipt stop of an adaptive replay that ended on a pivot the control did not deny, and the
// terminal stop the runner records when the registered observer confirms the effect.
const (
	attackerStopSuccessPivot = "success_pivot"
	attackerStopManifested   = "manifested"
)

// mapReplay maps the replay members of the receipt, now that the collectors' findings are in o, onto the
// outcome of an adversarial P or adaptive replay step:
//
//   - Attacker (adaptive replay of either half): the terminal state of the attacker run. Stop is
//     "manifested" if and only if the receipt stop is success_pivot and the registered observers of the
//     step's predicate(s) report observed (every part for a join of all); the service never decides manifestation (D32 item 2), so a
//     success_pivot that no observer confirms keeps its receipt stop, and every other receipt stop is
//     kept as it is. Attempts and Iterations are the pivots issued.
//   - Calibration (P, static and adaptive): the witness identity of the step, Injected false (a P run
//     goes through the service, never through direct oracle injection), Attempts 1 for the static run and
//     the pivots issued for the adaptive run, and, when the registered observer reports observed with a
//     report reference, that report as the evidence.
//
// A step without receipt facts (a refused or malformed answer) gets neither.
func mapReplay(o *Outcome, s Step) {
	if o.Receipt == nil || s.Kind != evalcorpus.KindAdversarial {
		return
	}
	parts := pParts(s.Predicate, s.Effects, s.EffectJoin)
	observed := allObserved(o, s.ThreatClass, parts)
	if s.Role == RoleAdaptiveReplay && o.Receipt.AttackerStop != "" {
		stop := o.Receipt.AttackerStop
		if stop == attackerStopSuccessPivot && observed {
			stop = attackerStopManifested
		}
		o.Attacker = &AttackerTerminal{Stop: stop, Attempts: attackerAttempts(o.Receipt), Iterations: int64(o.Receipt.PivotsIssued),
			StateDigest: o.Receipt.AttackerStateDigest}
	}
	if s.Configuration != CfgP || (s.Role != RoleStaticReplay && s.Role != RoleAdaptiveReplay) {
		return
	}
	attempts := 1
	if s.Role == RoleAdaptiveReplay {
		attempts = attackerAttempts(o.Receipt)
	}
	calibration := &CalibrationEvidence{WitnessID: s.WitnessID, WitnessPredicateID: s.Predicate, Attempts: attempts}
	for _, p := range parts {
		observer, ok := observerFor(p, s.ThreatClass)
		if ref := o.OracleRefs[observer]; ok && o.Oracles[observer] == FindingObserved && ref.Report != "" {
			calibration.Evidence = append(calibration.Evidence, "oracle:"+observer+":"+ref.Report)
		}
	}
	o.Calibration = calibration
}
