//go:build linux

package main

import (
	"errors"
	"reflect"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

func productPreflightInput(proposal protocol.Proposal) (sessionrepo.ProductPreflight, error) {
	input := sessionrepo.ProductPreflight{Tool: proposal.Tool}
	var err error
	switch proposal.Tool {
	case "read":
		input.Path, err = durableReadArguments(proposal.Arguments)
		input.MaxBytes = durableReadMaxBytes
	case "write":
		input.Path, input.Content, err = durableWriteArguments(proposal.Arguments)
	case "edit":
		input.Path, input.Edits, err = durableEditArguments(proposal.Arguments)
	default:
		err = errors.New("tool has no product preflight")
	}
	return input, err
}

func (e *DurableExecutor) productPreflight(proposal protocol.Proposal) (*sessionrepo.PreflightRefusal, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.quarantined || e.tip == nil {
		return nil, sessionrepo.ErrQuarantined
	}
	input, err := productPreflightInput(proposal)
	if err != nil {
		return nil, err
	}
	return e.store.CheckProductPreflight(e.tip.ID(), e.tip.TreeDigest(), input)
}

func (v *governedStoreReplayVerifier) VerifySoftRefusal(binding providerbridge.SoftRefusalBinding) error {
	e := binding.Evidence
	if !v.pins.Provider.Config.ProductSoftRefusals || v.store == nil || e.WorkflowID != v.pins.Provider.Config.WorkflowID || e.ConversationID != v.pins.Provider.Config.ConversationID || e.Generation != v.providerTipGeneration || e.TreeDigest != v.providerTipTree {
		return errors.New("soft refusal is outside the product Store lineage")
	}
	input, err := productPreflightInput(binding.Proposal)
	if err != nil {
		return err
	}
	refusal, err := v.store.CheckProductPreflight(e.Generation, e.TreeDigest, input)
	if err != nil {
		return err
	}
	want := &sessionrepo.PreflightRefusal{ArgumentDigest: e.TypedArgumentDigest, Code: e.Code, Hint: e.Hint}
	if refusal == nil || !reflect.DeepEqual(refusal, want) {
		return errors.New("soft refusal differs from recomputed sealed-generation preflight")
	}
	for _, record := range v.authByEffect {
		var auth governedOperationAdmissionRecord
		if decodeCanonicalGoverned(record.Event.Data, &auth) != nil {
			return errors.New("invalid Store authorization")
		}
		if auth.ConversationID == e.ConversationID && auth.GateDecision.ToolCallID == e.ToolCallID {
			return errors.New("proven-unexecuted call has Store authority")
		}
	}
	v.called = true
	return nil
}

func (v capturedCallProviderReplayVerifier) VerifySoftRefusal(binding providerbridge.SoftRefusalBinding) error {
	if binding.Record.Sequence <= v.prefixCount {
		return nil
	}
	return errors.New("soft refusal is outside the verified checkpoint prefix")
}

func activeUnexecutedCaptureDigest(journal *audit.Journal, toolCallID string) (string, error) {
	trace, err := journal.Trace()
	if err != nil {
		return "", err
	}
	var refusal *audit.Record
	for _, record := range trace.Records {
		if record.Event.Kind != providerbridge.SoftRefusalEvent {
			continue
		}
		var e providerbridge.SoftRefusalEvidence
		if decodeCanonicalGoverned(record.Event.Data, &e) != nil {
			return "", workflowrecovery.ErrObservation
		}
		if e.ToolCallID != toolCallID {
			continue
		}
		if refusal != nil {
			return "", workflowrecovery.ErrObservation
		}
		copy := record
		refusal = &copy
	}
	if refusal != nil {
		return providerbridge.SoftRefusalRetirementDigest(*refusal)
	}
	return activeDeniedCaptureDigest(journal, toolCallID)
}

func (e *productTurnEnv) verifyRetainedSoftRefusals(turn productsession.Turn, retained *retainedProductTurn) error {
	trace, err := retained.ProviderJournal.Trace()
	if err != nil {
		return err
	}
	hasRefusal := false
	for _, record := range trace.Records {
		if record.Event.Kind == providerbridge.SoftRefusalEvent {
			hasRefusal = true
			break
		}
	}
	if !hasRefusal {
		return nil
	}
	session, err := e.Catalog.Session(turn.Principal, turn.SessionID)
	if err != nil {
		return err
	}
	policy, err := e.gatePolicy(turn.ProjectID, turn.PolicyDigest)
	if err != nil {
		return err
	}
	bundle, err := retained.Store.EvidenceBundle()
	if err != nil {
		return err
	}
	config := providerbridge.Config{ProductSoftRefusals: true, WorkflowID: turn.Link.WorkflowID, ConversationID: turn.Link.ConversationID, ProfileID: session.ProviderProfile, InitialGenerationID: bundle.Baseline.ID, InitialTreeDigest: bundle.Baseline.TreeDigest, SystemPrompt: e.SystemPrompt, DeveloperPrompt: e.DeveloperPrompt}
	route := config.Route.Resolved()
	pins := GovernedReplayPins{
		Provider: providerbridge.RecoveryPins{Config: config, Model: route.Model, Policy: policy, RequireHostCallReservations: true, ImportedPrefixDigest: importedPrefixReplayPin(turn.Link.ImportedPrefixDigest, len(trace.Records))},
		Profile:  broker.Profile{ID: session.ProviderProfile, Model: route.Model, Route: config.Route, Messages: providerbridge.InitialPromptMessages(route, e.SystemPrompt, e.DeveloperPrompt), ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer, ResponseIDIssuer: governedResponseIssuer, Generation: bundle.Baseline.ID},
		Baseline: bundle.Baseline, StorePolicyDigest: e.StorePolicyDigest, MetadataPolicyDigest: e.MetadataPolicyDigest, BashOutputPaths: productTurnBashScope(turn, policy), CommandOutputBytes: productCommandOutputBytes,
	}
	_, err = ReplayGovernedTranscript(retained.ProviderJournal, retained.StoreJournal, retained.Store, pins)
	return err
}
