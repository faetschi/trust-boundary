//go:build linux

package main

import (
	"errors"
	"fmt"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
)

// errProductTurnEvidence marks retained predecessor evidence that differs from its receipt. The turn
// driver refuses the next turn's launch with productsession.CodePredecessorEvidenceInvalid.
var errProductTurnEvidence = errors.New("retained product turn evidence differs from its receipt")

// verifyProductTurnReceiptJournals checks that the retained provider, Store and command journals
// verify to exactly the heads and record counts the receipt commits. A nil command journal commits
// nothing, so it matches only a receipt that commits no command journal.
func verifyProductTurnReceiptJournals(receipt productsession.TurnReceipt, providerJournal, storeJournal, commandJournal *audit.Journal) error {
	if providerJournal == nil || storeJournal == nil {
		return errProductTurnEvidence
	}
	type committed struct {
		name    string
		held    *audit.Journal
		head    string
		records uint64
	}
	journals := []committed{
		{"provider", providerJournal, receipt.ProviderJournalHead, receipt.ProviderJournalCount},
		{"Store", storeJournal, receipt.StoreJournalHead, receipt.StoreJournalCount},
	}
	if commandJournal != nil {
		journals = append(journals, committed{"command", commandJournal, receipt.CommandJournalHead, receipt.CommandJournalCount})
	} else if receipt.CommandJournalCount != 0 {
		return fmt.Errorf("%w: command journal is not retained", errProductTurnEvidence)
	}
	for _, journal := range journals {
		trace, err := journal.held.Trace()
		if err != nil {
			return errors.Join(errProductTurnEvidence, err)
		}
		if uint64(len(trace.Records)) != journal.records || auditHeadHash(trace.Records) != journal.head {
			return fmt.Errorf("%w: %s journal head or record count", errProductTurnEvidence, journal.name)
		}
	}
	return nil
}
