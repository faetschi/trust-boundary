//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

func TestProductTurnProviderRefusalFailsAndAllowsNextTurn(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	baseline := f.measure(t)
	// The provider ignores parallel_tool_calls:false and returns two complete calls in one delta.
	body := productRejectedCaptureBody()
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{{FailStatus: 200, FailBody: body}}}
	admission := f.submit(t, "parallel", "Read task.txt and summarize it.")
	receipt, turn, err := f.run(t, admission, script)
	if err == nil || !strings.Contains(err.Error(), "the provider returned a response TBound cannot use (multiple or malformed tool calls); the turn was not applied") {
		t.Fatalf("want a readable provider capture refusal, got %v", err)
	}
	t.Logf("provider refusal: %v", err)
	if script.requests() != 1 {
		t.Errorf("refused response retried: requests=%d", script.requests())
	}
	if turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomeFailed || turn.ContextEligible {
		t.Errorf("refused response must settle failed without importing its text: state=%s outcome=%s eligible=%v", turn.State, turn.Outcome, turn.ContextEligible)
	}
	if receipt.Usage != turn.Usage || turn.Usage.HostCalls != 1 || turn.Usage.Tokens != 1024 {
		t.Errorf("want one charged exchange and no tool effect: receipt=%+v usage=%+v", receipt.Usage, turn.Usage)
	}
	wantRefund := productsession.Budget{HostCalls: turn.Reservation.HostCalls - turn.Usage.HostCalls,
		Tokens: turn.Reservation.Tokens - turn.Usage.Tokens, DurationSeconds: turn.Reservation.DurationSeconds - turn.Usage.DurationSeconds}
	if turn.Refund != wantRefund {
		t.Errorf("unused reservation not refunded: got %+v want %+v", turn.Refund, wantRefund)
	}
	session := f.currentSession(t)
	if session.Blocked || session.Reserved != (productsession.Budget{}) || session.Consumed != turn.Usage {
		t.Errorf("failed turn still holds the session: blocked=%v reserved=%+v consumed=%+v", session.Blocked, session.Reserved, session.Consumed)
	}
	if f.measure(t) != baseline || f.notesPublished(t) {
		t.Fatal("a refused response changed the project")
	}
	events, err := newProductEventProjector(f.env).turn(turn)
	if err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; last.Outcome != productsession.OutcomeFailed || !strings.Contains(last.Reason, "multiple or malformed tool calls") || !strings.Contains(last.Reason, "the turn was not applied") {
		t.Fatalf("chat terminal event: %+v", last)
	}
	f.followUp(t, "after-parallel", productsession.OutcomeFailed)
}

func productRejectedCaptureBody() string {
	return fmt.Sprintf(`data: {"id":"parallel-response","model":%q,"created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"read-call","type":"function","function":{"name":"read","arguments":"{\"path\":\"task.txt\"}"}},{"index":1,"id":"write-call","type":"function","function":{"name":"write","arguments":"{\"path\":\"notes.txt\",\"content\":\"must not run\"}"}}]},"finish_reason":null}]}

data: {"id":"parallel-response","model":%q,"created":1,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`, openrouter.ApprovedModel, openrouter.ApprovedModel)
}

func TestProductTurnCaptureRefusalRestartBoundaries(t *testing.T) {
	for _, point := range []string{"after-capture-refusal-record", "after-capture-refusal-checkpoint", "after-capture-refusal-checkpoint-unsupported-fields"} {
		t.Run(point, func(t *testing.T) {
			created := newProductDriverFixture(t, productsession.PublicationAuto)
			baseline := created.measure(t)
			if err := created.env.Catalog.Close(); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestProductTurnCrashChild$", "-test.count=1")
			child.Env = append(os.Environ(), productCrashRootEnv+"="+created.root, productCrashPointEnv+"="+point, productCrashSessionEnv+"="+created.session.ID)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != productCrashExit {
				t.Fatalf("child did not crash: %v\n%s", err, output)
			}
			f := openProductDriverFixture(t, created.root)
			f.session = created.session
			id := f.session.ID + "/turn-1"
			if before := f.turn(t, id); before.State != productsession.StateLinked {
				t.Fatalf("settled before crash: %+v", before)
			}
			err = f.env.ReconcileAfterRestart(context.Background())
			turn, session := f.turn(t, id), f.currentSession(t)
			if f.measure(t) != baseline || f.notesPublished(t) {
				t.Fatal("refused response changed project")
			}
			if point == "after-capture-refusal-record" {
				if err == nil || turn.State != productsession.StateUnknown || !session.Blocked || turn.Refund != (productsession.Budget{}) || session.Consumed != turn.Reservation {
					t.Fatalf("uncompleted host call refunded: turn=%+v session=%+v err=%v", turn, session, err)
				}
				if _, err := f.env.Catalog.SubmitTurn(f.principal, productsession.SubmitTurnRequest{SessionID: f.session.ID, ExpectedRevision: session.Revision, RequestKey: "blocked", Prompt: "next"}, baseline); !errors.Is(err, productsession.ErrCode(productsession.CodePredecessorUnresolved)) {
					t.Fatalf("unknown predecessor admitted: %v", err)
				}
				return
			}
			if err != nil || turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomeFailed || turn.Settlement != productsession.SettlementReconciledAfterRestart || session.Blocked || turn.ContextEligible {
				t.Fatalf("checkpointed refusal: turn=%+v session=%+v err=%v", turn, session, err)
			}
			if turn.Usage.HostCalls != 1 || turn.Usage.Tokens != 1024 || turn.Refund.HostCalls != turn.Reservation.HostCalls-1 || turn.Refund.Tokens != turn.Reservation.Tokens-1024 || session.Reserved != (productsession.Budget{}) || session.Consumed != turn.Usage {
				t.Fatalf("refusal accounting: turn=%+v session=%+v", turn, session)
			}
			events, eventErr := newProductEventProjector(f.env).turn(turn)
			if eventErr != nil || len(events) == 0 || !strings.Contains(events[len(events)-1].Reason, "the turn was not applied") {
				t.Fatalf("restarted chat refusal: %+v %v", events, eventErr)
			}
			f.followUp(t, "after-refusal-restart", productsession.OutcomeFailed)
		})
	}
}

func TestProductTurnMalformedProviderCaptureFails(t *testing.T) {
	for name, args := range map[string]string{
		"unknown argument":   `{"path":"missing.txt","unknown":true}`,
		"malformed argument": `{"path":42}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newProductDriverFixture(t, productsession.PublicationAuto)
			baseline := f.measure(t)
			script := &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: "bad-response", CallID: "bad-call", Tool: "read", Arguments: json.RawMessage(args)}, {ResponseID: "must-not-run", Text: "Wrong."}}}
			_, turn, err := f.run(t, f.submit(t, "bad", "Try the tool."), script)
			if err == nil || !strings.Contains(err.Error(), "invalid provider response") || turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomeFailed || script.requests() != 1 || turn.ContextEligible {
				t.Fatalf("capture rejection: state=%s outcome=%s requests=%d err=%v", turn.State, turn.Outcome, script.requests(), err)
			}
			if turn.Usage.HostCalls != 1 || turn.Usage.Tokens != 1024 || turn.Refund.HostCalls != turn.Reservation.HostCalls-1 || turn.Refund.Tokens != turn.Reservation.Tokens-1024 || f.measure(t) != baseline {
				t.Fatalf("capture refusal accounting/project: %+v", turn)
			}
			f.followUp(t, "after-invalid-capture", productsession.OutcomeFailed)
		})
	}
}
