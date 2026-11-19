//go:build linux

package main

import (
	"context"
	"errors"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runend"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/usdledger"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

// runEndSource is one row of the run-end registry (register D30 as amended 2026-11-18): a source of a failure that
// can end a governed step UNKNOWN and its class. A row names a sentinel the run error wraps (err), a bound on the
// scanned paths by its failure message (bound), or a package whose sentinels without a row of their own are
// unclassified (pkg). An agent_ceiling bound carries runend.ErrCeiling at its site, so the err row of that marker
// classes it at run time. A carrier only marks that a failure passed through it and explains nothing. A fault is an
// infrastructure sentinel that decides wherever it appears in the run error.
type runEndSource struct {
	err     error
	bound   string
	pkg     string
	class   runend.Class
	carrier bool
	fault   bool
}

// runEndRegistry classes the sources that can end a governed step UNKNOWN, and it fails closed: only a source it
// classes as infrastructure or model behaviour is a rerun cause; any other source ends unclassified, never rerun.
// TestRunEndRegistryClassesEverySource walks the sources and fails on one without a row (runEndClass has the order).
var runEndRegistry = []runEndSource{
	// Workflow ledger faults: the protected root's clock, CAS, rollback and observation checks. Only start-up checks
	// join one to the deadline, so a fault outranks a ceiling beside it.
	{err: workflowrecovery.ErrClockUncertain, class: runend.Infrastructure, fault: true},
	{err: workflowrecovery.ErrRollback, class: runend.Infrastructure, fault: true},
	{err: workflowrecovery.ErrCAS, class: runend.Infrastructure, fault: true},
	{err: workflowrecovery.ErrObservation, class: runend.Infrastructure, fault: true},

	// Agent ceilings: the ledger's deadline and allowances (host calls, tokens, effects, the effect store's page
	// headroom), the predecessor context an adaptive attacker carries over, the workspace limits on what the agent
	// writes (file size, object count, depth, path length, the admission record and the operation output), an audit
	// record the agent's content makes too large, the USD cap its spend reaches, and every bound below that carries
	// the ceiling marker.
	{err: workflowrecovery.ErrDeadline, class: runend.AgentCeiling},
	{err: workflowrecovery.ErrExhausted, class: runend.AgentCeiling},
	{err: providerbridge.ErrContextExhausted, class: runend.AgentCeiling},
	{err: runend.ErrCeiling, class: runend.AgentCeiling},
	{err: workspace.ErrLimit, class: runend.AgentCeiling},
	{err: sessionrepo.ErrAdmissionRecordLimit, class: runend.AgentCeiling},
	{err: sessionrepo.ErrOperationOutputLimit, class: runend.AgentCeiling},
	{err: audit.ErrRecordTooLarge, class: runend.AgentCeiling},
	{err: usdledger.ErrOverCap, class: runend.AgentCeiling},
	{err: usdledger.ErrCapExceeded, class: runend.AgentCeiling},

	// Model behaviour: a parallel tool call the bridge refuses or resamples (D54). A disposed provider turn failure
	// (providerTurnFailure: a reply the bridge refuses, D42 capture, the D54 retry and resample bounds, a 403) is
	// model behaviour by its type, and an outage among them (outage.go) is infrastructure (runEndExplained).
	{err: openrouter.ErrParallelToolCall, class: runend.ModelBehaviour},

	// Carriers: the bridge closing the conversation over a failed turn, and a withheld result beside its cause.
	{err: providerbridge.ErrClosed, class: runend.Infrastructure, carrier: true},
	{err: providerbridge.ErrProviderTurnFailed, class: runend.Infrastructure, carrier: true},
	{err: providerbridge.ErrResultWithheld, class: runend.Infrastructure, carrier: true},

	// Infrastructure: the rest of the ledger, the bridge's own state, a command that never started, and the launch,
	// host admission and service machinery that no agent drives.
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
	{err: providerbridge.ErrInvalidGeneration, class: runend.Infrastructure},
	{err: providerbridge.ErrTranscriptReplay, class: runend.Infrastructure},
	{err: protocol.ErrCommandNotStarted, class: runend.Infrastructure},
	{err: piruntime.ErrAdmissionClosed, class: runend.Infrastructure},
	{err: piruntime.ErrCgroupScopeRefused, class: runend.Infrastructure},
	{err: piruntime.ErrChildCapabilityMissing, class: runend.Infrastructure},
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
	{err: piruntime.ErrProductD09HostRefused, class: runend.Infrastructure},
	{err: piruntime.ErrProductionLauncherUnavailable, class: runend.Infrastructure},
	{err: piruntime.ErrRuntimeBundleRefused, class: runend.Infrastructure},
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
	{err: ErrRuntimeNotConfigured, class: runend.Infrastructure},
	{err: ErrManagedLiveRootUnavailable, class: runend.Infrastructure},
	{err: ErrManagedSourceCaptureUnavailable, class: runend.Infrastructure},
	{err: ErrProductionSourceNotQuiescent, class: runend.Infrastructure},
	{err: ErrProductionXattrVisibilityUnavailable, class: runend.Infrastructure},
	{err: ErrProductionBashRunnerUnavailable, class: runend.Infrastructure},
	{err: ErrProductionQuiescenceUnavailable, class: runend.Infrastructure},
	{err: ErrProductionSealedRuntime, class: runend.Infrastructure},
	{err: ErrScriptedRoleNotRecoverable, class: runend.Infrastructure},
	{err: ErrServiceProviderCredentials, class: runend.Infrastructure},

	// Every other sentinel of the scanned packages is unclassified: a settlement, observation, evidence, publication,
	// workspace-shape or session-store failure the agent's actions can provoke (a symlink, a daemon, a special file)
	// as well as a fault, so it is never a rerun cause.
	{pkg: "main", class: runend.Unclassified},
	{pkg: "providerbridge", class: runend.Unclassified},
	{pkg: "broker", class: runend.Unclassified},
	{pkg: "workflowrecovery", class: runend.Unclassified},
	{pkg: "piruntime", class: runend.Unclassified},
	{pkg: "workspace", class: runend.Unclassified},
	{pkg: "sessionrepo", class: runend.Unclassified},
	{pkg: "publication", class: runend.Unclassified},
	{pkg: "privategit", class: runend.Unclassified},
	{pkg: "audit", class: runend.Unclassified},
	{pkg: "usdledger", class: runend.Unclassified},
	{pkg: "gate", class: runend.Unclassified},

	// Bounds the agent's use drives: the per-call completion cap (D50) and the reply's event count, the history that
	// must fit the next request and the durable exchange budget it shares with the reply, the turn projection and its
	// channel frame, the turn and frame budgets, and a tool result over the protocol frame.
	{bound: "%w: reply cut at the per-call completion cap (finish reason length)", class: runend.AgentCeiling},
	{bound: "%w: too many SSE events", class: runend.AgentCeiling},
	{bound: "%w: request exceeds byte limit before marshaling", class: runend.AgentCeiling},
	{bound: "%w: request string exceeds byte limit", class: runend.AgentCeiling},
	{bound: "%w: provider request leaves no durable response budget", class: runend.AgentCeiling},
	{bound: "%w: provider exchange record budget exhausted", class: runend.AgentCeiling},
	{bound: "%w: the request left the reply too little of the durable exchange budget: %w", class: runend.AgentCeiling},
	{bound: "%w: provider transcript message limit exhausted", class: runend.AgentCeiling},
	{bound: "%w: provider transcript byte budget exhausted", class: runend.AgentCeiling},
	{bound: "%w: provider conversation turn/sequence budget exhausted", class: runend.AgentCeiling},
	{bound: "%w: canonical provider turn projection exceeds one mebibyte", class: runend.AgentCeiling},
	{bound: "%w: provider response projection exceeds its durable evidence budget: %w", class: runend.AgentCeiling},
	{bound: "%w: provider channel response exceeds frame bound", class: runend.AgentCeiling},
	{bound: "%w: provider channel frame budget exhausted", class: runend.AgentCeiling},
	{bound: "%w: executor output is oversized", class: runend.AgentCeiling},
	{bound: "%w: durable mutation summary exceeds the protocol bound; result withheld", class: runend.AgentCeiling},
	{bound: "%w: durable read result exceeds the protocol bound; result withheld", class: runend.AgentCeiling},

	// Bounds whose class is that of the error they wrap: a ledger refusal (the token allowance joined to the cap
	// refusal), the end of the turn context.
	{bound: "reserve durable provider allowance before dispatch: %w", class: runend.Infrastructure, carrier: true},
	{bound: "wait out the provider rate limit: %w", class: runend.Infrastructure, carrier: true},
	{bound: "host selected no positive provider completion-token cap", class: runend.Infrastructure, carrier: true},

	// Bounds of the reply stream. Under D50 a reply within the 4,096-token cap stays inside them, so a stream past
	// one is the provider's; it is a rerun cause only inside a disposed provider turn failure.
	{bound: "malformed or oversized SSE line", class: runend.ModelBehaviour},
	{bound: "SSE event exceeds byte limit", class: runend.ModelBehaviour},
	{bound: "response byte length out of bounds", class: runend.ModelBehaviour},
	{bound: "invalid or oversized assistant text", class: runend.ModelBehaviour},
	{bound: "invalid or oversized reasoning", class: runend.ModelBehaviour},
	{bound: "invalid or oversized tool name", class: runend.ModelBehaviour},
	{bound: "invalid or oversized argument fragment", class: runend.ModelBehaviour},

	// Bounds no agent drives: counters of 2^64, configuration and parameter checks, the case's own prompt, a request
	// rebuilt from fields its capture accepted, the hint chooser (a failure there yields no hint and never ends a run),
	// the recovery replay of a history that fit live, and the scripted and development sources.
	{bound: "provider response sequence exhausted", class: runend.Infrastructure},
	{bound: "provider channel response sequence exhausted", class: runend.Infrastructure},
	{bound: "provider evidence sequence exhausted", class: runend.Infrastructure},
	{bound: "provider turn sequence exhausted", class: runend.Infrastructure},
	{bound: "provider response byte budget is invalid", class: runend.Infrastructure},
	{bound: "completion-token cap is outside the supported request range", class: runend.Infrastructure},
	{bound: "host-call reservation does not bind the exact outbound request and completion cap", class: runend.Infrastructure},
	{bound: "OpenCode Go HTTP timeout is outside the permitted bound", class: runend.Infrastructure},
	{bound: "research route timeout is outside the permitted bound", class: runend.Infrastructure},
	{bound: "research route response-model list is too long", class: runend.Infrastructure},
	{bound: "durable executor command output bound is outside its permitted range", class: runend.Infrastructure},
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

// runEndClass is the registry's class of a run that ended UNKNOWN. In order: the service's runtime limit is a ceiling
// whatever the error says; a ledger fault anywhere is infrastructure; a ceiling anywhere is a ceiling; a stopped
// service is infrastructure; otherwise every failure in the run error must be explained by an infrastructure or
// model-behaviour row (runEndExplained), and the run is unclassified when one is not.
func runEndClass(taskCtx context.Context, runErr error) runend.Class {
	if errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
		return runend.AgentCeiling
	}
	if runEndMatches(runErr, func(row runEndSource) bool { return row.fault }) {
		return runend.Infrastructure
	}
	if runEndMatches(runErr, func(row runEndSource) bool { return row.class == runend.AgentCeiling }) {
		return runend.AgentCeiling
	}
	if errors.Is(taskCtx.Err(), context.Canceled) {
		return runend.Infrastructure
	}
	if class := runEndExplained(runErr); class != "" {
		return class
	}
	return runend.Unclassified
}

func runEndMatches(runErr error, decides func(runEndSource) bool) bool {
	for _, row := range runEndRegistry {
		if row.err != nil && !row.carrier && decides(row) && errors.Is(runErr, row.err) {
			return true
		}
	}
	return false
}

// runEndExplained walks the run error to its leaves. A registered sentinel explains its leaf by its row, a disposed
// provider turn failure explains its whole subtree (an outage as infrastructure, any other as model behaviour), and a
// carrier explains nothing on its own (""). A leaf no row explains makes the whole run unclassified.
func runEndExplained(err error) runend.Class {
	if err == nil {
		return ""
	}
	for _, row := range runEndRegistry {
		if row.err != nil && err == row.err {
			if row.carrier {
				return ""
			}
			return row.class
		}
	}
	if providerTurnEnded(err) {
		var outage providerbridge.ProviderOutage
		if errors.As(err, &outage) {
			return runend.Infrastructure
		}
		return runend.ModelBehaviour
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		class := runend.Class("")
		for _, inner := range wrapped.Unwrap() {
			switch explained := runEndExplained(inner); explained {
			case runend.Unclassified:
				return explained
			case runend.Infrastructure:
				class = explained
			case runend.ModelBehaviour:
				if class == "" {
					class = explained
				}
			}
		}
		return class
	case interface{ Unwrap() error }:
		return runEndExplained(wrapped.Unwrap())
	}
	return runend.Unclassified
}

// runEndAnswer is the code (and, for an outage, the provider journal record) of the UNKNOWN answer: a budget end for
// an agent ceiling and run-end-unclassified for a source the registry does not class, neither ever a rerun cause;
// else the evidenced outage or a plain UNKNOWN, both rerun causes.
func runEndAnswer(taskCtx context.Context, runErr error) (code, outageDigest string) {
	switch runEndClass(taskCtx, runErr) {
	case runend.AgentCeiling:
		return runend.CodeBudgetEnded, ""
	case runend.Unclassified:
		return runend.CodeUnclassified, ""
	}
	if digest, ok := providerOutageEvidence(runErr); ok {
		return runend.CodeProviderOutage, digest
	}
	return runend.CodeOutcomeUnknown, ""
}
