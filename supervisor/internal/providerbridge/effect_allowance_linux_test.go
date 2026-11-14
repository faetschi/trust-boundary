//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
)

// hostCallsLeftConversation is a host-call-accounted conversation whose workflow has maxHostCalls in all
// and spentBefore of them charged by earlier turns; its own reservations charge the rest.
func hostCallsLeftConversation(t *testing.T, responses []fixtureResponse, spentBefore, maxHostCalls uint64) (*Conversation, *audit.Journal, *fixtureDoer, *retryHooks) {
	t.Helper()
	cfg := recoveryTestConfig()
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	cfg.HostCallsLeft = func() uint64 { return maxHostCalls - spentBefore - uint64(len(hooks.reservations)) }
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "host-calls-left.audit"))
	doer := &fixtureDoer{responses: responses}
	conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-host-calls-left", "prompt"); err != nil {
		t.Fatal(err)
	}
	return conversation, journal, doer, hooks
}

var (
	capToolCall  = fixtureResponse{responseID: "response-tool", callID: "call-1", tool: "read", arguments: json.RawMessage(`{"path":"x"}`)}
	capTextReply = fixtureResponse{responseID: "response-text", text: "done"}
)

// requireUnknownEnd checks a turn that ended with its last attempt's host call reserved and closed by an
// unknown exchange (the shape abandon reconciles): the reservation is not settled, no call is captured,
// the conversation is closed, and the error is a journaled provider-turn failure, which Serve types.
func requireUnknownEnd(t *testing.T, conversation *Conversation, journal *audit.Journal, hooks *retryHooks, err error) {
	t.Helper()
	var journaled journaledFailure
	if !errors.Is(err, ErrClosed) || !errors.As(err, &journaled) {
		t.Fatalf("turn error = %v, want a journaled provider-turn failure", err)
	}
	kinds := journalKinds(t, journal)
	if len(kinds) < 2 || kinds[len(kinds)-2] != "provider_host_call_reserved" || kinds[len(kinds)-1] != "provider_exchange_unknown" {
		t.Fatalf("journal kinds = %v, want the reservation closed by an unknown exchange", kinds)
	}
	trace, _ := journal.Trace()
	var unknown struct {
		HostCall *HostCallBinding `json:"host_call"`
	}
	last := hooks.reservations[len(hooks.reservations)-1]
	if json.Unmarshal(trace.Records[len(trace.Records)-1].Event.Data, &unknown) != nil || unknown.HostCall == nil || *unknown.HostCall != last {
		t.Fatalf("unknown record = %+v, want it bound to the last reservation %+v", unknown, last)
	}
	for _, completion := range hooks.completions {
		if completion.Binding == last {
			t.Fatal("the last reservation was settled")
		}
	}
	if conversation.current != nil {
		t.Fatal("a tool call was captured")
	}
	if !errorsIsClosed(conversation) {
		t.Fatal("conversation stayed open")
	}
}

// The F26 unit-90 sequence: 6 of 7 host calls spent, the exchange takes the 7th and returns a tool call.
// It used to be captured and then its effect refused, which left it pending; now it is never captured.
// With 2 left the effect would fit, but it would take the last host call and the result could not be
// returned, so that tool call ends the turn too.
func TestToolCallWithoutHostCallsForItsEffectAndResultIsNotCaptured(t *testing.T) {
	for _, spent := range []uint64{6, 5} {
		conversation, journal, _, hooks := hostCallsLeftConversation(t, []fixtureResponse{capToolCall}, spent, 7)
		_, err := conversation.NextTurn(context.Background())
		if !errors.Is(err, ErrNoHostCallForToolEffect) {
			t.Fatalf("%d spent: turn error = %v, want ErrNoHostCallForToolEffect", spent, err)
		}
		requireUnknownEnd(t, conversation, journal, hooks, err)
	}
}

// The exchange itself is still sent on the last host calls: a text reply needs no effect and ends the task
// (the 2N+1 budgets of G1, the corpus scripts and the three-tool compliant runs).
func TestTextReplyOnTheLastHostCallCompletes(t *testing.T) {
	for _, spent := range []uint64{6, 5} {
		conversation, _, _, hooks := hostCallsLeftConversation(t, []fixtureResponse{capTextReply}, spent, 7)
		turn, err := conversation.NextTurn(context.Background())
		if err != nil || turn.AssistantText != "done" || len(hooks.completions) != 1 {
			t.Fatalf("%d spent: turn = %+v, err = %v, completions = %d", spent, turn, err, len(hooks.completions))
		}
	}
}

// With 3 left the exchange takes one and the effect and the result turn fit in the other two.
func TestToolCallWithThreeHostCallsLeftIsCaptured(t *testing.T) {
	conversation, _, _, hooks := hostCallsLeftConversation(t, []fixtureResponse{capToolCall}, 4, 7)
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.ToolCall == nil || conversation.current == nil || len(hooks.completions) != 1 {
		t.Fatalf("turn = %+v, err = %v, completions = %d", turn, err, len(hooks.completions))
	}
}

