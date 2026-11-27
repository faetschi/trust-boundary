// Package runend is the shared vocabulary of the run-end registry (register D30 as amended 2026-11-18): the class
// of a failure that can end a governed step UNKNOWN, the marker of a ceiling that has no sentinel of its own, and
// the codes of the guest's UNKNOWN answer. The registry that classes every source is runEndRegistry in cmd/tbound.
package runend

import "errors"

// Class is what a failure that ended a governed step UNKNOWN means for the rerun rule.
type Class string

const (
	// Infrastructure is a failure of the provider or of TBound's own machinery: a registered rerun cause, the
	// evidenced outage or a plain UNKNOWN.
	Infrastructure Class = "infrastructure"
	// AgentCeiling is the run meeting a limit its own resource use drives: resource_exhaustion, a treatment failure
	// (thesis §7.5) that scores CONSERVATIVE_FAILURE and is never rerun.
	AgentCeiling Class = "agent_ceiling"
	// ModelBehaviour is a provider reply TBound refuses (D42 refusal capture, the D54 retry and resample bounds, a
	// parallel tool call, a malformed or oversized stream). It stays a plain UNKNOWN, as before the registry: a
	// rerun cause only with complete no-effect evidence.
	ModelBehaviour Class = "model_behaviour"
	// Unclassified is every source the registry does not class as infrastructure or model behaviour: never a rerun
	// cause, scored like an agent ceiling (CONSERVATIVE_FAILURE) and reported as unclassified loss. The registry
	// fails closed, so a source without a row can only end conservatively.
	Unclassified Class = "unclassified"
)

// ErrCeiling marks a failure at an agent-driven bound that has no ledger sentinel: a reply cut at the per-call
// completion cap that is not a typed end (D64 item 8), a history that no longer fits the next request, a turn projection over its evidence budget.
var ErrCeiling = errors.New("the run met an agent-driven ceiling")

// The codes of a governed step's UNKNOWN answer that the run-end registry chooses.
const (
	CodeOutcomeUnknown = "task-outcome-unknown"
	CodeBudgetEnded    = "run-budget-exhausted"
	CodeProviderOutage = "provider-outage"
	CodeUnclassified   = "run-end-unclassified"

	// CodeLaunchNotAdmitted is a launch that failed before the task was admitted, with its cell settled and the
	// receipt binding the run's E04 points: the agent never ran, so it is an infrastructure end, a rerun cause.
	CodeLaunchNotAdmitted = "launch-not-admitted"
)

// Codes is the class every code of the Pi service's UNKNOWN answer carries to the host. CodeOutcomeUnknown carries
// both rerun-cause classes that have no code of their own, infrastructure and model behaviour, which the rerun rule
// treats alike. The host reads an UNKNOWN code that is not here as no rerun cause. A refused or busy answer is no UNKNOWN:
// no run happened, and the host spends the unit as a setup failure whatever its code (launch admission).
var Codes = map[string]Class{
	CodeOutcomeUnknown: Infrastructure,
	CodeBudgetEnded:    AgentCeiling,
	CodeProviderOutage: Infrastructure,
	CodeUnclassified:   Unclassified,

	CodeLaunchNotAdmitted: Infrastructure,

	"request-journal-unavailable":           Infrastructure,
	"socket-authority-changed":              Infrastructure,
	"retained-workflow-recovery-unresolved": Infrastructure,
	"terminal-journal-unavailable":          Infrastructure,
}
