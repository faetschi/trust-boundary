package openrouter

import (
	"encoding/json"
	"errors"
	"math"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
)

var (
	ErrForwardedModel   = errors.New("forwarded request names a model other than the route's")
	ErrForwardedRequest = errors.New("forwarded request is not an acceptable chat-completions body")
)

type forwardedRequest struct {
	Model               string             `json:"model"`
	Messages            []forwardedMessage `json:"messages"`
	Tools               []forwardedTool    `json:"tools,omitempty"`
	Stream              bool               `json:"stream"`
	ToolChoice          string             `json:"tool_choice,omitempty"`
	MaxCompletionTokens uint64             `json:"max_completion_tokens"`
}

type forwardedMessage struct {
	Role       Role                `json:"role"`
	Content    any                 `json:"content"`
	ToolCalls  []forwardedToolCall `json:"tool_calls,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	Name       string              `json:"name,omitempty"`
	Reasoning  string              `json:"reasoning,omitempty"`
}

type forwardedPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type forwardedToolCall struct {
	ID       string                  `json:"id"`
	Type     string                  `json:"type"`
	Function forwardedToolCallDetail `json:"function"`
}

type forwardedToolCallDetail struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type forwardedTool struct {
	Type     string            `json:"type"`
	Function forwardedFunction `json:"function"`
}

type forwardedFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
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
	out := forwardedRequest{Model: route.Model, Messages: make([]forwardedMessage, len(messages)), Stream: true, MaxCompletionTokens: completionLimit}
	for i, message := range messages {
		if out.Messages[i], err = parseForwardedMessage(message); err != nil {
			return BuiltRequest{}, ErrForwardedRequest
		}
	}
	if declared, ok := top["tools"]; ok {
		if out.Tools, err = parseForwardedTools(declared); err != nil {
			return BuiltRequest{}, ErrForwardedRequest
		}
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

func parseForwardedTools(raw []byte) ([]forwardedTool, error) {
	declared, err := array(raw)
	if err != nil {
		return nil, err
	}
	tools := make([]forwardedTool, len(declared))
	for i, item := range declared {
		o, err := object(item)
		if err != nil {
			return nil, err
		}
		if err := keys(o, []string{"type", "function"}, nil); err != nil {
			return nil, err
		}
		kind, err := str(o, "type")
		if err != nil || kind != "function" {
			return nil, errors.New("only function tools are forwarded")
		}
		fn, err := object(o["function"])
		if err != nil {
			return nil, err
		}
		if err := keys(fn, []string{"name", "description", "parameters"}, []string{"strict"}); err != nil {
			return nil, err
		}
		if raw, ok := fn["strict"]; ok && string(raw) != "true" && string(raw) != "false" {
			return nil, errors.New("tool strict must be a boolean")
		}
		name, err := str(fn, "name")
		if err != nil || !validText(name, maxText) {
			return nil, errors.New("invalid tool name")
		}
		description, err := str(fn, "description")
		if err != nil {
			return nil, err
		}
		if _, err := object(fn["parameters"]); err != nil {
			return nil, err
		}
		tools[i] = forwardedTool{Type: kind, Function: forwardedFunction{Name: name, Description: description, Parameters: fn["parameters"]}}
	}
	return tools, nil
}

func parseForwardedMessage(raw []byte) (forwardedMessage, error) {
	o, err := object(raw)
	if err != nil {
		return forwardedMessage{}, err
	}
	if err := keys(o, []string{"role"}, []string{"content", "tool_calls", "tool_call_id", "name", "reasoning"}); err != nil {
		return forwardedMessage{}, err
	}
	role, err := str(o, "role")
	if err != nil {
		return forwardedMessage{}, err
	}
	out := forwardedMessage{Role: Role(role)}
	var text *string
	if content, ok := o["content"]; ok && !isNull(content) {
		if out.Content, text, err = parseForwardedContent(content); err != nil {
			return forwardedMessage{}, err
		}
	}
	var prior []PriorToolCall
	if calls, ok := o["tool_calls"]; ok {
		if out.ToolCalls, prior, err = parseForwardedToolCalls(calls); err != nil {
			return forwardedMessage{}, err
		}
	}
	for field, dst := range map[string]*string{"tool_call_id": &out.ToolCallID, "name": &out.Name, "reasoning": &out.Reasoning} {
		if value, ok := o[field]; ok {
			if *dst, err = strRaw(value); err != nil {
				return forwardedMessage{}, err
			}
		}
	}
	if _, ok := o["name"]; ok && !validText(out.Name, maxText) {
		return forwardedMessage{}, errors.New("invalid message name")
	}
	if err := validateMessageCalls(Message{Role: out.Role, Content: text, ToolCallID: out.ToolCallID, ToolCalls: prior, Reasoning: out.Reasoning}, maxMessages); err != nil {
		return forwardedMessage{}, err
	}
	return out, nil
}

func parseForwardedContent(raw []byte) (any, *string, error) {
	if text, err := strRaw(raw); err == nil {
		return text, &text, nil
	}
	items, err := array(raw)
	if err != nil || len(items) == 0 {
		return nil, nil, errors.New("content is neither a string nor an array of text parts")
	}
	parts := make([]forwardedPart, len(items))
	var joined strings.Builder
	for i, item := range items {
		o, err := object(item)
		if err != nil {
			return nil, nil, err
		}
		if err := keys(o, []string{"type", "text"}, nil); err != nil {
			return nil, nil, err
		}
		kind, err := str(o, "type")
		if err != nil || kind != "text" {
			return nil, nil, errors.New("only text content parts are forwarded")
		}
		text, err := str(o, "text")
		if err != nil {
			return nil, nil, err
		}
		parts[i] = forwardedPart{Type: kind, Text: text}
		joined.WriteString(text)
	}
	flat := joined.String()
	return parts, &flat, nil
}

func parseForwardedToolCalls(raw []byte) ([]forwardedToolCall, []PriorToolCall, error) {
	items, err := array(raw)
	if err != nil {
		return nil, nil, err
	}
	calls := make([]forwardedToolCall, len(items))
	prior := make([]PriorToolCall, len(items))
	for i, item := range items {
		o, err := object(item)
		if err != nil {
			return nil, nil, err
		}
		if err := keys(o, []string{"id", "type", "function"}, nil); err != nil {
			return nil, nil, err
		}
		id, err := str(o, "id")
		if err != nil {
			return nil, nil, err
		}
		kind, err := str(o, "type")
		if err != nil || kind != "function" {
			return nil, nil, errors.New("only function tool calls are forwarded")
		}
		fn, err := object(o["function"])
		if err != nil {
			return nil, nil, err
		}
		if err := keys(fn, []string{"name", "arguments"}, nil); err != nil {
			return nil, nil, err
		}
		name, err := str(fn, "name")
		if err != nil {
			return nil, nil, err
		}
		arguments, err := str(fn, "arguments")
		if err != nil {
			return nil, nil, err
		}
		calls[i] = forwardedToolCall{ID: id, Type: kind, Function: forwardedToolCallDetail{Name: name, Arguments: arguments}}
		prior[i] = PriorToolCall{ID: id, Name: name, RawArguments: json.RawMessage(arguments)}
	}
	return calls, prior, nil
}
