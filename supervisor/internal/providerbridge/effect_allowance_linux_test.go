//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// toolChoice is the tool_choice member of an outbound request body.
func toolChoice(t *testing.T, body []byte) string {
	t.Helper()
	var request struct {
		ToolChoice string `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	return request.ToolChoice
}

// lastRecord is the newest provider journal record.
func lastRecord(t *testing.T, journal *audit.Journal) audit.Record {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) == 0 {
		t.Fatalf("provider journal: %d records, %v", len(trace.Records), err)
	}
	return trace.Records[len(trace.Records)-1]
}

func replayHostCallsLeft(t *testing.T, journal *audit.Journal) (TranscriptProjection, error) {
	t.Helper()
	return ReplayTranscript(journal, RecoveryPins{Config: recoveryTestConfig(), Model: ApprovedModel,
		Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}, nil)
}

// F29 units 63 and 90 (F26 unit 90 before them): with 1 or 2 host calls left the exchange cannot be followed
// by a tool call's effect and the exchange that returns its result, so it is sent with tools disabled and the
// model's text reply ends the task. The request and the journaled exchange both show it.
func TestLastHostCallsAreSentWithToolsDisabled(t *testing.T) {
	for _, spent := range []uint64{6, 5} {
		conversation, journal, doer, hooks := hostCallsLeftConversation(t, []fixtureResponse{capTextReply}, spent, 7)
		turn, err := conversation.NextTurn(context.Background())
		if err != nil || turn.AssistantText != "done" || turn.ToolCall != nil || len(hooks.completions) != 1 {
			t.Fatalf("%d spent: turn = %+v, err = %v, completions = %d", spent, turn, err, len(hooks.completions))
		}
		if got := toolChoice(t, doer.requests[0]); got != "none" {
			t.Fatalf("%d spent: tool_choice = %q, want none", spent, got)
		}
		if record := lastRecord(t, journal); record.Event.Kind != "provider_exchange" ||
			!strings.Contains(string(record.Event.Data), `"tools_disabled":true`) {
			t.Fatalf("%d spent: last record %s does not show the tools-disabled request: %s", spent, record.Event.Kind, record.Event.Data)
		}
		if _, err := replayHostCallsLeft(t, journal); err != nil {
			t.Fatalf("%d spent: replay of the tools-disabled exchange: %v", spent, err)
		}
	}
}

// A provider that returns a tool call on a tools-disabled request: the call is refused, never captured or
// executed, and the reply ends the turn as the final (text-less) answer. The exchange is journaled as complete
// with the refused call, its host call is settled, and replay accepts it, so the run ends typed, not UNKNOWN.
func TestToolCallDespiteDisabledToolsIsRefusedAndEndsTheTurn(t *testing.T) {
	for _, spent := range []uint64{6, 5} {
		conversation, journal, _, hooks := hostCallsLeftConversation(t, []fixtureResponse{capToolCall}, spent, 7)
		turn, err := conversation.NextTurn(context.Background())
		if err != nil || turn.ToolCall != nil || turn.FinishReason != "stop" || turn.AssistantText != "" {
			t.Fatalf("%d spent: turn = %+v, err = %v; want the text-less final reply", spent, turn, err)
		}
		if conversation.current != nil {
			t.Fatalf("%d spent: the refused tool call was captured", spent)
		}
		last := hooks.reservations[len(hooks.reservations)-1]
		if len(hooks.completions) != 1 || hooks.completions[0].Binding != last {
			t.Fatalf("%d spent: completions = %+v, want the last reservation settled", spent, hooks.completions)
		}
		record := lastRecord(t, journal)
		if record.Event.Kind != "provider_exchange" || hooks.completions[0].OutcomeDigest != record.Hash ||
			!strings.Contains(string(record.Event.Data), `"refused_tool_call":{"id":"call-1","name":"read"`) {
			t.Fatalf("%d spent: last record %s does not carry the refused call: %s", spent, record.Event.Kind, record.Event.Data)
		}
		projection, err := replayHostCallsLeft(t, journal)
		if err != nil || projection.CapturedCallCount != 0 || projection.ExchangeCount != 1 ||
			projection.HostCall == nil || *projection.HostCall != last || projection.HostCallOutcomeDigest != record.Hash {
			t.Fatalf("%d spent: replay = %+v, %v", spent, projection, err)
		}
	}
}

// tamperedExchangeJournal copies source with its last provider_exchange record rewritten by mutate.
func tamperedExchangeJournal(t *testing.T, source *audit.Journal, mutate func(*exchangeEvidence)) *audit.Journal {
	t.Helper()
	trace, err := source.Trace()
	if err != nil {
		t.Fatal(err)
	}
	last := -1
	for index, record := range trace.Records {
		if record.Event.Kind == "provider_exchange" {
			last = index
		}
	}
	copied := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "tampered-exchange.audit"))
	for index, record := range trace.Records {
		event := audit.Event{Kind: record.Event.Kind, ID: record.Event.ID, Data: append([]byte(nil), record.Event.Data...)}
		if index == last {
			var evidence exchangeEvidence
			if err := json.Unmarshal(event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			mutate(&evidence)
			if event.Data, err = json.Marshal(evidence); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := copied.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	return copied
}

// Replay accepts a refused call only where the live rule made one: on a tools-disabled request, recorded as
// refused_tool_call and not as a captured tool call.
func TestReplayRefusesForgedRefusedToolCalls(t *testing.T) {
	pins := RecoveryPins{Config: recoveryTestConfig(), Model: ApprovedModel, Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}
	refusedConversation, refused, _, _ := hostCallsLeftConversation(t, []fixtureResponse{capToolCall}, 6, 7)
	capturedConversation, captured, _, _ := hostCallsLeftConversation(t, []fixtureResponse{capToolCall}, 4, 7)
	for _, conversation := range []*Conversation{refusedConversation, capturedConversation} {
		if _, err := conversation.NextTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for name, journal := range map[string]*audit.Journal{
		"refused call recorded as captured": tamperedExchangeJournal(t, refused, func(e *exchangeEvidence) {
			e.ToolCall, e.RefusedToolCall = e.RefusedToolCall, nil
		}),
		"captured call recorded as refused": tamperedExchangeJournal(t, captured, func(e *exchangeEvidence) {
			e.RefusedToolCall, e.ToolCall = e.ToolCall, nil
		}),
		"refused call without its record": tamperedExchangeJournal(t, refused, func(e *exchangeEvidence) { e.RefusedToolCall = nil }),
	} {
		if _, err := ReplayTranscript(journal, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
			t.Errorf("%s: replay = %v, want ErrTranscriptReplay", name, err)
		}
	}
	for name, journal := range map[string]*audit.Journal{"refused": refused, "captured": captured} {
		if _, err := ReplayTranscript(journal, pins, nil); err != nil {
			t.Errorf("untampered %s journal: replay = %v", name, err)
		}
	}
}

// With 3 left the exchange takes one and the effect and the result turn fit in the other two: tools stay on.
func TestToolCallWithThreeHostCallsLeftIsCaptured(t *testing.T) {
	conversation, _, doer, hooks := hostCallsLeftConversation(t, []fixtureResponse{capToolCall}, 4, 7)
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.ToolCall == nil || conversation.current == nil || len(hooks.completions) != 1 {
		t.Fatalf("turn = %+v, err = %v, completions = %d", turn, err, len(hooks.completions))
	}
	if got := toolChoice(t, doer.requests[0]); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto", got)
	}
}

// Retried and resampled attempts each charge a host call, so the rule is applied per attempt: a refused
// attempt on 3 left leaves 2, and the next attempt goes out with tools disabled (a resample too, which
// otherwise repeats the refused request exactly). One host call more and tools stay on.
func TestRetriesAndResamplesCountAgainstTheToolCall(t *testing.T) {
	for name, refused := range map[string]fixtureResponse{
		"retry":    {failStatus: 503, failBody: unavailableBody},
		"resample": {failStatus: 200, failBody: parallelCallStream(t)},
	} {
		t.Run(name, func(t *testing.T) {
			conversation, journal, doer, hooks := hostCallsLeftConversation(t, []fixtureResponse{refused, capToolCall}, 4, 7)
			turn, err := conversation.NextTurn(context.Background())
			if len(hooks.reservations) != 2 || err != nil || turn.ToolCall != nil || conversation.current != nil {
				t.Fatalf("reservations = %d, turn = %+v, err = %v; want both attempts charged and the tool call refused",
					len(hooks.reservations), turn, err)
			}
			if first, second := toolChoice(t, doer.requests[0]), toolChoice(t, doer.requests[1]); first != "auto" || second != "none" {
				t.Fatalf("tool_choice = %q then %q, want auto then none", first, second)
			}
			if kinds := journalKinds(t, journal); !strings.Contains(strings.Join(kinds, ","), exchangeRetryKind) {
				t.Fatalf("journal kinds = %v, want the refused attempt journaled as retried", kinds)
			}
			if _, err := replayHostCallsLeft(t, journal); err != nil {
				t.Fatalf("replay: %v", err)
			}

			conversation, _, doer, hooks = hostCallsLeftConversation(t, []fixtureResponse{refused, capToolCall}, 3, 7)
			turn, err = conversation.NextTurn(context.Background())
			if err != nil || turn.ToolCall == nil || len(hooks.reservations) != 2 || toolChoice(t, doer.requests[1]) != "auto" {
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

// The refused call reaches the worker as an ordinary final turn: no tool call and finish reason stop, which
// the Pi adapter requires to agree.
func TestServeReleasesTheRefusedToolCallTurnAsAFinalReply(t *testing.T) {
	conversation, _, _, _ := hostCallsLeftConversation(t, []fixtureResponse{capToolCall}, 6, 7)
	serverConn, clientConn := net.Pipe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(context.Background(), serverConn, conversation) }()
	if err := writeChannelTestFrame(clientConn, map[string]any{
		"version": bridgeSchemaVersion, "sequence": 1, "kind": "next", "request_id": "request-final",
	}); err != nil {
		t.Fatal(err)
	}
	frame, err := readChannelTestFrame(clientConn)
	turn, _ := frame["turn"].(map[string]any)
	if err != nil || frame["kind"] != "turn" || turn == nil || turn["finish_reason"] != "stop" || turn["tool_call"] != nil {
		t.Fatalf("final turn frame = %v, err = %v", frame, err)
	}
	clientConn.Close()
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("provider bridge did not stop after the channel closed")
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
	if turn, err := conversation.NextTurn(context.Background()); err != nil || turn.ToolCall != nil {
		t.Fatalf("turn = %+v, err = %v; want the refused call to end the turn", turn, err)
	}
	if got := strings.Join(spend.sequence(), ","); got != "reserve,settle:unit-1" {
		t.Fatalf("spend = %s, want the unit settled to its usage", got)
	}
}
