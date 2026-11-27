package openrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/runend"
)

const (
	ApprovedModel         = "deepseek-v4.1-flash"
	Endpoint              = "https://opencode.ai/inference/go/v1/chat/completions"
	UserAgent             = "tbound/1.0"
	SessionHeader         = "X-Opencode-Session"
	MaxRequestBytes       = 4 << 20
	MaxResponseBytes      = 4 << 20
	MaxSSEEventBytes      = 1 << 20
	MaxSSEEvents          = 4096
	MaxToolArgumentsBytes = 1 << 20
	MaxAssistantTextBytes = 1 << 20
	maxText               = 256
)

const (
	// MaxReasoningSSEEvents bounds the stream of a reasoning route: a 4,096-token reasoning-plus-answer
	// completion is at most one event per token, so four times the cap leaves margin for framing events.
	MaxReasoningSSEEvents = 4 * 4096
	// maxMessages bounds the message count of a request; it is independent of the SSE event limits.
	maxMessages = 4096
)

type Role string

const (
	System    Role = "system"
	Developer Role = "developer"
	User      Role = "user"
	Assistant Role = "assistant"
	Tool      Role = "tool"
)

type Message struct {
	Role       Role            `json:"role"`
	Content    *string         `json:"content"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []PriorToolCall `json:"tool_calls,omitempty"`
	// Reasoning is the captured reasoning of an assistant turn. It is never serialized by Message itself:
	// only a route with the Reasoning mark sends it back, under that route's wire name.
	Reasoning string `json:"-"`
}
type PriorToolCall struct {
	ID, Name     string
	RawArguments json.RawMessage
}

type BuiltRequest struct {
	body []byte
	url  string
}

func (r BuiltRequest) RequestURL() string { return r.url }
func (r BuiltRequest) Body() []byte       { return append([]byte(nil), r.body...) }

type fnSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type functionTool struct {
	Type     string   `json:"type"`
	Function fnSchema `json:"function"`
}
type request struct {
	Model               string         `json:"model"`
	Messages            []wireMessage  `json:"messages"`
	Tools               []functionTool `json:"tools"`
	Stream              bool           `json:"stream"`
	ParallelToolCalls   bool           `json:"parallel_tool_calls"`
	ToolChoice          string         `json:"tool_choice"`
	Thinking            thinkingConfig `json:"thinking"`
	ReasoningEffort     string         `json:"reasoning_effort,omitempty"`
	MaxCompletionTokens uint64         `json:"max_completion_tokens,omitempty"`
}

// researchRequest is the fixed head of a research-route body. The frozen profile
// members are spliced after it by BuildRequestForRoute.
type researchRequest struct {
	Model               string         `json:"model"`
	Messages            []wireMessage  `json:"messages"`
	Tools               []functionTool `json:"tools"`
	Stream              bool           `json:"stream"`
	ToolChoice          string         `json:"tool_choice"`
	MaxCompletionTokens uint64         `json:"max_completion_tokens,omitempty"`
}

type thinkingConfig struct {
	Type string `json:"type"`
}

// wireMessage is a Message as sent: the reasoning of an assistant turn follows its other members under the
// route's wire name (reasoning_content for DeepSeek-native, reasoning for OpenRouter) and is empty otherwise.
type wireMessage struct {
	Message
	ReasoningContent string `json:"reasoning_content,omitempty"`
	ReasoningText    string `json:"reasoning,omitempty"`
}

const researchSchemaRead = `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to read (relative or absolute)"},"offset":{"type":"number","description":"Line number to start reading from (1-indexed)"},"limit":{"type":"number","description":"Maximum number of lines to read"}},"required":["path"]}`
const researchSchemaWrite = `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to write (relative or absolute)"},"content":{"type":"string","description":"Content to write to the file"}},"required":["path","content"]}`
const researchSchemaEdit = `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to edit (relative or absolute)"},"edits":{"type":"array","description":"One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.","items":{"type":"object","properties":{"oldText":{"type":"string","description":"Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},"newText":{"type":"string","description":"Replacement text for this targeted edit."}},"required":["oldText","newText"]}}},"required":["path","edits"]}`
const researchSchemaBash = `{"type":"object","properties":{"command":{"type":"string","description":"Shell command to execute"},"timeout":{"type":"number","description":"Timeout in seconds (optional, no default timeout)"}},"required":["command"]}`

// researchTools is the frozen tool surface of a research route: the schemas and descriptions its
// provider-request digests were measured with. Several of its promises are not what the durable executor
// does (a bash timeout, absolute paths, images), and it stays as measured.
func researchTools() []functionTool {
	return []functionTool{
		{"function", fnSchema{"read", "Read the contents of a file. Supports text files and images. Requests are forwarded to tbound.", json.RawMessage(researchSchemaRead)}},
		{"function", fnSchema{"write", "Write content to a file. Requests are forwarded to tbound.", json.RawMessage(researchSchemaWrite)}},
		{"function", fnSchema{"edit", "Edit a single file using exact text replacements. Requests are forwarded to tbound.", json.RawMessage(researchSchemaEdit)}},
		{"function", fnSchema{"bash", "Execute a bash command. Requests are forwarded to tbound.", json.RawMessage(researchSchemaBash)}},
	}
}

// The engineering schemas omit read windows and bash timeout, accept workspace-relative paths only,
// and advertise no images. The shared executor also accepts thesis read windows for retained calls.
// Route.CommandOutput selects the product Bash description. The unmarked route keeps the frozen short
// description while releasing thesis stream tails. TestAdvertisedToolSurfaceEqualsTheAdapterTextAndItsPins
// binds that description to the adapter and both modes to adapter/profile.json's schema digests.
const schemaRead = `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to read, relative to the workspace root (an absolute path ends the turn)"}},"required":["path"]}`
const schemaWrite = `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to write, relative to the workspace root (an absolute path ends the turn)"},"content":{"type":"string","description":"Content to write to the file"}},"required":["path","content"]}`
const schemaEdit = `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to edit, relative to the workspace root (an absolute path ends the turn)"},"edits":{"type":"array","description":"One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.","items":{"type":"object","properties":{"oldText":{"type":"string","description":"Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},"newText":{"type":"string","description":"Replacement text for this targeted edit."}},"required":["oldText","newText"]}}},"required":["path","edits"]}`
const schemaBash = `{"type":"object","properties":{"command":{"type":"string","description":"Shell command to run with bash in the workspace root"}},"required":["command"]}`

// tools is the engineering tool surface. commandOutput selects the bash description of an executor that
// releases bounded stdout and stderr; without it the description is the one every engineering request carried
// before product turns showed command output.
func tools(commandOutput bool) []functionTool {
	bashDescription := "Execute a bash command. Requests are forwarded to tbound."
	if commandOutput {
		bashDescription = "Execute a bash command in the workspace and return its exit code, stdout and stderr. Each stream is cut to its last 16 KiB. There is no timeout parameter: a command that runs longer than 10 minutes, or prints more than 1 MiB on one stream, is stopped and returns no result. Requests are forwarded to tbound."
	}
	return []functionTool{
		{"function", fnSchema{"read", "Read the whole contents of an existing UTF-8 text file in the workspace. There is no offset or limit. A missing file, an absolute path, a file of about 1 MiB or more, or a file that is not valid UTF-8 (a binary or image file) cannot be read and ends the turn. Requests are forwarded to tbound.", json.RawMessage(schemaRead)}},
		{"function", fnSchema{"write", "Write content to a file. Requests are forwarded to tbound.", json.RawMessage(schemaWrite)}},
		{"function", fnSchema{"edit", "Edit a single file using exact text replacements. Requests are forwarded to tbound.", json.RawMessage(schemaEdit)}},
		{"function", fnSchema{"bash", bashDescription, json.RawMessage(schemaBash)}},
	}
}

// engineeringTail and researchTail are the fixed request members that follow the
// tool list. The engineering tail disables provider thinking, which is specific
// to the OpenCode Go route; the research route adds the frozen profile members
// after the optional completion cap instead.
const (
	engineeringTail = `,"stream":true,"parallel_tool_calls":false,"tool_choice":"auto","thinking":{"type":"disabled"}`
	researchTail    = `,"stream":true,"tool_choice":"auto"`
	// reasoningEngineeringTail is the engineering tail of a Reasoning route (DeepSeek-native fields).
	reasoningEngineeringTail = `,"stream":true,"parallel_tool_calls":false,"tool_choice":"auto","thinking":{"type":"enabled"},"reasoning_effort":"high"`
)

// BuildRequestForRoute builds the registered request for one route. The route
// carries the endpoint and model; the zero route and the engineering route fix
// the endpoint, model, four Pi proxy tools, streaming, tool choice, and
// single-call behavior, with thinking disabled unless the route is marked for
// reasoning (reasoningEngineeringTail). Messages must come from trusted broker
// configuration/history: the runtime sources system/developer text from broker
// configuration, never Pi IPC. A positive completion cap is included in the exact
// outbound JSON body as max_completion_tokens and is therefore covered by
// provider-request digests and workflow token reservations. A research route
// sends its frozen four Pi proxy tools to its own endpoint and model, followed
// by the frozen profile members. It omits the OpenCode Go thinking member and
// parallel_tool_calls: require_parameters would drop its pinned provider, which
// lacks that parameter, and the capture still refuses a second call. The cap,
// when positive, is still the host-selected max_completion_tokens.
func BuildRequestForRoute(route Route, messages []Message, completionLimit uint64) (BuiltRequest, error) {
	return buildRequestForRoute(route, messages, completionLimit, "auto")
}

// BuildToolsDisabledRequestForRoute is BuildRequestForRoute with tool_choice "none": the request still declares
// the four tools, which the prior tool calls in messages refer to, but the model may only answer in text. It is
// the last provider call of a workflow whose host calls cannot carry a tool call's effect and result.
func BuildToolsDisabledRequestForRoute(route Route, messages []Message, completionLimit uint64) (BuiltRequest, error) {
	return buildRequestForRoute(route, messages, completionLimit, "none")
}

// buildRequestForRoute takes toolChoice "auto" or "none"; both are four bytes, the length the fixed tails
// count in the preflight size.
func buildRequestForRoute(route Route, messages []Message, completionLimit uint64, toolChoice string) (BuiltRequest, error) {
	route = route.Resolved()
	if err := route.Validate(); err != nil {
		return BuiltRequest{}, err
	}
	modelID := route.Model
	tail, reasoningKey := engineeringTail, ""
	switch {
	case route.IsResearch():
		tail = researchTail
		if route.Reasoning {
			reasoningKey = "reasoning"
		}
	case route.Reasoning:
		tail, reasoningKey = reasoningEngineeringTail, "reasoning_content"
	}
	if completionLimit > math.MaxInt32 {
		return BuiltRequest{}, errors.New("completion-token cap is outside the supported request range")
	}
	if len(messages) == 0 || len(messages) > maxMessages {
		return BuiltRequest{}, errors.New("invalid message count")
	}
	for i, message := range messages {
		if err := validateMessage(message); err != nil {
			return BuiltRequest{}, fmt.Errorf("message %d: %w", i, err)
		}
	}
	fixedTools := tools(route.CommandOutput)
	if route.IsResearch() {
		fixedTools = researchTools()
	}
	toolsJSON, err := json.Marshal(fixedTools)
	if err != nil {
		return BuiltRequest{}, fmt.Errorf("encode fixed tool schemas: %w", err)
	}
	expectedSize, err := preflightRequestSize(modelID, messages, toolsJSON, completionLimit, tail, reasoningKey)
	if err != nil {
		return BuiltRequest{}, err
	}
	for messageIndex, message := range messages {
		for callIndex, call := range message.ToolCalls {
			if _, err := protocol.CanonicalArgumentsDigest(call.Name, call.RawArguments); err != nil {
				return BuiltRequest{}, fmt.Errorf("message %d prior tool call %d: %w", messageIndex, callIndex, err)
			}
		}
	}
	wire := make([]wireMessage, len(messages))
	for i, message := range messages {
		wire[i].Message = message
		switch reasoningKey {
		case "reasoning":
			wire[i].ReasoningText = message.Reasoning
		case "reasoning_content":
			wire[i].ReasoningContent = message.Reasoning
		}
	}
	var body []byte
	if route.IsResearch() {
		body, err = json.Marshal(researchRequest{Model: modelID, Messages: wire, Tools: fixedTools, Stream: true,
			ToolChoice: toolChoice, MaxCompletionTokens: completionLimit})
		if err == nil {
			// Replace the closing brace with the frozen members: ",<members without its opening brace>".
			body = append(append(body[:len(body)-1], ','), route.members[1:]...)
			expectedSize += len(route.members) - 1
		}
	} else {
		thinking, effort := thinkingConfig{Type: "disabled"}, ""
		if route.Reasoning {
			thinking, effort = thinkingConfig{Type: "enabled"}, "high"
		}
		body, err = json.Marshal(request{Model: modelID, Messages: wire, Tools: fixedTools, Stream: true,
			ParallelToolCalls: false, ToolChoice: toolChoice, Thinking: thinking, ReasoningEffort: effort,
			MaxCompletionTokens: completionLimit})
	}
	if err != nil {
		return BuiltRequest{}, err
	}
	if len(body) != expectedSize || len(body) > MaxRequestBytes {
		return BuiltRequest{}, errors.New("encoded request size did not match its bounded preflight")
	}
	return BuiltRequest{body: body, url: route.Endpoint}, nil
}
func preflightRequestSize(modelID string, messages []Message, toolsJSON []byte, completionLimit uint64, tail, reasoningKey string) (int, error) {
	size := 0
	var failure error
	add := func(amount int) {
		switch {
		case failure != nil:
		case amount < 0 || amount > MaxRequestBytes-size:
			failure = fmt.Errorf("%w: request exceeds byte limit before marshaling", runend.ErrCeiling)
		default:
			size += amount
		}
	}
	addLiteral := func(literals ...string) {
		for _, literal := range literals {
			add(len(literal))
		}
	}
	addString := func(value string) {
		if failure != nil {
			return
		}
		length, err := escapedJSONStringLength(value)
		if err != nil {
			failure = err
			return
		}
		add(length)
	}
	addLiteral(`{"model":`)
	addString(modelID)
	addLiteral(`,"messages":[`)
	for index, message := range messages {
		if failure != nil {
			break
		}
		if index != 0 {
			add(1)
		}
		addLiteral(`{"role":`)
		addString(string(message.Role))
		addLiteral(`,"content":`)
		if message.Content == nil {
			addLiteral(`null`)
		} else {
			addString(*message.Content)
		}
		if message.ToolCallID != "" {
			addLiteral(`,"tool_call_id":`)
			addString(message.ToolCallID)
		}
		if len(message.ToolCalls) != 0 {
			addLiteral(`,"tool_calls":[`)
			for callIndex, call := range message.ToolCalls {
				if callIndex != 0 {
					add(1)
				}
				addLiteral(`{"id":`, `,"type":"function","function":{"name":`)
				addString(call.ID)
				addString(call.Name)
				addLiteral(`,"arguments":`)
				if failure == nil {
					argumentLength, err := escapedJSONBytesLength(call.RawArguments)
					if err != nil {
						failure = err
					}
					add(argumentLength)
				}
				add(2) // closes function and tool-call objects
			}
			add(1) // closes tool_calls array
		}
		if reasoningKey != "" && message.Reasoning != "" {
			addLiteral(`,"` + reasoningKey + `":`)
			addString(message.Reasoning)
		}
		add(1) // closes message object
	}
	addLiteral(`],"tools":`, tail)
	add(len(toolsJSON))
	if completionLimit > 0 {
		addLiteral(`,"max_completion_tokens":`)
		add(len(strconv.FormatUint(completionLimit, 10)))
	}
	add(1) // closes request object
	if failure != nil {
		return 0, failure
	}
	return size, nil
}

func escapedJSONStringLength(value string) (int, error) {
	if !utf8.ValidString(value) {
		return 0, errors.New("request string is not valid UTF-8")
	}
	length := 2 // enclosing quotes
	for _, r := range value {
		part := escapedRuneLength(r)
		if part > MaxRequestBytes-length {
			return 0, fmt.Errorf("%w: request string exceeds byte limit", runend.ErrCeiling)
		}
		length += part
	}
	return length, nil
}

func escapedJSONBytesLength(value []byte) (int, error) {
	if !utf8.Valid(value) {
		return 0, errors.New("tool arguments are not valid UTF-8")
	}
	length := 2 // enclosing quotes
	for len(value) != 0 {
		r, width := utf8.DecodeRune(value)
		part := escapedRuneLength(r)
		if part > MaxRequestBytes-length {
			return 0, fmt.Errorf("%w: request string exceeds byte limit", runend.ErrCeiling)
		}
		length += part
		value = value[width:]
	}
	return length, nil
}

func escapedRuneLength(r rune) int {
	switch r {
	case '"', '\\', '\b', '\f', '\n', '\r', '\t':
		return 2
	case '<', '>', '&', '\u2028', '\u2029':
		return 6
	default:
		if r < 0x20 {
			return 6
		}
		return utf8.RuneLen(r)
	}
}
func validateMessage(m Message) error { return validateMessageCalls(m, 1) }

func validateMessageCalls(m Message, maxCalls int) error {
	if m.Reasoning != "" && m.Role != Assistant {
		return errors.New("only an assistant message carries reasoning")
	}
	switch m.Role {
	case System, Developer, User:
		if m.Content == nil || m.ToolCallID != "" || len(m.ToolCalls) != 0 {
			return errors.New("invalid text message fields")
		}
	case Assistant:
		if m.ToolCallID != "" || (m.Content == nil && len(m.ToolCalls) == 0) {
			return errors.New("invalid assistant message fields")
		}
		if len(m.ToolCalls) > maxCalls {
			return errors.New("profile permits one prior tool call per assistant message")
		}
		seen := map[string]bool{}
		for _, call := range m.ToolCalls {
			if !validText(call.ID, maxText) || seen[call.ID] {
				return errors.New("invalid or repeated prior tool call ID")
			}
			seen[call.ID] = true
			if !validText(call.Name, maxText) || len(call.RawArguments) == 0 || len(call.RawArguments) > protocol.MaxFrameBytes {
				return errors.New("invalid or oversized prior tool-call fields")
			}
		}
	case Tool:
		if m.Content == nil || !validText(m.ToolCallID, maxText) || len(m.ToolCalls) != 0 {
			return errors.New("invalid tool result fields")
		}
	default:
		return errors.New("unsupported message role")
	}
	return nil
}
func (c PriorToolCall) MarshalJSON() ([]byte, error) {
	if !validText(c.ID, maxText) {
		return nil, errors.New("invalid prior call ID")
	}
	if _, err := protocol.CanonicalArgumentsDigest(c.Name, c.RawArguments); err != nil {
		return nil, err
	}
	wire := struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}{ID: c.ID, Type: "function"}
	wire.Function.Name, wire.Function.Arguments = c.Name, string(c.RawArguments)
	return json.Marshal(wire)
}

