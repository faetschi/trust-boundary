//go:build linux

package productdriver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
)

var receiptOutcomes = map[string]bool{
	productsession.OutcomePublished: true, productsession.OutcomeNoDiff: true,
	productsession.OutcomeFailed: true, productsession.OutcomeDiscarded: true, productsession.OutcomeCancelled: true,
	productsession.OutcomeAwaitingReview: true,
}

func EncodeReceipt(receipt productsession.TurnReceipt) ([]byte, error) {
	if err := validateReceipt(receipt); err != nil {
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

func DecodeReceipt(raw []byte) (productsession.TurnReceipt, error) {
	var receipt productsession.TurnReceipt
	if len(raw) > productsession.MaxReceiptBytes {
		return receipt, errors.New("product turn receipt exceeds its size bound")
	}
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return receipt, fmt.Errorf("product turn receipt is not strict JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return productsession.TurnReceipt{}, fmt.Errorf("decode product turn receipt: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return productsession.TurnReceipt{}, errors.New("product turn receipt has trailing data")
	}
	if err := validateReceipt(receipt); err != nil {
		return productsession.TurnReceipt{}, err
	}
	return receipt, nil
}

// validateReceipt checks a receipt on its own; the catalog checks it against the turn.
func validateReceipt(r productsession.TurnReceipt) error {
	identities := []string{r.SessionID, r.TurnID, r.WorkflowID, r.RunID, r.ConversationID, r.WorkerInstance, r.Settlement}
	for _, identity := range identities {
		if identity == "" {
			return errors.New("product turn receipt has an empty identity")
		}
	}
	switch {
	case r.SchemaVersion != productsession.ReceiptSchema:
		return errors.New("product turn receipt has an unknown schema")
	case r.Ordinal < 1 || r.TurnID != fmt.Sprintf("%s/turn-%d", r.SessionID, r.Ordinal):
		return errors.New("product turn receipt turn ID does not match its session and ordinal")
	case r.WorkerKind != productsession.WorkerSupervisorLoop ||
		r.Settlement != productsession.SettlementSupervisorLoopClosed && r.Settlement != productsession.SettlementReconciledAfterRestart:
		return errors.New("product turn receipt claims a worker or settlement this milestone does not run")
	case !slices.Equal(r.UnverifiedInputs, []string{"runtime_digest", "image_digest", "extension_digest"}):
		return errors.New("product turn receipt must label unmeasured registration inputs")
	case r.ProviderJournalCount == 0 || !validBareDigest(r.ProviderJournalHead) ||
		r.StoreJournalCount == 0 || !validBareDigest(r.StoreJournalHead):
		return errors.New("product turn receipt does not commit both child journals")
	case (r.CommandJournalCount == 0) != (r.CommandJournalHead == "") ||
		r.CommandJournalHead != "" && !validBareDigest(r.CommandJournalHead):
		return errors.New("product turn receipt has an incomplete or invalid command journal commitment")
	case (r.ImportedPrefixDigest == "") != (r.PredecessorReceiptDigest == "") ||
		r.ImportedPrefixDigest != "" && (!validDigest(r.ImportedPrefixDigest) || !validDigest(r.PredecessorReceiptDigest)):
		return errors.New("product turn receipt has an incomplete or invalid predecessor commitment")
	case !validDigest(r.BaselineDigest) || !validDigest(r.ResultBaselineDigest):
		return errors.New("product turn receipt has an invalid baseline digest")
	case !receiptOutcomes[r.Outcome]:
		return errors.New("product turn receipt has an unknown outcome")
	case r.Usage.HostCalls < 0 || r.Usage.Tokens < 0 || r.Usage.DurationSeconds < 0:
		return errors.New("product turn receipt has negative usage")
	case r.ToolSequence == nil || r.Denials == nil:
		return errors.New("product turn receipt omits its tool sequence or denials")
	}
	return nil
}

// validBareDigest reports a lowercase 64-digit hex SHA-256 without a prefix.
// Same format as validBareWorkflowDigest in cmd/tbound/governed_recovery_linux.go (package main, not importable).
func validBareDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// validDigest reports "sha256:" followed by a lowercase 64-digit hex SHA-256.
// Same format as validDigestText in cmd/tbound/production_command_runner_linux.go (package main, not importable).
func validDigest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) {
		return false
	}
	return validBareDigest(value[len(prefix):])
}
