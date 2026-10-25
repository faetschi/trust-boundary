//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
)

const productTurnReceiptSchema = productsession.ReceiptSchema

// errProductTurnEvidence marks retained predecessor evidence that differs from its receipt. The turn
// driver refuses the next turn's launch with productsession.CodePredecessorEvidenceInvalid.
var errProductTurnEvidence = errors.New("retained product turn evidence differs from its receipt")

// productTurnReceipt is the receipt cmd/tbound writes into product_turn_terminal; the catalog checks
// it against the turn before it settles. It commits the child journals by verified head and record
// count, so the next turn can check them before importing history.
type productTurnReceipt = productsession.TurnReceipt

// productTurnDenial is one DENY the turn released to the provider.
type productTurnDenial = productsession.ReceiptDenial

var productTurnReceiptOutcomes = map[string]bool{
	productsession.OutcomePublished: true, productsession.OutcomeNoDiff: true,
	productsession.OutcomeFailed: true, productsession.OutcomeDiscarded: true, productsession.OutcomeCancelled: true,
	"awaiting_review": true,
}

func encodeProductTurnReceipt(receipt productTurnReceipt) ([]byte, error) {
	if err := validateProductTurnReceipt(receipt); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if len(encoded) > productsession.MaxReceiptBytes {
		return nil, errors.New("product turn receipt exceeds its size bound")
	}
	return encoded, nil
}

func decodeProductTurnReceipt(raw []byte) (productTurnReceipt, error) {
	var receipt productTurnReceipt
	if len(raw) > productsession.MaxReceiptBytes {
		return receipt, errors.New("product turn receipt exceeds its size bound")
	}
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return receipt, fmt.Errorf("product turn receipt is not strict JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return productTurnReceipt{}, fmt.Errorf("decode product turn receipt: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return productTurnReceipt{}, errors.New("product turn receipt has trailing data")
	}
	if err := validateProductTurnReceipt(receipt); err != nil {
		return productTurnReceipt{}, err
	}
	return receipt, nil
}

// validateProductTurnReceipt checks a receipt on its own; the catalog checks it against the turn.
func validateProductTurnReceipt(r productTurnReceipt) error {
	identities := []string{r.SessionID, r.TurnID, r.WorkflowID, r.RunID, r.ConversationID, r.WorkerInstance, r.Settlement}
	for _, identity := range identities {
		if identity == "" {
			return errors.New("product turn receipt has an empty identity")
		}
	}
	switch {
	case r.SchemaVersion != productTurnReceiptSchema:
		return errors.New("product turn receipt has an unknown schema")
	case r.Ordinal < 1 || r.TurnID != fmt.Sprintf("%s/turn-%d", r.SessionID, r.Ordinal):
		return errors.New("product turn receipt turn ID does not match its session and ordinal")
	case r.WorkerKind != productsession.WorkerHeadlessFixture ||
		r.Settlement != productsession.SettlementHeadlessClosed && r.Settlement != productsession.SettlementReconciledAfterRestart:
		return errors.New("product turn receipt claims a worker or settlement this milestone does not run")
	case r.ProviderJournalCount == 0 || !validBareWorkflowDigest(r.ProviderJournalHead) ||
		r.StoreJournalCount == 0 || !validBareWorkflowDigest(r.StoreJournalHead):
		return errors.New("product turn receipt does not commit both child journals")
	case (r.CommandJournalCount == 0) != (r.CommandJournalHead == "") ||
		r.CommandJournalHead != "" && !validBareWorkflowDigest(r.CommandJournalHead):
		return errors.New("product turn receipt has an incomplete or invalid command journal commitment")
	case (r.ImportedPrefixDigest == "") != (r.PredecessorReceiptDigest == "") ||
		r.ImportedPrefixDigest != "" && (!validDigestText(r.ImportedPrefixDigest) || !validDigestText(r.PredecessorReceiptDigest)):
		return errors.New("product turn receipt has an incomplete or invalid predecessor commitment")
	case !validDigestText(r.BaselineDigest) || !validDigestText(r.ResultBaselineDigest):
		return errors.New("product turn receipt has an invalid baseline digest")
	case !productTurnReceiptOutcomes[r.Outcome]:
		return errors.New("product turn receipt has an unknown outcome")
	case r.Usage.HostCalls < 0 || r.Usage.Tokens < 0 || r.Usage.DurationSeconds < 0:
		return errors.New("product turn receipt has negative usage")
	case r.ToolSequence == nil || r.Denials == nil:
		return errors.New("product turn receipt omits its tool sequence or denials")
	}
	return nil
}

// verifyProductTurnReceiptJournals checks that the retained provider, Store and command journals
// verify to exactly the heads and record counts the receipt commits. A nil command journal commits
// nothing, so it matches only a receipt that commits no command journal.
func verifyProductTurnReceiptJournals(receipt productTurnReceipt, providerJournal, storeJournal, commandJournal *audit.Journal) error {
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
