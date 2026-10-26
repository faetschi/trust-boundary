package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pp "tbound/supervisor/internal/productprotocol"
)

func TestChatRegressionRetryKeepsEarlierUnknownOutcome(t *testing.T) {
	f := newChatFake(t)
	f.hooks[pp.OpSubmitTurn] = f.submitting(func(id string) {
		f.finish(id, "no_diff", "")
	})
	admit := f.hooks[pp.OpSubmitTurn]
	f.hooks[pp.OpSubmitTurn] = func(req pp.Request) (*pp.Response, bool) {
		admit(req)
		// The accepted reply is lost, then retries cannot even dial the service.
		_ = f.listener.Close()
		return nil, true
	}
	run := runChatTest(t, f, strings.NewReader("hello\n/quit\n"), nil, false, "--project", "demo")
	if run.exit() != 4 || !strings.Contains(run.stderr, "may have been applied") {
		t.Fatalf("lost accepted reply followed by dial failures: exit=%d stderr=%q", run.exit(), run.stderr)
	}
	if got := len(f.requests(pp.OpSubmitTurn)); got != 1 {
		t.Fatalf("accepted submissions=%d, want 1", got)
	}
}

func TestChatRetryConclusiveResponseClearsUnknownOutcome(t *testing.T) {
	for _, code := range []string{"", "REQUEST_CONFLICT"} {
		t.Run(code, func(t *testing.T) {
			f := newChatFake(t)
			attempt := 0
			f.hooks[pp.OpSubmitTurn] = func(pp.Request) (*pp.Response, bool) {
				attempt++
				if attempt == 1 {
					return nil, true
				}
				if code != "" {
					return chatErrResp(code, ""), false
				}
				return &pp.Response{Turn: &pp.TurnView{ID: "accepted"}}, false
			}
			client := &chatClient{socket: f.sock, attempts: 3}
			resp, err := client.call(context.Background(), pp.Request{Op: pp.OpSubmitTurn, SubmitTurn: &pp.SubmitTurn{RequestKey: "same"}}, time.Second)
			var transport *chatTransportError
			if errors.As(err, &transport) || (code == "" && (err != nil || resp.Turn.ID != "accepted")) ||
				(code != "" && !chatIsCode(err, code)) {
				t.Fatalf("conclusive response did not clear uncertainty: resp=%+v err=%v", resp, err)
			}
		})
	}
}

func TestChatRetryTransportCodeKeepsUnknownOutcome(t *testing.T) {
	for _, code := range []string{pp.CodeServiceUnavailable, pp.CodeProtocolError} {
		t.Run(code, func(t *testing.T) {
			f := newChatFake(t)
			attempt := 0
			f.hooks[pp.OpSubmitTurn] = func(pp.Request) (*pp.Response, bool) {
				attempt++
				if attempt == 1 {
					return nil, true
				}
				return chatErrResp(code, ""), false
			}
			client := &chatClient{socket: f.sock, attempts: 3}
			_, err := client.call(context.Background(), pp.Request{Op: pp.OpSubmitTurn, SubmitTurn: &pp.SubmitTurn{RequestKey: "same"}}, time.Second)
			if exit, _ := chatDescribe(err); exit != chatExitUnknown {
				t.Fatalf("%s after a lost accepted reply: exit=%d err=%v, want unknown", code, exit, err)
			}
		})
	}
}

func TestChatRetryMalformedResponseKeepsUnknownOutcome(t *testing.T) {
	f := newChatFake(t)
	attempt := 0
	f.hooks[pp.OpSubmitTurn] = func(pp.Request) (*pp.Response, bool) {
		attempt++
		if attempt == 1 {
			return nil, true
		}
		return &pp.Response{Status: "invalid"}, false
	}
	run := runChatTest(t, f, strings.NewReader("hello\n/quit\n"), nil, false, "--project", "demo")
	if run.exit() != 4 || !strings.Contains(run.stderr, "may have been applied") {
		t.Fatalf("a malformed retry reply erased uncertainty: exit=%d stderr=%q", run.exit(), run.stderr)
	}
}

func TestChatReconnectWithTruncatedSnapshotKeepsRevisionAndPendingTurn(t *testing.T) {
	f := newChatFake(t)
	id := f.addTurn("change a.go", "linked")
	awaitingReview(f)(id)
	f.view.TotalTurns, f.view.TurnsTruncated, f.view.Revision = 1000, true, 2001
	f.view.Turns[0].Ordinal = 1000
	f.hooks[pp.OpResolveTurn] = resolving(f, "discarded", "user")
	run := runChatTest(t, f, strings.NewReader("/status\n/discard\n/quit\n"), nil, false, "--session", f.view.ID)
	if run.exit() != 0 {
		t.Fatalf("truncated reconnect: exit=%d stderr=%q", run.exit(), run.stderr)
	}
	got := f.requests(pp.OpResolveTurn)
	if len(got) != 1 || got[0].ResolveTurn.ExpectedRevision != 2001 || got[0].ResolveTurn.TurnID != id || len(f.requests(pp.OpSubmitTurn)) != 0 {
		t.Fatalf("truncated snapshot lost revision or pending turn: resolutions=%+v", got)
	}
	if !strings.Contains(run.stdout, "showing 1 of 1000 turns") {
		t.Fatal("status hid snapshot truncation")
	}
}

func TestChatRegressionCommandsAtReviewPrompt(t *testing.T) {
	for _, command := range []string{"/quit", "/cancel", "/publish", "/discard", "/status"} {
		t.Run(command, func(t *testing.T) {
			f := newChatFake(t)
			f.hooks[pp.OpSubmitTurn] = f.submitting(awaitingReview(f))
			f.hooks[pp.OpResolveTurn] = resolving(f, "discarded", "user")
			f.hooks[pp.OpCancelTurn] = func(req pp.Request) (*pp.Response, bool) {
				f.finish(req.CancelTurn.TurnID, "discarded", "cancelled")
				return &pp.Response{Turn: f.turnCopy(req.CancelTurn.TurnID)}, false
			}
			run := runChatTest(t, f, strings.NewReader("change a.go\n"+command+"\nk\n/quit\n"), nil, false, "--project", "demo")
			if run.exit() != 0 {
				t.Fatalf("exit=%d stderr=%q", run.exit(), run.stderr)
			}
			switch command {
			case "/quit":
				if strings.Contains(run.stdout, "kept:") {
					t.Fatal("/quit was ignored at the review prompt")
				}
			case "/cancel":
				if got := len(f.requests(pp.OpCancelTurn)); got != 1 {
					t.Fatalf("cancel requests=%d, want 1", got)
				}
			case "/publish", "/discard":
				got := f.requests(pp.OpResolveTurn)
				if len(got) != 1 || got[0].ResolveTurn.Decision != strings.TrimPrefix(command, "/") {
					t.Fatalf("resolution requests=%v, want %s", got, command)
				}
			case "/status":
				if !strings.Contains(run.stdout, "turn-1 awaiting_review: change a.go") {
					t.Fatal("/status was ignored at the review prompt")
				}
			}
		})
	}
}
