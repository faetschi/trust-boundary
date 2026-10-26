//go:build linux

package productsession

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
)

func TestSettlementRequiresAReceiptForThisTurn(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "auto", sessionBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	mustLink(t, c, turn.ID)
	usage := Budget{1, 1, 1}
	mutated := func(mutate func(*TurnReceipt)) []byte {
		r, err := DecodeReceipt(receiptFor(c, turn.ID, OutcomeNoDiff, usage, b0))
		if err != nil {
			t.Fatal(err)
		}
		mutate(&r)
		data, _ := json.Marshal(r)
		return data
	}
	for name, terminal := range map[string]TurnTerminal{
		"null receipt":              {Outcome: OutcomeNoDiff, Settlement: SettlementSupervisorLoopClosed, Receipt: []byte("null"), Usage: usage, ResultBaselineDigest: b0},
		"unknown settlement":        {Outcome: OutcomeNoDiff, Settlement: "UNKNOWN", Receipt: mutated(func(r *TurnReceipt) { r.Settlement = "UNKNOWN" }), Usage: usage, ResultBaselineDigest: b0},
		"other workflow":            {Outcome: OutcomeNoDiff, Settlement: SettlementSupervisorLoopClosed, Receipt: mutated(func(r *TurnReceipt) { r.WorkflowID = "wf-other" }), Usage: usage, ResultBaselineDigest: b0},
		"usage differs":             {Outcome: OutcomeNoDiff, Settlement: SettlementSupervisorLoopClosed, Receipt: mutated(func(r *TurnReceipt) { r.Usage = Budget{} }), Usage: usage, ResultBaselineDigest: b0},
		"pi claims supervisor-loop": {Outcome: OutcomeNoDiff, Settlement: SettlementSupervisorLoopClosed, Receipt: mutated(func(r *TurnReceipt) { r.WorkerKind = WorkerPi }), Usage: usage, ResultBaselineDigest: b0},
		"outcome differs":           {Outcome: OutcomeFailed, Settlement: SettlementSupervisorLoopClosed, Receipt: mutated(func(*TurnReceipt) {}), Usage: usage, ResultBaselineDigest: b0},
		"no journal commit":         {Outcome: OutcomeNoDiff, Settlement: SettlementSupervisorLoopClosed, Receipt: mutated(func(r *TurnReceipt) { r.ProviderJournalCount = 0 }), Usage: usage, ResultBaselineDigest: b0},
		"published without tx":      {Outcome: OutcomePublished, Settlement: SettlementSupervisorLoopClosed, Receipt: mutated(func(r *TurnReceipt) { r.Outcome = OutcomePublished; r.ResultBaselineDigest = b1 }), Usage: usage, ResultBaselineDigest: b1},
	} {
		if err := c.CompleteTurn(turn.ID, terminal); !isCode(err, CodeInvalidRequest) {
			t.Errorf("%s: CompleteTurn err=%v, want INVALID_REQUEST", name, err)
		}
	}
	sess, _ := c.Session(uid1, s.ID)
	if _, err := submit(c, uid1, s.ID, sess.Revision, "k2", "p", b0); !isCode(err, CodeTurnActive) {
		t.Fatalf("a refused settlement must leave the turn unresolved: %v", err)
	}
	if err := complete(c, turn.ID, OutcomeNoDiff, usage, b0); err != nil {
		t.Fatalf("the matching receipt settles the turn: %v", err)
	}
}

func TestOpenRecomputesTheRequestPayloadDigest(t *testing.T) {
	dir := privateDir(t)
	reg := signedRegistration(t, nil)
	source := filepath.Join(dir, "source.jsonl")
	c := openAt(t, source, reg)
	s := mustSession(t, c, uid1, "alpha", "auto", sessionBudget, "s")
	mustSubmit(t, c, uid1, s.ID, 0, "k1", "original prompt")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := audit.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := j.Trace()
	_ = j.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{kindSessionCreated, kindTurnAdmitted} {
		forged := filepath.Join(dir, kind+".jsonl")
		out, err := audit.Open(forged)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range trace.Records {
			event := record.Event
			if event.Kind == kind {
				event.Data = changeRecordedRequest(t, kind, event.Data)
			}
			if _, err := out.Append(event); err != nil {
				t.Fatal(err)
			}
		}
		_ = out.Close()
		if _, err := Open(forged, reg); !isCode(err, CodeCatalogCorrupt) {
			t.Errorf("%s with a stale payload digest: Open err=%v, want CATALOG_CORRUPT", kind, err)
		}
	}
}

// changeRecordedRequest edits the recorded request but keeps its original payload digest.
func changeRecordedRequest(t *testing.T, kind string, data []byte) []byte {
	t.Helper()
	var changed any
	switch kind {
	case kindSessionCreated:
		var r sessionCreatedRecord
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		r.Budget.Tokens--
		changed = r
	case kindTurnAdmitted:
		var r turnAdmittedRecord
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		r.Prompt = "changed prompt"
		r.PromptDigest = sha256Digest([]byte(r.Prompt))
		changed = r
	}
	out, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUnknownTurnFencesARegistrationChange(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	regA := signedRegistration(t, nil)
	regB := signedRegistration(t, func(r *Registration) { r.Limits.ReviewTimeoutSeconds = 601 })
	c := openAt(t, path, regA)
	s := mustSession(t, c, uid1, "alpha", "auto", sessionBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	mustLink(t, c, turn.ID)
	if err := c.MarkTurnUnknown(turn.ID, "worker settlement could not be established"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path, regB)
	wantCode(t, err, CodePredecessorUnresolved)
}

func isCode(err error, code Code) bool { return errors.Is(err, ErrCode(code)) }
