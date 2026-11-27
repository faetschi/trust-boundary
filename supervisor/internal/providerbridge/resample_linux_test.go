//go:build linux

package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
)

// parallelCallStream is the real OpenCode Go G1 stream (one read call at index 0) with a second call,
// a bash call at index 1, streamed before the finish chunk: the shape of the F17 and M3 refusals.
func parallelCallStream(t *testing.T) string {
	t.Helper()
	body := []byte(singleCallStream(t))
	const head = `data: {"id":"0217926074272997e031431b40d2024cf18919c414bfdfa14ad95","object":"chat.completion.chunk","created":1792607427,"model":"deepseek-v4.1-flash","choices":[{"index":0,`
	finish := bytes.Index(body, []byte(head+`"finish_reason":"tool_calls"`))
	if finish < 0 {
		t.Fatal("the G1 fixture no longer has its finish chunk")
	}
	second := head + `"delta":{"role":"assistant","content":"","tool_calls":[{"index":1,"id":"call_01_parallel","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]}}],"usage":null}` + "\n\n"
	return string(body[:finish]) + second + string(body[finish:])
}

func singleCallStream(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("../broker/openrouter/testdata/opencode-go-g1-turn1.sse")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func resampleTrace(t *testing.T, journal *audit.Journal) audit.Trace {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func retryEvidenceAt(t *testing.T, record audit.Record) exchangeRetryEvidence {
	t.Helper()
	var evidence exchangeRetryEvidence
	if record.Event.Kind != exchangeRetryKind || json.Unmarshal(record.Event.Data, &evidence) != nil {
		t.Fatalf("record %d is not a retry record: %s", record.Sequence, record.Event.Kind)
	}
	return evidence
}

func resamplePins(t *testing.T, cfg Config) RecoveryPins {
	return RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}
}

// Decision 13: a stream refused only for a parallel tool call is resent with the same request bytes, each
// attempt is its own reserved and settled host call, the refused stream is journaled whole, and replay
// accepts the journal.
func TestParallelCallRefusalIsResampledAndTheNextAttemptSucceeds(t *testing.T) {
	parallel := parallelCallStream(t)
	conversation, journal, doer, hooks, cfg := retryConversation(t, []fixtureResponse{
		{failStatus: 200, failBody: parallel},
		{responseID: "response-ok", text: "finished"},
	})
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.AssistantText != "finished" {
		t.Fatalf("turn = %+v, err = %v", turn, err)
	}
	if doer.call != 2 || len(hooks.reservations) != 2 || len(hooks.completions) != 2 {
		t.Fatalf("attempts=%d reservations=%d completions=%d, want 2 each", doer.call, len(hooks.reservations), len(hooks.completions))
	}
	if !bytes.Equal(doer.requests[0], doer.requests[1]) || hooks.reservations[0].RequestDigest != hooks.reservations[1].RequestDigest {
		t.Fatal("the resample did not send the same request bytes")
	}
	wantKinds := []string{"provider_user_admitted", "provider_host_call_reserved", exchangeRetryKind,
		"provider_host_call_reserved", "provider_exchange"}
	if got := journalKinds(t, journal); strings.Join(got, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("journal kinds = %v, want %v", got, wantKinds)
	}
	trace := resampleTrace(t, journal)
	evidence := retryEvidenceAt(t, trace.Records[2])
	if evidence.StatusCode != 200 || string(evidence.Response) != parallel || evidence.Attempt != 1 || evidence.HostCall != hooks.reservations[0] {
		t.Fatalf("the refused attempt is not journaled whole: status=%d attempt=%d same=%v", evidence.StatusCode, evidence.Attempt, string(evidence.Response) == parallel)
	}
	if hooks.completions[0].OutcomeDigest != trace.Records[2].Hash {
		t.Fatal("the refused attempt's host call is not settled by its own record")
	}
	projection, err := ReplayTranscript(journal, resamplePins(t, cfg), nil)
	if err != nil || projection.ProviderRetries != 1 || projection.ExchangeCount != 1 || projection.HostCallRetryableFailure {
		t.Fatalf("replay = %+v, err = %v", projection, err)
	}
}

// After the bound the turn ends UNKNOWN exactly as before decision 13: the last refused attempt is the
// provider_exchange_unknown record and its host call stays reserved.
func TestParallelCallResampleBoundEndsUnknown(t *testing.T) {
	parallel := parallelCallStream(t)
	conversation, journal, doer, hooks, cfg := retryConversation(t, []fixtureResponse{
		{failStatus: 200, failBody: parallel}, {failStatus: 200, failBody: parallel}, {failStatus: 200, failBody: parallel},
		{responseID: "never", text: "unreachable"},
	})
	_, err := conversation.NextTurn(context.Background())
	if err == nil || !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), "tool-call index must be zero") {
		t.Fatalf("exhausted resamples did not fail the turn with the capture refusal: %v", err)
	}
	if MaxParallelCallResamples != 2 || doer.call != 1+MaxParallelCallResamples || len(hooks.reservations) != 3 || len(hooks.completions) != 2 {
		t.Fatalf("attempts=%d reservations=%d completions=%d", doer.call, len(hooks.reservations), len(hooks.completions))
	}
	wantKinds := []string{"provider_user_admitted",
		"provider_host_call_reserved", exchangeRetryKind,
		"provider_host_call_reserved", exchangeRetryKind,
		"provider_host_call_reserved", "provider_exchange_unknown"}
	if got := journalKinds(t, journal); strings.Join(got, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("journal kinds = %v, want %v", got, wantKinds)
	}
	trace := resampleTrace(t, journal)
	var unknown struct {
		Response []byte `json:"response"`
		Failure  string `json:"failure"`
	}
	if err := json.Unmarshal(trace.Records[6].Event.Data, &unknown); err != nil || string(unknown.Response) != parallel ||
		unknown.Failure != "capture OpenRouter response: tool-call index must be zero" {
		t.Fatalf("the last attempt's UNKNOWN evidence changed: %v %q", err, unknown.Failure)
	}
	if _, err := ReplayTranscript(journal, resamplePins(t, cfg), nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("a journal ending in an UNKNOWN exchange replayed: %v", err)
	}
}

