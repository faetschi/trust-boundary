//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/productdriver"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

func (f *productDriverFixture) resolve(t *testing.T, turnID, key, candidate, decision string) (productsession.TurnReceipt, error) {
	t.Helper()
	return f.env.driver().ResolveTurn(context.Background(), f.principal, productsession.ResolveTurnRequest{SessionID: f.session.ID,
		TurnID: turnID, ExpectedRevision: f.currentSession(t).Revision, RequestKey: key, CandidateDigest: candidate, Decision: decision})
}

func (f *productDriverFixture) cancel(t *testing.T, turnID, key string) (productsession.Turn, error) {
	t.Helper()
	return f.env.driver().CancelTurn(context.Background(), f.principal, productsession.CancelTurnRequest{SessionID: f.session.ID,
		TurnID: turnID, ExpectedRevision: f.currentSession(t).Revision, RequestKey: key})
}

func (f *productDriverFixture) turn(t *testing.T, id string) productsession.Turn {
	t.Helper()
	turn, ok := f.env.Catalog.Turn(id)
	if !ok {
		t.Fatalf("turn %s is not in the catalog", id)
	}
	return turn
}

// followUp runs a final-answer turn after a P4 predecessor and requires its first provider request to
// carry the host boundary naming the predecessor's outcome, unless there is no predecessor.
func (f *productDriverFixture) followUp(t *testing.T, key, predecessorOutcome string) productsession.Turn {
	t.Helper()
	want := fmt.Sprintf("ended with outcome %q", predecessorOutcome)
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: key + "-final", Text: productDriverAnswerB}},
		check: func(history []providerbridge.ScriptMessage) error {
			if predecessorOutcome == "" {
				return nil
			}
			for _, message := range history {
				if message.Role == "user" && strings.Contains(message.Content, want) {
					return nil
				}
			}
			return fmt.Errorf("no host boundary %s in %+v", want, history)
		}}
	_, turn, err := f.run(t, f.submit(t, key, productDriverPromptB), script)
	if err != nil || turn.Outcome != productsession.OutcomeNoDiff {
		t.Fatalf("turn after a %s predecessor: %+v %v", predecessorOutcome, turn, err)
	}
	return turn
}

func (f *productDriverFixture) notesPublished(t *testing.T) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.projectDir, "notes.txt"))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil || string(data) != productDriverNotes {
		t.Fatalf("notes.txt: %q %v", data, err)
	}
	return true
}

func (f *productDriverFixture) awaitReview(t *testing.T) productsession.Turn {
	t.Helper()
	_, turn, err := f.run(t, f.submit(t, "turn-1", productDriverPromptA), productDriverTurnOne(false))
	if err != nil || turn.State != productsession.StateAwaitingReview {
		t.Fatalf("review turn: %+v %v", turn, err)
	}
	return turn
}

func TestProductTurnReviewPublishesOnlyTheExactCandidate(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	baseline := f.measure(t)
	pending := f.awaitReview(t)
	if _, err := f.resolve(t, pending.ID, "resolve-stale", baseline, productsession.DecisionPublish); !errors.Is(err,
		productsession.ErrCode(productsession.CodeCandidateMismatch)) || f.notesPublished(t) {
		t.Fatalf("stale candidate: %v", err)
	}
	revision := f.currentSession(t).Revision
	request := productsession.ResolveTurnRequest{SessionID: f.session.ID, TurnID: pending.ID, ExpectedRevision: revision,
		RequestKey: "resolve-1", CandidateDigest: pending.CandidateDigest, Decision: productsession.DecisionPublish}
	receipt, err := f.env.driver().ResolveTurn(context.Background(), f.principal, request)
	turn := f.turn(t, pending.ID)
	if err != nil || turn.Outcome != productsession.OutcomePublished || receipt.PublicationTransactionID == "" ||
		turn.ResultBaselineDigest != pending.CandidateDigest || f.measure(t) != pending.CandidateDigest || !f.notesPublished(t) ||
		turn.Usage != pending.Usage || turn.Refund != pending.Refund {
		t.Fatalf("publish: turn=%+v receipt=%+v err=%v", turn, receipt, err)
	}
	if again, err := f.env.driver().ResolveTurn(context.Background(), f.principal, request); err != nil || !reflect.DeepEqual(again, receipt) {
		t.Fatalf("replayed resolution: %+v %v", again, err)
	}
	f.followUp(t, "turn-2", productsession.OutcomePublished)
}

func TestProductTurnReviewDiscardKeepsTheProjectAndTheSpend(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	baseline := f.measure(t)
	pending := f.awaitReview(t)
	if _, err := f.resolve(t, pending.ID, "resolve-1", pending.CandidateDigest, productsession.DecisionDiscard); err != nil {
		t.Fatal(err)
	}
	turn, session := f.turn(t, pending.ID), f.currentSession(t)
	if turn.Outcome != productsession.OutcomeDiscarded || turn.TerminalReason != productsession.ReasonUser ||
		f.measure(t) != baseline || f.notesPublished(t) || session.Consumed != pending.Usage || session.Reserved != (productsession.Budget{}) {
		t.Fatalf("discard: turn=%+v session=%+v", turn, session)
	}
	f.followUp(t, "turn-2", productsession.OutcomeDiscarded)
}

