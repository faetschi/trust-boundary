package openrouter

import (
	"encoding/json"
	"errors"
	"math"

	"tbound/supervisor/internal/broker/protocol"
)

var (
	ErrForwardedModel   = errors.New("forwarded request names a model other than the route's")
	ErrForwardedRequest = errors.New("forwarded request is not an acceptable chat-completions body")
)

type forwardedRequest struct {
	Model               string          `json:"model"`
	Messages            json.RawMessage `json:"messages"`
	Tools               json.RawMessage `json:"tools,omitempty"`
	Stream              bool            `json:"stream"`
	ToolChoice          string          `json:"tool_choice,omitempty"`
	MaxCompletionTokens uint64          `json:"max_completion_tokens"`
}

func BuildForwardedRequest(route Route, raw []byte, completionLimit uint64) (BuiltRequest, error) {
	route = route.Resolved()
	if !route.IsResearch() || route.Validate() != nil || completionLimit == 0 || completionLimit > math.MaxInt32 {
		return BuiltRequest{}, ErrForwardedRequest
	}
	if len(raw) == 0 || len(raw) > MaxRequestBytes || protocol.ValidateStrictJSON(raw) != nil {
		return BuiltRequest{}, ErrForwardedRequest
	}
	top, err := object(raw)
	if err != nil {
		return BuiltRequest{}, ErrForwardedRequest
	}
	model, err := str(top, "model")
	if err != nil {
		return BuiltRequest{}, ErrForwardedRequest
	}
	if model != route.Model {
		return BuiltRequest{}, ErrForwardedModel
	}
	var streams bool
	if json.Unmarshal(top["stream"], &streams) != nil || !streams {
		return BuiltRequest{}, ErrForwardedRequest
	}
	messages, err := array(top["messages"])
	if err != nil || len(messages) == 0 || len(messages) > maxMessages {
		return BuiltRequest{}, ErrForwardedRequest
	}
	out := forwardedRequest{Model: route.Model, Messages: top["messages"], Stream: true, MaxCompletionTokens: completionLimit}
	if declared, ok := top["tools"]; ok {
		if _, err := array(declared); err != nil {
			return BuiltRequest{}, ErrForwardedRequest
		}
		out.Tools = declared
	}
	if choice, ok := top["tool_choice"]; ok {
		if err := json.Unmarshal(choice, &out.ToolChoice); err != nil || (out.ToolChoice != "auto" && out.ToolChoice != "none" && out.ToolChoice != "required") {
			return BuiltRequest{}, ErrForwardedRequest
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return BuiltRequest{}, ErrForwardedRequest
	}
	body = append(append(body[:len(body)-1], ','), route.members[1:]...)
	if len(body) > MaxRequestBytes {
		return BuiltRequest{}, ErrForwardedRequest
	}
	return BuiltRequest{body: body, url: route.Endpoint}, nil
}
