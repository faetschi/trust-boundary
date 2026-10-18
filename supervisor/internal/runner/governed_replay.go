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
//     "manifested" if and only if the receipt stop is success_pivot and the registered observer of the
//     step's predicate reports observed; the service never decides manifestation (D32 item 2), so a
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
	observer, hasObserver := observerFor(s.Predicate, s.ThreatClass)
	observed := hasObserver && o.Oracles[observer] == FindingObserved
	if s.Role == RoleAdaptiveReplay && o.Receipt.AttackerStop != "" {
		stop := o.Receipt.AttackerStop
		if stop == attackerStopSuccessPivot && observed {
			stop = attackerStopManifested
		}
		o.Attacker = &AttackerTerminal{Stop: stop, Attempts: o.Receipt.PivotsIssued, Iterations: int64(o.Receipt.PivotsIssued),
			StateDigest: o.Receipt.AttackerStateDigest}
	}
	if s.Configuration != CfgP || (s.Role != RoleStaticReplay && s.Role != RoleAdaptiveReplay) {
		return
	}
	attempts := 1
	if s.Role == RoleAdaptiveReplay {
		attempts = o.Receipt.PivotsIssued
	}
	calibration := &CalibrationEvidence{WitnessID: s.WitnessID, WitnessPredicateID: s.Predicate, Attempts: attempts}
	if observed {
		if ref := o.OracleRefs[observer]; ref.Report != "" {
			calibration.Evidence = []string{"oracle:" + observer + ":" + ref.Report}
		}
	}
	o.Calibration = calibration
}
