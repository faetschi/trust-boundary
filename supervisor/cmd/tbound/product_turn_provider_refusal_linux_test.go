//go:build linux

package main

import (
	"fmt"
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
	body := fmt.Sprintf(`data: {"id":"parallel-response","model":%q,"created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"read-call","type":"function","function":{"name":"read","arguments":"{\"path\":\"task.txt\"}"}},{"index":1,"id":"write-call","type":"function","function":{"name":"write","arguments":"{\"path\":\"notes.txt\",\"content\":\"must not run\"}"}}]},"finish_reason":null}]}

data: {"id":"parallel-response","model":%q,"created":1,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`, openrouter.ApprovedModel, openrouter.ApprovedModel)
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{{FailStatus: 200, FailBody: body}}}
	admission := f.submit(t, "parallel", "Read task.txt and summarize it.")
	receipt, turn, err := f.run(t, admission, script)
	if err == nil || !strings.Contains(err.Error(), "multiple or malformed tool calls") {
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
	f.followUp(t, "after-parallel", productsession.OutcomeFailed)
}
