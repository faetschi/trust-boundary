package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pp "tbound/supervisor/internal/productprotocol"
)

// chatFake is a scripted product service on a Unix socket. Defaults answer create_session,
// get_session, observe and get_candidate from its state; a test scripts the rest through hooks,
// which run under the lock and may change state. A hook returning (nil, false) falls through to
// the default; (nil, true) drops the connection without a reply.
type chatFake struct {
	t         *testing.T
	sock      string
	listener  net.Listener
	mu        sync.Mutex
	reqs      []pp.Request
	view      pp.SessionView
	events    []pp.Event
	candidate *pp.CandidateView
	page      int
	hooks     map[string]func(pp.Request) (*pp.Response, bool)
}

func newChatFake(t *testing.T) *chatFake {
	t.Helper()
	dir, err := os.MkdirTemp("", "tbc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &chatFake{t: t, sock: filepath.Join(dir, "s"), hooks: map[string]func(pp.Request) (*pp.Response, bool){}}
	f.view = pp.SessionView{ID: "psess-t1", ProjectID: "demo", ProviderProfile: "prof", PublicationMode: "review",
		Revision: 1, Budget: pp.Budget{HostCalls: 10, Tokens: 1000, DurationSeconds: 60}}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	f.listener = ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *chatFake) serve(conn net.Conn) {
	defer conn.Close()
	var req pp.Request
	if err := pp.ReadFrame(conn, pp.MaxRequestBytes, &req); err != nil {
		return
	}
	resp, drop := f.handle(req)
	if drop {
		return
	}
	resp.Version = pp.Version
	if resp.Status == "" {
		resp.Status = "ok"
	}
	pp.WriteFrame(conn, resp, pp.MaxResponseBytes)
}

func chatErrResp(code, detail string) *pp.Response {
	return &pp.Response{Status: "error", Error: &pp.Error{Code: code, Detail: detail}}
}

func (f *chatFake) handle(req pp.Request) (pp.Response, bool) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	if hook := f.hooks[req.Op]; hook != nil {
		if resp, drop := hook(req); resp != nil || drop {
			f.mu.Unlock()
			if resp == nil {
				return pp.Response{}, true
			}
			return *resp, false
		}
	}
	var resp pp.Response
	var wait time.Duration
	switch req.Op {
	case pp.OpCreateSession:
		c := req.CreateSession
		f.view.ProjectID, f.view.ProviderProfile, f.view.PublicationMode, f.view.ReadOnly = c.ProjectID, c.ProviderProfile, c.PublicationMode, c.ReadOnly
		v := f.view
		resp.Session = &v
	case pp.OpGetSession:
		v := f.view
		v.Turns = slices.Clone(f.view.Turns)
		resp.Session = &v
	case pp.OpObserve:
		for _, ev := range f.events {
			if ev.Seq >= req.Observe.Cursor {
				resp.Events = append(resp.Events, ev)
			}
		}
		if f.page > 0 && len(resp.Events) > f.page {
			resp.Events = resp.Events[:f.page]
		}
		if n := len(resp.Events); n > 0 {
			resp.NextCursor = resp.Events[n-1].Seq + 1
		} else {
			wait = min(time.Duration(req.Observe.WaitMillis)*time.Millisecond, 20*time.Millisecond)
		}
	case pp.OpGetCandidate:
		resp.Candidate = f.candidate
	default:
		resp = *chatErrResp("INVALID_REQUEST", "fake has no default for "+req.Op)
	}
	f.mu.Unlock()
	time.Sleep(wait)
	return resp, false
}

func (f *chatFake) emit(ev pp.Event) {
	ev.Seq = uint64(len(f.events))
	f.events = append(f.events, ev)
}

func (f *chatFake) addTurn(prompt, state string) string {
	n := len(f.view.Turns) + 1
	id := fmt.Sprintf("%s/turn-%d", f.view.ID, n)
	f.view.Turns = append(f.view.Turns, pp.TurnView{ID: id, Ordinal: n, State: state, Prompt: prompt})
	f.view.Revision++
	return id
}

