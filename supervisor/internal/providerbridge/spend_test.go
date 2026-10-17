package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
)

const (
	spendTestModel    = "deepseek/deepseek-v4.1-flash"
	spendTestEndpoint = "https://openrouter.ai/api/v1/chat/completions"
	spendTestMembers  = `{"provider":{"order":["example-provider"],"allow_fallbacks":false,"data_collection":"deny","require_parameters":true,"zdr":true},"temperature":0,"seed":7,"usage":{"include":true}}`
)

func spendTestRoute(t *testing.T) openrouter.Route {
	t.Helper()
	route, err := openrouter.NewResearchRoute(openrouter.ResearchSpec{Model: spendTestModel, Endpoint: spendTestEndpoint,
		ProfileDigest: strings.Repeat("ab", 32), Members: spendTestMembers, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return route
}

// spendRecorder is a SpendAccountant that records the order of its calls and
// what the transport had seen at that time.
type spendRecorder struct {
	mu         sync.Mutex
	events     []string
	usage      json.RawMessage
	reserveErr error
	reserved   SpendReservation
	doer       *spendDoer
}

func (s *spendRecorder) note(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *spendRecorder) ReserveSpend(_ context.Context, reservation SpendReservation) (string, error) {
	s.note("reserve")
	if s.doer != nil && s.doer.calls != 0 {
		panic("spend reserved after the request was sent")
	}
	s.mu.Lock()
	s.reserved = reservation
	s.mu.Unlock()
	if s.reserveErr != nil {
		return "", s.reserveErr
	}
	return "unit-1", nil
}

func (s *spendRecorder) ReleaseSpend(_ context.Context, unit string) error {
	s.note("release:" + unit)
	return nil
}

func (s *spendRecorder) SettleSpend(_ context.Context, unit string, usage json.RawMessage) error {
	s.note("settle:" + unit)
	s.mu.Lock()
	s.usage = append(json.RawMessage(nil), usage...)
	s.mu.Unlock()
	return nil
}

func (s *spendRecorder) UncertainSpend(_ context.Context, unit string) error {
	s.note("uncertain:" + unit)
	return nil
}

func (s *spendRecorder) sequence() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

// spendDoer answers a research chat completion with a usage-bearing trailer.
type spendDoer struct {
	calls  int
	fail   error
	status int
	url    string
	body   []byte
}

func (d *spendDoer) Do(request *http.Request) (*http.Response, error) {
	d.calls++
	d.url = request.URL.String()
	d.body, _ = io.ReadAll(request.Body)
	if d.fail != nil {
		return nil, d.fail
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	first, _ := json.Marshal(map[string]any{"id": "resp-1", "model": spendTestModel, "created": 1, "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": nil}}})
	last, _ := json.Marshal(map[string]any{"id": "resp-1", "model": spendTestModel, "created": 1, "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	usage, _ := json.Marshal(map[string]any{"id": "resp-1", "model": spendTestModel, "created": 1, "object": "chat.completion.chunk",
		"choices": []any{}, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002}})
	raw := []byte("data: " + string(first) + "\n\ndata: " + string(last) + "\n\ndata: " + string(usage) + "\n\ndata: [DONE]\n\n")
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), Request: request}, nil
}

