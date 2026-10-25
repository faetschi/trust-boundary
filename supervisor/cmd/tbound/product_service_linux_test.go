//go:build linux

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

func TestProductServiceAutoTurnProjectsItsObservations(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationAuto, "create-1")
	if again := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &productprotocol.CreateSession{
		ProjectID: productDriverProject, ProviderProfile: productDriverProfile, PublicationMode: productsession.PublicationAuto,
		Budget: productprotocol.Budget{HostCalls: 60, Tokens: 60 * 1024, DurationSeconds: 1800}, RequestKey: "create-1"}}); !again.Replayed ||
		again.Session.ID != session.ID {
		t.Fatalf("create_session replay: %+v", again)
	}

	script := serviceWriteScript("auto", "notes.txt", productDriverNotes, productDriverAnswerA, true)
	admitted := f.submit(session, "turn-1", productDriverPromptA, script)
	replay := f.mustCall(f.principalA, f.submitRequest(session, "turn-1", productDriverPromptA))
	if !replay.Replayed || replay.Turn.ID != admitted.ID {
		t.Fatalf("an identical submit must return the same turn: %+v", replay)
	}
	f.wantError(f.principalA, f.submitRequest(session, "turn-1", productDriverPromptB), string(productsession.CodeRequestConflict))

	events, cursor := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnTerminal))
	wantKinds := []string{productprotocol.EventTurnAdmitted, productprotocol.EventTurnLinked, productprotocol.EventToolCall,
		productprotocol.EventToolResult, productprotocol.EventToolCall, productprotocol.EventToolResult,
		productprotocol.EventAssistantMessage, productprotocol.EventTurnTerminal}
	if !reflect.DeepEqual(eventKinds(events), wantKinds) {
		t.Fatalf("event kinds %v, want %v", eventKinds(events), wantKinds)
	}
	hint := providerbridge.HintCandidates("policy_rule_deny")["0"]
	byIndex := func(i int) productprotocol.Event { return events[i] }
	if e := byIndex(0); e.TurnID != admitted.ID || e.Text != productDriverPromptA {
		t.Fatalf("turn_admitted: %+v", e)
	}
	if e := byIndex(2); e.Tool != "write" || e.Verdict != "ALLOW" || e.ToolCallID != "auto-call-write" || e.Hint != "" {
		t.Fatalf("allowed tool_call: %+v", e)
	}
	if e := byIndex(4); e.Tool != "edit" || e.Verdict != "DENY" || e.ReasonCode != "policy_rule_deny" || e.Hint != hint || hint == "" {
		t.Fatalf("denied tool_call must carry the static hint: %+v", e)
	}
	if e := byIndex(5); e.Verdict != "DENY" || e.Text != "denied" {
		t.Fatalf("denied tool_result: %+v", e)
	}
	if e := byIndex(6); e.Text != productDriverAnswerA {
		t.Fatalf("assistant_message: %+v", e)
	}
	if e := byIndex(7); e.Outcome != productsession.OutcomePublished || e.Reason != "" {
		t.Fatalf("turn_terminal: %+v", e)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{f.root, f.stateRoot, f.projectDir, governedReplaySystem, governedReplayDeveloper} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("an observation carries %q", secret)
		}
	}
	if got := script.requests(); got != 3 {
		t.Fatalf("the provider saw %d requests; an identical submit must not start a second run", got)
	}
	if !f.notesPublished("notes.txt") {
		t.Fatal("the auto-publish turn did not publish its file")
	}

	final := f.getSession(f.principalA, session.ID)
	if len(final.Turns) != 1 || final.Turns[0].State != string(productsession.StateTerminal) || final.Turns[0].Outcome != productsession.OutcomePublished ||
		final.Consumed.HostCalls == 0 || final.Reserved != (productprotocol.Budget{}) {
		t.Fatalf("get_session: %+v", final)
	}
	if listed := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpListSessions}); len(listed.Sessions) != 1 ||
		listed.Sessions[0].ID != session.ID || len(listed.Sessions[0].Turns) != 0 {
		t.Fatalf("list_sessions: %+v", listed.Sessions)
	}

	// The cursor is an index into the list: at the end nothing is new, and past the end is a client error.
	if more := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpObserve,
		Observe: &productprotocol.Observe{SessionID: session.ID, Cursor: cursor}}); len(more.Events) != 0 || more.NextCursor != cursor {
		t.Fatalf("observe at the end: %+v", more)
	}
	f.wantError(f.principalA, productprotocol.Request{Op: productprotocol.OpObserve,
		Observe: &productprotocol.Observe{SessionID: session.ID, Cursor: cursor + 1}}, string(productsession.CodeInvalidRequest))
	// A second observe from zero replays the same list from the retained records.
	if again, _ := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnTerminal)); !reflect.DeepEqual(again, events) {
		t.Fatalf("the replayed list differs: %v", eventKinds(again))
	}
}