// A cap that refuses the next attempt stops the resampling before anything is sent; the turn ends through
// the existing failure path.
func TestParallelCallResampleStopsAtACap(t *testing.T) {
	parallel := parallelCallStream(t)
	for name, limit := range map[string]func(hooks *retryHooks, cfg *Config){
		"host-call allowance": func(hooks *retryHooks, cfg *Config) {
			reserve := cfg.ReserveHostCall
			cfg.ReserveHostCall = func(ctx context.Context, request broker.RequestRecord, limit uint64) (HostCallBinding, error) {
				if len(hooks.reservations) == 1 {
					return HostCallBinding{}, errors.New("workflow host-call allowance exhausted")
				}
				return reserve(ctx, request, limit)
			}
		},
		// The token ceiling leaves less than the first attempt's completion cap, so the same bytes cannot be sent.
		"token ceiling": func(hooks *retryHooks, cfg *Config) {
			cfg.SelectCompletionTokenLimit = func(context.Context) (uint64, error) {
				hooks.limits++
				return uint64(513 - hooks.limits), nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := recoveryTestConfig()
			hooks := &retryHooks{}
			hooks.apply(&cfg)
			limit(hooks, &cfg)
			journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "cap.audit"))
			doer := &fixtureDoer{responses: []fixtureResponse{{failStatus: 200, failBody: parallel}, {responseID: "r", text: "late"}}}
			conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
			if err != nil {
				t.Fatal(err)
			}
			if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
				t.Fatal(err)
			}
			if _, err := conversation.NextTurn(context.Background()); err == nil || !errors.Is(err, ErrClosed) {
				t.Fatalf("the cap did not fail the turn: %v", err)
			}
			kinds := journalKinds(t, journal)
			if doer.call != 1 || len(hooks.reservations) != 1 || kinds[2] != exchangeRetryKind ||
				kinds[len(kinds)-1] != "provider_exchange_unknown" {
				t.Fatalf("attempts=%d reservations=%d kinds=%v, want one sent attempt and an UNKNOWN end", doer.call, len(hooks.reservations), kinds)
			}
		})
	}
}

