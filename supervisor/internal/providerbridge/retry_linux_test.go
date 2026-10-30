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
	"time"

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
		status      int
		contentType string
		body        string
		retryAfter  string
		want        bool
	}{
		"502":                         {502, "", "", "", true},
		"503 with the endpoint body":  {503, "", unavailableBody, "", true},
		"503 bare":                    {503, "", "", "", true},
		"504":                         {504, "", "", "", true},
		"422 endpoint unavailable":    {422, "", unavailableBody, "", true},
		"422 other":                   {422, "", `{"error":"invalid"}`, "", false},
		"400":                         {400, "", unavailableBody, "", false},
		"401":                         {401, "", "", "", false},
		"429":                         {429, "", "", "", false},
		"429 retry-after 0":           {429, "", "", "0", true},
		"429 retry-after 3":           {429, "", "", "3", true},
		"429 retry-after 10":          {429, "", "", "10", true},
		"429 retry-after 11":          {429, "", "", "11", false},
		"429 retry-after 3600":        {429, "", "", "3600", false},
		"429 retry-after date":        {429, "", "", "Wed, 21 Oct 2026 07:28:00 GMT", false},
		"429 retry-after -1":          {429, "", "", "-1", false},
		"429 retry-after 1.5":         {429, "", "", "1.5", false},
		"503 ignores retry-after":     {503, "", "", "3600", true},
		"500":                         {500, "", unavailableBody, "", false},
		"200":                         {200, "", "", "", false},
		"503 carrying stream bytes":   {503, "", "data: {\"id\":\"x\"}\n\n", "", false},
		"503 carrying a completion":   {503, "", `{"choices":[{}]}`, "", false},
		"503 above the body bound":    {503, "", strings.Repeat("x", retryFailureBodyLimit+1), "", false},
		"422 stream bytes and phrase": {422, "", "data: Endpoint is unavailable", "", false},

		"503 SSE event and data":        {503, "", "event: message\ndata: {\"choices\":[{\"delta\":{\"content\":\"model output\"}}]}\n\n", "", false},
		"503 SSE event only":            {503, "", "event: ping\n\n", "", false},
		"503 SSE id and retry":          {503, "", "id: 7\nretry: 100\n\n", "", false},
		"503 SSE comment":               {503, "", ": keep-alive\n\n", "", false},
		"503 SSE after a JSON line":     {503, "", unavailableBody + "\ndata: x\n", "", false},
		"503 SSE content type":          {503, "text/event-stream", unavailableBody, "", false},
		"503 SSE content type params":   {503, "Text/Event-Stream; charset=utf-8", "", "", false},
		"503 JSON content type":         {503, "application/json; charset=utf-8", unavailableBody, "", true},
		"503 escaped choices key":       {503, "", `{"choices":[{"delta":{"content":"x"}}]}`, "", false},
		"503 error plus choices":        {503, "", `{"error":"x","choices":[]}`, "", false},
		"503 not an object":             {503, "", `[{"error":"x"}]`, "", false},
		"503 trailing data":             {503, "", unavailableBody + " trailing", "", false},
		"503 two objects":               {503, "", unavailableBody + unavailableBody, "", false},
		"503 other top-level key":       {503, "", `{"detail":"x"}`, "", false},
		"503 unknown error member":      {503, "", `{"error":{"message":"x","choices":[]}}`, "", false},
		"503 message not a string":      {503, "", `{"error":{"message":7}}`, "", false},
		"503 HTML gateway page":         {503, "", "<html>Bad Gateway</html>", "", false},
		"503 string error":              {503, "", `{"error":"overloaded"}`, "", true},
		"422 phrase in the message":     {422, "", `{"error":{"message":"Upstream: Endpoint is unavailable."}}`, "", true},
		"422 phrase in the string":      {422, "", `{"error":"Endpoint is unavailable"}`, "", true},
		"422 phrase only in type":       {422, "", `{"error":{"type":"Endpoint is unavailable","message":"x"}}`, "", false},
		"422 phrase outside error":      {422, "", `{"note":"Endpoint is unavailable"}`, "", false},
		"422 empty":                     {422, "", "", "", false},
		"429 SSE body with retry-after": {429, "", "data: x", "1", false},

		"503 duplicate top-level error":      {503, "", `{"error":{"choices":[{"delta":{"content":"already produced"}}]},"error":"overloaded"}`, "", false},
		"503 duplicate identical error":      {503, "", `{"error":"overloaded","error":"overloaded"}`, "", false},
		"503 duplicate via escaped key":      {503, "", `{"error":"a","error":"b"}`, "", false},
		"503 duplicate nested type":          {503, "", `{"error":{"type":"a","type":"b","message":"m"}}`, "", false},
		"422 duplicate message phrase last":  {422, "", `{"error":{"message":"x","message":"Endpoint is unavailable"}}`, "", false},
		"422 duplicate message phrase first": {422, "", `{"error":{"message":"Endpoint is unavailable","message":"x"}}`, "", false},
		"429 duplicate error":                {429, "", `{"error":"a","error":"b"}`, "3", false},
		"503 marker for an ineligible type":  {503, contentTypeIneligible, unavailableBody, "", false},
		"503 text/plain":                     {503, "text/plain", unavailableBody, "", false},
		"503 vendor +json":                   {503, "application/problem+json", unavailableBody, "", true},
		"503 stored oversized header":        {503, "application/json; x=" + strings.Repeat("a", 130), unavailableBody, "", false},
		"503 stored malformed header":        {503, "application/json,", unavailableBody, "", false},
		"503 bare token +json":               {503, "foo+json", unavailableBody, "", false},
		"503 no type +json":                  {503, "/x+json", unavailableBody, "", false},
		"503 no subtype":                     {503, "application/", unavailableBody, "", false},
		"503 empty +json subtype":            {503, "application/+json", unavailableBody, "", false},
		"503 two slashes":                    {503, "application/a/b+json", unavailableBody, "", false},
		"503 bare json":                      {503, "json", unavailableBody, "", false},
		"503 application json":               {503, "application/json", unavailableBody, "", true},
		"503 problem+json":                   {503, "application/problem+json", unavailableBody, "", true},
	} {
		if got := RetryableProviderFailure(test.status, test.contentType, []byte(test.body), test.retryAfter); got != test.want {
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

func withRecordedRetryWait(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	original := retryWait
	retryWait = func(_ context.Context, delay time.Duration) error { waits = append(waits, delay); return nil }
	t.Cleanup(func() { retryWait = original })
	return &waits
}

// A rate-limited attempt is repeated only when the provider names a short wait, which is honoured.
func TestRateLimitedAttemptIsRetriedAfterItsRetryAfter(t *testing.T) {
	waits := withRecordedRetryWait(t)
	conversation, journal, doer, hooks, cfg := retryConversation(t, []fixtureResponse{
		{failStatus: 429, retryAfter: "3"}, {failStatus: 503}, {responseID: "response-ok", text: "finished"},
	})
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.AssistantText != "finished" {
		t.Fatalf("turn = %+v, err = %v", turn, err)
	}
	if doer.call != 3 || len(hooks.reservations) != 3 || len(*waits) != 1 || (*waits)[0] != 3*time.Second {
		t.Fatalf("attempts=%d reservations=%d waits=%v, want 3, 3 and one 3 s wait", doer.call, len(hooks.reservations), *waits)
	}
	projection, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel,
		Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}, nil)
	if err != nil || projection.ProviderRetries != 2 {
		t.Fatalf("replay of a journal with a rate-limited retry: %+v, %v", projection, err)
	}
	// Forging the Retry-After out of the eligible range is refused by replay.
	forged := tamperedRetryJournal(t, journal, func(e *exchangeRetryEvidence) { e.RetryAfter = "60" })
	if _, err := ReplayTranscript(forged, RecoveryPins{Config: cfg, Model: ApprovedModel,
		Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("a retry record with an ineligible Retry-After replayed: %v", err)
	}
}

func TestRateLimitWithoutAShortRetryAfterIsNotRetried(t *testing.T) {
	for name, retryAfter := range map[string]string{"absent": "", "long": "30", "date": "Wed, 21 Oct 2026 07:28:00 GMT"} {
		waits := withRecordedRetryWait(t)
		conversation, journal, doer, hooks, _ := retryConversation(t, []fixtureResponse{
			{failStatus: 429, retryAfter: retryAfter}, {responseID: "r", text: "late"},
		})
		if _, err := conversation.NextTurn(context.Background()); err == nil {
			t.Fatalf("%s: rate-limited attempt succeeded", name)
		}
		kinds := journalKinds(t, journal)
		if doer.call != 1 || len(hooks.reservations) != 1 || len(*waits) != 0 || kinds[len(kinds)-1] != "provider_exchange_unknown" {
			t.Fatalf("%s: attempts=%d reservations=%d waits=%v kinds=%v", name, doer.call, len(hooks.reservations), *waits, kinds)
		}
	}
}

func TestRetryWaitEndsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := retryWait(ctx, MaxRetryAfter); !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("a canceled context did not end the wait: %v after %v", err, time.Since(started))
	}
}
