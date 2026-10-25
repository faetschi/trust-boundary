//go:build linux

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
)

func TestProductServiceExpiryTickerDiscardsAnUnresolvedReview(t *testing.T) {
	f := newProductServiceFixtureWithReviewTimeout(t, 1)
	f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationReview, "create-1")
	f.submit(session, "turn-1", productDriverPromptA, serviceWriteScript("exp", "notes.txt", productDriverNotes, productDriverAnswerA, false))
	events, _ := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnTerminal))
	review, last := events[len(events)-2], events[len(events)-1]
	if review.Kind != productprotocol.EventTurnAwaitingReview || last.Outcome != productsession.OutcomeDiscarded ||
		last.Reason != productsession.ReasonExpired || f.notesPublished("notes.txt") {
		t.Fatalf("expiry: %v, last %+v", eventKinds(events), last)
	}
}

// A cancel request that arrives while the turn runs is answered at once, appears in the event list only
// once the turn settles, and stops the turn at its next step boundary. Provider events of the running
// turn are read through the driver's held journal handle.
func TestProductServiceCancelWhileRunningIsWithheldUntilTheTurnSettles(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationAuto, "create-1")
	started, release := make(chan struct{}), make(chan struct{})
	script := serviceWriteScript("run", "notes.txt", productDriverNotes, productDriverAnswerA, false)
	script.onRequest = func(n int) {
		if n == 2 {
			close(started)
			<-release
		}
	}
	turn := f.submit(session, "turn-1", productDriverPromptA, script)
	select {
	case <-started:
	case <-time.After(60 * time.Second):
		t.Fatal("the turn never reached its second exchange")
	}
	observe := func() []productprotocol.Event {
		return f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpObserve,
			Observe: &productprotocol.Observe{SessionID: session.ID}}).Events
	}
	running := observe()
	wantRunning := []string{productprotocol.EventTurnAdmitted, productprotocol.EventTurnLinked, productprotocol.EventToolCall, productprotocol.EventToolResult}
	if !reflect.DeepEqual(eventKinds(running), wantRunning) {
		t.Fatalf("events of the running turn %v, want %v", eventKinds(running), wantRunning)
	}
	current := f.getSession(f.principalA, session.ID)
	response := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpCancelTurn, CancelTurn: &productprotocol.CancelTurn{
		SessionID: session.ID, TurnID: turn.ID, ExpectedRevision: current.Revision, RequestKey: "cancel-1"}})
	if got := response.Turn; got == nil || !got.CancelRequested || got.State != string(productsession.StateLinked) {
		t.Fatalf("cancel_turn on a running turn: %+v", got)
	}
	if got := f.getSession(f.principalA, session.ID).Turns[0]; !got.CancelRequested {
		t.Fatalf("get_session does not show the cancel request: %+v", got)
	}
	if got := observe(); !reflect.DeepEqual(got, running) {
		t.Fatalf("the cancel request appeared among the events of a running turn: %v", eventKinds(got))
	}
	close(release)
	events, _ := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnTerminal))
	want := append(append([]string{}, wantRunning...), productprotocol.EventAssistantMessage, productprotocol.EventTurnCancelRequested,
		productprotocol.EventTurnTerminal)
	if !reflect.DeepEqual(eventKinds(events), want) || events[len(events)-1].Outcome != productsession.OutcomeCancelled || f.notesPublished("notes.txt") {
		t.Fatalf("events after the turn settled: %v, last %+v", eventKinds(events), events[len(events)-1])
	}
	if !reflect.DeepEqual(events[:len(running)], running) {
		t.Fatal("the list changed other than at its end")
	}
}

func TestProductServiceCreateSessionRefusals(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	create := func(mutate func(*productprotocol.CreateSession)) productprotocol.Request {
		r := &productprotocol.CreateSession{ProjectID: productDriverProject, ProviderProfile: productDriverProfile,
			PublicationMode: productsession.PublicationAuto, Budget: productprotocol.Budget{HostCalls: 5, Tokens: 5 * 1024, DurationSeconds: 60},
			RequestKey: "create-refused"}
		mutate(r)
		return productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: r}
	}
	f.wantError(f.principalA, create(func(r *productprotocol.CreateSession) { r.ProjectID = "no-such-project" }), string(productsession.CodeUnknownProject))
	f.wantError(f.principalA, create(func(r *productprotocol.CreateSession) { r.PublicationMode = "yolo" }), string(productsession.CodeInvalidRequest))
	f.wantError(f.principalA, create(func(r *productprotocol.CreateSession) { r.Budget.Tokens = 1 << 40 }), string(productsession.CodeBudgetExceedsLimit))
	f.wantError(f.unregistered, create(func(*productprotocol.CreateSession) {}), string(productsession.CodeUnauthorizedPrincipal))
	f.wantError(f.unregistered, create(func(r *productprotocol.CreateSession) { r.ProjectID = "no-such-project" }), string(productsession.CodeUnauthorizedPrincipal))
}

func TestProductServiceAnswersInternalFailuresWithoutDetail(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationAuto, "create-1")
	if err := os.Rename(f.projectDir, f.projectDir+".gone"); err != nil {
		t.Fatal(err)
	}
	unavailable := productprotocol.CodeServiceUnavailable
	f.wantError(f.principalA, f.submitRequest(session, "turn-1", productDriverPromptA), unavailable)
	f.wantError(f.principalA, productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &productprotocol.CreateSession{
		ProjectID: productDriverProject, ProviderProfile: productDriverProfile, PublicationMode: productsession.PublicationAuto,
		RequestKey: "create-2"}}, unavailable)
	if got := f.getSession(f.principalA, session.ID); len(got.Turns) != 0 {
		t.Fatalf("a refused submit admitted a turn: %+v", got)
	}
}

func TestProductServiceReadsCandidateFilesWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	write := func(name string, data []byte) {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("dir/text.txt", []byte("héllo\n"))
	write("binary.bin", []byte("a\x00b"))
	write("latin1.txt", []byte{0xff, 0xfe})
	write("big.txt", []byte(strings.Repeat("x", productCandidateMaxFileBytes+1)))
	write("outside/secret.txt", []byte("secret"))
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "dir", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside", "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if text, ok, err := readGenerationText(dir, "dir/text.txt"); err != nil || !ok || text != "héllo\n" {
		t.Fatalf("a text file: %q %v %v", text, ok, err)
	}
	for _, name := range []string{"binary.bin", "latin1.txt", "big.txt"} {
		if text, ok, err := readGenerationText(dir, name); err != nil || ok || text != "" {
			t.Errorf("%s must be listed without content: %q %v %v", name, text, ok, err)
		}
	}
	for _, name := range []string{"leak.txt", "dir/link/secret.txt", "missing.txt"} {
		if text, _, err := readGenerationText(dir, name); err == nil || text != "" {
			t.Errorf("%s: %q %v, want an error", name, text, err)
		}
	}
}