func TestProductServiceReviewTurnPublishesItsExactCandidate(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationReview, "create-1")
	turn := f.submit(session, "turn-1", productDriverPromptA, serviceWriteScript("rev", "notes.txt", productDriverNotes, productDriverAnswerA, false))

	events, cursor := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnAwaitingReview))
	review := events[len(events)-1]
	if review.CandidateDigest == "" || !reflect.DeepEqual(review.Paths, []string{"notes.txt"}) || review.Text != "1 changed paths" {
		t.Fatalf("turn_awaiting_review: %+v", review)
	}
	if f.notesPublished("notes.txt") {
		t.Fatal("a review turn changed the project before its resolution")
	}

	response := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpGetCandidate,
		GetCandidate: &productprotocol.TurnRef{SessionID: session.ID, TurnID: turn.ID}})
	want := productprotocol.CandidateView{TurnID: turn.ID, CandidateDigest: review.CandidateDigest, Changes: []productprotocol.CandidateChange{
		{Path: "notes.txt", Op: "create", Diff: "@@ -0,0 +1,1 @@\n+notes from turn one\n"}}}
	if response.Candidate == nil || !reflect.DeepEqual(*response.Candidate, want) {
		t.Fatalf("get_candidate: %+v, want %+v", response.Candidate, want)
	}

	current := f.getSession(f.principalA, session.ID)
	resolve := func(key, digest, decision string) productprotocol.Request {
		return productprotocol.Request{Op: productprotocol.OpResolveTurn, ResolveTurn: &productprotocol.ResolveTurn{SessionID: session.ID,
			TurnID: turn.ID, ExpectedRevision: current.Revision, RequestKey: key, CandidateDigest: digest, Decision: decision}}
	}
	f.wantError(f.principalA, resolve("resolve-stale", "sha256:"+strings.Repeat("0", 64), productsession.DecisionPublish),
		string(productsession.CodeCandidateMismatch))
	if f.notesPublished("notes.txt") {
		t.Fatal("a stale candidate digest authorized publication")
	}
	published := f.mustCall(f.principalA, resolve("resolve-1", review.CandidateDigest, productsession.DecisionPublish))
	if published.Turn == nil || published.Turn.Outcome != productsession.OutcomePublished || published.Turn.Resolution != productsession.DecisionPublish ||
		!f.notesPublished("notes.txt") {
		t.Fatalf("resolve_turn: %+v", published.Turn)
	}
	if again := f.mustCall(f.principalA, resolve("resolve-1", review.CandidateDigest, productsession.DecisionPublish)); again.Turn.Outcome != productsession.OutcomePublished {
		t.Fatalf("an identical resolve must answer with the settled turn: %+v", again.Turn)
	}
	f.wantError(f.principalA, productprotocol.Request{Op: productprotocol.OpGetCandidate,
		GetCandidate: &productprotocol.TurnRef{SessionID: session.ID, TurnID: turn.ID}}, string(productsession.CodeReviewNotPending))

	tail, _ := f.observeUntil(f.principalA, session.ID, cursor, kindIs(productprotocol.EventTurnTerminal))
	if kinds := eventKinds(tail); !reflect.DeepEqual(kinds, []string{productprotocol.EventTurnResolution, productprotocol.EventTurnTerminal}) ||
		tail[0].Reason != productsession.DecisionPublish || tail[0].CandidateDigest != review.CandidateDigest ||
		tail[1].Outcome != productsession.OutcomePublished {
		t.Fatalf("events after the awaiting-review event: %+v", tail)
	}
}

