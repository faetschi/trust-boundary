package providerbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
)

// ScriptTurn is one scripted provider response: a tool call (Tool and its JSON-object Arguments)
// or assistant text. ResponseID and CallID are optional; the scripted transport issues
// "scripted-response-<n>" and "scripted-call-<n>" for the n-th exchange when they are empty.
type ScriptTurn struct {
	ResponseID string          `json:"response_id,omitempty"`
	CallID     string          `json:"call_id,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Text       string          `json:"text,omitempty"`
	// FailStatus, when set, makes the scripted transport answer that HTTP status with FailBody
	// instead of a completion: failure injection for the offline provider (retry tests).
	FailStatus int    `json:"fail_status,omitempty"`
	FailBody   string `json:"fail_body,omitempty"`
}

// ScriptMessage is the part of one request message that a ScriptSource may read: the role, the text
// content, and for a tool message the provider call it answers (Content is then the Go-owned
// protocol Result JSON of that call).
type ScriptMessage struct {
	Role       string
	Content    string
	ToolCallID string
}

// ScriptSource decides the next provider response. Next is called once per outbound request with
// the complete message history of that request (system, developer, user and every earlier turn and
// tool Result), so a source can read the last Result. An error fails the exchange; the transport
// invents nothing.
type ScriptSource interface {
	Next(history []ScriptMessage) (ScriptTurn, error)
}

// ErrScriptExhausted is returned by a source that has no turn left.
var ErrScriptExhausted = errors.New("scripted provider has no turn left")

// StaticScriptFinalText is the assistant text that ends a static script that does not end with a
// text turn of its own.
const StaticScriptFinalText = "Scripted replay complete."

// StaticScript replays a fixed list of turns, one per request, and then one final text turn. It
// ignores the history. After the text turn it refuses to answer.
type StaticScript struct {
	mu    sync.Mutex
	turns []ScriptTurn
	next  int
	done  bool
}

// NewStaticScript returns a source that owns a copy of turns.
func NewStaticScript(turns []ScriptTurn) *StaticScript {
	copied := make([]ScriptTurn, len(turns))
	for i, turn := range turns {
		copied[i] = turn
		copied[i].Arguments = append(json.RawMessage(nil), turn.Arguments...)
	}
	return &StaticScript{turns: copied}
}

// Next implements ScriptSource.
func (s *StaticScript) Next([]ScriptMessage) (ScriptTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return ScriptTurn{}, ErrScriptExhausted
	}
	if s.next < len(s.turns) {
		turn := s.turns[s.next]
		s.next++
		if turn.Tool == "" {
			s.done = true
		}
		turn.Arguments = append(json.RawMessage(nil), turn.Arguments...)
		return turn, nil
	}
	s.done = true
	return ScriptTurn{Text: StaticScriptFinalText}, nil
}

// NewScriptedConversation constructs a Conversation whose provider transport is a ScriptSource
// instead of a model. Everything else is the real Conversation: the registered request is built and
// journaled, the response is captured and correlated, host-call accounting (cfg.ReserveHostCall and
// cfg.CompleteHostCall) charges every scripted turn like a model turn, and Pi, the proxy tools and
// the command cell run unchanged. It performs no network access and loads no credentials.
//
// Monetary spend accounting is dropped (cfg.Spend is ignored): a scripted turn has no provider cost
// and no usage object. The composition root, not a client, chooses the source, and chooses it only
// from the admitted role of a corpus trial.
func NewScriptedConversation(cfg Config, journal *audit.Journal, source ScriptSource) (*Conversation, error) {
	if journal == nil {
		return nil, errors.New("scripted conversation requires the protected durable audit journal")
	}
	return newScripted(cfg, journal, source)
}

func newScripted(cfg Config, journal eventJournal, source ScriptSource) (*Conversation, error) {
	if source == nil || reflectNil(source) {
		return nil, errors.New("scripted conversation requires a script source")
	}
	if journal == nil || reflectNil(journal) {
		return nil, errors.New("scripted conversation requires the protected durable audit journal")
	}
	cfg.Spend = nil
	route := cfg.Route.Resolved()
	return newWithDoer(cfg, route.Model, &scriptedDoer{source: source, endpoint: route.Endpoint, model: route.Model}, journal)
}

// scriptedDoer is the in-memory provider transport of a scripted conversation. It answers the
// route's chat-completions URL only, reads the request history for the source, and renders the
// source's turn as the SSE capture body that the registered broker expects.
type scriptedDoer struct {
	mu       sync.Mutex
	source   ScriptSource
	endpoint string
	model    string
	calls    int
}

func (d *scriptedDoer) Do(request *http.Request) (*http.Response, error) {
	if d == nil {
		return nil, errors.New("scripted provider is not configured")
	}
	if request == nil || request.Method != http.MethodPost || request.URL == nil || request.URL.String() != d.endpoint {
		return nil, errors.New("scripted provider refused a route outside the registered profile")
	}
	history, err := scriptHistory(request)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.mu.Unlock()
	turn, err := d.source.Next(history)
	if err != nil {
		return nil, fmt.Errorf("scripted provider: %w", err)
	}
	if turn.FailStatus != 0 {
		return &http.Response{StatusCode: turn.FailStatus, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(turn.FailBody)), Request: request}, nil
	}
	if turn.ResponseID == "" {
		turn.ResponseID = fmt.Sprintf("scripted-response-%d", n)
	}
	if turn.Tool != "" && turn.CallID == "" {
		turn.CallID = fmt.Sprintf("scripted-call-%d", n)
	}
	body, err := encodeScriptTurn(turn, d.model)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}, nil
}

// scriptHistory decodes the messages of the outbound request body.
func scriptHistory(request *http.Request) ([]ScriptMessage, error) {
	if request.Body == nil {
		return nil, errors.New("scripted provider request has no body")
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, openrouter.MaxRequestBytes+1))
	if err != nil || len(raw) > openrouter.MaxRequestBytes {
		return nil, errors.New("scripted provider request body is unreadable or too large")
	}
	var body struct {
		Messages []struct {
			Role       string  `json:"role"`
			Content    *string `json:"content"`
			ToolCallID string  `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("scripted provider request body: %w", err)
	}
	history := make([]ScriptMessage, len(body.Messages))
	for i, message := range body.Messages {
		history[i] = ScriptMessage{Role: message.Role, ToolCallID: message.ToolCallID}
		if message.Content != nil {
			history[i].Content = *message.Content
			// Scripted D32 sources retain their Result-only public projection.
			if message.Role == "tool" {
				raw, _, hinted := strings.Cut(*message.Content, "\nhint: ")
				if hinted {
					result, err := protocol.DecodeResult([]byte(raw))
					if err != nil || modelResultText([]byte(raw), result) != *message.Content {
						return nil, errors.New("scripted provider tool hint differs from the fixed Result hint")
					}
					history[i].Content = raw
				}
			}
		}
	}
	return history, nil
}

