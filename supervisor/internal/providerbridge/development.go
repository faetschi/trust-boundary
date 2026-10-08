package providerbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
)

// DevelopmentProviderTurn is one scripted, offline provider response used only
// by the explicit, non-claim-bearing development launch route. It carries no
// credential, endpoint selection, model choice, or request material: the owner
// Conversation fixes the approved model and the registered request shape. The
// caller may only script the bounded response projection.
type DevelopmentProviderTurn struct {
	ResponseID string          `json:"response_id"`
	CallID     string          `json:"call_id,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Text       string          `json:"text,omitempty"`
}

// NewDevelopmentOfflineConversation constructs a Conversation whose provider
// transport is the caller-supplied scripted offline turns. It performs no
// network access and loads no credentials. This is an explicit development
// seam: it must never be reachable from the production serve --pi route, and a
// Conversation built here carries no provider-authenticity, G1, or containment
// claim. Production callers must use NewFromCredentials/NewFromEnvironment.
func NewDevelopmentOfflineConversation(cfg Config, journal *audit.Journal, turns []DevelopmentProviderTurn) (*Conversation, error) {
	if journal == nil {
		return nil, errors.New("development offline conversation requires the protected durable audit journal")
	}
	if len(turns) == 0 {
		return nil, errors.New("development offline conversation requires at least one scripted provider turn")
	}
	copied := make([]DevelopmentProviderTurn, len(turns))
	for index, turn := range turns {
		copied[index] = turn
		copied[index].Arguments = append(json.RawMessage(nil), turn.Arguments...)
	}
	return newWithDoer(cfg, ApprovedModel, &developmentOfflineDoer{turns: copied}, journal)
}

// developmentOfflineDoer is the in-memory provider transport for the development
// seam. It answers the fixed registered OpenRouter route only and derives the
// SSE capture body from the scripted turn so the owner Conversation still
// journals a full exchange/gate/result chain.
type developmentOfflineDoer struct {
	mu    sync.Mutex
	turns []DevelopmentProviderTurn
	call  int
}

func (d *developmentOfflineDoer) Do(request *http.Request) (*http.Response, error) {
	if d == nil {
		return nil, errors.New("development offline provider is not configured")
	}
	if request == nil || request.Method != http.MethodPost || request.URL == nil || request.URL.String() != openrouter.Endpoint {
		return nil, errors.New("development offline provider refused a route outside the registered profile")
	}
	d.mu.Lock()
	if d.call >= len(d.turns) {
		d.mu.Unlock()
		return nil, errors.New("development offline provider response script is exhausted")
	}
	turn := d.turns[d.call]
	d.call++
	d.mu.Unlock()
	body, err := encodeDevelopmentProviderTurn(turn)
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

// encodeDevelopmentProviderTurn renders the bounded SSE capture body the
// registered broker expects. It mirrors the fixed offline fixture shape: one
// assistant delta and one terminal finish-reason chunk, both carrying the
// approved model identity.
func encodeDevelopmentProviderTurn(turn DevelopmentProviderTurn) ([]byte, error) {
	if turn.ResponseID == "" {
		return nil, errors.New("development offline turn requires a response ID")
	}
	delta := map[string]any{"role": "assistant"}
	finish := "stop"
	if turn.Tool != "" {
		if turn.CallID == "" || len(turn.Arguments) == 0 {
			return nil, errors.New("development offline tool turn requires a call ID and arguments")
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
			return nil, errors.New("development offline text turn requires assistant text")
		}
		delta["content"] = turn.Text
	}
	first, err := json.Marshal(map[string]any{
		"id": turn.ResponseID, "model": ApprovedModel, "created": 1, "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	})
	if err != nil {
		return nil, err
	}
	last, err := json.Marshal(map[string]any{
		"id": turn.ResponseID, "model": ApprovedModel, "created": 1, "object": "chat.completion.chunk",
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
