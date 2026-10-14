//go:build linux

package main

import (
	"errors"
	"fmt"
	"reflect"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

var ErrProviderRecoveryRequired = errors.New("provider transcript recovery is required before constructing a fresh conversation")

// The constructor has no transcript restoration. Even a settled journal cannot
// be reused with reset history, generation and event counters.
func requireFreshProviderConversation(journal *audit.Journal) error {
	if journal == nil {
		return ErrProviderRecoveryRequired
	}
	trace, err := journal.Trace()
	if err != nil {
		return errors.Join(ErrProviderRecoveryRequired, fmt.Errorf("verify provider journal before fresh conversation: %w", err))
	}
	if len(trace.Records) != 0 || len(trace.Effects) != 0 {
		return ErrProviderRecoveryRequired
	}
	return nil
}

func (a *SessionOperationAuthority) NewProviderConversation(config providerbridge.Config) (*providerbridge.Conversation, error) {
	return a.newProviderConversation(config, providerbridge.NewFromEnvironment)
}

// newProviderConversation keeps the constructor's complete host admission and
// ledger checks in one place. The factory is package-private and exists only so
// offline subprocess tests can use the pinned development transport; production
// always calls NewFromEnvironment above.
func (a *SessionOperationAuthority) newProviderConversation(config providerbridge.Config,
	factory func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error)) (*providerbridge.Conversation, error) {
	if a == nil {
		return nil, ErrGovernedSessionEvidence
	}
	if factory == nil {
		return nil, ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	if a.conversation != nil {
		a.mu.Unlock()
		return nil, errors.New("governed provider conversation already exists")
	}
	providerJournal := a.providerJournal
	ledger, providerTokenUpperBound := a.workflowLedger, a.providerTokenUpperBound
	requireLedger := a.requireWorkflowLedger
	expectedGeneration, expectedTree := a.initialGeneration, a.initialTreeDigest
	ledgerPins := a.workflowLedgerPins
	if err := requireFreshProviderConversation(providerJournal); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	if config.InitialGenerationID != "" && config.InitialGenerationID != expectedGeneration ||
		config.InitialTreeDigest != "" && config.InitialTreeDigest != expectedTree {
		a.mu.Unlock()
		return nil, errors.New("provider conversation start differs from the verified current Store tip")
	}
	config.InitialGenerationID, config.InitialTreeDigest = expectedGeneration, expectedTree
	if config.WorkflowID != a.workflowID || config.ConversationID != a.conversationID || config.ProfileID != a.providerProfileID {
		a.mu.Unlock()
		return nil, errors.New("provider conversation identity differs from durable governed session")
	}
	if requireLedger && ledger == nil {
		a.mu.Unlock()
		return nil, workflowrecovery.ErrMissing
	}
	if ledger != nil {
		checkpoint, _ := ledger.Snapshot()
		if !reflect.DeepEqual(checkpoint.Pins, ledgerPins) || checkpoint.Pins.WorkflowID != a.workflowID ||
			checkpoint.Pins.SessionID != a.sessionID || checkpoint.Pins.RecoveryGeneration == 0 ||
			checkpoint.Pins.StoreTipGenerationID != expectedGeneration ||
			checkpoint.Pins.StoreTipTreeDigest != bareStoreTreeDigest(expectedTree) ||
			len(checkpoint.Pins.ProviderTranscripts) == 0 ||
			checkpoint.Pins.ProviderTranscripts[len(checkpoint.Pins.ProviderTranscripts)-1].ConversationID != a.conversationID {
			a.mu.Unlock()
			return nil, workflowrecovery.ErrObservation
		}
	}
	a.mu.Unlock()
	// Discard any caller callback. Only the validated workflow ledger owned by
	// this SessionOperationAuthority may reserve provider allowance.
	config.BeforeExchange = nil
	config.SelectCompletionTokenLimit = nil
	config.ReserveHostCall = nil
	config.CompleteHostCall = nil
	config.AdvanceProgress = nil
	if ledger != nil {
		if providerTokenUpperBound == 0 {
			return nil, ErrGovernedSessionEvidence
		}
		config.SelectCompletionTokenLimit = a.selectProviderCompletionLimit
		config.ReserveHostCall = a.reserveProviderHostCall
		config.CompleteHostCall = a.completeProviderHostCall
		config.AdvanceProgress = a.advanceProviderProgress
	}
	if err := a.requirePreparedSource(); err != nil {
		return nil, err
	}
	conversation, err := factory(config, providerJournal)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conversation != nil {
		conversation.Close()
		return nil, errors.New("governed provider conversation raced another constructor")
	}
	a.conversation = conversation
	a.providerConfig = config
	return conversation, nil
}

// AdmitPrompt must be called by trusted host/session-control before that exact
// prompt is forwarded into Pi. It confirms that the real DurableExecutor tip
// is the verified Store tip and that the D06 source/provenance is still valid;
// providerbridge then durably records user admission before entering history.
func (a *SessionOperationAuthority) AdmitPrompt(conversation *providerbridge.Conversation, durable *DurableExecutor,
	taskID, prompt string) error {
	if err := a.requirePreparedSource(); err != nil {
		return err
	}
	if conversation == nil || durable == nil {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	store, ownedConversation := a.store, a.conversation
	if a.requireWorkflowLedger {
		if len(a.workflowLedgerPins.ProviderTranscripts) == 0 ||
			(a.workflowLedgerPins.RecoveryGeneration == 1 &&
				(a.initialPrompt == "" || a.initialPromptTaskID == "" ||
					(!a.initialPromptAdmitted && (taskID != a.initialPromptTaskID || prompt != a.initialPrompt)))) ||
			(a.workflowLedgerPins.RecoveryGeneration > 1 &&
				(a.recoveryPrompt == "" || a.recoveryPromptTaskID == "" ||
					(!a.recoveryPromptAdmitted && (taskID != a.recoveryPromptTaskID || prompt != a.recoveryPrompt)))) {
			a.mu.Unlock()
			return errors.New("workflow session requires its exact host-admitted initial prompt")
		}
	}
	a.mu.Unlock()
	if durable.store != store || conversation != ownedConversation {
		return errors.New("prompt admission conversation/executor is not bound to this retained session Store")
	}
	verified, err := store.Verify()
	if err != nil {
		return err
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return err
	}
	tip := durable.Tip()
	if tip == nil || verified.SealedGeneration != tip.ID() || verified.SealedTreeDigest != tip.TreeDigest() ||
		len(bundle.Generations) == 0 || bundle.Generations[len(bundle.Generations)-1].ID != tip.ID() ||
		bundle.Generations[len(bundle.Generations)-1].TreeDigest != tip.TreeDigest() {
		return errors.New("prompt admission refused because DurableExecutor tip differs from verified Store tip")
	}
	if err := conversation.AdmitPrompt(taskID, prompt); err != nil {
		return fmt.Errorf("persist host-admitted prompt before Pi forwarding: %w", err)
	}
	a.mu.Lock()
	if a.requireWorkflowLedger {
		if a.workflowLedgerPins.RecoveryGeneration == 1 {
			a.initialPromptAdmitted = true
		} else if !a.recoveryPromptAdmitted {
			a.recoveryPromptAdmitted = true
		}
	}
	a.mu.Unlock()
	return nil
}