func TestProductServiceCancelDuringReviewDiscards(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationReview, "create-1")
	turn := f.submit(session, "turn-1", productDriverPromptA, serviceWriteScript("can", "notes.txt", productDriverNotes, productDriverAnswerA, false))
	_, cursor := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnAwaitingReview))

	current := f.getSession(f.principalA, session.ID)
	cancel := productprotocol.Request{Op: productprotocol.OpCancelTurn, CancelTurn: &productprotocol.CancelTurn{SessionID: session.ID,
		TurnID: turn.ID, ExpectedRevision: current.Revision, RequestKey: "cancel-1"}}
	response := f.mustCall(f.principalA, cancel)
	if got := response.Turn; got == nil || !got.CancelRequested || got.Outcome != productsession.OutcomeDiscarded ||
		got.TerminalReason != productsession.ReasonCancelled {
		t.Fatalf("cancel_turn: %+v", got)
	}
	if again := f.mustCall(f.principalA, cancel); again.Turn.Outcome != productsession.OutcomeDiscarded {
		t.Fatalf("an identical cancel must answer with the settled turn: %+v", again.Turn)
	}
	tail, _ := f.observeUntil(f.principalA, session.ID, cursor, kindIs(productprotocol.EventTurnTerminal))
	if kinds := eventKinds(tail); !reflect.DeepEqual(kinds, []string{productprotocol.EventTurnCancelRequested, productprotocol.EventTurnTerminal}) ||
		tail[1].Outcome != productsession.OutcomeDiscarded || tail[1].Reason != productsession.ReasonCancelled {
		t.Fatalf("events after cancel: %+v", tail)
	}
	if f.notesPublished("notes.txt") {
		t.Fatal("a cancelled review changed the project")
	}
}

