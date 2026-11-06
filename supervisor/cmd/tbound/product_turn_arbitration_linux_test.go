//go:build linux

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"tbound/supervisor/internal/productdriver"
	"tbound/supervisor/internal/productsession"
)

func TestProductTurnArbitrationCancelBetweenCheckAndLink(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	admission := f.submit(t, "turn-1", productDriverPromptA)
	paused := false
	f.env.crashAt = func(point string) {
		if point == "before-link" {
			paused = true
			if _, err := f.cancel(t, admission.Turn.ID, "cancel"); err != nil {
				t.Errorf("cancel before link: %v", err)
			}
		}
	}
	script := productDriverTurnOne(false)
	_, turn, err := f.run(t, admission, script)
	var refusal *productdriver.Refusal
	if !paused || !errors.As(err, &refusal) || refusal.Code != productsession.CodeCancelled || turn.Link != nil ||
		turn.Outcome != productsession.OutcomeCancelled || turn.Refund != turn.Reservation || script.requests() != 0 || f.notesPublished(t) {
		t.Fatalf("cancel between check and link: paused=%v turn=%+v err=%v", paused, turn, err)
	}
	if session := f.currentSession(t); session.Consumed != (productsession.Budget{}) || session.Reserved != (productsession.Budget{}) || session.Blocked {
		t.Fatalf("unlinked cancellation was charged or blocked: %+v", session)
	}
	if next := f.submit(t, "turn-2", productDriverPromptB); next.Turn.PredecessorTurnID != "" {
		t.Fatalf("unlinked cancellation became a predecessor: %+v", next)
	}
}

func TestProductTurnArbitrationDeadlinePassesAfterExpiryScan(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	pending := f.awaitReview(t)
	paused := false
	var skew atomic.Int64
	f.env.reviewClock = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	f.env.crashAt = func(point string) {
		if point == "before-review-resolution" {
			paused = true
			// Advance the clock across the review deadline after the initial expiry scan.
			skew.Store(int64(2 * time.Hour))
		}
	}
	_, err := f.resolve(t, pending.ID, "publish", pending.CandidateDigest, productsession.DecisionPublish)
	turn := f.turn(t, pending.ID)
	if !paused || !errors.Is(err, productsession.ErrCode(productsession.CodeReviewNotPending)) ||
		turn.Outcome != productsession.OutcomeDiscarded || turn.TerminalReason != productsession.ReasonExpired || turn.Resolution != "" || f.notesPublished(t) {
		t.Fatalf("resolution crossing deadline: paused=%v turn=%+v err=%v", paused, turn, err)
	}
}

func TestProductTurnArbitrationCancelBeforeAutoPublication(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	admission := f.submit(t, "turn-1", productDriverPromptA)
	paused := false
	f.env.crashAt = func(point string) {
		if point == "before-auto-publication" {
			paused = true
			if _, err := f.cancel(t, admission.Turn.ID, "cancel"); err != nil {
				t.Errorf("cancel before auto publication: %v", err)
			}
		}
	}
	_, turn, err := f.run(t, admission, productDriverTurnOne(false))
	if err != nil || !paused || turn.Outcome != productsession.OutcomeCancelled || !turn.CancelRequested || f.notesPublished(t) {
		t.Fatalf("cancel before auto publication: paused=%v turn=%+v err=%v", paused, turn, err)
	}
}

func TestProductTurnArbitrationCancelJustAfterAutoPublicationStarted(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	admission := f.submit(t, "turn-1", productDriverPromptA)
	started := false
	f.env.crashAt = func(point string) {
		if point == "after-auto-publication-started" {
			started = true
			if _, err := f.cancel(t, admission.Turn.ID, "cancel"); err != nil {
				t.Errorf("cancel after publication started: %v", err)
			}
		}
	}
	_, turn, err := f.run(t, admission, productDriverTurnOne(false))
	if err != nil || !started || !turn.CancelRequested || turn.Outcome != productsession.OutcomePublished || !f.notesPublished(t) {
		t.Fatalf("cancel after publication started: started=%v outcome=%s cancelled=%v err=%v", started, turn.Outcome, turn.CancelRequested, err)
	}
}

func TestProductTurnArbitrationRecordedCancelPreventsPublishResolution(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	pending := f.awaitReview(t)
	recorded, resume := make(chan struct{}), make(chan struct{})
	f.env.crashAt = func(point string) {
		if point == "after-cancel-recorded" {
			close(recorded)
			<-resume
		}
	}
	cancelRequest := productsession.CancelTurnRequest{SessionID: f.session.ID, TurnID: pending.ID,
		ExpectedRevision: f.currentSession(t).Revision, RequestKey: "cancel"}
	cancelDone := make(chan error, 1)
	go func() {
		_, err := f.env.driver().CancelTurn(context.Background(), f.principal, cancelRequest)
		cancelDone <- err
	}()
	select {
	case <-recorded:
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not reach its durable record")
	}
	request := productsession.ResolveTurnRequest{SessionID: f.session.ID, TurnID: pending.ID,
		ExpectedRevision: f.currentSession(t).Revision, RequestKey: "publish", CandidateDigest: pending.CandidateDigest,
		Decision: productsession.DecisionPublish}
	resolveAttempt, resolveDone := make(chan struct{}), make(chan error, 1)
	go func() {
		close(resolveAttempt)
		_, err := f.env.driver().ResolveTurn(context.Background(), f.principal, request)
		resolveDone <- err
	}()
	<-resolveAttempt
	close(resume)
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	err := <-resolveDone
	turn := f.turn(t, pending.ID)
	if (!errors.Is(err, productsession.ErrCode(productsession.CodeCancelled)) &&
		!errors.Is(err, productsession.ErrCode(productsession.CodeReviewNotPending))) || turn.Resolution != "" ||
		turn.Outcome != productsession.OutcomeDiscarded || turn.TerminalReason != productsession.ReasonCancelled || f.notesPublished(t) {
		t.Fatalf("publish after cancellation: outcome=%s reason=%s resolution=%s err=%v", turn.Outcome, turn.TerminalReason, turn.Resolution, err)
	}
}

func TestProductTurnArbitrationCancelAfterPublishResolutionReportsRealDisposition(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	pending := f.awaitReview(t)
	recorded := false
	f.env.crashAt = func(point string) {
		if point == "after-resolution" {
			recorded = true
			if _, err := f.cancel(t, pending.ID, "cancel"); err != nil {
				t.Errorf("cancel after publish resolution: %v", err)
			}
		}
	}
	_, err := f.resolve(t, pending.ID, "publish", pending.CandidateDigest, productsession.DecisionPublish)
	turn := f.turn(t, pending.ID)
	if err != nil || !recorded || !turn.CancelRequested || turn.Outcome != productsession.OutcomePublished || !f.notesPublished(t) {
		t.Fatalf("cancel after publish resolution: recorded=%v outcome=%s cancelled=%v err=%v", recorded, turn.Outcome, turn.CancelRequested, err)
	}
}
