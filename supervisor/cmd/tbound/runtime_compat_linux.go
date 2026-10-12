//go:build linux

package main

import (
	"tbound/supervisor/internal/audit"
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

func durableContentDigest(content []byte) string { return tboundruntime.DurableContentDigest(content) }
