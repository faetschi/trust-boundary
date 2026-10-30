//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/sessionrepo"
)

// durableArgumentRefusal returns the no-effect refusal that the durable decoder for tool reports for raw. Only the
// tagged decoder sites return one; a path refusal, a valid call and any other error return nil.
func durableArgumentRefusal(tool string, raw json.RawMessage) *protocol.NoEffectError {
	var err error
	switch tool {
	case "write":
		_, _, err = durableWriteArguments(raw)
	case "edit":
		_, _, err = durableEditArguments(raw)
	case "bash":
		_, err = durableBashArguments(raw)
	case "read":
		_, _, err = durableReadArguments(raw)
	default:
		return nil
	}
	var refusal *protocol.NoEffectError
	if errors.As(err, &refusal) {
		return refusal
	}
	return nil
}

// VerifyNoEffect proves that the refusal of an ALLOWed call left no Store effect. It reads the current tip and the
// complete retained Store evidence; it executes nothing and changes nothing.
func (e *DurableExecutor) VerifyNoEffect(ctx context.Context, proposal protocol.Proposal, decision gate.Decision,
	inputGeneration, inputTreeDigest string) error {
	if e == nil || e.store == nil {
		return errors.New("durable executor is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.quarantined {
		return errors.New("durable executor is quarantined after incomplete production command evidence")
	}
	if e.tip == nil || e.tip.ID() != inputGeneration || e.tip.TreeDigest() != inputTreeDigest {
		return errors.New("no-effect proof is not bound to the current Store tip")
	}
	// Store.Verify rejects a quarantined or unusable Store, which EvidenceBundle deliberately still exports.
	if _, err := e.store.Verify(); err != nil {
		return fmt.Errorf("verify Store for no-effect proof: %w", err)
	}
	bundle, err := e.store.EvidenceBundle()
	if err != nil {
		return fmt.Errorf("export Store evidence for no-effect proof: %w", err)
	}
	return verifyDurableNoEffect(proposal, decision, e.callIssuer, inputGeneration, inputTreeDigest, bundle)
}

// verifyDurableNoEffect accepts a refusal only when the retained arguments reproduce it and the complete Store
// evidence holds no operation, authorization, intent or journal record bound to the call. It reads the bundle only.
func verifyDurableNoEffect(proposal protocol.Proposal, decision gate.Decision, callIssuer, inputGeneration, inputTreeDigest string,
	bundle sessionrepo.EvidenceBundle) error {
	if decision.Verdict != gate.Allow || decision.Tool != proposal.Tool || decision.ToolCallID != proposal.ToolCallID {
		return errors.New("no-effect proof needs the exact ALLOW decision for this call")
	}
	digest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
	if err != nil || digest != decision.CanonicalArgumentsDigest {
		return errors.New("no-effect proof: retained arguments do not match the decision digest")
	}
	if durableArgumentRefusal(proposal.Tool, proposal.Arguments) == nil {
		return errors.New("no-effect proof: retained arguments do not reproduce a decoder refusal")
	}
	if !bundleHasGeneration(bundle, inputGeneration, inputTreeDigest) {
		return errors.New("no-effect proof: input generation and tree are not in the retained Store history")
	}
	operation, err := durableOperationIdentity(proposal, decision, callIssuer)
	if err != nil {
		return err
	}
	decisionID, err := durableTrustedIdentity("decision", proposal, decision)
	if err != nil {
		return err
	}
	for _, completed := range bundle.Operations {
		if completed.Operation == operation || completed.DecisionID == decisionID {
			return errors.New("Store completed an operation for this call; no-effect refusal is not proven")
		}
	}
	identifiers := map[string]bool{}
	for _, identifier := range []string{proposal.ToolCallID, decisionID, operation.ProposalID, operation.LeaseID} {
		if identifier != "" {
			identifiers[identifier] = true
		}
	}
	if decision.ResponseID != nil && decision.ResponseID.Opaque != "" {
		identifiers[decision.ResponseID.Opaque] = true
	}
	for _, journal := range [][]byte{bundle.AuditJournal, bundle.AdmissionJournal} {
		for _, line := range bytes.Split(journal, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var record audit.Record
			if json.Unmarshal(line, &record) != nil {
				return errors.New("retained Store evidence for the no-effect proof is not decodable")
			}
			// An effect_outcome holds the raw read or command output; its effect_intent, checked here too, holds the identity.
			if record.Event.Kind == "effect_outcome" || len(record.Event.Data) == 0 {
				continue
			}
			var data any
			if json.Unmarshal(record.Event.Data, &data) != nil {
				return errors.New("retained Store evidence for the no-effect proof is not decodable")
			}
			if recordBindsIdentifier(data, "", identifiers) {
				return errors.New("Store retains a record bound to this call; no-effect refusal is not proven")
			}
		}
	}
	return nil
}

// recordBindsIdentifier reports whether a decoded Store record names one of identifiers in an identity field: a key
// ending in "id", or a correlation identifier's "opaque". File and command bytes sit under other keys, so output that
// merely mentions an identifier does not bind the record to the call.
func recordBindsIdentifier(value any, key string, identifiers map[string]bool) bool {
	switch value := value.(type) {
	case string:
		return identifiers[value] && (strings.HasSuffix(key, "id") || key == "opaque")
	case map[string]any:
		for childKey, child := range value {
			if recordBindsIdentifier(child, childKey, identifiers) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if recordBindsIdentifier(child, key, identifiers) {
				return true
			}
		}
	}
	return false
}

func bundleHasGeneration(bundle sessionrepo.EvidenceBundle, generation, treeDigest string) bool {
	if bundle.Baseline.ID == generation && bundle.Baseline.TreeDigest == treeDigest {
		return true
	}
	for _, retained := range bundle.Generations {
		if retained.ID == generation && retained.TreeDigest == treeDigest {
			return true
		}
	}
	return false
}