func TestProductServiceSecondPrincipalCannotSeeOrActOnASession(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationReview, "create-1")
	turn := f.submit(session, "turn-1", productDriverPromptA, serviceWriteScript("iso", "notes.txt", productDriverNotes, productDriverAnswerA, false))
	events, _ := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnAwaitingReview))
	digest := events[len(events)-1].CandidateDigest

	denied := string(productsession.CodeUnauthorizedPrincipal)
	for name, request := range map[string]productprotocol.Request{
		"get_session": {Op: productprotocol.OpGetSession, GetSession: &productprotocol.SessionRef{SessionID: session.ID}},
		"observe":     {Op: productprotocol.OpObserve, Observe: &productprotocol.Observe{SessionID: session.ID}},
		"submit_turn": f.submitRequest(session, "intruder", productDriverPromptB),
		"get_candidate": {Op: productprotocol.OpGetCandidate,
			GetCandidate: &productprotocol.TurnRef{SessionID: session.ID, TurnID: turn.ID}},
		"cancel_turn": {Op: productprotocol.OpCancelTurn, CancelTurn: &productprotocol.CancelTurn{SessionID: session.ID, TurnID: turn.ID,
			ExpectedRevision: 1, RequestKey: "intruder"}},
		"resolve_turn": {Op: productprotocol.OpResolveTurn, ResolveTurn: &productprotocol.ResolveTurn{SessionID: session.ID, TurnID: turn.ID,
			ExpectedRevision: 1, RequestKey: "intruder", CandidateDigest: digest, Decision: productsession.DecisionPublish}},
	} {
		t.Logf("principal B: %s", name)
		f.wantError(f.principalB, request, denied)
	}
	if listed := f.mustCall(f.principalB, productprotocol.Request{Op: productprotocol.OpListSessions}); len(listed.Sessions) != 0 {
		t.Fatalf("principal B lists principal A's sessions: %+v", listed.Sessions)
	}
	f.wantError(f.unregistered, productprotocol.Request{Op: productprotocol.OpListSessions}, denied)
	if f.notesPublished("notes.txt") {
		t.Fatal("another principal published the candidate")
	}
	// A is unaffected, and still resolves its own turn.
	current := f.getSession(f.principalA, session.ID)
	if current.Turns[0].State != string(productsession.StateAwaitingReview) {
		t.Fatalf("principal A's session changed: %+v", current)
	}
	f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpResolveTurn, ResolveTurn: &productprotocol.ResolveTurn{
		SessionID: session.ID, TurnID: turn.ID, ExpectedRevision: current.Revision, RequestKey: "resolve-1", CandidateDigest: digest,
		Decision: productsession.DecisionDiscard}})
	// The turn is settled and has a resolution. Another principal still learns nothing about it from
	// the answer to a resolve or cancel, whatever state the turn is in.
	f.wantError(f.principalB, productprotocol.Request{Op: productprotocol.OpResolveTurn, ResolveTurn: &productprotocol.ResolveTurn{
		SessionID: session.ID, TurnID: turn.ID, ExpectedRevision: current.Revision, RequestKey: "resolve-1", CandidateDigest: digest,
		Decision: productsession.DecisionDiscard}}, denied)
	f.wantError(f.principalB, productprotocol.Request{Op: productprotocol.OpCancelTurn, CancelTurn: &productprotocol.CancelTurn{
		SessionID: session.ID, TurnID: turn.ID, ExpectedRevision: current.Revision + 1, RequestKey: "cancel-1"}}, denied)
	f.wantError(f.principalB, productprotocol.Request{Op: productprotocol.OpObserve, Observe: &productprotocol.Observe{SessionID: session.ID}}, denied)
	// Naming A's turn inside B's own session does not reach it either.
	own := f.createSession(f.principalB, productsession.PublicationReview, "create-b")
	f.wantError(f.principalB, productprotocol.Request{Op: productprotocol.OpCancelTurn, CancelTurn: &productprotocol.CancelTurn{
		SessionID: own.ID, TurnID: turn.ID, ExpectedRevision: own.Revision, RequestKey: "cancel-b"}}, string(productsession.CodeUnknownTurn))
	f.wantError(f.principalB, productprotocol.Request{Op: productprotocol.OpGetCandidate,
		GetCandidate: &productprotocol.TurnRef{SessionID: own.ID, TurnID: turn.ID}}, string(productsession.CodeUnknownTurn))
}

