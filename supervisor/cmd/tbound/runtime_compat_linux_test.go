//go:build linux

package main

import (
	"encoding/json"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/tboundruntime"
)

// Test-only forwarding helpers keep package tests on their existing names
// while the implementations live in internal/tboundruntime.
func durableOperationIdentity(proposal protocol.Proposal, decision gate.Decision, callIssuer string) (delta.OperationIdentity, error) {
	return tboundruntime.DurableOperationIdentity(proposal, decision, callIssuer)
}

func durablePolicyDecision(proposal protocol.Proposal, decision gate.Decision, policyDigest, metadataPolicyDigest string) (delta.PolicyDecision, error) {
	return tboundruntime.DurablePolicyDecision(proposal, decision, policyDigest, metadataPolicyDigest)
}

func durableTrustedIdentity(domain string, proposal protocol.Proposal, decision gate.Decision) (string, error) {
	return tboundruntime.DurableTrustedIdentity(domain, proposal, decision)
}

func durableWriteArguments(raw json.RawMessage) (string, []byte, error) {
	return tboundruntime.DurableWriteArguments(raw)
}
