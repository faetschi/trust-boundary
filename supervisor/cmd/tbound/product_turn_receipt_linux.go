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

const (
	productTurnReceiptSchema = "tbound-product-turn-receipt/v1"
	// productWorkerHeadlessFixture is the only worker of milestone 1: the in-process deterministic driver.
	productWorkerHeadlessFixture = "headless-fixture"
)

// errProductTurnEvidence marks retained predecessor evidence that differs from its receipt. The turn
// driver refuses the next turn's launch with productsession.CodePredecessorEvidenceInvalid.
var errProductTurnEvidence = errors.New("retained product turn evidence differs from its receipt")

// productTurnReceipt is the receipt cmd/tbound writes into product_turn_terminal. It commits the child
// journals by verified head and record count, so the next turn can check them before importing history.
type productTurnReceipt struct {
	SchemaVersion            string                `json:"schema_version"`
	SessionID                string                `json:"session_id"`
	TurnID                   string                `json:"turn_id"`
	Ordinal                  int                   `json:"ordinal"`
	WorkflowID               string                `json:"workflow_id"`
	RunID                    string                `json:"run_id"`
	ConversationID           string                `json:"conversation_id"`
	WorkerKind               string                `json:"worker_kind"`
	WorkerInstance           string                `json:"worker_instance"`
	ProviderJournalHead      string                `json:"provider_journal_head"`
	ProviderJournalCount     uint64                `json:"provider_journal_count"`
	StoreJournalHead         string                `json:"store_journal_head"`
	StoreJournalCount        uint64                `json:"store_journal_count"`
	ImportedPrefixDigest     string                `json:"imported_prefix_digest"`
	PredecessorReceiptDigest string                `json:"predecessor_receipt_digest"`
	BaselineDigest           string                `json:"baseline_digest"`
	ResultBaselineDigest     string                `json:"result_baseline_digest"`
	Outcome                  string                `json:"outcome"`
	Settlement               string                `json:"settlement"`
	PublicationTransactionID string                `json:"publication_transaction_id"`
	Usage                    productsession.Budget `json:"usage"`
	ToolSequence             []string              `json:"tool_sequence"`
	Denials                  []productTurnDenial   `json:"denials"`
}

// productTurnDenial is one DENY the turn released to the provider.
type productTurnDenial struct {
	ToolCallID string `json:"tool_call_id"`
	Tool       string `json:"tool"`
	ReasonCode string `json:"reason_code"`
}

var productTurnReceiptOutcomes = map[string]bool{
	productsession.OutcomePublished: true, productsession.OutcomeNoDiff: true,
	productsession.OutcomeFailed: true, "awaiting_review": true,
}

func encodeProductTurnReceipt(receipt productTurnReceipt) ([]byte, error) {
	if err := receipt.validate(); err != nil {
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
	if err := receipt.validate(); err != nil {
		return productTurnReceipt{}, err
	}
	return receipt, nil
}

func (r productTurnReceipt) validate() error {
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
	case r.WorkerKind != productWorkerHeadlessFixture:
		return errors.New("product turn receipt claims a worker this milestone does not run")
	case r.ProviderJournalCount == 0 || !validBareWorkflowDigest(r.ProviderJournalHead) ||
		r.StoreJournalCount == 0 || !validBareWorkflowDigest(r.StoreJournalHead):
		return errors.New("product turn receipt does not commit both child journals")
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

// verifyProductTurnReceiptJournals checks that the retained provider and Store journals verify to
// exactly the heads and record counts the receipt commits.
func verifyProductTurnReceiptJournals(receipt productTurnReceipt, providerJournal, storeJournal *audit.Journal) error {
	if providerJournal == nil || storeJournal == nil {
		return errProductTurnEvidence
	}
	for _, journal := range []struct {
		name    string
		held    *audit.Journal
		head    string
		records uint64
	}{
		{"provider", providerJournal, receipt.ProviderJournalHead, receipt.ProviderJournalCount},
		{"Store", storeJournal, receipt.StoreJournalHead, receipt.StoreJournalCount},
	} {
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