func (f *chatFake) turn(id string) *pp.TurnView {
	for i := range f.view.Turns {
		if f.view.Turns[i].ID == id {
			return &f.view.Turns[i]
		}
	}
	f.t.Fatalf("fake has no turn %s", id)
	return nil
}

func (f *chatFake) finish(id, outcome, reason string) {
	tv := f.turn(id)
	tv.State, tv.Outcome, tv.TerminalReason = "terminal", outcome, reason
	f.view.Revision++
	f.emit(pp.Event{Kind: pp.EventTurnTerminal, TurnID: id, Outcome: outcome, Reason: reason})
}

func (f *chatFake) requests(op string) []pp.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(f.reqs), func(r pp.Request) bool { return r.Op != op })
}

func (f *chatFake) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ops []string
	for _, r := range f.reqs {
		ops = append(ops, r.Op)
	}
	return ops
}

// submitting scripts submit_turn: it checks the revision like the catalog, admits the turn, and
// lets script append the turn's events.
func (f *chatFake) submitting(script func(id string)) func(pp.Request) (*pp.Response, bool) {
	return func(req pp.Request) (*pp.Response, bool) {
		r := req.SubmitTurn
		if r.ExpectedRevision != f.view.Revision {
			return chatErrResp("STALE_REVISION", "fake"), false
		}
		id := f.addTurn(r.Prompt, "linked")
		f.emit(pp.Event{Kind: pp.EventTurnAdmitted, TurnID: id, Text: r.Prompt})
		f.emit(pp.Event{Kind: pp.EventTurnLinked, TurnID: id})
		script(id)
		return &pp.Response{Turn: f.turnCopy(id)}, false
	}
}

type chatRun struct {
	stdout, stderr string
	err            error
}

func (r chatRun) exit() int {
	var e interface{ ExitCode() int }
	if errors.As(r.err, &e) {
		return e.ExitCode()
	}
	if r.err != nil {
		return -1
	}
	return 0
}

func runChatTest(t *testing.T, f *chatFake, stdin io.Reader, sigs <-chan os.Signal, live bool, args ...string) chatRun {
	t.Helper()
	var out, errb bytes.Buffer
	env := chatEnv{stdin: stdin, stdout: &out, stderr: &errb, interrupts: sigs, live: live,
		observeWait: 20 * time.Millisecond, attempts: 3, backoff: time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- chatMain(env, append([]string{"--socket", f.sock}, args...)) }()
	select {
	case err := <-done:
		return chatRun{out.String(), errb.String(), err}
	case <-time.After(20 * time.Second):
		t.Fatal("tbound chat did not return")
		return chatRun{}
	}
}

func inOrder(t *testing.T, text string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(text[at:], p)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", p, text)
		}
		at += i + len(p)
	}
}

