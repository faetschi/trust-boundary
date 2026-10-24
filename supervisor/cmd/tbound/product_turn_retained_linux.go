//go:build linux

package main

import (
	"errors"
	"fmt"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/sessionrepo"
)

// productTurnPaths names the retained evidence of one finished product turn.
type productTurnPaths struct {
	StoreRoot           string
	StoreJournalPath    string
	ProviderJournalPath string
}

// retainedProductTurn holds a finished turn's Store and audit journals, opened without repair, for
// ReplayGovernedPredecessorContext in a fresh process.
type retainedProductTurn struct {
	Store           *sessionrepo.Store
	StoreJournal    *audit.Journal
	ProviderJournal *audit.Journal
	closers         []func() error
}

// openRetainedProductTurn opens existing evidence only: it creates no journal or directory, and every
// Store operation and transition is checked against its durable authorization in the retained Store
// journal, never against a live authority. options carries the turn's Store policy, limits, xattr
// profile and admission scope; its journal is replaced and its callbacks are unused.
func openRetainedProductTurn(paths productTurnPaths, options sessionrepo.Options) (*retainedProductTurn, error) {
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
	root, err := openExistingPrivateRecoveryDirectory(paths.StoreRoot)
	if err != nil {
		return fail(fmt.Errorf("open retained Store root: %w", err))
	}
	// A Bash turn needs its old command-settlement verifier, which this opener does not have; the
	// historical verifier refuses such a Store.
	historical, err := newGovernedHistoricalStoreVerifier(storeJournal, options.PolicyDigest, options.MetadataPolicyDigest, nil)
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

// importedPrefixReplayPin is RecoveryPins.ImportedPrefixDigest for replaying a provider journal of
// records entries: a conversation's import record opens every non-empty journal, and an empty journal
// has no record to pin yet.
func importedPrefixReplayPin(digest string, records int) string {
	if records == 0 {
		return ""
	}
	return digest
}