func TestProductServiceRestartReconcilesBeforeServing(t *testing.T) {
	f := newProductServiceFixture(t)
	first := f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationReview, "create-1")
	turn := f.submit(session, "turn-1", productDriverPromptA, serviceWriteScript("rst", "notes.txt", productDriverNotes, productDriverAnswerA, false))
	before, _ := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnAwaitingReview))
	if err := first.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := net.Dial("unix", f.socketPath); err == nil {
		t.Fatal("the socket still accepts after shutdown")
	}

	options := f.options()
	options.beforeListen = func(env *productTurnEnv) {
		if left := env.Catalog.UnresolvedTurns(); len(left) != 0 {
			t.Errorf("the service reached its listener with %d unresolved turns", len(left))
		}
		if _, err := os.Lstat(f.socketPath); !os.IsNotExist(err) {
			t.Errorf("the socket exists before reconciliation finished: %v", err)
		}
	}
	f.start(options)
	info, err := os.Lstat(f.socketPath)
	if err != nil || info.Mode().Perm() != 0o660 || info.Mode().Type() != os.ModeSocket {
		t.Fatalf("socket: %v %v", info, err)
	}
	if dir, err := os.Lstat(filepath.Dir(f.socketPath)); err != nil || dir.Mode().Perm()&^0o750 != 0 {
		t.Fatalf("socket directory: %v %v", dir, err)
	}

	// The turn was reconciled from retained evidence, and its history reads from the retained files.
	events, _ := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnTerminal))
	if !reflect.DeepEqual(eventKinds(events), append(eventKinds(before), productprotocol.EventTurnTerminal)) {
		t.Fatalf("events after restart %v, before %v", eventKinds(events), eventKinds(before))
	}
	if !reflect.DeepEqual(events[:len(before)], before) {
		t.Fatal("the events of a turn changed across the restart")
	}
	if last := events[len(events)-1]; last.Outcome != productsession.OutcomeDiscarded || last.Reason != productsession.ReasonRestart {
		t.Fatalf("terminal event after restart: %+v", last)
	}
	f.wantError(f.principalA, productprotocol.Request{Op: productprotocol.OpGetCandidate,
		GetCandidate: &productprotocol.TurnRef{SessionID: session.ID, TurnID: turn.ID}}, string(productsession.CodeReviewNotPending))

	// Nothing the service should accept is refused: the session takes its next turn.
	current := f.getSession(f.principalA, session.ID)
	next := f.submit(current, "turn-2", productDriverPromptB, serviceAnswerScript("rst2", productDriverAnswerB))
	tail, _ := f.observeUntil(f.principalA, session.ID, uint64(len(events)), func(e productprotocol.Event) bool {
		return e.Kind == productprotocol.EventTurnTerminal && e.TurnID == next.ID
	})
	if last := tail[len(tail)-1]; last.Outcome != productsession.OutcomeNoDiff {
		t.Fatalf("turn after restart: %+v", last)
	}
	if f.notesPublished("notes.txt") {
		t.Fatal("restart reconciliation published a candidate")
	}
}

func TestProductServiceRefusesMalformedRequestsAndKeepsServing(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	frame := func(payload []byte) []byte {
		out := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
		return append(out, payload...)
	}
	raw := func(data []byte) *productprotocol.Response {
		conn, err := net.Dial("unix", f.socketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write(data); err != nil {
			t.Fatal(err)
		}
		var response productprotocol.Response
		if err := productprotocol.ReadFrame(conn, productprotocol.MaxResponseBytes, &response); err != nil {
			t.Fatalf("no response to a malformed request: %v", err)
		}
		return &response
	}
	version := `"version":"` + productprotocol.Version + `"`
	for name, data := range map[string][]byte{
		"not JSON":          frame([]byte("not json")),
		"truncated JSON":    frame([]byte(`{"version":`)),
		"oversized frame":   binary.BigEndian.AppendUint32(nil, productprotocol.MaxRequestBytes+1),
		"zero length frame": binary.BigEndian.AppendUint32(nil, 0),
		"unknown op":        frame([]byte(`{` + version + `,"op":"drop_catalog"}`)),
		"wrong version":     frame([]byte(`{"version":"v0","op":"list_sessions"}`)),
		"unknown field":     frame([]byte(`{` + version + `,"op":"list_sessions","principal":0}`)),
		"duplicate key":     frame([]byte(`{` + version + `,"op":"list_sessions","op":"get_session"}`)),
		"two payloads": frame([]byte(`{` + version + `,"op":"get_session","get_session":{"session_id":"x"},` +
			`"observe":{"session_id":"x","cursor":0,"wait_ms":0}}`)),
		"missing payload": frame([]byte(`{` + version + `,"op":"submit_turn"}`)),
		"wait too long":   frame([]byte(`{` + version + `,"op":"observe","observe":{"session_id":"x","cursor":0,"wait_ms":30001}}`)),
		"trailing data":   frame([]byte(`{` + version + `,"op":"list_sessions"} {}`)),
	} {
		response := raw(data)
		if response.Status != productprotocol.StatusError || response.Error == nil || response.Error.Code != productprotocol.CodeProtocolError ||
			response.Error.Detail != "" {
			t.Errorf("%s: %+v", name, response)
		}
	}
	// A client that sends half a frame and leaves must not disturb the service.
	if conn, err := net.Dial("unix", f.socketPath); err != nil {
		t.Fatal(err)
	} else {
		_, _ = conn.Write([]byte{0, 0})
		_ = conn.Close()
	}
	if got := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpListSessions}); got.Status != productprotocol.StatusOK {
		t.Fatalf("the service stopped serving: %+v", got)
	}
	f.wantError(f.principalA, productprotocol.Request{Op: productprotocol.OpGetSession,
		GetSession: &productprotocol.SessionRef{SessionID: "psess-" + strings.Repeat("0", 32)}}, string(productsession.CodeUnknownSession))
}