// Only the parallel-call refusal is resampled: every other refused 200 stream ends UNKNOWN on the first
// attempt, as before.
func TestOtherRefusedStreamsAreNotResampled(t *testing.T) {
	parallel := parallelCallStream(t)
	for name, body := range map[string]string{
		"two calls in one delta":        rejectedCaptureBody,
		"negative index":                strings.Replace(parallel, `"index":1,"id":"call_01_parallel"`, `"index":-1,"id":"call_01_parallel"`, 1),
		"single call cut before [DONE]": strings.TrimSuffix(singleCallStream(t), "data: [DONE]\n\n"),
		"changed call id":               strings.Replace(parallel, `"index":1,"id":"call_01_parallel"`, `"index":0,"id":"call_01_parallel"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			conversation, journal, doer, hooks, _ := retryConversation(t, []fixtureResponse{
				{failStatus: 200, failBody: body}, {responseID: "r", text: "late"},
			})
			if _, err := conversation.NextTurn(context.Background()); err == nil {
				t.Fatal("a refused stream released a turn")
			}
			kinds := journalKinds(t, journal)
			if doer.call != 1 || len(hooks.reservations) != 1 || len(hooks.completions) != 0 || kinds[len(kinds)-1] != "provider_exchange_unknown" {
				t.Fatalf("attempts=%d reservations=%d completions=%d kinds=%v", doer.call, len(hooks.reservations), len(hooks.completions), kinds)
			}
		})
	}
	// The parallel-call stream itself is resampled only on a 200 received whole.
	for name, respond := range map[string]func(*http.Request) (*http.Response, error){
		"status 500": func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(parallel))}, nil
		},
		"read cut": func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r,
				Body: &failingBody{data: []byte(parallel), err: io.ErrUnexpectedEOF}}, nil
		},
	} {
		cfg := recoveryTestConfig()
		hooks := &retryHooks{}
		hooks.apply(&cfg)
		calls := 0
		doer := doerFunc(func(r *http.Request) (*http.Response, error) { calls++; return respond(r) })
		conversation, err := newWithDoer(cfg, ApprovedModel, doer, openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "whole.audit")))
		if err != nil {
			t.Fatal(err)
		}
		if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
			t.Fatal(err)
		}
		if _, err := conversation.NextTurn(context.Background()); err == nil || calls != 1 || len(hooks.reservations) != 1 {
			t.Fatalf("%s: attempts=%d reservations=%d err=%v, want one attempt", name, calls, len(hooks.reservations), err)
		}
	}
}

// Resamples and pre-effect retries share the per-turn bound of extra attempts.
func TestResamplesAndRetriesShareTheBound(t *testing.T) {
	parallel := parallelCallStream(t)
	conversation, journal, doer, _, _ := retryConversation(t, []fixtureResponse{
		{failStatus: 503}, {failStatus: 200, failBody: parallel}, {failStatus: 200, failBody: parallel},
		{responseID: "never", text: "unreachable"},
	})
	if _, err := conversation.NextTurn(context.Background()); err == nil {
		t.Fatal("the turn was released after the shared bound")
	}
	kinds := journalKinds(t, journal)
	if doer.call != 1+MaxProviderRetries || kinds[len(kinds)-1] != "provider_exchange_unknown" {
		t.Fatalf("attempts=%d kinds=%v", doer.call, kinds)
	}
}

// Replay requires a resample to repeat the refused request: a retry record turned into a parallel-call
// refusal, over a journal whose next attempt carried another completion cap, does not replay.
func TestReplayRequiresTheResampleToRepeatTheRequest(t *testing.T) {
	cfg := recoveryTestConfig()
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	cfg.SelectCompletionTokenLimit = func(context.Context) (uint64, error) { hooks.limits++; return uint64(513 - hooks.limits), nil }
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "caps.audit"))
	conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{
		{failStatus: 503}, {responseID: "response-ok", text: "finished"}}}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	pins := resamplePins(t, cfg)
	if _, err := ReplayTranscript(journal, pins, nil); err != nil {
		t.Fatalf("an HTTP retry with another cap must replay: %v", err)
	}
	parallel := []byte(parallelCallStream(t))
	asResample := func(e *exchangeRetryEvidence) { e.StatusCode, e.ContentType, e.Response = 200, "", parallel }
	if _, err := ReplayTranscript(tamperedRetryJournal(t, journal, asResample), pins, nil); !errors.Is(err, ErrTranscriptReplay) ||
		!strings.Contains(err.Error(), "does not repeat the refused request") {
		t.Fatalf("a resample with other request bytes replayed: %v", err)
	}
}

// Product turns keep their durable capture refusal: decision 13 is a thesis rule.
func TestProductTurnsAreNotResampled(t *testing.T) {
	cfg := recoveryTestConfig()
	cfg.ProductCaptureRefusals = true
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "product.audit"))
	doer := &fixtureDoer{responses: []fixtureResponse{{failStatus: 200, failBody: parallelCallStream(t)}, {responseID: "r", text: "late"}}}
	conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err == nil {
		t.Fatal("a product turn released a refused stream")
	}
	kinds := journalKinds(t, journal)
	if doer.call != 1 || kinds[len(kinds)-1] != CaptureRefusalEvent {
		t.Fatalf("attempts=%d kinds=%v, want the product capture refusal", doer.call, kinds)
	}
}

// Replay accepts a 200 retry record only for the parallel-call class, and never in a product journal.
func TestReplayRefusesForgedResampleRecords(t *testing.T) {
	conversation, journal, _, _, cfg := retryConversation(t, []fixtureResponse{
		{failStatus: 200, failBody: parallelCallStream(t)}, {responseID: "response-ok", text: "finished"},
	})
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	pins := resamplePins(t, cfg)
	if _, err := ReplayTranscript(tamperedRetryJournal(t, journal, func(*exchangeRetryEvidence) {}), pins, nil); err != nil {
		t.Fatalf("an unmodified copy must replay: %v", err)
	}
	for name, mutate := range map[string]func(*exchangeRetryEvidence){
		"other refusal": func(e *exchangeRetryEvidence) { e.Response = []byte(rejectedCaptureBody) },
		"good stream": func(e *exchangeRetryEvidence) {
			e.Response = textResponse(fixtureResponse{responseID: "x", text: "ok"})
		},
		"not a 200":     func(e *exchangeRetryEvidence) { e.StatusCode = 201 },
		"wrong attempt": func(e *exchangeRetryEvidence) { e.Attempt = 2 },
		"another request": func(e *exchangeRetryEvidence) {
			e.Request.Body = append([]byte(nil), e.Request.Body...)
			e.Request.Body[0] ^= 1
		},
	} {
		if _, err := ReplayTranscript(tamperedRetryJournal(t, journal, mutate), pins, nil); !errors.Is(err, ErrTranscriptReplay) {
			t.Errorf("%s: forged resample record replayed: %v", name, err)
		}
	}
	pins.Config.ProductCaptureRefusals = true
	if _, err := ReplayTranscript(journal, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("a product replay accepted a resample record: %v", err)
	}
}

// A crash after the refused attempt is journaled and settled leaves the ordinary idle point before an
// exchange; a crash before its record leaves the open reservation, which stays UNKNOWN.
func TestJournalEndingAfterAResampleReplaysToAwaitingExchange(t *testing.T) {
	conversation, journal, _, hooks, cfg := retryConversation(t, []fixtureResponse{
		{failStatus: 200, failBody: parallelCallStream(t)}, {responseID: "response-ok", text: "finished"},
	})
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	pins := resamplePins(t, cfg)
	projection, err := ReplayTranscript(truncatedRetryJournal(t, journal, 3, func(*exchangeRetryEvidence) {}), pins, nil)
	if err != nil || projection.Disposition != DispositionAwaitingProviderExchange || !projection.HostCallRetryableFailure ||
		projection.ProviderRetries != 1 || projection.HostCall == nil || *projection.HostCall != hooks.reservations[0] {
		t.Fatalf("projection = %+v, err = %v", projection, err)
	}
	if _, err := ReplayTranscript(truncatedRetryJournal(t, journal, 2, func(*exchangeRetryEvidence) {}), pins, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("a journal ending with an open reservation replayed: %v", err)
	}
}

// On the research route every attempt is its own USD ledger reservation: the refused attempt keeps its
// worst case (uncertain), the next one is reserved under its own attempt number and settled.
func TestResearchRouteResampleBooksEveryAttempt(t *testing.T) {
	spend := &attemptSpend{}
	conversation, doer, _ := researchResampleConversation(t, spend)
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatalf("NextTurn: %v", err)
	}
	if len(spend.reservations) != 2 || spend.reservations[0].Attempt != 0 || spend.reservations[1].Attempt != 1 ||
		spend.reservations[0].Turn != 1 || spend.reservations[1].Turn != 1 || doer.inner.calls != 1 || len(doer.raw) != 0 {
		t.Fatalf("reservations = %+v", spend.reservations)
	}
	if got := strings.Join(spend.events, ","); got != "reserve:unit-0,uncertain:unit-0,reserve:unit-1,settle:unit-1" {
		t.Fatalf("spend sequence = %s", got)
	}
}

// If the refused attempt's unit cannot be marked uncertain, the turn is not resampled: the refused
// stream is journaled as the unknown exchange and the unit is marked again on the way out.
func TestResearchRouteSpendFailureEndsTheTurnWithTheRefusedStreamJournaled(t *testing.T) {
	spend := &attemptSpend{uncertainErr: errors.New("ledger write failed")}
	conversation, doer, journal := researchResampleConversation(t, spend)
	if _, err := conversation.NextTurn(context.Background()); err == nil {
		t.Fatal("the turn was released")
	}
	events := journal.snapshot()
	last := events[len(events)-1]
	var unknown struct {
		Response []byte `json:"response"`
	}
	if doer.inner.calls != 0 || last.Kind != "provider_exchange_unknown" ||
		json.Unmarshal(last.Data, &unknown) != nil || !strings.Contains(string(unknown.Response), `"index":1`) {
		t.Fatalf("calls after the refusal=%d kind=%s, want the refused stream journaled as unknown", doer.inner.calls, last.Kind)
	}
	if got := strings.Join(spend.events, ","); got != "reserve:unit-0,uncertain:unit-0,uncertain:unit-0" {
		t.Fatalf("spend sequence = %s", got)
	}
}

func researchResampleConversation(t *testing.T, spend SpendAccountant) (*Conversation, *queuedSpendDoer, *memoryJournal) {
	t.Helper()
	parallel := strings.ReplaceAll(parallelCallStream(t), `"model":"deepseek-v4.1-flash"`, `"model":"`+spendTestModel+`"`)
	parallel = strings.Replace(parallel, `"name":"read"`, `"name":"unregistered"`, 1)
	doer := &queuedSpendDoer{raw: [][]byte{[]byte(parallel)}}
	journal := &memoryJournal{}
	cfg := Config{
		ConversationID: "conversation-spend", WorkflowID: "workflow-spend", ProfileID: "profile-spend",
		InitialGenerationID: "generation-1", InitialTreeDigest: "sha256:" + strings.Repeat("0", 64),
		SystemPrompt: "Spend system prompt.", DeveloperPrompt: "Spend developer profile.",
		Route: spendTestRoute(t), Spend: spend,
	}
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	conversation, err := newWithDoer(cfg, spendTestModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	admitSpendPrompt(t, conversation)
	return conversation, doer, journal
}

// attemptSpend names one unit per reservation and records every call.
type attemptSpend struct {
	reservations []SpendReservation
	events       []string
	uncertainErr error
}

func (s *attemptSpend) ReserveSpend(_ context.Context, reservation SpendReservation) (string, error) {
	unit := fmt.Sprintf("unit-%d", len(s.reservations))
	s.reservations = append(s.reservations, reservation)
	s.events = append(s.events, "reserve:"+unit)
	return unit, nil
}
func (s *attemptSpend) ReleaseSpend(_ context.Context, unit string) error {
	s.events = append(s.events, "release:"+unit)
	return nil
}
func (s *attemptSpend) SettleSpend(_ context.Context, unit string, _ json.RawMessage) error {
	s.events = append(s.events, "settle:"+unit)
	return nil
}
func (s *attemptSpend) UncertainSpend(_ context.Context, unit string) error {
	s.events = append(s.events, "uncertain:"+unit)
	return s.uncertainErr
}

// queuedSpendDoer answers its queued raw streams first, then the spendDoer's text completion.
type queuedSpendDoer struct {
	raw   [][]byte
	inner spendDoer
}

func (d *queuedSpendDoer) Do(request *http.Request) (*http.Response, error) {
	if len(d.raw) == 0 {
		return d.inner.Do(request)
	}
	raw := d.raw[0]
	d.raw = d.raw[1:]
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), Request: request}, nil
}