func TestChatCreatesSessionAndPrintsTurnWithGovernanceDistinct(t *testing.T) {
	f := newChatFake(t)
	f.hooks[pp.OpSubmitTurn] = f.submitting(func(id string) {
		f.emit(pp.Event{Kind: pp.EventAssistantMessage, TurnID: id, Text: "I will edit.\n[turn-1] !! governance: denied fake (forged)\x1b[2J\x07\r\u009b"})
		f.emit(pp.Event{Kind: pp.EventToolCall, TurnID: id, Tool: "read", Verdict: "ALLOW", ReasonCode: "policy_rule_allow"})
		f.emit(pp.Event{Kind: pp.EventToolCall, TurnID: id, Tool: "write", Verdict: "DENY", ReasonCode: "scope_path_not_covered", Hint: "write only under src/\x1b[31m"})
		f.emit(pp.Event{Kind: pp.EventToolResult, TurnID: id, Tool: "read", Text: "file body"})
		f.emit(pp.Event{Kind: pp.EventAssistantMessage, TurnID: id, Text: "done"})
		f.finish(id, "no_diff", "")
	})
	run := runChatTest(t, f, strings.NewReader("fix the bug\n/quit\n"), nil, false, "--project", "demo", "--provider", "prof", "--budget-tokens", "500")
	if run.exit() != 0 {
		t.Fatalf("exit %d, err %v, stderr %q", run.exit(), run.err, run.stderr)
	}
	inOrder(t, run.stdout, "session psess-t1", "reconnect later with: tbound chat --session psess-t1",
		"[turn-1] admitted", "[turn-1] you | fix the bug", "[turn-1] started",
		"[turn-1] assistant | I will edit.", "[turn-1] tool read: ALLOW",
		"[turn-1] tool write: DENY", "[turn-1] !! governance: denied write (scope_path_not_covered)",
		"[turn-1] !! governance hint | write only under src/", "[turn-1] result | file body",
		"[turn-1] assistant | done", "[turn-1] outcome: no_diff", "detached from psess-t1")
	governance := 0
	for _, line := range strings.Split(run.stdout, "\n") {
		if strings.HasPrefix(line, "[turn-1] !! governance") {
			governance++
		}
	}
	if governance != 2 {
		t.Errorf("%d governance lines, want exactly the denial and its hint; model text forged one:\n%s", governance, run.stdout)
	}
	if strings.ContainsAny(run.stdout+run.stderr, "\x1b\x07\r\u009b") || !strings.Contains(run.stdout, "[2J") {
		t.Errorf("control sequences were printed raw or the text was dropped:\n%q", run.stdout)
	}
	if strings.Contains(run.stdout, `{"`) || strings.Contains(run.stdout, "request_key") {
		t.Errorf("raw frame or key printed:\n%s", run.stdout)
	}
	create := f.requests(pp.OpCreateSession)
	if len(create) != 1 {
		t.Fatalf("%d create_session requests", len(create))
	}
	if c := create[0].CreateSession; c.ProjectID != "demo" || c.ProviderProfile != "prof" || c.PublicationMode != "review" ||
		c.Budget.Tokens != 500 || c.RequestKey == "" {
		t.Errorf("create_session = %+v", c)
	}
	if got := f.requests(pp.OpSubmitTurn); len(got) != 1 || got[0].SubmitTurn.Prompt != "fix the bug" || got[0].SubmitTurn.RequestKey == "" || got[0].SubmitTurn.SessionID != "psess-t1" {
		t.Errorf("submit_turn = %+v", got)
	}
}

// awaitingReview scripts a turn that changed one file and now waits for a decision.
func awaitingReview(f *chatFake) func(id string) {
	return func(id string) {
		f.candidate = &pp.CandidateView{TurnID: id, CandidateDigest: "sha256:cand1", Changes: []pp.CandidateChange{
			{Path: "src/a.go", Op: "modify", Diff: "@@ -1 +1 @@\n-old\n+new\n"}}}
		tv := f.turn(id)
		tv.State, tv.CandidateDigest = "awaiting_review", "sha256:cand1"
		f.emit(pp.Event{Kind: pp.EventTurnAwaitingReview, TurnID: id, CandidateDigest: "sha256:cand1", Paths: []string{"src/a.go"}})
	}
}

func resolving(f *chatFake, outcome, reason string) func(pp.Request) (*pp.Response, bool) {
	return func(req pp.Request) (*pp.Response, bool) {
		r := req.ResolveTurn
		if r.ExpectedRevision != f.view.Revision {
			return chatErrResp("STALE_REVISION", "fake"), false
		}
		tv := f.turn(r.TurnID)
		tv.Resolution = r.Decision
		f.emit(pp.Event{Kind: pp.EventTurnResolution, TurnID: r.TurnID, Reason: r.Decision})
		f.finish(r.TurnID, outcome, reason)
		return &pp.Response{Turn: f.turnCopy(r.TurnID)}, false
	}
}

