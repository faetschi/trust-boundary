//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
)

const unavailableBody = `{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`

type retryHooks struct {
	reservations []HostCallBinding
	completions  []HostCallCompletion
	limits       int
}

func (h *retryHooks) apply(cfg *Config) {
	cfg.SelectCompletionTokenLimit = func(context.Context) (uint64, error) { h.limits++; return 512, nil }
	cfg.ReserveHostCall = func(_ context.Context, request broker.RequestRecord, limit uint64) (HostCallBinding, error) {
		binding := HostCallBinding{CallID: fmt.Sprintf("host-call-%d", len(h.reservations)+1),
			RequestDigest: broker.DigestRequestRecord(request), TokenUpperBound: limit}
		h.reservations = append(h.reservations, binding)
		return binding, nil
	}
	cfg.CompleteHostCall = func(_ context.Context, completion HostCallCompletion) error {
		h.completions = append(h.completions, completion)
		return nil
	}
}

func retryConversation(t *testing.T, responses []fixtureResponse) (*Conversation, *audit.Journal, *fixtureDoer, *retryHooks, Config) {
	t.Helper()
	cfg := recoveryTestConfig()
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "retry.audit"))
	doer := &fixtureDoer{responses: responses}
	conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-retry", "prompt for a flaky provider"); err != nil {
		t.Fatal(err)
	}
	return conversation, journal, doer, hooks, cfg
}

func journalKinds(t *testing.T, journal *audit.Journal) []string {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, len(trace.Records))
	for i, record := range trace.Records {
		kinds[i] = record.Event.Kind
	}
	return kinds
}

func TestRetryableFailureClassification(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		body   string
		want   bool
	}{
		"502":                         {502, "", true},
		"503 with the endpoint body":  {503, unavailableBody, true},
		"503 bare":                    {503, "", true},
		"504":                         {504, "", true},
		"422 endpoint unavailable":    {422, unavailableBody, true},
		"422 other":                   {422, `{"error":"invalid"}`, false},
		"400":                         {400, unavailableBody, false},
		"401":                         {401, "", false},
		"429":                         {429, "", false},
		"500":                         {500, unavailableBody, false},
		"200":                         {200, "", false},
		"503 carrying stream bytes":   {503, "data: {\"id\":\"x\"}\n\n", false},
		"503 carrying a completion":   {503, `{"choices":[{}]}`, false},
		"503 above the body bound":    {503, strings.Repeat("x", retryFailureBodyLimit+1), false},
		"422 stream bytes and phrase": {422, "data: Endpoint is unavailable", false},
	} {
		if got := RetryableProviderFailure(test.status, []byte(test.body)); got != test.want {
			t.Errorf("%s: retryable = %v, want %v", name, got, test.want)
		}
	}
}

func TestRetryableFailuresThenSuccessAreEachReservedAndReplay(t *testing.T) {
	conversation, journal, doer, hooks, cfg := retryConversation(t, []fixtureResponse{
		{failStatus: 503, failBody: unavailableBody},
		{failStatus: 422, failBody: unavailableBody},
		{responseID: "response-ok", text: "finished"},
	})
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.AssistantText != "finished" || turn.ToolCall != nil {
		t.Fatalf("turn = %+v, err = %v", turn, err)
	}
	if doer.call != 3 || len(hooks.reservations) != 3 || len(hooks.completions) != 3 || hooks.limits != 3 {
		t.Fatalf("attempts=%d reservations=%d completions=%d limit selections=%d, want 3 each",
			doer.call, len(hooks.reservations), len(hooks.completions), hooks.limits)
	}
	wantKinds := []string{"provider_user_admitted",
		"provider_host_call_reserved", exchangeRetryKind,
		"provider_host_call_reserved", exchangeRetryKind,
		"provider_host_call_reserved", "provider_exchange"}
	if got := journalKinds(t, journal); strings.Join(got, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("journal kinds = %v, want %v", got, wantKinds)
	}
	trace, _ := journal.Trace()
	for index, completion := range hooks.completions {
		if completion.Binding != hooks.reservations[index] || completion.OutcomeDigest != trace.Records[[]int{2, 4, 6}[index]].Hash {
			t.Fatalf("completion %d does not settle reservation %d with its own outcome record: %+v", index, index, completion)
		}
	}

	projection, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel,
		Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}, nil)
	if err != nil {
		t.Fatalf("replay of a journal with retryable failures then success: %v", err)
	}
	if projection.ProviderRetries != 2 || projection.ExchangeCount != 1 || projection.ProviderSequence != 1 ||
		projection.HostCall == nil || *projection.HostCall != hooks.reservations[2] ||
		projection.HostCallOutcomeDigest != trace.Records[6].Hash || projection.HostCallRetryableFailure || !VerifyTranscriptProjectionCommitment(projection) ||
		projection.Disposition != DispositionAwaitingHostPrompt {
		t.Fatalf("projection = %+v", projection)
	}
}