type ToolCall struct {
	ID, Name                 string
	RawArguments             json.RawMessage
	CanonicalArgumentsDigest string
}

// CapturedResponse is minted by the bounded parser. Private fields prevent ordinary adapter IPC decoding from constructing it. Parsing and typed mapping alone do not authenticate that the bytes came from OpenRouter; the future trusted broker transport must establish that provenance.
type CapturedResponse struct {
	id, model, provider, systemFingerprint, text, finish, nativeFinish string
	reasoning                                                          string
	created                                                            int64
	usage, raw                                                         []byte
	call                                                               *ToolCall
	dropped                                                            int
}

func (r CapturedResponse) ResponseID() string    { return r.id }
func (r CapturedResponse) Model() string         { return r.model }
func (r CapturedResponse) Provider() string      { return r.provider }
func (r CapturedResponse) Created() int64        { return r.created }
func (r CapturedResponse) AssistantText() string { return r.text }

// Reasoning is the reasoning text streamed by the provider, accumulated only when the response was parsed for a
// Reasoning route. It is evidence to be sent back on later requests; it carries no authority.
func (r CapturedResponse) Reasoning() string      { return r.reasoning }
func (r CapturedResponse) FinishReason() string   { return r.finish }
func (r CapturedResponse) Usage() json.RawMessage { return append(json.RawMessage(nil), r.usage...) }
func (r CapturedResponse) RawResponse() []byte    { return append([]byte(nil), r.raw...) }
func (r CapturedResponse) DroppedToolCalls() int  { return r.dropped }
func (r CapturedResponse) ToolCall() (ToolCall, bool) {
	if r.call == nil {
		return ToolCall{}, false
	}
	c := *r.call
	c.RawArguments = append(json.RawMessage(nil), c.RawArguments...)
	return c, true
}