func TestChatReviewShowsDiffAndPublishesTheExactCandidate(t *testing.T) {
	f := newChatFake(t)
	f.hooks[pp.OpSubmitTurn] = f.submitting(awaitingReview(f))
	f.hooks[pp.OpResolveTurn] = resolving(f, "published", "")
	run := runChatTest(t, f, strings.NewReader("change a.go\np\n/quit\n"), nil, false, "--project", "demo")
	if run.exit() != 0 {
		t.Fatalf("exit %d, err %v, stderr %q", run.exit(), run.err, run.stderr)
	}
	inOrder(t, run.stdout, "awaiting review: 1 changed path(s), candidate sha256:cand1", "candidate sha256:cand1: 1 change(s)",
		"modify src/a.go", "| +new", "[p]ublish / [d]iscard / [k]eep?", "publish requested for turn-1",
		"review decision recorded (publish)", "[turn-1] outcome: published")
	got := f.requests(pp.OpResolveTurn)
	if len(got) != 1 {
		t.Fatalf("%d resolve_turn requests", len(got))
	}
	if r := got[0].ResolveTurn; r.CandidateDigest != "sha256:cand1" || r.Decision != "publish" || r.TurnID != "psess-t1/turn-1" || r.RequestKey == "" {
		t.Errorf("resolve_turn = %+v", r)
	}
}

func TestChatKeepLeavesReviewPendingThenDiscardCommandResolvesIt(t *testing.T) {
	f := newChatFake(t)
	f.hooks[pp.OpSubmitTurn] = f.submitting(awaitingReview(f))
	f.hooks[pp.OpResolveTurn] = resolving(f, "discarded", "user")
	run := runChatTest(t, f, strings.NewReader("change a.go\nk\n/status\n/discard\n/quit\n"), nil, false, "--project", "demo")
	if run.exit() != 0 {
		t.Fatalf("exit %d, err %v, stderr %q", run.exit(), run.err, run.stderr)
	}
	inOrder(t, run.stdout, "kept: the turn stays awaiting review", "turn-1 awaiting_review: change a.go",
		"discard requested for turn-1", "[turn-1] outcome: discarded (user)")
	if got := f.requests(pp.OpResolveTurn); len(got) != 1 || got[0].ResolveTurn.Decision != "discard" || got[0].ResolveTurn.CandidateDigest != "sha256:cand1" {
		t.Errorf("resolve_turn = %+v", got)
	}
	if strings.Count(run.stdout, "candidate sha256:cand1: 1 change(s)") != 1 {
		t.Errorf("the diff was shown more than once:\n%s", run.stdout)
	}
}

func TestChatRetriesTransportFailureWithTheSameRequestKey(t *testing.T) {
	f := newChatFake(t)
	admit := f.submitting(func(id string) { f.finish(id, "no_diff", "") })
	var admitted *pp.Response
	attempts := 0
	f.hooks[pp.OpSubmitTurn] = func(req pp.Request) (*pp.Response, bool) {
		attempts++
		if attempts == 1 { // the service admits the turn, then the reply is lost
			admitted, _ = admit(req)
			return nil, true
		}
		replay := *admitted
		replay.Replayed = true
		return &replay, false
	}
	run := runChatTest(t, f, strings.NewReader("hello\n/quit\n"), nil, false, "--project", "demo")
	if run.exit() != 0 {
		t.Fatalf("exit %d, err %v, stderr %q", run.exit(), run.err, run.stderr)
	}
	got := f.requests(pp.OpSubmitTurn)
	if len(got) != 2 || got[0].SubmitTurn.RequestKey != got[1].SubmitTurn.RequestKey || *got[0].SubmitTurn != *got[1].SubmitTurn {
		t.Fatalf("submit_turn requests must be identical, got %+v", got)
	}
	inOrder(t, run.stdout, "already accepted this prompt", "[turn-1] outcome: no_diff")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.view.Turns) != 1 {
		t.Errorf("%d turns admitted", len(f.view.Turns))
	}
}