func TestRetryExhaustionFallsBackToTheUnknownExchangePath(t *testing.T) {
	conversation, journal, doer, hooks, cfg := retryConversation(t, []fixtureResponse{
		{failStatus: 503}, {failStatus: 503}, {failStatus: 503}, {responseID: "never", text: "unreachable"},
	})
	_, err := conversation.NextTurn(context.Background())
	if err == nil || !errors.Is(err, ErrClosed) {
		t.Fatalf("exhausted retries did not fail the turn: %v", err)
	}
	if doer.call != 1+MaxProviderRetries || len(hooks.reservations) != 3 || len(hooks.completions) != 2 {
		t.Fatalf("attempts=%d reservations=%d completions=%d, want %d attempts, 3 reservations, 2 settled",
			doer.call, len(hooks.reservations), len(hooks.completions), 1+MaxProviderRetries)
	}
	wantKinds := []string{"provider_user_admitted",
		"provider_host_call_reserved", exchangeRetryKind,
		"provider_host_call_reserved", exchangeRetryKind,
		"provider_host_call_reserved", "provider_exchange_unknown"}
	if got := journalKinds(t, journal); strings.Join(got, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("journal kinds = %v, want %v", got, wantKinds)
	}
	if _, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel,
		Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("a journal ending in an UNKNOWN exchange replayed: %v", err)
	}
}

func TestOtherFailuresAreNeverRetried(t *testing.T) {
	for name, failure := range map[string]fixtureResponse{
		"400":               {failStatus: 400, failBody: `{"error":"bad"}`},
		"401":               {failStatus: 401},
		"422 other message": {failStatus: 422, failBody: `{"error":"unprocessable"}`},
		"500":               {failStatus: 500, failBody: unavailableBody},
	} {
		conversation, journal, doer, hooks, _ := retryConversation(t, []fixtureResponse{failure, {responseID: "r", text: "late"}})
		if _, err := conversation.NextTurn(context.Background()); err == nil {
			t.Fatalf("%s: failed attempt succeeded", name)
		}
		kinds := journalKinds(t, journal)
		if doer.call != 1 || len(hooks.reservations) != 1 || len(hooks.completions) != 0 ||
			kinds[len(kinds)-1] != "provider_exchange_unknown" {
			t.Fatalf("%s: attempts=%d reservations=%d completions=%d kinds=%v, want one attempt ending UNKNOWN",
				name, doer.call, len(hooks.reservations), len(hooks.completions), kinds)
		}
	}
}

func TestTransportErrorsAndMissingHostCallAccountingAreNotRetried(t *testing.T) {
	// No host-call accounting: no retry even for a 503, there is no reservation to charge.
	cfg := recoveryTestConfig()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "plain.audit"))
	doer := &fixtureDoer{responses: []fixtureResponse{{failStatus: 503}, {responseID: "r", text: "late"}}}
	conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err == nil || doer.call != 1 {
		t.Fatalf("a conversation without host-call accounting retried: attempts=%d err=%v", doer.call, err)
	}
}

func TestExhaustedHostCallAllowanceStopsTheRetriesThroughTheExistingFailurePath(t *testing.T) {
	cfg := recoveryTestConfig()
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	reserve := cfg.ReserveHostCall
	cfg.ReserveHostCall = func(ctx context.Context, request broker.RequestRecord, limit uint64) (HostCallBinding, error) {
		if len(hooks.reservations) == 2 {
			return HostCallBinding{}, errors.New("workflow host-call allowance exhausted")
		}
		return reserve(ctx, request, limit)
	}
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "exhausted.audit"))
	doer := &fixtureDoer{responses: []fixtureResponse{{failStatus: 503}, {failStatus: 503}, {responseID: "r", text: "late"}}}
	conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err == nil || !errors.Is(err, ErrClosed) {
		t.Fatalf("the refused reservation did not fail the turn: %v", err)
	}
	if doer.call != 2 || len(hooks.reservations) != 2 {
		t.Fatalf("attempts=%d reservations=%d, want the third attempt never dispatched", doer.call, len(hooks.reservations))
	}
}

