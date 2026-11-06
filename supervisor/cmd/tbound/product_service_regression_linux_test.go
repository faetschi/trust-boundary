//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productdriver"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

func TestProductServiceRegressionObservationAndPredecessorDoNotCompete(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	_, pred, err := f.run(t, f.submit(t, "first", "hello"), serviceAnswerScript("first", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := openExistingRecoveryJournal(pred.Link.ProviderJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	admission := f.submit(t, "contended", "follow up")
	// Hold the predecessor verifier's writer handle while independent observers read it.
	done := make(chan error, 2)
	launch := make(chan error, 1)
	go func() {
		_, err := f.env.driver().RunTurn(context.Background(), admission)
		launch <- err
	}()
	for range 2 {
		go func() {
			_, err := newProductEventProjector(f.env).turn(pred)
			done <- err
		}()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Errorf("observation contended with predecessor verification: %v", err)
		}
	}
	err = <-launch
	blockedTurn, _ := f.env.Catalog.Turn(admission.Turn.ID)
	if !errors.Is(err, audit.ErrAlreadyOpen) || blockedTurn.State != productsession.StateAbandoned || f.currentSession(t).Blocked {
		t.Fatalf("transient lock contention must abandon without blocking: state=%s blocked=%v err=%v",
			blockedTurn.State, f.currentSession(t).Blocked, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	_, next, err := f.run(t, f.submit(t, "retry", "follow up"), serviceAnswerScript("retry", "hello again"))
	if err != nil || next.Outcome != productsession.OutcomeNoDiff {
		t.Fatalf("follow-up after releasing verifier: state=%s outcome=%s err=%v", next.State, next.Outcome, err)
	}
}

func TestProductServiceSessionSnapshotBoundsLongHistory(t *testing.T) {
	f := newProductServiceFixture(t)
	svc := f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationAuto, "create")
	baseline, err := svc.env.MeasureProjectBaseline(productDriverProject)
	if err != nil {
		t.Fatal(err)
	}
	const history = 950
	for i := range history {
		current, err := svc.catalog.Session(f.principalA, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		admission, err := svc.catalog.SubmitTurn(f.principalA, productsession.SubmitTurnRequest{SessionID: session.ID,
			ExpectedRevision: current.Revision, RequestKey: fmt.Sprintf("history-%d", i), Prompt: strings.Repeat("<", 1024)}, baseline)
		if err != nil {
			t.Fatal(err)
		}
		if i < history-1 {
			if err := svc.catalog.AbandonUnlinkedTurn(admission.Turn.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	view := f.getSession(f.principalA, session.ID)
	if view.TotalTurns != history || !view.TurnsTruncated || len(view.Turns) != productServiceRecentTurns ||
		view.Turns[0].Ordinal != history-productServiceRecentTurns+1 || view.Turns[len(view.Turns)-1].State != string(productsession.StateAdmitted) {
		t.Fatalf("long session snapshot lost its bound or active turn: total=%d truncated=%v turns=%d", view.TotalTurns, view.TurnsTruncated, len(view.Turns))
	}
	if err := productprotocol.WriteFrame(&bytes.Buffer{}, &productprotocol.Response{Session: &view}, productprotocol.MaxResponseBytes); err != nil {
		t.Fatal(err)
	}
	// Even an unresolved turn outside the recent window must be included.
	current, _ := svc.catalog.Session(f.principalA, session.ID)
	last := current.Turns[len(current.Turns)-1]
	current.Turns = append([]string{last}, current.Turns[:len(current.Turns)-1]...)
	if got := svc.sessionView(current, true); len(got.Turns) != productServiceRecentTurns+1 || got.Turns[0].ID != last {
		t.Fatalf("snapshot omitted unresolved work outside its recent window: %d turns", len(got.Turns))
	}
}

func TestProductServiceReviewedObservationDoesNotOpenWriterJournals(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	turn := f.awaitReview(t)
	if _, err := f.resolve(t, turn.ID, "discard", turn.CandidateDigest, productsession.DecisionDiscard); err != nil {
		t.Fatal(err)
	}
	turn = f.turn(t, turn.ID)
	paths, err := productTurnPathsFromLink(*turn.Link)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := openRetainedProductTurn(paths, f.env.storeOptions(turn), f.env.OpenCommandHistory)
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	events, err := newProductEventProjector(f.env).turn(turn)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == productprotocol.EventTurnAwaitingReview {
			if !reflect.DeepEqual(event.Paths, []string{"notes.txt"}) {
				t.Fatalf("observation lost candidate paths while predecessor verifier holds journals: %v", event.Paths)
			}
			return
		}
	}
	t.Fatal("review event missing")
}

func TestProductServiceCandidatePathsExcludeRevertedAndDeniedChanges(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprintf("denied=%v", denied), func(t *testing.T) {
			var scope *productsession.ProductScope
			if denied {
				scope = &productsession.ProductScope{Writable: []string{"notes.txt"}}
			}
			f := newScopedProductDriverFixture(t, productsession.PublicationReview, scope, false, nil)
			script := productDriverTurnOne(false)
			script.turns = append([]providerbridge.ScriptTurn{scopeDriverWrite("changed", "task.txt", "temporary\n"),
				scopeDriverWrite("reverted", "task.txt", "turn fixture\n")}, script.turns...)
			_, turn, err := f.run(t, f.submit(t, "review", "change and revert"), script)
			if err != nil || turn.State != productsession.StateAwaitingReview {
				t.Fatalf("review state=%s err=%v", turn.State, err)
			}
			paths, err := newProductEventProjector(f.env).candidatePaths(turn)
			if err != nil || !reflect.DeepEqual(paths, []string{"notes.txt"}) {
				t.Fatalf("candidate paths must describe net allowed changes: paths=%v err=%v", paths, err)
			}
		})
	}
}

func TestProductServiceSocketLockExcludesDifferentCatalogs(t *testing.T) {
	f := newProductServiceFixture(t)
	first := f.start(f.options())
	f.cfg.CatalogPath = filepath.Join(f.stateRoot, "second-catalog.jsonl")
	if second, err := startProductService(f.cfg, f.options()); err == nil {
		_ = second.Shutdown()
		t.Fatal("another catalog replaced the live service socket")
	}
	f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpListSessions})
	if err := first.Shutdown(); err != nil {
		t.Fatal(err)
	}
	f.start(f.options())
	f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpListSessions})
}