// TrustedCapture maps parser output and broker-assigned issuer, sequence, and generation values. It does not authenticate provider provenance; only trusted broker code may call it after capturing its provider exchange.
func (r CapturedResponse) TrustedCapture(callIssuer, responseIssuer, generation string, sequence uint64) (protocol.TrustedCapture, error) {
	if len(r.raw) == 0 || r.finish != "tool_calls" || r.call == nil {
		return protocol.TrustedCapture{}, errors.New("response has no completed tool call")
	}
	if !validText(callIssuer, maxText) || !validText(responseIssuer, maxText) || !validText(generation, 128) || sequence == 0 {
		return protocol.TrustedCapture{}, errors.New("invalid broker capture metadata")
	}
	return protocol.TrustedCapture{
		ResponseID: correlation.Identifier{Issuer: responseIssuer, Opaque: r.id},
		ToolCallID: correlation.Identifier{Issuer: callIssuer, Opaque: r.call.ID},
		ToolName:   r.call.Name, RawArguments: append(json.RawMessage(nil), r.call.RawArguments...),
		CanonicalizationProfile:  protocol.CanonicalizationProfile,
		CanonicalArgumentsDigest: r.call.CanonicalArgumentsDigest, Sequence: sequence, Generation: generation,
	}, nil
}

