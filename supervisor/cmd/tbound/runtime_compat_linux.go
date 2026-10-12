//go:build linux

package main

import (
	"encoding/json"
	"hash"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/tboundruntime"
	"tbound/supervisor/internal/workflow"
)

// Keep the command's existing construction names available while the reusable
// implementations live in internal/tboundruntime.
type DurableExecutor = tboundruntime.DurableExecutor
type DurableExecutorConfig = tboundruntime.DurableExecutorConfig
type AuditDecisionRecorder = tboundruntime.AuditDecisionRecorder
type PublicationRuntime = tboundruntime.PublicationRuntime

type durableMutationSummaryPayload = tboundruntime.DurableMutationSummaryPayload
type durableGenerationSummary = tboundruntime.DurableGenerationSummary
type durableTransitionSummary = tboundruntime.DurableTransitionSummary
type durableCommandSummary = tboundruntime.DurableCommandSummary
type durableReadSummaryPayload = tboundruntime.DurableReadSummaryPayload
type durableDecisionEvent = tboundruntime.DurableDecisionEvent

const durableReadMaxBytes = tboundruntime.DurableReadMaxBytes
const durableDecisionEventKind = tboundruntime.DurableDecisionEventKind

func NewDurableExecutor(config DurableExecutorConfig) (*DurableExecutor, error) {
	return tboundruntime.NewDurableExecutor(config)
}

func NewAuditDecisionRecorder(journal *audit.Journal) (*AuditDecisionRecorder, error) {
	return tboundruntime.NewAuditDecisionRecorder(journal)
}

func NewPublicationRuntime(executor *DurableExecutor, options workflow.FinalizerOptions) (*PublicationRuntime, error) {
	return tboundruntime.NewPublicationRuntime(executor, options)
}

// The following forwarding helpers preserve same-package fixture/test call
// sites. Their implementations and result types are owned by tboundruntime.
func durableMutationSummary(tool string, mutation sessionrepo.MutationResult, runner sessionrepo.CommandRunner) (json.RawMessage, error) {
	return tboundruntime.DurableMutationSummary(tool, mutation, runner)
}

func durableCommandRunnerProfile(runner sessionrepo.CommandRunner) string {
	return tboundruntime.DurableCommandRunnerProfile(runner)
}

func durableOperationIdentity(proposal protocol.Proposal, decision gate.Decision, callIssuer string) (delta.OperationIdentity, error) {
	return tboundruntime.DurableOperationIdentity(proposal, decision, callIssuer)
}

func durablePolicyDecision(proposal protocol.Proposal, decision gate.Decision, policyDigest, metadataPolicyDigest string) (delta.PolicyDecision, error) {
	return tboundruntime.DurablePolicyDecision(proposal, decision, policyDigest, metadataPolicyDigest)
}

func durableTrustedIdentity(domain string, proposal protocol.Proposal, decision gate.Decision) (string, error) {
	return tboundruntime.DurableTrustedIdentity(domain, proposal, decision)
}

func writeHashField(digest hash.Hash, value string) {
	tboundruntime.WriteHashField(digest, value)
}

func durableWriteArguments(raw json.RawMessage) (string, []byte, error) {
	return tboundruntime.DurableWriteArguments(raw)
}

func durableEditArguments(raw json.RawMessage) (string, []sessionrepo.Replacement, error) {
	return tboundruntime.DurableEditArguments(raw)
}

func durableBashArguments(raw json.RawMessage) (string, error) {
	return tboundruntime.DurableBashArguments(raw)
}

func durableReadSummary(path string, generation *sessionrepo.Generation, content []byte) (json.RawMessage, error) {
	return tboundruntime.DurableReadSummary(path, generation, content)
}

func durableReadArguments(raw json.RawMessage) (string, error) {
	return tboundruntime.DurableReadArguments(raw)
}

func durableContentDigest(content []byte) string { return tboundruntime.DurableContentDigest(content) }

func durableNoTrailingJSON(decoder *json.Decoder) error {
	return tboundruntime.DurableNoTrailingJSON(decoder)
}

func validIdentityText(value string) bool { return tboundruntime.ValidIdentityText(value) }

func validBareSHA256(value string) bool { return tboundruntime.ValidBareSHA256(value) }

func durableDecisionEventID(proposal protocol.Proposal, decision gate.Decision) (string, error) {
	return tboundruntime.DurableDecisionEventID(proposal, decision)
}