func TestProductServiceObservationCorruptRetainedJournalIsRetryable(t *testing.T) {
	f := newProductServiceFixture(t)
	svc := f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationAuto, "create")
	turn := f.submit(session, "first", "hello", serviceAnswerScript("first", "hello"))
	f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnTerminal))
	svc.turns.Wait()
	retained, _ := svc.catalog.Turn(turn.ID)
	data, err := os.ReadFile(retained.Link.ProviderJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retained.Link.ProviderJournalPath, append(data, []byte("torn")...), 0o600); err != nil {
		t.Fatal(err)
	}
	// A fresh projector must verify the retained bytes rather than use its settled cache.
	svc.events.mu.Lock()
	delete(svc.events.settled, turn.ID)
	svc.events.mu.Unlock()
	f.wantError(f.principalA, chatObserve(session.ID, 0, 0), productprotocol.CodeServiceUnavailable)
	if err := os.WriteFile(retained.Link.ProviderJournalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f.mustCall(f.principalA, chatObserve(session.ID, 0, 0))
	if view := f.getSession(f.principalA, session.ID); view.Blocked {
		t.Fatal("observation verification failure blocked the session")
	}
}

func TestProductServiceRegressionReviewMutationCursor(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	turn := f.awaitReview(t)
	projector := newProductEventProjector(f.env)
	read := func() []productprotocol.Event {
		t.Helper()
		events, err := projector.session(f.currentSession(t))
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	before := read()
	// Reproduce the durable writes in the cancellation window before discard completes.
	if _, _, err := f.env.Catalog.CancelTurn(f.principal, productsession.CancelTurnRequest{SessionID: f.session.ID,
		TurnID: turn.ID, ExpectedRevision: f.currentSession(t).Revision, RequestKey: "cancel"}); err != nil {
		t.Fatal(err)
	}
	cancelled := read()
	if _, _, err := f.env.Catalog.ResolveTurn(f.principal, productsession.ResolveTurnRequest{SessionID: f.session.ID,
		TurnID: turn.ID, ExpectedRevision: f.currentSession(t).Revision, RequestKey: "discard",
		CandidateDigest: turn.CandidateDigest, Decision: productsession.DecisionDiscard}); err != nil {
		t.Fatal(err)
	}
	resolving := read()
	for _, events := range [][]productprotocol.Event{cancelled, resolving} {
		if !reflect.DeepEqual(events, before) {
			t.Errorf("review mutations escaped before finality: before=%v after=%v", eventKinds(before), eventKinds(events))
		}
	}
	turn, _ = f.env.Catalog.Turn(turn.ID)
	// What the driver writes when a discarded review ends: the awaiting-review receipt with the outcome changed.
	receipt, err := productdriver.DecodeReceipt(turn.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Outcome, receipt.ResultBaselineDigest = productsession.OutcomeDiscarded, turn.BaselineDigest
	encoded, err := productdriver.EncodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.env.Catalog.CompleteTurn(turn.ID, productsession.TurnTerminal{Outcome: productsession.OutcomeDiscarded, Settlement: receipt.Settlement,
		Receipt: encoded, Usage: turn.Usage, ResultBaselineDigest: receipt.ResultBaselineDigest, ContextEligible: true,
		Reason: productsession.ReasonCancelled}); err != nil {
		t.Fatal(err)
	}
	final := read()
	if len(final) < len(cancelled) || !reflect.DeepEqual(final[:len(cancelled)], cancelled) {
		t.Fatalf("an event moved behind an issued cursor: issued=%v final=%v", eventKinds(cancelled), eventKinds(final))
	}
	remaining := observeWindow(final, uint64(len(before)))
	if want := []string{productprotocol.EventTurnResolution, productprotocol.EventTurnCancelRequested, productprotocol.EventTurnTerminal}; !reflect.DeepEqual(eventKinds(remaining.Events), want) {
		t.Fatalf("events after review cursor=%v, want %v", eventKinds(remaining.Events), want)
	}
}
