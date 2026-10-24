//go:build linux

package productsession

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

// ReceiptSchema is the product turn receipt the composition writes and the
// catalog checks before it settles a turn.
const ReceiptSchema = "tbound-product-turn-receipt/v1"

// Worker kinds and the settlement each one can report. A headless fixture is
// the in-process deterministic driver: it has no OS process to settle, so its
// receipt never claims a STOPPED settlement.
const (
	WorkerHeadlessFixture     = "headless-fixture"
	WorkerPi                  = "pi"
	SettlementHeadlessClosed  = "headless-fixture-closed"
	SettlementStopped         = "stopped"
	receiptOutcomeAwaitReview = "awaiting_review"
	maxReceiptListLen         = 4096
)

var workerSettlement = map[string]string{
	WorkerHeadlessFixture: SettlementHeadlessClosed,
	WorkerPi:              SettlementStopped,
}

// TurnReceipt is the terminal evidence of one turn. It commits to the turn's
// retained provider and Store journals, which the next turn re-verifies.
type TurnReceipt struct {
	SchemaVersion            string          `json:"schema_version"`
	SessionID                string          `json:"session_id"`
	TurnID                   string          `json:"turn_id"`
	Ordinal                  int             `json:"ordinal"`
	WorkflowID               string          `json:"workflow_id"`
	RunID                    string          `json:"run_id"`
	ConversationID           string          `json:"conversation_id"`
	WorkerKind               string          `json:"worker_kind"`
	WorkerInstance           string          `json:"worker_instance"`
	ProviderJournalHead      string          `json:"provider_journal_head"`
	ProviderJournalCount     uint64          `json:"provider_journal_count"`
	StoreJournalHead         string          `json:"store_journal_head"`
	StoreJournalCount        uint64          `json:"store_journal_count"`
	ImportedPrefixDigest     string          `json:"imported_prefix_digest"`
	PredecessorReceiptDigest string          `json:"predecessor_receipt_digest"`
	BaselineDigest           string          `json:"baseline_digest"`
	ResultBaselineDigest     string          `json:"result_baseline_digest"`
	Outcome                  string          `json:"outcome"`
	Settlement               string          `json:"settlement"`
	PublicationTransactionID string          `json:"publication_transaction_id,omitempty"`
	Usage                    Budget          `json:"usage"`
	ToolSequence             []string        `json:"tool_sequence"`
	Denials                  []ReceiptDenial `json:"denials"`
}

// ReceiptDenial names one denied tool call of the turn.
type ReceiptDenial struct {
	ToolCallID string `json:"tool_call_id"`
	Tool       string `json:"tool"`
	ReasonCode string `json:"reason_code"`
}

// DecodeReceipt strictly decodes a receipt: strict JSON within MaxReceiptBytes,
// no unknown fields, the v1 schema.
func DecodeReceipt(data []byte) (TurnReceipt, error) {
	var r TurnReceipt
	if !validReceipt(data) {
		return r, fail(CodeInvalidRequest, "receipt is not strict JSON within its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil || r.SchemaVersion != ReceiptSchema {
		return TurnReceipt{}, fail(CodeInvalidRequest, "receipt does not match its schema")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return TurnReceipt{}, fail(CodeInvalidRequest, "receipt has trailing data")
	}
	return r, nil
}

// checkReceipt requires a receipt that describes exactly this linked turn and
// the record that settles it.
func checkReceipt(t *Turn, data []byte, outcome, settlement string, usage Budget, resultBaseline string) error {
	r, err := DecodeReceipt(data)
	if err != nil {
		return err
	}
	l := t.Link
	mismatch := func(field string) error {
		return fail(CodeInvalidRequest, fmt.Sprintf("receipt %s does not match the turn", field))
	}
	switch {
	case r.SessionID != t.SessionID || r.TurnID != t.ID || r.Ordinal != t.Ordinal:
		return mismatch("identity")
	case l == nil || r.WorkflowID != l.WorkflowID || r.RunID != l.RunID || r.ConversationID != l.ConversationID ||
		r.ImportedPrefixDigest != l.ImportedPrefixDigest:
		return mismatch("workflow link")
	case r.PredecessorReceiptDigest != t.PredecessorReceiptDigest || r.BaselineDigest != t.BaselineDigest ||
		r.ResultBaselineDigest != resultBaseline:
		return mismatch("baseline or predecessor")
	case r.Outcome != outcome || r.Settlement != settlement || r.Usage != usage:
		return mismatch("outcome, settlement or usage")
	case workerSettlement[r.WorkerKind] != r.Settlement || !validIdentity(r.WorkerInstance):
		return mismatch("worker settlement")
	case !isLowerHex(r.ProviderJournalHead) || len(r.ProviderJournalHead) != 64 || r.ProviderJournalCount == 0 ||
		!isLowerHex(r.StoreJournalHead) || len(r.StoreJournalHead) != 64 || r.StoreJournalCount == 0:
		return mismatch("journal commitment")
	case (outcome == OutcomePublished) != (r.PublicationTransactionID != "") ||
		r.PublicationTransactionID != "" && !validIdentity(r.PublicationTransactionID):
		return mismatch("publication transaction")
	case len(r.ToolSequence) > maxReceiptListLen || len(r.Denials) > maxReceiptListLen ||
		slices.ContainsFunc(r.ToolSequence, func(s string) bool { return !validIdentity(s) }) ||
		slices.ContainsFunc(r.Denials, func(d ReceiptDenial) bool {
			return !validIdentity(d.ToolCallID) || !validIdentity(d.Tool) || !validIdentity(d.ReasonCode)
		}):
		return mismatch("tool sequence or denials")
	}
	return nil
}