// ErrParallelToolCall is the capture refusal of a stream that starts a second tool call (a tool-call
// index above zero) off the research route; a research capture keeps call 0 and drops the rest (D64).
// The sentinel lets the bridge apply decision 13's bounded resample to exactly this class. Its text
// is the refusal text journaled before the sentinel existed.
var ErrParallelToolCall = errors.New("tool-call index must be zero")

type assembly struct {
	id, kind string
	name     strings.Builder
	args     []byte
}
type stream struct {
	out                  CapturedResponse
	init, finished, done bool
	tool                 *assembly
	dropped              map[int64]bool
	dropParallel         bool
	events               int
	legacyNulls          bool
	reasoning            bool
	allowLength          bool
	maxEvents            int
}

// CaptureSSE reads and bounds the complete body before parsing it, preserving
// exact bytes. This decoder does not authenticate provenance or perform HTTP;
// its caller must be the trusted broker after a real provider exchange. The
// fixed request does not enable stream_options.include_usage, so usage-only
// trailing chunks are outside this profile.
func CaptureSSE(reader io.Reader) (CapturedResponse, error) {
	return captureSSE(reader, false, Route{}, false)
}

// CaptureSSEForRoute parses like CaptureSSE. A route with the Reasoning mark also accumulates the streamed
// reasoning (bounded by MaxAssistantTextBytes) and allows MaxReasoningSSEEvents events.
func CaptureSSEForRoute(reader io.Reader, route Route) (CapturedResponse, error) {
	return captureSSE(reader, false, route, false)
}