// Retried and resampled attempts each charge a host call before the response is judged, so the check
// sees them: an attempt that takes the room makes the final tool call uncapturable, one host call more
// and it is captured.
func TestRetriesAndResamplesCountAgainstTheToolCall(t *testing.T) {
	for name, refused := range map[string]fixtureResponse{
		"retry":    {failStatus: 503, failBody: unavailableBody},
		"resample": {failStatus: 200, failBody: parallelCallStream(t)},
	} {
		t.Run(name, func(t *testing.T) {
			conversation, journal, _, hooks := hostCallsLeftConversation(t, []fixtureResponse{refused, capToolCall}, 4, 7)
			_, err := conversation.NextTurn(context.Background())
			if len(hooks.reservations) != 2 || !errors.Is(err, ErrNoHostCallForToolEffect) {
				t.Fatalf("reservations = %d, err = %v; want both attempts charged and the tool call refused", len(hooks.reservations), err)
			}
			requireUnknownEnd(t, conversation, journal, hooks, err)
			if kinds := journalKinds(t, journal); !strings.Contains(strings.Join(kinds, ","), exchangeRetryKind) {
				t.Fatalf("journal kinds = %v, want the refused attempt journaled as retried", kinds)
			}

			conversation, _, _, hooks = hostCallsLeftConversation(t, []fixtureResponse{refused, capToolCall}, 3, 7)
			turn, err := conversation.NextTurn(context.Background())
			if err != nil || turn.ToolCall == nil || len(hooks.reservations) != 2 {
				t.Fatalf("with one host call more: turn = %+v, err = %v, reservations = %d", turn, err, len(hooks.reservations))
			}
		})
	}
}

// A retry or resample is not attempted when no host call is left for it: the reservation it would need
// is refused before anything is reserved, which leaves no host call for abandon to close. The turn ends
// on the refused attempt instead, whose own reservation is closed as unknown.
func TestNoRetryOrResampleWithoutAHostCallForIt(t *testing.T) {
	for name, refused := range map[string]fixtureResponse{
		"retry":    {failStatus: 503, failBody: unavailableBody},
		"resample": {failStatus: 200, failBody: parallelCallStream(t)},
	} {
		t.Run(name, func(t *testing.T) {
			conversation, journal, doer, hooks := hostCallsLeftConversation(t, []fixtureResponse{refused, capTextReply}, 6, 7)
			_, err := conversation.NextTurn(context.Background())
			if doer.call != 1 || len(hooks.reservations) != 1 {
				t.Fatalf("attempts = %d, reservations = %d; want the refused attempt only", doer.call, len(hooks.reservations))
			}
			requireUnknownEnd(t, conversation, journal, hooks, err)
		})
	}
}

// A refused tool call reaches the worker as an error frame and Serve types it as a failed provider turn,
// the channel error the service settles and abandon reconciles.
func TestServeTypesTheRefusedToolCallAsAProviderTurnFailure(t *testing.T) {
	conversation, _, _, _ := hostCallsLeftConversation(t, []fixtureResponse{capToolCall}, 6, 7)
	if err := serveOneTurn(t, conversation); !errors.Is(err, ErrProviderTurnFailed) || !errors.Is(err, ErrNoHostCallForToolEffect) {
		t.Fatalf("Serve = %v, want ErrProviderTurnFailed wrapping ErrNoHostCallForToolEffect", err)
	}
}

// On the research route the refused exchange's spend is settled to its usage, not left uncertain.
func TestRefusedToolCallSettlesItsSpend(t *testing.T) {
	doer := &spendDoer{callID: "call-spend"}
	spend := &spendRecorder{doer: doer}
	conversation, err := newWithDoer(Config{
		ConversationID: "conversation-spend", WorkflowID: "workflow-spend", ProfileID: "profile-spend",
		InitialGenerationID: "generation-1", InitialTreeDigest: "sha256:" + strings.Repeat("0", 64),
		SystemPrompt: "Spend system prompt.", DeveloperPrompt: "Spend developer profile.",
		Route: spendTestRoute(t), Spend: spend, HostCallsLeft: func() uint64 { return 1 },
	}, spendTestModel, doer, &memoryJournal{})
	if err != nil {
		t.Fatal(err)
	}
	admitSpendPrompt(t, conversation)
	if _, err := conversation.NextTurn(context.Background()); !errors.Is(err, ErrNoHostCallForToolEffect) {
		t.Fatalf("turn error = %v, want ErrNoHostCallForToolEffect", err)
	}
	if got := strings.Join(spend.sequence(), ","); got != "reserve,settle:unit-1" {
		t.Fatalf("spend = %s, want the unit settled to its usage", got)
	}
}