func newSpendConversation(t *testing.T, spend SpendAccountant, doer broker.HTTPDoer) *Conversation {
	t.Helper()
	c, err := newWithDoer(Config{
		ConversationID: "conversation-spend", WorkflowID: "workflow-spend", ProfileID: "profile-spend",
		InitialGenerationID: "generation-1", InitialTreeDigest: "sha256:" + strings.Repeat("0", 64),
		SystemPrompt: "Spend system prompt.", DeveloperPrompt: "Spend developer profile.",
		Route: spendTestRoute(t), Spend: spend,
	}, spendTestModel, doer, &memoryJournal{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func admitSpendPrompt(t *testing.T, c *Conversation) {
	t.Helper()
	if err := c.AdmitPrompt("task-spend", "Say ok."); err != nil {
		t.Fatal(err)
	}
}

func TestResearchRouteReservesSpendBeforeSendAndSettlesTheUsageAfterward(t *testing.T) {
	doer := &spendDoer{}
	spend := &spendRecorder{doer: doer}
	journal := &memoryJournal{}
	c := newSpendConversation(t, spend, doer)
	c.journal = journal
	admitSpendPrompt(t, c)
	turn, err := c.NextTurn(context.Background())
	if err != nil {
		t.Fatalf("NextTurn: %v", err)
	}
	// The Pi worker pins one model label and refuses any other, so the Turn carries
	// that label; the durable exchange evidence records the provider's real model.
	if turn.Model != ApprovedModel || doer.url != spendTestEndpoint || doer.calls != 1 {
		t.Fatalf("research exchange differs: model=%q url=%q calls=%d", turn.Model, doer.url, doer.calls)
	}
	recorded := false
	for _, event := range journal.snapshot() {
		if event.Kind == "provider_exchange" {
			var evidence exchangeEvidence
			if err := json.Unmarshal(event.Data, &evidence); err != nil || evidence.Model != spendTestModel {
				t.Fatalf("exchange evidence model = %q (%v)", evidence.Model, err)
			}
			recorded = true
		}
	}
	if !recorded {
		t.Fatal("no exchange evidence was journaled")
	}
	if got, want := spend.sequence(), []string{"reserve", "settle:unit-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spend order %v, want %v", got, want)
	}
	if !bytes.Contains(doer.body, []byte(`"provider":{"order":["example-provider"]`)) {
		t.Fatalf("frozen provider routing missing from the request: %s", doer.body)
	}
	var usage struct {
		PromptTokens int `json:"prompt_tokens"`
	}
	if err := json.Unmarshal(spend.usage, &usage); err != nil || usage.PromptTokens != 11 {
		t.Fatalf("settled usage = %s (%v)", spend.usage, err)
	}
	if spend.reserved.WorkflowID != "workflow-spend" || spend.reserved.ConversationID != "conversation-spend" ||
		spend.reserved.Turn != 1 || len(spend.reserved.Request.Body()) == 0 {
		t.Fatalf("reservation does not name the exact request: %+v", spend.reserved)
	}
}

func TestResearchRouteSpendRefusalSendsNothing(t *testing.T) {
	doer := &spendDoer{}
	spend := &spendRecorder{doer: doer, reserveErr: errors.New("usdledger recorded an overage; no further reservations")}
	c := newSpendConversation(t, spend, doer)
	admitSpendPrompt(t, c)
	if _, err := c.NextTurn(context.Background()); err == nil || !strings.Contains(err.Error(), "overage") {
		t.Fatalf("spend refusal not surfaced: %v", err)
	}
	if doer.calls != 0 {
		t.Fatalf("provider saw %d requests after a spend refusal", doer.calls)
	}
	if got := spend.sequence(); !reflect.DeepEqual(got, []string{"reserve"}) {
		t.Fatalf("spend calls after refusal: %v", got)
	}
	if _, err := c.NextTurn(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("conversation survived a spend refusal: %v", err)
	}
}

func TestResearchRouteReleasesSpendWhenALaterReservationRefusesDispatch(t *testing.T) {
	doer := &spendDoer{}
	spend := &spendRecorder{doer: doer}
	c := newSpendConversation(t, spend, doer)
	c.cfg.SelectCompletionTokenLimit = func(context.Context) (uint64, error) { return 64, nil }
	c.cfg.ReserveHostCall = func(context.Context, broker.RequestRecord, uint64) (HostCallBinding, error) {
		return HostCallBinding{}, errors.New("workflow ledger exhausted")
	}
	c.cfg.CompleteHostCall = func(context.Context, HostCallCompletion) error { return nil }
	admitSpendPrompt(t, c)
	if _, err := c.NextTurn(context.Background()); err == nil {
		t.Fatal("exhausted workflow ledger allowed a turn")
	}
	if doer.calls != 0 {
		t.Fatalf("provider saw %d requests", doer.calls)
	}
	if got, want := spend.sequence(), []string{"reserve", "release:unit-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spend order %v, want %v", got, want)
	}
	if spend.reserved.CompletionLimit != 64 {
		t.Fatalf("reservation did not carry the host-selected completion cap: %d", spend.reserved.CompletionLimit)
	}
}

func TestResearchRouteMarksSpendUncertainWhenTheOutcomeIsAmbiguous(t *testing.T) {
	for name, doer := range map[string]*spendDoer{
		"transport failure": {fail: errors.New("connection reset")},
		"server error":      {status: http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			spend := &spendRecorder{doer: doer}
			c := newSpendConversation(t, spend, doer)
			admitSpendPrompt(t, c)
			if _, err := c.NextTurn(context.Background()); err == nil {
				t.Fatal("ambiguous exchange released a turn")
			}
			if got, want := spend.sequence(), []string{"reserve", "uncertain:unit-1"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("spend order %v, want %v", got, want)
			}
		})
	}
}

func TestEngineeringConversationNeverTouchesSpendAccounting(t *testing.T) {
	doer := &fixtureDoer{responses: []fixtureResponse{{responseID: "resp-eng", text: "ok"}}}
	c := newFixtureConversation(t, &memoryJournal{}, doer, "generation-1", "sha256:"+strings.Repeat("0", 64))
	if c.route.IsResearch() || c.cfg.Spend != nil {
		t.Fatalf("engineering conversation carries a research route or spend hook: %+v", c.route)
	}
	if err := c.AdmitPrompt("task-eng", "Say ok."); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextTurn(context.Background()); err != nil {
		t.Fatalf("engineering turn changed: %v", err)
	}
	if len(doer.requests) != 1 || bytes.Contains(doer.requests[0], []byte(`"provider"`)) {
		t.Fatal("engineering request carries research members")
	}
}

func TestResearchRouteRejectsAnEngineeringModelAndAMismatchedRoute(t *testing.T) {
	cfg := Config{ConversationID: "c", WorkflowID: "w", ProfileID: "p", InitialGenerationID: "g",
		InitialTreeDigest: "sha256:" + strings.Repeat("0", 64), SystemPrompt: "s", DeveloperPrompt: "d", Route: spendTestRoute(t)}
	if _, err := newWithDoer(cfg, ApprovedModel, &spendDoer{}, &memoryJournal{}); err == nil {
		t.Fatal("research route accepted the engineering model")
	}
	cfg.Route = openrouter.Route{Model: "other", Endpoint: openrouter.Endpoint}
	if _, err := newWithDoer(cfg, "other", &spendDoer{}, &memoryJournal{}); err == nil {
		t.Fatal("unregistered route accepted")
	}
}