func TestChatExhaustedRetriesAreAnUnknownOutcomeNotAResubmission(t *testing.T) {
	f := newChatFake(t)
	f.hooks[pp.OpSubmitTurn] = func(pp.Request) (*pp.Response, bool) { return nil, true }
	run := runChatTest(t, f, strings.NewReader("hello\n/quit\n"), nil, false, "--project", "demo")
	if run.exit() != 4 {
		t.Fatalf("exit %d, want 4; err %v", run.exit(), run.err)
	}
	got := f.requests(pp.OpSubmitTurn)
	if len(got) != 3 || got[0].SubmitTurn.RequestKey != got[2].SubmitTurn.RequestKey || got[0].SubmitTurn.RequestKey != got[1].SubmitTurn.RequestKey {
		t.Errorf("want 3 attempts under one key, got %+v", got)
	}
	if !strings.Contains(run.stderr, "reconnect with --session") {
		t.Errorf("no guidance on the unknown outcome:\n%s", run.stderr)
	}
}

func TestChatStaleRevisionIsReportedAndNeverResubmitted(t *testing.T) {
	f := newChatFake(t)
	f.hooks[pp.OpSubmitTurn] = func(pp.Request) (*pp.Response, bool) {
		f.view.Revision += 4 // another client moved the session
		return chatErrResp("STALE_REVISION", "expected 1"), false
	}
	run := runChatTest(t, f, strings.NewReader("hello\n/quit\n"), nil, false, "--project", "demo")
	if run.exit() != 3 {
		t.Fatalf("exit %d, want 3; err %v", run.exit(), run.err)
	}
	if got := f.requests(pp.OpSubmitTurn); len(got) != 1 {
		t.Errorf("%d submit_turn requests, want exactly the first", len(got))
	}
	if !strings.Contains(run.stderr, "STALE_REVISION") || !strings.Contains(run.stderr, "Nothing was resubmitted") {
		t.Errorf("stale state not reported:\n%s", run.stderr)
	}
	if !strings.Contains(run.stdout, "revision 5") {
		t.Errorf("session was not refreshed and shown:\n%s", run.stdout)
	}
	ops := f.ops()
	if last := slices.Index(ops, pp.OpSubmitTurn); ops[last+1] != pp.OpGetSession {
		t.Errorf("expected a get_session refresh after the stale submit, ops %v", ops)
	}
}

func TestChatReconnectReplaysFromTheStartAndSubmitsNothing(t *testing.T) {
	f := newChatFake(t)
	f.page = 2
	t1 := f.addTurn("first", "terminal")
	f.emit(pp.Event{Kind: pp.EventTurnAdmitted, TurnID: t1, Text: "first"})
	f.emit(pp.Event{Kind: pp.EventAssistantMessage, TurnID: t1, Text: "answer one"})
	f.emit(pp.Event{Kind: pp.EventToolCall, TurnID: t1, Tool: "write", Verdict: "DENY", Hint: "not there"})
	f.finish(t1, "no_diff", "")
	t2 := f.addTurn("second", "terminal")
	f.emit(pp.Event{Kind: pp.EventAssistantMessage, TurnID: t2, Text: "answer two"})
	f.finish(t2, "published", "")
	run := runChatTest(t, f, strings.NewReader("/quit\n"), nil, false, "--session", "psess-t1")
	if run.exit() != 0 {
		t.Fatalf("exit %d, err %v, stderr %q", run.exit(), run.err, run.stderr)
	}
	inOrder(t, run.stdout, "session psess-t1", "[turn-1] you | first", "assistant | answer one", "!! governance hint | not there",
		"[turn-1] outcome: no_diff", "assistant | answer two", "[turn-2] outcome: published")
	var cursors []uint64
	for _, r := range f.requests(pp.OpObserve) {
		cursors = append(cursors, r.Observe.Cursor)
	}
	if !slices.Equal(cursors[:1], []uint64{0}) || !slices.IsSorted(cursors) {
		t.Errorf("observe cursors %v must start at 0 and only advance", cursors)
	}
	for _, op := range []string{pp.OpSubmitTurn, pp.OpCreateSession, pp.OpResolveTurn, pp.OpCancelTurn} {
		if got := f.requests(op); len(got) != 0 {
			t.Errorf("reconnect sent %s: %+v", op, got)
		}
	}
}