// tamperedRetryJournal re-appends every event of a real retry journal with one retry record
// rewritten by mutate; the audit hash chain of the copy is valid, only the evidence is wrong.
func tamperedRetryJournal(t *testing.T, source *audit.Journal, mutate func(*exchangeRetryEvidence)) *audit.Journal {
	return truncatedRetryJournal(t, source, -1, mutate)
}

func truncatedRetryJournal(t *testing.T, source *audit.Journal, keep int, mutate func(*exchangeRetryEvidence)) *audit.Journal {
	t.Helper()
	trace, err := source.Trace()
	if err != nil {
		t.Fatal(err)
	}
	copyJournal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "tampered.audit"))
	mutated := false
	for index, record := range trace.Records {
		if keep >= 0 && index >= keep {
			break
		}
		event := audit.Event{Kind: record.Event.Kind, ID: record.Event.ID, Data: append([]byte(nil), record.Event.Data...)}
		if record.Event.Kind == exchangeRetryKind && !mutated {
			var evidence exchangeRetryEvidence
			if err := json.Unmarshal(event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			mutate(&evidence)
			if event.Data, err = json.Marshal(evidence); err != nil {
				t.Fatal(err)
			}
			mutated = true
		}
		if _, err := copyJournal.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	return copyJournal
}

func TestReplayRefusesForgedRetryRecords(t *testing.T) {
	conversation, journal, _, _, cfg := retryConversation(t, []fixtureResponse{
		{failStatus: 503}, {responseID: "response-ok", text: "finished"},
	})
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}
	if _, err := ReplayTranscript(tamperedRetryJournal(t, journal, func(*exchangeRetryEvidence) {}), pins, nil); err != nil {
		t.Fatalf("an unmodified copy must replay: %v", err)
	}
	for name, mutate := range map[string]func(*exchangeRetryEvidence){
		"another reservation": func(e *exchangeRetryEvidence) { e.HostCall.CallID = "host-call-other" },
		"another request": func(e *exchangeRetryEvidence) {
			e.Request.Body = append([]byte(nil), e.Request.Body...)
			e.Request.Body[0] ^= 1
		},
		"not retryable":  func(e *exchangeRetryEvidence) { e.StatusCode = 400 },
		"wrong attempt":  func(e *exchangeRetryEvidence) { e.Attempt = 2 },
		"wrong turn":     func(e *exchangeRetryEvidence) { e.Turn = 2 },
		"wrong workflow": func(e *exchangeRetryEvidence) { e.WorkflowID = "workflow-other" },
	} {
		if _, err := ReplayTranscript(tamperedRetryJournal(t, journal, mutate), pins, nil); !errors.Is(err, ErrTranscriptReplay) {
			t.Errorf("%s: forged retry record replayed: %v", name, err)
		}
	}
}

// A crash after a failed attempt was journaled and before the next reservation leaves a journal that
// replays to the ordinary idle point before an exchange: nothing is open and nothing is UNKNOWN.
func TestJournalEndingAfterARetryableFailureReplaysToAwaitingExchange(t *testing.T) {
	conversation, journal, _, hooks, cfg := retryConversation(t, []fixtureResponse{
		{failStatus: 503}, {responseID: "response-ok", text: "finished"},
	})
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}
	cut := truncatedRetryJournal(t, journal, 3, func(*exchangeRetryEvidence) {}) // user prompt, reservation, retry record
	projection, err := ReplayTranscript(cut, pins, nil)
	if err != nil {
		t.Fatalf("journal ending after a retryable failure did not replay: %v", err)
	}
	if projection.Disposition != DispositionAwaitingProviderExchange || !projection.HostCallRetryableFailure || projection.ProviderRetries != 1 ||
		projection.ExchangeCount != 0 || projection.HostCall == nil || *projection.HostCall != hooks.reservations[0] {
		t.Fatalf("projection = %+v", projection)
	}
	// A journal that ends with the failed attempt's reservation still open (crash before the record) is
	// the existing unsettled-reservation refusal.
	open := truncatedRetryJournal(t, journal, 2, func(*exchangeRetryEvidence) {})
	if _, err := ReplayTranscript(open, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("a journal ending with an open reservation replayed: %v", err)
	}
}