func TestProductTurnReviewExpires(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	pending := f.awaitReview(t)
	if err := f.env.driver().ExpireReviews(time.Now()); err != nil || f.turn(t, pending.ID).State != productsession.StateAwaitingReview {
		t.Fatalf("a review expired early: %v", err)
	}
	if err := f.env.driver().ExpireReviews(time.Now().Add(601 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if turn := f.turn(t, pending.ID); turn.Outcome != productsession.OutcomeDiscarded || turn.TerminalReason != productsession.ReasonExpired ||
		f.notesPublished(t) {
		t.Fatalf("expired review: %+v", turn)
	}
	if _, err := f.resolve(t, pending.ID, "resolve-late", pending.CandidateDigest, productsession.DecisionPublish); !errors.Is(err,
		productsession.ErrCode(productsession.CodeReviewNotPending)) {
		t.Fatalf("resolution after expiry: %v", err)
	}
}

func TestProductTurnCancelBeforeLaunchRefundsAndBlocksNothing(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	first := f.submit(t, "turn-1", productDriverPromptA)
	turn, err := f.cancel(t, first.Turn.ID, "cancel-1")
	if err != nil || turn.Outcome != productsession.OutcomeCancelled || turn.Refund != turn.Reservation || turn.Link != nil {
		t.Fatalf("cancel before launch: %+v %v", turn, err)
	}
	script := productDriverTurnOne(false)
	if _, _, err := f.run(t, first, script); err == nil || script.requests() != 0 {
		t.Fatalf("a cancelled turn ran: %v", err)
	}
	// A cancel recorded while no driver holds the turn is honoured when RunTurn starts it.
	second := f.submit(t, "turn-2", productDriverPromptA)
	if _, _, err := f.env.Catalog.CancelTurn(f.principal, productsession.CancelTurnRequest{SessionID: f.session.ID,
		TurnID: second.Turn.ID, ExpectedRevision: f.currentSession(t).Revision, RequestKey: "cancel-2"}); err != nil {
		t.Fatal(err)
	}
	_, turn, err = f.run(t, second, script)
	var refusal *productdriver.Refusal
	if !errors.As(err, &refusal) || refusal.Code != productsession.CodeCancelled || script.requests() != 0 ||
		turn.Outcome != productsession.OutcomeCancelled || turn.Refund != turn.Reservation {
		t.Fatalf("RunTurn of a cancelled admission: %+v %v", turn, err)
	}
	session := f.currentSession(t)
	if session.Blocked || session.Consumed != (productsession.Budget{}) || session.Reserved != (productsession.Budget{}) {
		t.Fatalf("session after cancellations: %+v", session)
	}
	f.submit(t, "turn-3", productDriverPromptA)
}

func TestProductTurnCancelMidTurnStopsBeforeTheNextExchange(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	baseline := f.measure(t)
	admission := f.submit(t, "turn-1", productDriverPromptA)
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{
		{ResponseID: "c-response-write", CallID: "c-call-write", Tool: "write",
			Arguments: json.RawMessage(`{"path":"notes.txt","content":"notes from turn one\n"}`)},
		{ResponseID: "c-response-read", CallID: "c-call-read", Tool: "read", Arguments: json.RawMessage(`{"path":"notes.txt"}`)},
		{ResponseID: "c-response-final", Text: productDriverAnswerA},
	}}
	script.onRequest = func(n int) {
		if n == 2 {
			if _, err := f.cancel(t, admission.Turn.ID, "cancel-1"); err != nil {
				t.Errorf("cancel mid-turn: %v", err)
			}
		}
	}
	receipt, turn, err := f.run(t, admission, script)
	if err != nil || script.requests() != 2 || turn.Outcome != productsession.OutcomeCancelled || turn.ContextEligible ||
		f.measure(t) != baseline || f.notesPublished(t) || receipt.PublicationTransactionID != "" {
		t.Fatalf("cancel mid-turn: requests=%d turn=%+v err=%v", script.requests(), turn, err)
	}
	hostCalls, tokens := productDriverExpectedUsage(t, turn.Link.ProviderJournalPath, 2)
	if turn.Usage.HostCalls != hostCalls || turn.Usage.Tokens != tokens || f.currentSession(t).Consumed != turn.Usage {
		t.Fatalf("cancelled turn usage %+v, ledger model host calls %d tokens %d", turn.Usage, hostCalls, tokens)
	}
	f.followUp(t, "turn-2", productsession.OutcomeCancelled)
}

func TestProductTurnCancelDuringReviewDiscards(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	pending := f.awaitReview(t)
	turn, err := f.cancel(t, pending.ID, "cancel-1")
	if err != nil || turn.Outcome != productsession.OutcomeDiscarded || turn.TerminalReason != productsession.ReasonCancelled ||
		f.notesPublished(t) {
		t.Fatalf("cancel during review: %+v %v", turn, err)
	}
}

func TestProductTurnCancelAfterAutoPublicationStartedReportsTheRealDisposition(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	admission := f.submit(t, "turn-1", productDriverPromptA)
	cancelled := false
	f.env.publicationFaults = func(point string, _ int) error {
		if point == "after-terminal-checkpoint" && !cancelled {
			cancelled = true
			if _, err := f.cancel(t, admission.Turn.ID, "cancel-1"); err != nil {
				t.Errorf("cancel during publication: %v", err)
			}
		}
		return nil
	}
	_, turn, err := f.run(t, admission, productDriverTurnOne(false))
	if err != nil || !cancelled || !turn.CancelRequested || turn.Outcome != productsession.OutcomePublished || !f.notesPublished(t) {
		t.Fatalf("cancel after publication started: cancelled=%v turn=%+v err=%v", cancelled, turn, err)
	}
}