// TruncatedUsage returns the usage object of a Reasoning route's stream that ended with finish_reason "length",
// which CaptureSSE refuses. The stream must otherwise parse exactly as CaptureSSEForRoute requires, and the
// result is for spend settlement only: nothing of the truncated reply is released.
func TruncatedUsage(raw []byte, route Route) (json.RawMessage, bool) {
	if !route.Reasoning {
		return nil, false
	}
	captured, err := captureSSE(bytes.NewReader(raw), false, route, true)
	if err != nil || captured.finish != "length" || len(captured.usage) == 0 {
		return nil, false
	}
	return captured.Usage(), true
}

func CaptureExchange(raw []byte, route Route, completionLimit uint64, cut bool) (CapturedResponse, error) {
	if !cut {
		return CaptureSSEForRoute(bytes.NewReader(raw), route)
	}
	if !route.IsResearch() || !route.Reasoning || completionLimit == 0 {
		return CapturedResponse{}, errors.New("a reply cut at the completion cap is typed only on the research route with a known cap")
	}
	captured, err := captureSSE(bytes.NewReader(raw), false, route, true)
	if err != nil {
		return CapturedResponse{}, err
	}
	var usage struct {
		Completion *uint64 `json:"completion_tokens"`
	}
	if captured.finish != "length" || json.Unmarshal(captured.usage, &usage) != nil || usage.Completion == nil || *usage.Completion != completionLimit {
		return CapturedResponse{}, errors.New("the reply is not cut at the completion cap")
	}
	return captured, nil
}

// CaptureSSELegacyNulls parses with the strictness before f53fd29f: JSON null in a
// delta role or tool_calls, or a tool delta id or type, is an error. Only replay of
// capture-refusal records journaled before that fix may use it.
func CaptureSSELegacyNulls(reader io.Reader) (CapturedResponse, error) {
	return captureSSE(reader, true, Route{}, false)
}

func captureSSE(reader io.Reader, legacyNulls bool, route Route, allowLength bool) (CapturedResponse, error) {
	if reader == nil {
		return CapturedResponse{}, errors.New("nil response reader")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxResponseBytes+1))
	if err != nil {
		return CapturedResponse{}, fmt.Errorf("read response: %w", err)
	}
	if len(raw) == 0 || len(raw) > MaxResponseBytes {
		return CapturedResponse{}, errors.New("response byte length out of bounds")
	}
	return decodeSSE(raw, legacyNulls, route.Reasoning, allowLength, route.IsResearch())
}