func TestChatReconnectOffersAPendingReview(t *testing.T) {
	f := newChatFake(t)
	id := f.addTurn("change", "linked")
	f.emit(pp.Event{Kind: pp.EventTurnAdmitted, TurnID: id, Text: "change"})
	awaitingReview(f)(id)
	f.hooks[pp.OpResolveTurn] = resolving(f, "published", "")
	run := runChatTest(t, f, strings.NewReader("p\n/quit\n"), nil, false, "--session", "psess-t1")
	if run.exit() != 0 {
		t.Fatalf("exit %d, err %v, stderr %q", run.exit(), run.err, run.stderr)
	}
	inOrder(t, run.stdout, "awaiting review", "| +new", "[p]ublish / [d]iscard / [k]eep?", "publish requested", "outcome: published")
	if got := f.requests(pp.OpSubmitTurn); len(got) != 0 {
		t.Errorf("reconnect submitted %+v", got)
	}
	if got := f.requests(pp.OpResolveTurn); len(got) != 1 || got[0].ResolveTurn.CandidateDigest != "sha256:cand1" {
		t.Errorf("resolve_turn = %+v", got)
	}
}

// activeTurn scripts a turn that stays running. cancelHook decides whether cancel_turn settles it.
func activeTurn(f *chatFake, sigs chan os.Signal, signals int, settleOnCancel bool) {
	f.hooks[pp.OpSubmitTurn] = f.submitting(func(string) {})
	observed := false
	f.hooks[pp.OpObserve] = func(pp.Request) (*pp.Response, bool) {
		if !observed && len(f.events) >= 2 {
			observed = true
			for range signals {
				sigs <- os.Interrupt
			}
		}
		return nil, false
	}
	f.hooks[pp.OpCancelTurn] = func(req pp.Request) (*pp.Response, bool) {
		r := req.CancelTurn
		if r.ExpectedRevision != f.view.Revision {
			return chatErrResp("STALE_REVISION", "fake"), false
		}
		f.turn(r.TurnID).CancelRequested = true
		f.emit(pp.Event{Kind: pp.EventTurnCancelRequested, TurnID: r.TurnID})
		if settleOnCancel {
			f.finish(r.TurnID, "cancelled", "")
		}
		return &pp.Response{Turn: f.turnCopy(r.TurnID)}, false
	}
}

func TestChatFirstCtrlCCancelsAndKeepsObservingUntilSettled(t *testing.T) {
	f := newChatFake(t)
	sigs := make(chan os.Signal, 2)
	activeTurn(f, sigs, 1, true)
	run := runChatTest(t, f, strings.NewReader("long job\n/quit\n"), sigs, false, "--project", "demo")
	if run.exit() != 0 {
		t.Fatalf("exit %d, err %v, stderr %q", run.exit(), run.err, run.stderr)
	}
	if got := f.requests(pp.OpCancelTurn); len(got) != 1 {
		t.Fatalf("%d cancel_turn requests, want 1", len(got))
	}
	inOrder(t, run.stdout, "cancel requested for turn-1", "[turn-1] cancel requested", "[turn-1] outcome: cancelled", "detached from")
}

func TestChatSecondCtrlCDetachesWithoutCancellingMore(t *testing.T) {
	f := newChatFake(t)
	sigs := make(chan os.Signal, 2)
	activeTurn(f, sigs, 2, false)
	run := runChatTest(t, f, strings.NewReader("long job\n"), sigs, false, "--project", "demo")
	if run.exit() != 0 {
		t.Fatalf("exit %d, err %v, stderr %q", run.exit(), run.err, run.stderr)
	}
	if got := f.requests(pp.OpCancelTurn); len(got) != 1 {
		t.Errorf("%d cancel_turn requests, want only the first press", len(got))
	}
	if !strings.Contains(run.stdout, "detached from psess-t1") || strings.Contains(run.stdout, "outcome:") {
		t.Errorf("did not detach while the turn was unsettled:\n%s", run.stdout)
	}
}