func TestProductServiceReadsThePrincipalFromTheSocketPeer(t *testing.T) {
	f := newProductServiceFixture(t)
	options := f.options()
	options.Principal = nil
	f.start(options)
	session := f.createSession(f.principalA, productsession.PublicationAuto, "create-1")
	got, err := f.svc.catalog.Session(productsession.Principal(os.Getuid()), session.ID)
	if err != nil || got.Principal != productsession.Principal(os.Getuid()) {
		t.Fatalf("the session is not owned by the peer UID: %+v %v", got, err)
	}
	if listed := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpListSessions}); len(listed.Sessions) != 1 {
		t.Fatalf("list_sessions: %+v", listed.Sessions)
	}
}

func TestProductServiceShutdownLetsARunningTurnFinish(t *testing.T) {
	f := newProductServiceFixture(t)
	svc := f.start(f.options())
	session := f.createSession(f.principalA, productsession.PublicationAuto, "create-1")
	started, release := make(chan struct{}), make(chan struct{})
	script := serviceAnswerScript("slow", productDriverAnswerA)
	script.onRequest = func(int) {
		close(started)
		<-release
	}
	turn := f.submit(session, "turn-1", productDriverPromptA, script)
	select {
	case <-started:
	case <-time.After(60 * time.Second):
		t.Fatal("the turn never reached its provider exchange")
	}

	var stopped atomic.Bool
	done := make(chan error, 1)
	go func() {
		err := svc.Shutdown()
		stopped.Store(true)
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if stopped.Load() {
		t.Fatal("shutdown returned while a turn was running")
	}
	if _, err := net.Dial("unix", f.socketPath); err == nil {
		t.Fatal("a stopping service still accepts connections")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	f.start(f.options())
	events, _ := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnTerminal))
	last := events[len(events)-1]
	if last.TurnID != turn.ID || last.Outcome != productsession.OutcomeNoDiff || last.Reason != "" {
		t.Fatalf("the running turn was not left to finish: %+v", last)
	}
}

func TestProductServiceSocketHandling(t *testing.T) {
	f := newProductServiceFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.socketPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.socketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := startProductService(f.cfg, f.options()); err == nil {
		t.Fatal("the service replaced a file that is not a socket")
	}
	if data, err := os.ReadFile(f.socketPath); err != nil || string(data) != "not a socket" {
		t.Fatalf("the refused path was changed: %q %v", data, err)
	}
	// A failed start released the catalog, so a start over a stale socket works.
	if err := os.Remove(f.socketPath); err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: f.socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	f.start(f.options())
	if got := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpListSessions}); got.Status != productprotocol.StatusOK {
		t.Fatalf("service over a stale socket: %+v", got)
	}
}

