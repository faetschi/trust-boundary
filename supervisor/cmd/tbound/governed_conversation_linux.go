//go:build linux

package main

import (
	"errors"
	"fmt"

	"tbound/supervisor/internal/providerbridge"
)

func (a *SessionOperationAuthority) NewProviderConversation(config providerbridge.Config) (*providerbridge.Conversation, error) {
	if err := a.requirePreparedSource(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	config.InitialGenerationID, config.InitialTreeDigest = a.initialGeneration, a.initialTreeDigest
	if config.WorkflowID != a.workflowID || config.ConversationID != a.conversationID || config.ProfileID != a.providerProfileID {
		a.mu.Unlock()
		return nil, errors.New("provider conversation identity differs from durable governed session")
	}
	if a.conversation != nil {
		a.mu.Unlock()
		return nil, errors.New("governed provider conversation already exists")
	}
	a.mu.Unlock()
	conversation, err := providerbridge.NewFromEnvironment(config, a.providerJournal)
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
	return nil
}