func TestChatCtrlCWhenIdleDetachesWithoutCancelling(t *testing.T) {
	f := newChatFake(t)
	f.hooks[pp.OpSubmitTurn] = f.submitting(func(id string) { f.finish(id, "no_diff", "") })
	pr, pw := io.Pipe()
	defer pw.Close()
	sigs := make(chan os.Signal, 2)
	go func() {
		fmt.Fprintln(pw, "hello")
		for len(f.requests(pp.OpSubmitTurn)) == 0 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
		sigs <- os.Interrupt
	}()
	run := runChatTest(t, f, pr, sigs, false, "--project", "demo")
	if run.exit() != 0 || !strings.Contains(run.stdout, "detached from") {
		t.Fatalf("exit %d, err %v\n%s", run.exit(), run.err, run.stdout)
	}
	if got := f.requests(pp.OpCancelTurn); len(got) != 0 {
		t.Errorf("an idle Ctrl-C cancelled: %+v", got)
	}
}

func TestChatCancelCommandWhileATurnRunsOnATerminal(t *testing.T) {
	f := newChatFake(t)
	sigs := make(chan os.Signal, 2)
	activeTurn(f, sigs, 0, true)
	started := make(chan struct{})
	var once sync.Once
	observe := f.hooks[pp.OpObserve]
	f.hooks[pp.OpObserve] = func(req pp.Request) (*pp.Response, bool) {
		if len(f.events) >= 2 {
			once.Do(func() { close(started) })
		}
		return observe(req)
	}
	pr, pw := io.Pipe()
	go func() {
		fmt.Fprintln(pw, "long job")
		<-started
		fmt.Fprintln(pw, "/cancel")
		for len(f.requests(pp.OpCancelTurn)) == 0 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(pw, "/quit")
	}()
	run := runChatTest(t, f, pr, sigs, true, "--project", "demo")
	if run.exit() != 0 {
		t.Fatalf("exit %d, err %v, stderr %q", run.exit(), run.err, run.stderr)
	}
	inOrder(t, run.stdout, "> ", "cancel requested for turn-1", "[turn-1] outcome: cancelled")
}

func TestChatRefusedCreateEndsWithTheMappedExit(t *testing.T) {
	f := newChatFake(t)
	f.hooks[pp.OpCreateSession] = func(pp.Request) (*pp.Response, bool) { return chatErrResp("UNKNOWN_PROJECT", "demo"), false }
	run := runChatTest(t, f, strings.NewReader(""), nil, false, "--project", "demo")
	if run.exit() != 2 || !strings.Contains(run.stderr, "UNKNOWN_PROJECT") {
		t.Errorf("exit %d, stderr %q", run.exit(), run.stderr)
	}
	if got := f.requests(pp.OpCreateSession); len(got) != 1 {
		t.Errorf("a service refusal was retried: %d requests", len(got))
	}
}

func TestChatUnknownTurnOutcomeLeavesExit4(t *testing.T) {
	f := newChatFake(t)
	f.hooks[pp.OpSubmitTurn] = f.submitting(func(id string) {
		f.turn(id).State = "unknown"
		f.emit(pp.Event{Kind: pp.EventTurnTerminal, TurnID: id, Outcome: "unknown", Reason: "no disposition"})
	})
	run := runChatTest(t, f, strings.NewReader("go\n/quit\n"), nil, false, "--project", "demo")
	if run.exit() != 4 {
		t.Errorf("exit %d, want 4; stdout %s", run.exit(), run.stdout)
	}
}

func TestChatFlagsAreValidated(t *testing.T) {
	f := newChatFake(t)
	for name, args := range map[string][]string{
		"neither":         {},
		"both":            {"--project", "p", "--session", "s"},
		"session+setting": {"--session", "s", "--mode", "auto"},
		"bad mode":        {"--project", "p", "--mode", "yolo"},
		"negative budget": {"--project", "p", "--budget-tokens", "-1"},
		"positional":      {"--project", "p", "extra"},
	} {
		run := runChatTest(t, f, strings.NewReader(""), nil, false, args...)
		if run.err == nil || run.exit() != -1 {
			t.Errorf("%s: err %v, want a usage error (main exits 2)", name, run.err)
		}
	}
	if len(f.ops()) != 0 {
		t.Errorf("invalid flags reached the service: %v", f.ops())
	}
}