func IsParallelToolCallRefusal(raw []byte, route Route) bool {
	if len(raw) == 0 || len(raw) > MaxResponseBytes {
		return false
	}
	_, err := decodeSSE(raw, false, route.Reasoning, false, false)
	return errors.Is(err, ErrParallelToolCall)
}

func decodeSSE(raw []byte, legacyNulls, reasoning, allowLength, dropParallel bool) (CapturedResponse, error) {
	if !utf8.Valid(raw) {
		return CapturedResponse{}, errors.New("SSE is invalid UTF-8")
	}
	s := stream{legacyNulls: legacyNulls, reasoning: reasoning, allowLength: allowLength, dropParallel: dropParallel, maxEvents: MaxSSEEvents}
	if reasoning {
		s.maxEvents = MaxReasoningSSEEvents
	}
	var data []byte
	hasData := false
	for off := 0; off < len(raw); {
		n := bytes.IndexByte(raw[off:], '\n')
		if n < 0 {
			return CapturedResponse{}, errors.New("unterminated SSE line")
		}
		end := off + n
		line := raw[off:end]
		off = end + 1
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if bytes.IndexByte(line, '\r') >= 0 || len(line) > MaxSSEEventBytes {
			return CapturedResponse{}, errors.New("malformed or oversized SSE line")
		}
		if len(line) == 0 {
			if !hasData {
				continue
			}
			s.events++
			if s.events > s.maxEvents {
				return CapturedResponse{}, fmt.Errorf("%w: too many SSE events", runend.ErrCeiling)
			}
			if s.done {
				return CapturedResponse{}, errors.New("data after [DONE]")
			}
			if string(data) == "[DONE]" {
				s.done = true
			} else {
				if err := s.chunk(data); err != nil {
					return CapturedResponse{}, err
				}
			}
			data = data[:0]
			hasData = false
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, ok := bytes.Cut(line, []byte{':'})
		if !ok {
			value = nil
		}
		if !bytes.Equal(field, []byte("data")) {
			return CapturedResponse{}, errors.New("unsupported SSE field")
		}
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		add := len(value)
		if hasData {
			add++
		}
		if add > MaxSSEEventBytes-len(data) {
			return CapturedResponse{}, errors.New("SSE event exceeds byte limit")
		}
		if hasData {
			data = append(data, '\n')
		}
		data = append(data, value...)
		hasData = true
	}
	if hasData {
		return CapturedResponse{}, errors.New("SSE event is not terminated")
	}
	if !s.done || !s.init || !s.finished {
		return CapturedResponse{}, errors.New("missing completion, finish_reason, or [DONE]")
	}
	if err := s.finalize(); err != nil {
		return CapturedResponse{}, err
	}
	s.out.raw = append([]byte(nil), raw...)
	return s.out, nil
}
func (s *stream) chunk(raw []byte) error {
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return fmt.Errorf("invalid SSE JSON: %w", err)
	}
	o, err := object(raw)
	if err != nil {
		return err
	}
	if err = keys(o, []string{"id", "model", "created", "choices"}, []string{"provider", "object", "system_fingerprint", "usage", "service_tier"}); err != nil {
		return err
	}
	id, e1 := str(o, "id")
	model, e2 := str(o, "model")
	created, e3 := integer(o["created"])
	if e1 != nil || !validText(id, maxText) || e2 != nil || !validText(model, maxText) || e3 != nil || created < 0 {
		return errors.New("invalid response ID, model, or created value")
	}
	provider, hasProvider, e := optStr(o, "provider")
	if e != nil || (hasProvider && !validText(provider, maxText)) {
		return errors.New("invalid provider")
	}
	if v, ok := o["object"]; ok {
		name, e := strRaw(v)
		if e != nil || name != "chat.completion.chunk" {
			return errors.New("unsupported chunk object")
		}
	}
	if v, ok := o["system_fingerprint"]; ok {
		fp, e := strRaw(v)
		if e != nil || (s.out.systemFingerprint != "" && fp != s.out.systemFingerprint) {
			return errors.New("invalid or changed system fingerprint")
		}
		s.out.systemFingerprint = fp
	}
	if v, ok := o["usage"]; ok && !isNull(v) {
		if _, e := object(v); e != nil {
			return errors.New("usage is not an object")
		}
		if len(s.out.usage) != 0 {
			return errors.New("repeated usage metadata")
		}
		s.out.usage = append([]byte(nil), v...)
	}
	if !s.init {
		s.out.id, s.out.model, s.out.created = id, model, created
		s.init = true
	} else if id != s.out.id || model != s.out.model || created != s.out.created {
		return errors.New("response identity changed within stream")
	}
	if hasProvider {
		if s.out.provider != "" && s.out.provider != provider {
			return errors.New("provider changed within stream")
		}
		s.out.provider = provider
	}
	choices, e := array(o["choices"])
	if e != nil {
		return errors.New("choices is not an array")
	}
	if s.finished {
		return s.trailerChunk(choices)
	}
	if len(choices) != 1 {
		return errors.New("chunk must have exactly one choice")
	}
	c, e := object(choices[0])
	if e != nil {
		return e
	}
	if e = keys(c, []string{"index", "delta"}, []string{"finish_reason", "logprobs", "native_finish_reason"}); e != nil {
		return e
	}
	index, e := integer(c["index"])
	if e != nil || index != 0 {
		return errors.New("choice index must be zero")
	}
	if v, ok := c["logprobs"]; ok && !isNull(v) {
		return errors.New("logprobs are unsupported")
	}
	if v, ok := c["native_finish_reason"]; ok && !isNull(v) {
		native, e := strRaw(v)
		if e != nil || (s.out.nativeFinish != "" && native != s.out.nativeFinish) {
			return errors.New("invalid or unstable native finish reason")
		}
		s.out.nativeFinish = native
	}
	if e = s.delta(c["delta"]); e != nil {
		return e
	}
	// Some OpenCode Go upstreams omit finish_reason until the final chunk; absent means null.
	finishRaw, hasFinish := c["finish_reason"]
	if !hasFinish || isNull(finishRaw) {
		return nil
	}
	finish, e := strRaw(finishRaw)
	if e == nil && finish == "length" && !s.allowLength && !s.finished {
		// The reply met max_completion_tokens: the per-call cap of the agent's own token use (D50).
		return fmt.Errorf("%w: reply cut at the per-call completion cap (finish reason length)", runend.ErrCeiling)
	}
	if e != nil || (finish != "stop" && finish != "tool_calls" && !(s.allowLength && finish == "length")) || s.finished {
		return errors.New("unsupported or repeated finish reason")
	}
	s.finished = true
	s.out.finish = finish
	return nil
}

