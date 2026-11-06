//go:build linux

package productdriver

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsession"
)

func digestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestProductTurnReceiptIsStrict(t *testing.T) {
	receipt := productsession.TurnReceipt{SchemaVersion: productsession.ReceiptSchema, SessionID: "psess-1", TurnID: "psess-1/turn-2",
		Ordinal: 2, WorkflowID: "w", RunID: "r", ConversationID: "c", UnverifiedInputs: []string{"runtime_digest", "image_digest", "extension_digest"},
		WorkerKind: productsession.WorkerSupervisorLoop, WorkerInstance: "i",
		ProviderJournalHead: strings.Repeat("a", 64), ProviderJournalCount: 3, StoreJournalHead: strings.Repeat("b", 64), StoreJournalCount: 4,
		ImportedPrefixDigest: digestBytes([]byte("prefix")), PredecessorReceiptDigest: digestBytes([]byte("receipt")),
		BaselineDigest: digestBytes([]byte("base")), ResultBaselineDigest: digestBytes([]byte("base")),
		Outcome: productsession.OutcomeNoDiff, Settlement: productsession.SettlementSupervisorLoopClosed, ToolSequence: []string{}, Denials: []productsession.ReceiptDenial{}}
	encoded, err := EncodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReceipt(encoded)
	if err != nil || decoded.TurnID != receipt.TurnID || decoded.ImportedPrefixDigest != receipt.ImportedPrefixDigest {
		t.Fatalf("receipt did not round-trip: %+v %v", decoded, err)
	}
	for name, raw := range map[string][]byte{
		"unknown field":  append(encoded[:len(encoded)-1:len(encoded)-1], []byte(`,"extra":1}`)...),
		"trailing data":  append(append([]byte(nil), encoded...), []byte(` {}`)...),
		"duplicate key":  append([]byte(`{"ordinal":2,`), encoded[1:]...),
		"over the bound": append(append([]byte(nil), encoded...), make([]byte, productsession.MaxReceiptBytes)...),
	} {
		if _, err := DecodeReceipt(raw); err == nil {
			t.Errorf("receipt with %s was accepted", name)
		}
	}
	for name, mutate := range map[string]func(*productsession.TurnReceipt){
		"missing unverified inputs": func(r *productsession.TurnReceipt) { r.UnverifiedInputs = nil },
		"claims verified runtime":   func(r *productsession.TurnReceipt) { r.UnverifiedInputs = []string{"image_digest", "extension_digest"} },
		"other worker":              func(r *productsession.TurnReceipt) { r.WorkerKind = "pi-process" },
		"turn ID mismatch":          func(r *productsession.TurnReceipt) { r.TurnID = "psess-1/turn-3" },
		"half predecessor":          func(r *productsession.TurnReceipt) { r.PredecessorReceiptDigest = "" },
		"no journal head":           func(r *productsession.TurnReceipt) { r.StoreJournalHead = "" },
		"unknown outcome":           func(r *productsession.TurnReceipt) { r.Outcome = "unknown" },
		"missing denials list":      func(r *productsession.TurnReceipt) { r.Denials = nil },
	} {
		changed := receipt
		mutate(&changed)
		if _, err := EncodeReceipt(changed); err == nil {
			t.Errorf("receipt with %s was encoded", name)
		}
	}
}
