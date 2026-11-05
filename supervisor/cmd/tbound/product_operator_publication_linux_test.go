//go:build linux

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

// operatorResolveOnDisk runs operator resolution the way the root worker does, against the catalog on disk.
func operatorResolveOnDisk(f *productDriverFixture, turnID string, dry bool) (productsession.Turn, error) {
	return productsession.OperatorResolve(filepath.Join(f.env.StateRoot, "catalog.jsonl"), f.env.Registration, turnID, "operator test", dry,
		func(turn productsession.Turn) (productsession.OperatorEvidence, error) {
			return f.env.operatorEvidence(context.Background(), turn)
		})
}

// awaitReviewThenUnknown stops a review-mode turn the way a restart that cannot place its review does: the turn holds
// a candidate, nothing was published, the turn is unknown and the service is gone. A Bash turn settled one command.
func awaitReviewThenUnknown(t *testing.T, bash bool) (*productDriverFixture, productsession.Turn) {
	t.Helper()
	var f *productDriverFixture
	prompt, script := productDriverPromptA, productDriverTurnOne(false)
	if bash {
		f, prompt = newBashProductDriverFixture(t, productsession.PublicationReview, productScopeNotes(), false, []string{}, seedBashProject(t)), scopeDriverPrompt
		script = &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("create", `printf 'inside\n' > notes/a.txt`),
			scopeDriverFinal("t1", scopeDriverAnswer)}}
	} else {
		f = newProductDriverFixture(t, productsession.PublicationReview)
	}
	_, turn, err := f.run(t, f.submit(t, "turn-1", prompt), script)
	if err != nil || turn.State != productsession.StateAwaitingReview {
		t.Fatalf("review turn: %+v %v", turn, err)
	}
	if err := f.env.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Catalog.MarkTurnUnknown(turn.ID, "the review disposition is not established after restart"); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Catalog.Close(); err != nil {
		t.Fatal(err)
	}
	return f, f.turn(t, turn.ID)
}

// A Bash command that settled normally leaves a command journal but no unsettled activity: the Store matched every
// Bash effect to its retained settlement. Operator resolution must not refuse such a turn as command activity.
func TestProductOperatorResolvesATurnWhoseBashCommandsSettled(t *testing.T) {
	f, turn := awaitReviewThenUnknown(t, true)
	if turn.State != productsession.StateUnknown {
		t.Fatalf("turn: %+v", turn)
	}
	for _, dry := range []bool{true, false} {
		got, err := operatorResolveOnDisk(f, turn.ID, dry)
		if err != nil {
			t.Fatalf("dry=%v: %v", dry, err)
		}
		if !dry && (got.Outcome != productsession.OutcomeAbandoned || got.ContextEligible || got.Usage != got.Reservation) {
			t.Fatalf("resolution: %+v", got)
		}
	}
	reopened := openBashProductDriverFixture(t, f.root, productScopeNotes(), []string{})
	reopened.session = f.session
	if got := reopened.turn(t, turn.ID); got.State != productsession.StateTerminal || got.Outcome != productsession.OutcomeAbandoned ||
		got.OperatorResolution == nil || reopened.currentSession(t).Blocked {
		t.Fatalf("the abandonment did not replay: %+v", got)
	}
	reopened.followUp(t, "turn-2", "")
}

// Command records that no settlement verifier vouched for are still refused.
func TestProductOperatorRefusesCommandRecordsItCannotVerify(t *testing.T) {
	f, turn := awaitReviewThenUnknown(t, false)
	journal, err := audit.Open(turn.Link.CommandJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"note": "a record no verifier recognizes"})
	if _, err := journal.Append(audit.Event{Kind: "operator-test-command", ID: "unverified", Data: data}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		if _, err := operatorResolveOnDisk(f, turn.ID, dry); err == nil || !strings.Contains(err.Error(), "command") {
			t.Fatalf("dry=%v: unverifiable command records resolved: %v", dry, err)
		}
	}
}