func TestChatExitCodeTable(t *testing.T) {
	want := map[string]int{
		"UNAUTHORIZED_PRINCIPAL": 2, "UNKNOWN_PROJECT": 2, "UNKNOWN_PROVIDER_PROFILE": 2, "PUBLICATION_MODE_NOT_PERMITTED": 2,
		"INVALID_REQUEST": 2, "BUDGET_EXCEEDS_LIMIT": 2, "BUDGET_EXHAUSTED": 2, "UNKNOWN_SESSION": 2, "UNKNOWN_TURN": 2,
		"CANCEL_NOT_ALLOWED": 2,
		"REQUEST_CONFLICT":   3, "STALE_REVISION": 3, "STALE_BASELINE": 3, "PROJECT_BUSY": 3, "TURN_ACTIVE": 3, "PENDING_REVIEW": 3,
		"WRONG_TURN_STATE": 3, "CANDIDATE_MISMATCH": 3, "REVIEW_NOT_PENDING": 3, "CANCELLED": 3,
		"PROJECT_BLOCKED": 4, "PREDECESSOR_UNRESOLVED": 4, "PREDECESSOR_EVIDENCE_INVALID": 4,
		"CATALOG_CORRUPT": 1, "PROTOCOL_ERROR": 1, "SERVICE_UNAVAILABLE": 1, "SOMETHING_NEW": 1,
	}
	for code, exit := range want {
		if got, _ := chatDescribe(&chatServiceError{Code: code}); got != exit {
			t.Errorf("%s maps to %d, want %d", code, got, exit)
		}
	}
	if got, _ := chatDescribe(&chatTransportError{err: io.EOF, sent: true, unknownOutcome: true}); got != 4 {
		t.Errorf("a mutating transport failure maps to %d, want 4", got)
	}
	if got, _ := chatDescribe(&chatTransportError{err: io.EOF}); got != 1 {
		t.Errorf("a read transport failure maps to %d, want 1", got)
	}
	for _, c := range []struct {
		outcome, reason string
		want            int
	}{{"published", "", 0}, {"no_diff", "", 0}, {"discarded", "user", 0}, {"cancelled", "", 0},
		{"failed", "", 1}, {"failed", "STALE_BASELINE", 3}, {"failed", "PREDECESSOR_EVIDENCE_INVALID", 4}, {"unknown", "", 4}} {
		if got := chatOutcomeExit(c.outcome, c.reason); got != c.want {
			t.Errorf("outcome %s/%s maps to %d, want %d", c.outcome, c.reason, got, c.want)
		}
	}
	if err := chatExit(0); err != nil {
		t.Errorf("exit 0 is an error: %v", err)
	}
}

func TestChatCleanStripsTerminalControls(t *testing.T) {
	in := "a\x1b[2Jb\x07c\rd\te\nf\x7fg\u009bh‮i\x00j"
	if got, want := chatClean(in), "a[2Jbcd\te\nfghij"; got != want {
		t.Errorf("chatClean = %q, want %q", got, want)
	}
	if got := chatLine("x\ny\tz\x1b"); got != "x y z" {
		t.Errorf("chatLine = %q", got)
	}
	if got := chatQuote("| ", "a\nb\n"); got != "| a\n| b\n" {
		t.Errorf("chatQuote = %q", got)
	}
}

func (f *chatFake) turnCopy(id string) *pp.TurnView {
	tv := *f.turn(id)
	return &tv
}

func TestChatServiceNotRunningIsPlainErrorNotAnUnknownOutcome(t *testing.T) {
	f := newChatFake(t)
	run := runChatTest(t, f, strings.NewReader(""), nil, false, "--socket", filepath.Join(filepath.Dir(f.sock), "missing"), "--project", "demo")
	if run.exit() != 1 || strings.Contains(run.stderr, "may have been applied") {
		t.Errorf("exit %d, stderr %q; nothing was sent, so the outcome is known", run.exit(), run.stderr)
	}
}