// encodeScriptTurn renders the bounded SSE capture body the registered broker expects: one
// assistant delta and one terminal finish-reason chunk, both carrying the route's model identity.
func encodeScriptTurn(turn ScriptTurn, model string) ([]byte, error) {
	if turn.ResponseID == "" {
		return nil, errors.New("scripted turn requires a response ID")
	}
	delta := map[string]any{"role": "assistant"}
	finish := "stop"
	if turn.Tool != "" {
		if turn.CallID == "" || len(turn.Arguments) == 0 {
			return nil, errors.New("scripted tool turn requires a call ID and arguments")
		}
		call := map[string]any{
			"index": 0,
			"id":    turn.CallID,
			"type":  "function",
			"function": map[string]any{
				"name": turn.Tool, "arguments": string(turn.Arguments),
			},
		}
		delta["tool_calls"] = []any{call}
		finish = "tool_calls"
	} else {
		if turn.Text == "" {
			return nil, errors.New("scripted text turn requires assistant text")
		}
		delta["content"] = turn.Text
	}
	first, err := json.Marshal(map[string]any{
		"id": turn.ResponseID, "model": model, "created": 1, "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	})
	if err != nil {
		return nil, err
	}
	last, err := json.Marshal(map[string]any{
		"id": turn.ResponseID, "model": model, "created": 1, "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
	})
	if err != nil {
		return nil, err
	}
	out := append([]byte("data: "), first...)
	out = append(out, []byte("\r\n\r\ndata: ")...)
	out = append(out, last...)
	out = append(out, []byte("\r\n\r\ndata: [DONE]\r\n\r\n")...)
	return out, nil
}
