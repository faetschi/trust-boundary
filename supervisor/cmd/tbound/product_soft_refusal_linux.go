//go:build linux

package main

import (
	"errors"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

func (e *productTurnEnv) verifyRetainedSoftRefusals(turn productsession.Turn, retained *retainedProductTurn, pins workflowrecovery.Pins) error {
	trace, err := retained.ProviderJournal.Trace()
	if err != nil {
		return err
	}
	hasRefusal := false
	for _, record := range trace.Records {
		if record.Event.Kind == providerbridge.SoftRefusalEvent || record.Event.Kind == providerbridge.CaptureRefusalEvent {
			hasRefusal = true
			break
		}
	}
	matched := false
	for _, transcript := range pins.ProviderTranscripts {
		if transcript.ConversationID != turn.Link.ConversationID {
			continue
		}
		if matched || transcript.JournalCount > uint64(len(trace.Records)) ||
			transcript.JournalCount == 0 && transcript.JournalHead != "" ||
			transcript.JournalCount > 0 && trace.Records[transcript.JournalCount-1].Hash != transcript.JournalHead {
			return errors.New("soft refusal journal differs from its protected checkpoint prefix")
		}
		matched = true
	}
	if !matched && hasRefusal {
		return errors.New("soft refusal has no protected provider checkpoint")
	}
	if !hasRefusal {
		return nil
	}
	if call := pins.HostCallReservation; call != nil && call.CapturedCall != nil && call.CapturedCall.Disposition == workflowrecovery.CapturedCallRetired {
		capture := call.CapturedCall
		digest, err := activeUnexecutedCaptureDigest(retained.ProviderJournal, capture.ToolCallID)
		if err != nil || digest != capture.RetirementDigest {
			return errors.New("retired call does not commit the retained refusal record")
		}
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
	config := providerbridge.Config{ProductCaptureRefusals: true, ProductSoftRefusals: true, WorkflowID: turn.Link.WorkflowID, ConversationID: turn.Link.ConversationID, ProfileID: session.ProviderProfile, InitialGenerationID: bundle.Baseline.ID, InitialTreeDigest: bundle.Baseline.TreeDigest, SystemPrompt: e.SystemPrompt, DeveloperPrompt: e.DeveloperPrompt}
	config.Route = config.Route.WithCommandOutput(true) // replay the request bytes a product turn sent
	route := config.Route.Resolved()
	replayPins := GovernedReplayPins{
		Provider: providerbridge.RecoveryPins{Config: config, Model: route.Model, Policy: policy, RequireHostCallReservations: true, ImportedPrefixDigest: importedPrefixReplayPin(turn.Link.ImportedPrefixDigest, len(trace.Records))},
		Profile:  broker.Profile{ID: session.ProviderProfile, Model: route.Model, Route: config.Route, Messages: providerbridge.InitialPromptMessages(route, e.SystemPrompt, e.DeveloperPrompt), ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer, ResponseIDIssuer: governedResponseIssuer, Generation: bundle.Baseline.ID},
		Baseline: bundle.Baseline, StorePolicyDigest: e.StorePolicyDigest, MetadataPolicyDigest: e.MetadataPolicyDigest, BashOutputPaths: productTurnBashScope(turn, policy), CommandOutputBytes: productCommandOutputBytes,
	}
	_, err = ReplayGovernedTranscript(retained.ProviderJournal, retained.StoreJournal, retained.Store, replayPins)
	return err
}