func TestProductServiceRefusesStartWithoutAHostOrConversationFactory(t *testing.T) {
	f := newProductServiceFixture(t)
	options := f.options()
	options.Host = nil
	if _, err := startProductService(f.cfg, options); !errors.Is(err, errProductServiceNoHost) {
		t.Fatalf("no host: %v", err)
	}
	options.Host = func(env *productTurnEnv) error { return nil }
	if _, err := startProductService(f.cfg, options); !errors.Is(err, errProductServiceNoConversation) {
		t.Fatalf("no conversation factory: %v", err)
	}
	// The real command has no host profile yet, so it refuses with the same explicit error.
	if productServiceHostProfile != nil {
		t.Skip("a host profile is wired in")
	}
	writeConfig := func(cfg productServiceConfig) string {
		encoded, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(f.root, "service.json")
		if err := os.WriteFile(file, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	// The temp root is not a product default directory, so the config check comes first.
	if err := run([]string{"product-serve", "--config", writeConfig(f.cfg)}, os.Stdout, os.Stderr); err == nil ||
		!strings.Contains(err.Error(), "not below a product default directory") {
		t.Fatalf("the real command accepted a config outside the product directories: %v", err)
	}
	defaults := productServiceConfig{SchemaVersion: productServiceConfigSchema,
		RegistrationPath: productsession.DefaultRegistrationPath, RegistrationSignaturePath: productsession.DefaultRegistrationSignaturePath,
		RegistrationPublicKeyPath: productsession.DefaultRegistrationPublicKeyPath, CatalogPath: productsession.DefaultCatalogPath,
		StateRoot: "/var/lib/tbound-product", SocketPath: productsession.DefaultServiceSocketPath, WorkerMode: productServiceWorkerFixture,
		Projects: map[string]string{"project-alpha": "/var/lib/tbound-product/projects/alpha"}}
	if err := run([]string{"product-serve", "--config", writeConfig(defaults)}, os.Stdout, os.Stderr); !errors.Is(err, errProductServiceNoHost) {
		t.Fatalf("the real command with default paths and no host profile: %v", err)
	}
	if err := run([]string{"product-serve"}, os.Stdout, os.Stderr); err == nil {
		t.Fatal("product-serve without --config succeeded")
	}
}

func TestProductServiceCreateSessionDefaultsAnEmptyProviderProfile(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	request := productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &productprotocol.CreateSession{
		ProjectID: productDriverProject, PublicationMode: productsession.PublicationAuto,
		Budget: productprotocol.Budget{HostCalls: 10, Tokens: 10 * 1024, DurationSeconds: 100}, RequestKey: "create-default-profile"}}
	created := f.mustCall(f.principalA, request)
	if created.Session == nil || created.Session.ProviderProfile != productDriverProfile {
		t.Fatalf("an empty provider profile must be the registration's first profile: %+v", created.Session)
	}
	if again := f.mustCall(f.principalA, request); !again.Replayed || again.Session.ID != created.Session.ID {
		t.Fatalf("a defaulted create must replay under its request key: %+v", again)
	}
	request.CreateSession.ProviderProfile, request.CreateSession.RequestKey = "unregistered-profile", "create-bad-profile"
	f.wantError(f.principalA, request, string(productsession.CodeUnknownProviderProfile))
}

func TestProductServiceCreateSessionDefaultsZeroBudgetFieldsToTheSessionLimit(t *testing.T) {
	f := newProductServiceFixture(t)
	f.start(f.options())
	limit := f.svc.env.Registration.Limits.Session
	create := func(key string, budget productprotocol.Budget) productprotocol.Response {
		return f.call(f.principalA, productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &productprotocol.CreateSession{
			ProjectID: productDriverProject, ProviderProfile: productDriverProfile, PublicationMode: productsession.PublicationAuto,
			Budget: budget, RequestKey: key}})
	}
	all := create("create-zero", productprotocol.Budget{})
	if all.Session == nil || all.Session.Budget != (productprotocol.Budget{HostCalls: limit.HostCalls, Tokens: limit.Tokens,
		DurationSeconds: limit.DurationSeconds}) {
		t.Fatalf("a zero budget must be the session limit: %+v %+v", all.Session, all.Error)
	}
	partial := create("create-partial", productprotocol.Budget{HostCalls: 7})
	if partial.Session == nil || partial.Session.Budget != (productprotocol.Budget{HostCalls: 7, Tokens: limit.Tokens,
		DurationSeconds: limit.DurationSeconds}) {
		t.Fatalf("only the zero fields default: %+v %+v", partial.Session, partial.Error)
	}
	negative := create("create-negative", productprotocol.Budget{HostCalls: -1})
	if negative.Error == nil || negative.Error.Code != string(productsession.CodeInvalidRequest) || negative.Session != nil {
		t.Fatalf("a negative budget field must stay refused: %+v", negative)
	}
}
