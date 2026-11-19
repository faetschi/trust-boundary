//go:build linux

package main

import (
	"context"
	"errors"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runend"
	"tbound/supervisor/internal/workflowrecovery"
)

// runEndSource is one row of the run-end registry (register D30 as amended 2026-11-18): a source of a failure that
// can end a governed step UNKNOWN and its class. A row names a sentinel the run error wraps (err), a bound on the
// provider path by its failure message (bound), or a source with neither (is). An agent_ceiling bound carries
// runend.ErrCeiling at its site, so the err row of that marker classes it at run time; the other bound rows place
// their message for the enumeration. A carrier only marks that a failure passed through it: the class is that of
// what it wraps.
type runEndSource struct {
	err     error
	bound   string
	is      func(taskCtx context.Context, runErr error) bool
	class   runend.Class
	carrier bool
}

// runEndRegistry classes every source that can end a governed step UNKNOWN. TestRunEndRegistryClassesEverySource
// walks the sources and fails on one without a row. Order matters: the first row the run error matches decides, so a
// ledger fault outranks a ceiling (only start-up checks join ErrDeadline to a fault) and a ceiling outranks an
// outage. An error that matches no row is a plain UNKNOWN: infrastructure or model behaviour, a rerun cause.
var runEndRegistry = []runEndSource{
	// The service's runtime limit ended the run: its task context met the deadline, whatever the error says.
	{is: func(taskCtx context.Context, _ error) bool { return errors.Is(taskCtx.Err(), context.DeadlineExceeded) },
		class: runend.AgentCeiling},

	// Workflow ledger faults: the protected root's clock, CAS, rollback and observation checks.
	{err: workflowrecovery.ErrClockUncertain, class: runend.Infrastructure},
	{err: workflowrecovery.ErrRollback, class: runend.Infrastructure},
	{err: workflowrecovery.ErrCAS, class: runend.Infrastructure},
	{err: workflowrecovery.ErrObservation, class: runend.Infrastructure},

	// Agent ceilings: the ledger's deadline and allowances (host calls, tokens, effects, the effect store's page
	// headroom), the predecessor context an adaptive attacker carries over, and every provider-path bound below that
	// carries the ceiling marker.
	{err: workflowrecovery.ErrDeadline, class: runend.AgentCeiling},
	{err: workflowrecovery.ErrExhausted, class: runend.AgentCeiling},
	{err: providerbridge.ErrContextExhausted, class: runend.AgentCeiling},
	{err: runend.ErrCeiling, class: runend.AgentCeiling},

	// A provider outage with its journal record: the provider-outage code (D30 as amended).
	{is: func(_ context.Context, runErr error) bool { _, ok := providerOutageEvidence(runErr); return ok },
		class: runend.Infrastructure},

	// Model behaviour: a parallel tool call the bridge refuses or resamples (D54).
	{err: openrouter.ErrParallelToolCall, class: runend.ModelBehaviour},

	// Carriers: the bridge closing the conversation over a failed turn.
	{err: providerbridge.ErrClosed, class: runend.Infrastructure, carrier: true},
	{err: providerbridge.ErrProviderTurnFailed, class: runend.Infrastructure, carrier: true},

	// The rest of the ledger, the bridge and the command protocol.
	{err: workflowrecovery.ErrInvalid, class: runend.Infrastructure},
	{err: workflowrecovery.ErrMissing, class: runend.Infrastructure},
	{err: workflowrecovery.ErrOwnerFence, class: runend.Infrastructure},
	{err: workflowrecovery.ErrRestartRequired, class: runend.Infrastructure},
	{err: workflowrecovery.ErrPendingUnknown, class: runend.Infrastructure},
	{err: workflowrecovery.ErrFreshAuthorization, class: runend.Infrastructure},
	{err: workflowrecovery.ErrOwnerStore, class: runend.Infrastructure},
	{err: workflowrecovery.ErrOwnerStoreUnsupported, class: runend.Infrastructure},
	{err: workflowrecovery.ErrSQLiteEngine, class: runend.Infrastructure},
	{err: workflowrecovery.ErrEffectNotReserved, class: runend.Infrastructure},
	{err: providerbridge.ErrWrongState, class: runend.Infrastructure},
	{err: providerbridge.ErrResultWithheld, class: runend.Infrastructure},
	{err: providerbridge.ErrInvalidGeneration, class: runend.Infrastructure},
	{err: providerbridge.ErrTranscriptReplay, class: runend.Infrastructure},
	// A scripted source (a replay or P step, outside the rerun scope) asked past its script.
	{err: providerbridge.ErrScriptExhausted, class: runend.Infrastructure},
	{err: protocol.ErrCommandNotStarted, class: runend.Infrastructure},

	// The Pi runtime: launch, host admission, observation and settlement.
	{err: piruntime.ErrAdmissionClosed, class: runend.Infrastructure},
	{err: piruntime.ErrCgroupScopeRefused, class: runend.Infrastructure},
	{err: piruntime.ErrChildCapabilityMissing, class: runend.Infrastructure},
	{err: piruntime.ErrD09CellDeltaUnknown, class: runend.Infrastructure},
	{err: piruntime.ErrD09CommandReceiptInvalid, class: runend.Infrastructure},
	{err: piruntime.ErrD09ManagedLiveRootRefused, class: runend.Infrastructure},
	{err: piruntime.ErrD09RouteProbeLaunched, class: runend.Infrastructure},
	{err: piruntime.ErrD09SessionCompositionUnavailable, class: runend.Infrastructure},
	{err: piruntime.ErrD09TemplateInvalid, class: runend.Infrastructure},
	{err: piruntime.ErrD09WorkerExitedBeforeAdmission, class: runend.Infrastructure},
	{err: piruntime.ErrD09XattrCalibrationSignature, class: runend.Infrastructure},
	{err: piruntime.ErrHostAdmissionRefused, class: runend.Infrastructure},
	{err: piruntime.ErrHostAttestationMissing, class: runend.Infrastructure},
	{err: piruntime.ErrHostBindingsMissing, class: runend.Infrastructure},
	{err: piruntime.ErrHostProfileMismatch, class: runend.Infrastructure},
	{err: piruntime.ErrHostProfileMissing, class: runend.Infrastructure},
	{err: piruntime.ErrLinuxLauncherRefused, class: runend.Infrastructure},
	{err: piruntime.ErrObservationCapacity, class: runend.Infrastructure},
	{err: piruntime.ErrObservationCleanup, class: runend.Infrastructure},
	{err: piruntime.ErrObservationSink, class: runend.Infrastructure},
	{err: piruntime.ErrProductD09HostRefused, class: runend.Infrastructure},
	{err: piruntime.ErrProductionLauncherUnavailable, class: runend.Infrastructure},
	{err: piruntime.ErrRuntimeBundleRefused, class: runend.Infrastructure},
	{err: piruntime.ErrSessionCleanupCapacity, class: runend.Infrastructure},
	{err: piruntime.ErrSettlementCapacity, class: runend.Infrastructure},
	{err: piruntime.ErrUnknownSettlement, class: runend.Infrastructure},

	// The service and the session: launch admission (the corpus catalog), recovery, publication and settlement. A
	// refused or busy answer never becomes an UNKNOWN step (runend.Codes).
	{err: ErrPiServiceCorpus, class: runend.Infrastructure},
	{err: ErrPiWorkflowCatalog, class: runend.Infrastructure},
	{err: ErrPiServiceUnavailable, class: runend.Infrastructure},
	{err: ErrPiServiceRefused, class: runend.Infrastructure},
	{err: ErrPiServiceFrameTooLarge, class: runend.Infrastructure},
	{err: ErrPiServiceRequestConflict, class: runend.Infrastructure},
	{err: ErrPiServiceUnresolvedPrior, class: runend.Infrastructure},
	{err: ErrPiServiceUnreconciled, class: runend.Infrastructure},
	{err: ErrProviderRecoveryRequired, class: runend.Infrastructure},
	{err: ErrGovernedRecoveryUnavailable, class: runend.Infrastructure},
	{err: ErrGovernedRecoveryUnsupportedVersion, class: runend.Infrastructure},
	{err: ErrGovernedSessionEvidence, class: runend.Infrastructure},
	{err: ErrRuntimeNotConfigured, class: runend.Infrastructure},
	{err: ErrManagedLiveRootUnavailable, class: runend.Infrastructure},
	{err: ErrManagedLiveRootRetained, class: runend.Infrastructure},
	{err: ErrManagedSourceCaptureUnavailable, class: runend.Infrastructure},
	{err: ErrProductionSourceNotQuiescent, class: runend.Infrastructure},
	{err: ErrProductionXattrVisibilityUnavailable, class: runend.Infrastructure},
	{err: ErrProductionBashRunnerUnavailable, class: runend.Infrastructure},
	{err: ErrProductionQuiescenceUnavailable, class: runend.Infrastructure},
	{err: ErrProductionLifecycleUnsettled, class: runend.Infrastructure},
	{err: ErrProductionCommandEvidence, class: runend.Infrastructure},
	{err: ErrProductionSealedRuntime, class: runend.Infrastructure},
	{err: ErrProductionWorkerSettlementIncomplete, class: runend.Infrastructure},
	{err: ErrScriptedRoleNotRecoverable, class: runend.Infrastructure},
	{err: ErrServiceProviderCredentials, class: runend.Infrastructure},

	// Provider-path bounds the agent's use drives: the per-call completion cap (D50), the history that must fit the
	// next request and the durable exchange budget it shares with the reply, the turn projection and its channel
	// frame, the turn and frame budgets, and a tool result over the protocol frame.
	{bound: "%w: reply cut at the per-call completion cap (finish reason length)", class: runend.AgentCeiling},
	{bound: "%w: request exceeds byte limit before marshaling", class: runend.AgentCeiling},
	{bound: "%w: request string exceeds byte limit", class: runend.AgentCeiling},
	{bound: "%w: provider request leaves no durable response budget", class: runend.AgentCeiling},
	{bound: "%w: provider exchange record budget exhausted", class: runend.AgentCeiling},
	{bound: "%w: provider response byte length out of bounds", class: runend.AgentCeiling},
	{bound: "%w: provider transcript message limit exhausted", class: runend.AgentCeiling},
	{bound: "%w: provider transcript byte budget exhausted", class: runend.AgentCeiling},
	{bound: "%w: provider conversation turn/sequence budget exhausted", class: runend.AgentCeiling},
	{bound: "%w: canonical provider turn projection exceeds one mebibyte", class: runend.AgentCeiling},
	{bound: "%w: provider response projection exceeds its durable evidence budget: %w", class: runend.AgentCeiling},
	{bound: "%w: provider channel response exceeds frame bound", class: runend.AgentCeiling},
	{bound: "%w: provider channel frame budget exhausted", class: runend.AgentCeiling},
	{bound: "%w: executor output is oversized", class: runend.AgentCeiling},

	// Provider-path bounds whose class is that of the error they wrap: a ledger refusal (the token allowance joined to
	// the cap refusal), the end of the turn context.
	{bound: "reserve durable provider allowance before dispatch: %w", class: runend.Infrastructure, carrier: true},
	{bound: "wait out the provider rate limit: %w", class: runend.Infrastructure, carrier: true},
	{bound: "host selected no positive provider completion-token cap", class: runend.Infrastructure, carrier: true},

	// Provider-path bounds of the reply stream. Under D50 a reply within the 4,096-token cap stays inside them, so a
	// stream past one is the provider's.
	{bound: "malformed or oversized SSE line", class: runend.ModelBehaviour},
	{bound: "too many SSE events", class: runend.ModelBehaviour},
	{bound: "SSE event exceeds byte limit", class: runend.ModelBehaviour},
	{bound: "response byte length out of bounds", class: runend.ModelBehaviour},
	{bound: "invalid or oversized assistant text", class: runend.ModelBehaviour},
	{bound: "invalid or oversized reasoning", class: runend.ModelBehaviour},
	{bound: "invalid or oversized tool name", class: runend.ModelBehaviour},
	{bound: "invalid or oversized argument fragment", class: runend.ModelBehaviour},

	// Provider-path bounds no agent drives: counters of 2^64, an internal parameter check, the case's own prompt, a
	// request rebuilt from fields its capture accepted, the hint chooser (a failure there yields no hint and never ends
	// a run), the recovery replay of a history that fit live, and the scripted and development sources.
	{bound: "provider response sequence exhausted", class: runend.Infrastructure},
	{bound: "provider channel response sequence exhausted", class: runend.Infrastructure},
	{bound: "provider evidence sequence exhausted", class: runend.Infrastructure},
	{bound: "provider turn sequence exhausted", class: runend.Infrastructure},
	{bound: "provider response byte budget is invalid", class: runend.Infrastructure},
	{bound: "completion-token cap is outside the supported request range", class: runend.Infrastructure},
	{bound: "host-call reservation does not bind the exact outbound request and completion cap", class: runend.Infrastructure},
	{bound: "user task identity or prompt is invalid or exceeds its bound", class: runend.Infrastructure},
	{bound: "invalid or oversized prior tool-call fields", class: runend.Infrastructure},
	{bound: "decision input exceeds bound", class: runend.Infrastructure},
	{bound: "decision response exceeds bound", class: runend.Infrastructure},
	{bound: "local chooser input exceeds bound", class: runend.Infrastructure},
	{bound: "local completion exceeds bound", class: runend.Infrastructure},
	{bound: "provider replay message budget exhausted", class: runend.Infrastructure},
	{bound: "provider replay transcript byte budget exhausted", class: runend.Infrastructure},
	{bound: "scripted provider request body is unreadable or too large", class: runend.Infrastructure},
	{bound: "development offline provider response script is exhausted", class: runend.Infrastructure},
}

// runEndClass is the registry's class of a run that ended UNKNOWN.
func runEndClass(taskCtx context.Context, runErr error) runend.Class {
	for _, row := range runEndRegistry {
		switch {
		case row.carrier || row.bound != "":
		case row.is != nil && row.is(taskCtx, runErr), row.err != nil && errors.Is(runErr, row.err):
			return row.class
		}
	}
	return runend.Infrastructure
}

// runEndAnswer is the code (and, for an outage, the provider journal record) of the UNKNOWN answer: a budget end
// for an agent ceiling, never a rerun cause; else the evidenced outage or a plain UNKNOWN, both rerun causes.
func runEndAnswer(taskCtx context.Context, runErr error) (code, outageDigest string) {
	if runEndClass(taskCtx, runErr) == runend.AgentCeiling {
		return runend.CodeBudgetEnded, ""
	}
	if digest, ok := providerOutageEvidence(runErr); ok {
		return runend.CodeProviderOutage, digest
	}
	return runend.CodeOutcomeUnknown, ""
}