// trailerChunk accepts a terminal metadata chunk that real providers send after
// finish_reason (for example an OpenRouter usage chunk). It permits an empty
// choices array or one choice with a null, absent or equal finish_reason, and it rejects any new
// trusted content or tool call, so nothing can be smuggled in after completion.
func (s *stream) trailerChunk(choices []json.RawMessage) error {
	if len(choices) == 0 {
		return nil
	}
	if len(choices) != 1 {
		return errors.New("trailer chunk must have at most one choice")
	}
	c, e := object(choices[0])
	if e != nil {
		return e
	}
	if e = keys(c, []string{"index", "delta"}, []string{"finish_reason", "logprobs", "native_finish_reason"}); e != nil {
		return e
	}
	index, e := integer(c["index"])
	if e != nil || index != 0 {
		return errors.New("choice index must be zero")
	}
	if v, ok := c["finish_reason"]; ok && !isNull(v) {
		finish, e := strRaw(v)
		if e != nil || finish != s.out.finish {
			return errors.New("conflicting finish reason after completion")
		}
	}
	if v, ok := c["logprobs"]; ok && !isNull(v) {
		return errors.New("logprobs are unsupported")
	}
	return s.trailerDelta(c["delta"])
}

// trailerDelta validates the delta of a post-finish trailer: it may repeat the
// assistant role or carry a null/empty content string, but it must not add any
// assistant text or a tool call after the stream already completed.
func (s *stream) trailerDelta(raw []byte) error {
	d, e := object(raw)
	if e != nil {
		return errors.New("choice delta is not an object")
	}
	if e = keys(d, nil, []string{"role", "content", "tool_calls", "reasoning", "reasoning_content", "reasoning_details", "refusal"}); e != nil {
		return e
	}
	if v, ok := d["tool_calls"]; ok && !isNull(v) {
		return errors.New("tool call after completion")
	}
	if v, ok := d["content"]; ok && !isNull(v) {
		text, e := strRaw(v)
		if e != nil || text != "" {
			return errors.New("content after completion")
		}
	}
	if v, ok := d["role"]; ok && !isNull(v) {
		role, e := strRaw(v)
		if e != nil || role != "assistant" {
			return errors.New("role changed after completion")
		}
	}
	return nil
}

