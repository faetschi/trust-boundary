//go:build linux

package main

import (
	"errors"
	"fmt"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/productdriver"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

// productTurnChildResolved requires the child workflow to have no unresolved work: every Store
// effect attempt succeeded without quarantine, and no provider call or effect reservation is open
// in the checkpoint pins.
func productTurnChildResolved(bundle sessionrepo.EvidenceBundle, pins workflowrecovery.Pins) error {
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil {
		return err
	}
	if disposition.Quarantined || disposition.Unknowns != 0 || disposition.Successes != disposition.Attempts {
		return errors.New("Store evidence is quarantined or has an unresolved effect")
	}
	return productTurnReservationsResolved(pins)
}

func productTurnReservationsResolved(pins workflowrecovery.Pins) error {
	if effect := pins.EffectReservation; effect != nil && effect.Disposition != workflowrecovery.EffectReservationTerminal &&
		effect.Disposition != workflowrecovery.EffectReservationRetired {
		return errors.New("an effect reservation is unresolved")
	}
	if call := pins.HostCallReservation; call != nil && call.Disposition != workflowrecovery.HostCallTerminal {
		return errors.New("a provider host call is unresolved")
	}
	if pins.PendingCallDisposition == workflowrecovery.PendingCallCaptured || pins.PendingCallDisposition == workflowrecovery.PendingCallUnknown {
		return errors.New("a captured provider call is unresolved")
	}
	return nil
}

// predecessorContext rebuilds the history a turn imports. It reopens the predecessor's retained
// journals and Store, checks them against its receipt, and replays a context-eligible predecessor
// into a context ending in the boundary message; a predecessor that is not context-eligible adds
// only its boundary to the context it imported itself. Any failure wraps errProductTurnEvidence.
func (e *productTurnEnv) predecessorContext(turn productsession.Turn, providerProfile string) (providerbridge.PredecessorContext, error) {
	var none providerbridge.PredecessorContext
	if turn.PredecessorTurnID == "" {
		return none, nil
	}
	evidence := func(err error) (providerbridge.PredecessorContext, error) {
		return none, fmt.Errorf("%w: predecessor %s: %w", errProductTurnEvidence, turn.PredecessorTurnID, err)
	}
	pred, ok := e.Catalog.Turn(turn.PredecessorTurnID)
	// A turn refused before its link has no journals or receipt; its own refusal blocked the session
	// or project, so a successor cannot carry verified history across it.
	if !ok || pred.SessionID != turn.SessionID || pred.State != productsession.StateTerminal || pred.Link == nil ||
		pred.ReceiptDigest == "" || pred.ReceiptDigest != turn.PredecessorReceiptDigest {
		return evidence(errors.New("no settled, linked receipt"))
	}
	receipt, err := productdriver.DecodeReceipt(pred.Receipt)
	if err != nil {
		return evidence(err)
	}
	paths, err := productTurnPathsFromLink(*pred.Link)
	if err != nil {
		return evidence(err)
	}
	retained, err := openRetainedProductTurn(paths, e.storeOptions(pred), e.OpenCommandHistory)
	if err != nil {
		return evidence(err)
	}
	defer retained.Close()
	if err := verifyProductTurnReceiptJournals(receipt, retained.ProviderJournal, retained.StoreJournal, retained.CommandJournal); err != nil {
		return evidence(err)
	}
	boundary := providerbridge.TurnBoundary{PredecessorReceiptDigest: pred.ReceiptDigest, Outcome: pred.Outcome,
		ProjectBaselineDigest: delta.TreeDigestProfile + bareWorkflowDigest(turn.BaselineDigest)}
	if !pred.ContextEligible {
		previous, err := e.predecessorContext(pred, providerProfile)
		if err != nil {
			return none, err
		}
		if previous.Digest() != pred.Link.ImportedPrefixDigest {
			return evidence(errors.New("rebuilt context differs from the imported prefix it linked"))
		}
		return providerbridge.ExtendPredecessorContext(previous, boundary)
	}
	policy, err := e.gatePolicy(pred.ProjectID, pred.PolicyDigest)
	if err != nil {
		return evidence(err)
	}
	bundle, err := retained.Store.EvidenceBundle()
	if err != nil {
		return evidence(err)
	}
	if sha256DigestFromBarePin(bareStoreTreeDigest(bundle.Baseline.TreeDigest)) != receipt.BaselineDigest {
		return evidence(errors.New("retained Store baseline differs from the receipt"))
	}
	providerTrace, err := retained.ProviderJournal.Trace()
	if err != nil {
		return evidence(err)
	}
	config := providerbridge.Config{ProductCaptureRefusals: true, ProductSoftRefusals: true, WorkflowID: pred.Link.WorkflowID, ConversationID: pred.Link.ConversationID,
		ProfileID: providerProfile, InitialGenerationID: bundle.Baseline.ID, InitialTreeDigest: bundle.Baseline.TreeDigest,
		SystemPrompt: e.SystemPrompt, DeveloperPrompt: e.DeveloperPrompt}
	config.Route = config.Route.WithCommandOutput(true) // a product turn's executor releases bounded Bash output
	route := config.Route.Resolved()
	pins := GovernedReplayPins{
		Provider: providerbridge.RecoveryPins{Config: config, Model: route.Model, Policy: policy, RequireHostCallReservations: true,
			ImportedPrefixDigest: importedPrefixReplayPin(pred.Link.ImportedPrefixDigest, len(providerTrace.Records))},
		Profile: broker.Profile{ID: providerProfile, Model: route.Model, Route: config.Route,
			Messages:     providerbridge.InitialPromptMessages(route, e.SystemPrompt, e.DeveloperPrompt),
			ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
			ResponseIDIssuer: governedResponseIssuer, Generation: bundle.Baseline.ID},
		Baseline: bundle.Baseline, StorePolicyDigest: e.StorePolicyDigest, MetadataPolicyDigest: e.MetadataPolicyDigest,
		BashOutputPaths:    productTurnBashScope(pred, policy),
		CommandOutputBytes: productCommandOutputBytes,
	}
	var commands []governedRecoveryCommandVerifier
	if retained.Commands != nil {
		commands = append(commands, retained.Commands)
	}
	replayed, _, err := ReplayGovernedPredecessorContext(retained.ProviderJournal, retained.StoreJournal, retained.Store, pins, boundary, commands...)
	if err == nil && retained.Commands != nil {
		err = retained.Commands.VerifyUnchanged()
	}
	if err != nil {
		return evidence(err)
	}
	return replayed, nil
}
