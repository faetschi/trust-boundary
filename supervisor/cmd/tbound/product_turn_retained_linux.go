//go:build linux

package main

import (
	"errors"
	"fmt"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/sessionrepo"
)

// productTurnPaths names the retained evidence of one finished product turn. CommandJournalPath and
// Link are empty for evidence that has no command journal.
type productTurnPaths struct {
	StoreRoot           string
	StoreJournalPath    string
	ProviderJournalPath string
	CommandJournalPath  string
	Link                productsession.TurnLink
}

// retainedProductTurn holds a finished turn's Store and audit journals, opened without repair, for
// ReplayGovernedPredecessorContext in a fresh process. CommandJournal and Commands are nil for
// evidence without a command journal, and Commands is nil without a history opener.
type retainedProductTurn struct {
	Store           *sessionrepo.Store
	StoreJournal    *audit.Journal
	ProviderJournal *audit.Journal
	CommandJournal  *audit.Journal
	Commands        governedRecoveryCommandVerifier
	closers         []func() error
}

// openRetainedProductTurn opens existing evidence only: it creates no journal or directory, and every
// Store operation and transition is checked against its durable authorization in the retained Store
// journal, never against a live authority. options carries the turn's Store policy, limits, xattr
// profile and admission scope; its journal is replaced and its callbacks are unused. A retained Bash
// effect is verified against the command settlements the turn retained, through openHistory; without
// one the historical verifier refuses the Store.
func openRetainedProductTurn(paths productTurnPaths, options sessionrepo.Options,
	openHistory productCommandHistoryOpener) (*retainedProductTurn, error) {
	turn := &retainedProductTurn{}
	fail := func(err error) (*retainedProductTurn, error) { return nil, errors.Join(err, turn.Close()) }
	providerJournal, err := openExistingRecoveryJournal(paths.ProviderJournalPath)
	if err != nil {
		return fail(fmt.Errorf("open retained provider journal: %w", err))
	}
	turn.closers = append(turn.closers, providerJournal.Close)
	storeJournal, err := openExistingRecoveryJournal(paths.StoreJournalPath)
	if err != nil {
		return fail(fmt.Errorf("open retained Store journal: %w", err))
	}
	turn.closers = append(turn.closers, storeJournal.Close)
	if paths.CommandJournalPath != "" {
		if turn.CommandJournal, err = openExistingRecoveryJournal(paths.CommandJournalPath); err != nil {
			return fail(fmt.Errorf("open retained command journal: %w", err))
		}
		turn.closers = append(turn.closers, turn.CommandJournal.Close)
		if openHistory != nil {
			if turn.Commands, err = openHistory(turn.CommandJournal, paths.Link); err != nil {
				return fail(fmt.Errorf("open retained command settlements: %w", err))
			}
		}
	}
	root, err := openExistingPrivateRecoveryDirectory(paths.StoreRoot)
	if err != nil {
		return fail(fmt.Errorf("open retained Store root: %w", err))
	}
	var settlement historicalSettlementVerifier
	if turn.Commands != nil {
		settlement = turn.Commands.VerifySettlement
	}
	historical, err := newGovernedHistoricalStoreVerifier(storeJournal, options.PolicyDigest, options.MetadataPolicyDigest, settlement)
	if err != nil {
		_ = root.Close()
		return fail(fmt.Errorf("index retained Store authorizations: %w", err))
	}
	options.Journal, options.ExposureBinder = storeJournal, nil
	// The preview repairs nothing and checks every effect against its retained authorization record.
	store, err := sessionrepo.OpenRecoveryPreview(root, options)
	if closeErr := root.Close(); err == nil && closeErr != nil {
		_ = store.Close()
		err = closeErr
	}
	if err != nil {
		return fail(fmt.Errorf("open retained Store: %w", err))
	}
	turn.closers = append(turn.closers, store.Close)
	bundle, err := store.EvidenceBundle()
	if err == nil {
		err = historical.VerifyBundle(bundle)
	}
	if err == nil && turn.Commands != nil {
		err = turn.Commands.VerifyStoreBundle(bundle)
	}
	if err != nil {
		return fail(fmt.Errorf("verify retained Store authorizations: %w", err))
	}
	turn.Store, turn.StoreJournal, turn.ProviderJournal = store, storeJournal, providerJournal
	return turn, nil
}

// Close closes everything openRetainedProductTurn opened, newest first.
func (t *retainedProductTurn) Close() error {
	var errs []error
	for index := len(t.closers) - 1; index >= 0; index-- {
		errs = append(errs, t.closers[index]())
	}
	t.closers = nil
	return errors.Join(errs...)
}

// productD09CommandHistory is the opener of a production turn: the research recovery verifier of D09
// command receipts over the turn's retained command journal, bound to the turn's workflow, run,
// session and conversation. The journal is pinned as the host holds it, and the caller checks that
// pin against the receipt's committed head and count.
func productD09CommandHistory(profile piruntime.HostProfile, template piruntime.D09CellTemplate) productCommandHistoryOpener {
	return func(journal *audit.Journal, link productsession.TurnLink) (governedRecoveryCommandVerifier, error) {
		trace, err := journal.Trace()
		if err != nil {
			return nil, err
		}
		identity, err := recoveryFileIdentityDigest(link.CommandJournalPath, false)
		if err != nil {
			return nil, err
		}
		history, err := newProductionHistoricalBashHistory([]productionRecoveryCommandJournal{{Journal: journal, Path: link.CommandJournalPath,
			Pin:      productionHistoricalCommandPin{IdentityDigest: identity, Head: auditHeadHash(trace.Records), RecordCount: uint64(len(trace.Records))},
			Sessions: []productionRecoveryCommandSession{{RunID: link.RunID, SessionID: link.SessionID, ConversationID: link.ConversationID}}}},
			profile, template, link.WorkflowID, map[string]struct{}{link.ConversationID: {}})
		if err != nil {
			return nil, err
		}
		return history, nil
	}
}

// importedPrefixReplayPin is RecoveryPins.ImportedPrefixDigest for replaying a provider journal of
// records entries: a conversation's import record opens every non-empty journal, and an empty journal
// has no record to pin yet.
func importedPrefixReplayPin(digest string, records int) string {
	if records == 0 {
		return ""
	}
	return digest
}