func (s *stream) delta(raw []byte) error {
	d, e := object(raw)
	if e != nil {
		return errors.New("choice delta is not an object")
	}
	// role/content/tool_calls are the trusted delta fields. reasoning,
	// reasoning_content, reasoning_details, and refusal are provider-emitted
	// non-claim fields that real models stream; they are ignored and cannot
	// affect the trusted capture.
	if e = keys(d, nil, []string{"role", "content", "tool_calls", "reasoning", "reasoning_content", "reasoning_details", "refusal"}); e != nil {
		return e
	}
	if v, ok := d["role"]; ok && !s.null(v) {
		role, e := strRaw(v)
		if e != nil || role != "assistant" {
			return errors.New("unsupported delta role")
		}
	}
	if v, ok := d["content"]; ok && !isNull(v) {
		text, e := strRaw(v)
		if e != nil || len(text) > MaxAssistantTextBytes-len(s.out.text) {
			return errors.New("invalid or oversized assistant text")
		}
		s.out.text += text
	}
	if s.reasoning {
		// the first non-empty of reasoning_content and reasoning is taken, so a provider repeating the text under both names counts it once.
		for _, name := range []string{"reasoning_content", "reasoning"} {
			v, ok := d[name]
			if !ok || isNull(v) {
				continue
			}
			text, e := strRaw(v)
			if e != nil || len(text) > MaxAssistantTextBytes-len(s.out.reasoning) {
				return errors.New("invalid or oversized reasoning")
			}
			if text == "" {
				continue
			}
			s.out.reasoning += text
			break
		}
	}
	if v, ok := d["tool_calls"]; ok && !s.null(v) {
		calls, e := array(v)
		if e != nil || (len(calls) > 1 && !s.dropParallel) {
			return errors.New("multiple or malformed tool calls")
		}
		for _, call := range calls {
			if e := s.toolDelta(call); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *stream) toolDelta(raw []byte) error {
	t, e := object(raw)
	if e != nil {
		return errors.New("tool-call delta is not an object")
	}
	if e = keys(t, []string{"index"}, []string{"id", "type", "function"}); e != nil {
		return e
	}
	index, e := integer(t["index"])
	if e == nil && index > 0 {
		if !s.dropParallel {
			return ErrParallelToolCall
		}
		if s.dropped == nil {
			s.dropped = map[int64]bool{}
		}
		s.dropped[index] = true
		return nil
	}
	if e != nil || index != 0 {
		return errors.New("tool-call index must be zero")
	}
	if s.tool == nil {
		s.tool = &assembly{}
	}
	a := s.tool
	if v, ok := t["id"]; ok && !s.null(v) {
		id, e := strRaw(v)
		if e != nil || !validText(id, maxText) || (a.id != "" && id != a.id) {
			return errors.New("empty or changed tool-call ID")
		}
		a.id = id
	}
	if v, ok := t["type"]; ok && !s.null(v) {
		kind, e := strRaw(v)
		if e != nil || kind != "function" || (a.kind != "" && a.kind != kind) {
			return errors.New("unsupported or changed tool-call type")
		}
		a.kind = kind
	}
	if v, ok := t["function"]; ok {
		f, e := object(v)
		if e != nil {
			return errors.New("tool-call function is not an object")
		}
		if e = keys(f, nil, []string{"name", "arguments"}); e != nil {
			return e
		}
		if v, ok := f["name"]; ok {
			name, e := strRaw(v)
			if e != nil || len(name) > maxText-a.name.Len() {
				return errors.New("invalid or oversized tool name")
			}
			a.name.WriteString(name)
		}
		if v, ok := f["arguments"]; ok {
			args, e := strRaw(v)
			if e != nil || len(args) > MaxToolArgumentsBytes-len(a.args) {
				return errors.New("invalid or oversized argument fragment")
			}
			a.args = append(a.args, args...)
		}
	}
	return nil
}
func (s *stream) finalize() error {
	s.out.dropped = len(s.dropped)
	switch s.out.finish {
	case "length":
		return nil
	case "stop":
		if s.tool != nil || s.out.dropped != 0 {
			return errors.New("stop finish reason conflicts with a tool call")
		}
		return nil
	case "tool_calls":
		if s.tool == nil || s.tool.id == "" || s.tool.kind != "function" || s.tool.name.Len() == 0 || len(s.tool.args) == 0 {
			return errors.New("tool call is incomplete")
		}
		name := s.tool.name.String()
		raw := append(json.RawMessage(nil), s.tool.args...)
		digest, e := protocol.CanonicalArgumentsDigest(name, raw)
		if e != nil {
			return fmt.Errorf("tool arguments fail registered schema: %w", e)
		}
		s.out.call = &ToolCall{ID: s.tool.id, Name: name, RawArguments: raw, CanonicalArgumentsDigest: digest}
		return nil
	default:
		return errors.New("unsupported finish reason")
	}
}

func object(raw []byte) (map[string]json.RawMessage, error) {
	var v map[string]json.RawMessage
	if e := json.Unmarshal(raw, &v); e != nil || v == nil {
		return nil, errors.New("expected JSON object")
	}
	return v, nil
}
func keys(o map[string]json.RawMessage, required, optional []string) error {
	allowed := map[string]bool{}
	for _, k := range required {
		allowed[k] = true
		if _, ok := o[k]; !ok {
			return fmt.Errorf("missing field %q", k)
		}
	}
	for _, k := range optional {
		allowed[k] = true
	}
	for k := range o {
		if !allowed[k] {
			return fmt.Errorf("unsupported field %q", k)
		}
	}
	return nil
}
func str(o map[string]json.RawMessage, k string) (string, error) {
	v, ok := o[k]
	if !ok {
		return "", errors.New("missing string")
	}
	return strRaw(v)
}
func optStr(o map[string]json.RawMessage, k string) (string, bool, error) {
	v, ok := o[k]
	if !ok {
		return "", false, nil
	}
	s, e := strRaw(v)
	return s, true, e
}
func strRaw(v []byte) (string, error) { var s string; e := json.Unmarshal(v, &s); return s, e }
func integer(v []byte) (int64, error) {
	var n json.Number
	d := json.NewDecoder(bytes.NewReader(v))
	d.UseNumber()
	if e := d.Decode(&n); e != nil {
		return 0, e
	}
	return strconv.ParseInt(n.String(), 10, 64)
}
func array(v []byte) ([]json.RawMessage, error) {
	var a []json.RawMessage
	if e := json.Unmarshal(v, &a); e != nil || a == nil {
		return nil, errors.New("expected JSON array")
	}
	return a, nil
}
func isNull(v []byte) bool { return bytes.Equal(bytes.TrimSpace(v), []byte("null")) }
func validText(s string, limit int) bool {
	if s == "" || len(s) > limit || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *stream) null(v []byte) bool { return !s.legacyNulls && isNull(v) }
