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
)

const (
	Endpoint = "https://openrouter.ai/api/v1/chat/completions"
	MaxRequestBytes = 4 << 20
	MaxResponseBytes = 4 << 20
	MaxSSEEventBytes = 1 << 20
	MaxSSEEvents = 4096
	MaxToolArgumentsBytes = 1 << 20
	MaxAssistantTextBytes = 1 << 20
	maxText = 256
	maxDepth = 64
)

type Role string
const (
	System Role = "system"
	Developer Role = "developer"
	User Role = "user"
	Assistant Role = "assistant"
	Tool Role = "tool"
)

type Message struct {
	Role Role `json:"role"`
	Content *string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolCalls []PriorToolCall `json:"tool_calls,omitempty"`
}
type PriorToolCall struct { ID, Name string; RawArguments json.RawMessage }

type BuiltRequest struct { body []byte }
func (r BuiltRequest) RequestURL() string { return Endpoint }
func (r BuiltRequest) Body() []byte { return append([]byte(nil), r.body...) }

type fnSchema struct {
	Name string `json:"name"`
	Description string `json:"description"`
	Parameters json.RawMessage `json:"parameters"`
}
type functionTool struct {
	Type string `json:"type"`
	Function fnSchema `json:"function"`
}
type request struct {
	Model string `json:"model"`
	Messages []Message `json:"messages"`
	Tools []functionTool `json:"tools"`
	Stream bool `json:"stream"`
	ParallelToolCalls bool `json:"parallel_tool_calls"`
	ToolChoice string `json:"tool_choice"`
}

const schemaRead = `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to read (relative or absolute)"},"offset":{"type":"number","description":"Line number to start reading from (1-indexed)"},"limit":{"type":"number","description":"Maximum number of lines to read"}},"required":["path"]}`
const schemaWrite = `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to write (relative or absolute)"},"content":{"type":"string","description":"Content to write to the file"}},"required":["path","content"]}`
const schemaEdit = `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to edit (relative or absolute)"},"edits":{"type":"array","description":"One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.","items":{"type":"object","properties":{"oldText":{"type":"string","description":"Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},"newText":{"type":"string","description":"Replacement text for this targeted edit."}},"required":["oldText","newText"]}},"required":["path","edits"]}`
const schemaBash = `{"type":"object","properties":{"command":{"type":"string","description":"Shell command to execute"},"timeout":{"type":"number","description":"Timeout in seconds (optional, no default timeout)"}},"required":["command"]}`

func tools() []functionTool {
	return []functionTool{
		{"function", fnSchema{"read", "Read the contents of a file. Supports text files and images. Requests are forwarded to tbound.", json.RawMessage(schemaRead)}},
		{"function", fnSchema{"write", "Write content to a file. Requests are forwarded to tbound.", json.RawMessage(schemaWrite)}},
		{"function", fnSchema{"edit", "Edit a single file using exact text replacements. Requests are forwarded to tbound.", json.RawMessage(schemaEdit)}},
		{"function", fnSchema{"bash", "Execute a bash command. Requests are forwarded to tbound.", json.RawMessage(schemaBash)}},
	}
}

// BuildRequest fixes the endpoint, four Pi proxy tools, streaming, tool choice,
 // and single-call behavior. modelID and messages must come from trusted broker
 // configuration/history. The future runtime must source system/developer text
 // from broker configuration, never from Pi IPC.
func BuildRequest(modelID string, messages []Message) (BuiltRequest, error) {
	if !validText(modelID, maxText) {
		return BuiltRequest{}, errors.New("invalid trusted model ID")
	}
	if len(messages) == 0 || len(messages) > MaxSSEEvents {
		return BuiltRequest{}, errors.New("invalid message count")
	}
	for i, message := range messages {
		if err := validateMessage(message); err != nil {
			return BuiltRequest{}, fmt.Errorf("message %d: %w", i, err)
		}
	}
	fixedTools := tools()
	toolsJSON, err := json.Marshal(fixedTools)
	if err != nil {
		return BuiltRequest{}, fmt.Errorf("encode fixed tool schemas: %w", err)
	}
	expectedSize, err := preflightRequestSize(modelID, messages, toolsJSON)
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
	body, err := json.Marshal(request{modelID, messages, fixedTools, true, false, "auto"})
	if err != nil {
		return BuiltRequest{}, err
	}
	if len(body) != expectedSize || len(body) > MaxRequestBytes {
		return BuiltRequest{}, errors.New("encoded request size did not match its bounded preflight")
	}
	return BuiltRequest{body}, nil
}
func preflightRequestSize(modelID string, messages []Message, toolsJSON []byte) (int, error) {
	size := 0
	add := func(amount int) error {
		if amount < 0 || amount > MaxRequestBytes-size {
			return errors.New("request exceeds byte limit before marshaling")
		}
		size += amount
		return nil
	}
	addString := func(value string) error {
		length, err := escapedJSONStringLength(value)
		if err != nil {
			return err
		}
		return add(length)
	}
	if err := add(len(`{"model":`)); err != nil {
		return 0, err
	}
	if err := addString(modelID); err != nil {
		return 0, err
	}
	if err := add(len(`,"messages":[`)); err != nil {
		return 0, err
	}
	for index, message := range messages {
		if index != 0 {
			if err := add(1); err != nil {
				return 0, err
			}
		}
		if err := add(len(`{"role":`)); err != nil {
			return 0, err
		}
		if err := addString(string(message.Role)); err != nil {
			return 0, err
		}
		if err := add(len(`,"content":`)); err != nil {
			return 0, err
		}
		if message.Content == nil {
			if err := add(len(`null`)); err != nil {
				return 0, err
			}
		} else if err := addString(*message.Content); err != nil {
			return 0, err
		}
		if message.ToolCallID != "" {
			if err := add(len(`,"tool_call_id":`)); err != nil {
				return 0, err
			}
			if err := addString(message.ToolCallID); err != nil {
				return 0, err
			}
		}
		if len(message.ToolCalls) != 0 {
			if err := add(len(`,"tool_calls":[`)); err != nil {
				return 0, err
			}
			for callIndex, call := range message.ToolCalls {
				if callIndex != 0 {
					if err := add(1); err != nil {
						return 0, err
					}
				}
				for _, literal := range []string{`{"id":`, `,"type":"function","function":{"name":`} {
					if err := add(len(literal)); err != nil {
						return 0, err
					}
				}
				if err := addString(call.ID); err != nil {
					return 0, err
				}
				if err := addString(call.Name); err != nil {
					return 0, err
				}
				if err := add(len(`,"arguments":`)); err != nil {
					return 0, err
				}
				argumentLength, err := escapedJSONBytesLength(call.RawArguments)
				if err != nil {
					return 0, err
				}
				if err := add(argumentLength); err != nil {
					return 0, err
				}
				if err := add(2); err != nil { // closes function and tool-call objects
					return 0, err
				}
			}
			if err := add(1); err != nil { // closes tool_calls array
				return 0, err
			}
		}
		if err := add(1); err != nil { // closes message object
			return 0, err
		}
	}
	for _, literal := range []string{`],"tools":`, `,"stream":true,"parallel_tool_calls":false,"tool_choice":"auto"}`} {
		if err := add(len(literal)); err != nil {
			return 0, err
		}
	}
	if err := add(len(toolsJSON)); err != nil {
		return 0, err
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
			return 0, errors.New("request string exceeds byte limit")
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
			return 0, errors.New("request string exceeds byte limit")
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
func validateMessage(m Message) error {
	switch m.Role {
	case System, Developer, User:
		if m.Content == nil || m.ToolCallID != "" || len(m.ToolCalls) != 0 {
			return errors.New("invalid text message fields")
		}
	case Assistant:
		if m.ToolCallID != "" || (m.Content == nil && len(m.ToolCalls) == 0) {
			return errors.New("invalid assistant message fields")
		}
		if len(m.ToolCalls) > 1 {
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
	if !validText(c.ID, maxText) { return nil, errors.New("invalid prior call ID") }
	if _, err := protocol.CanonicalArgumentsDigest(c.Name, c.RawArguments); err != nil { return nil, err }
	wire := struct {
		ID string `json:"id"`
		Type string `json:"type"`
		Function struct {
			Name string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}{ID:c.ID, Type:"function"}
	wire.Function.Name, wire.Function.Arguments = c.Name, string(c.RawArguments)
	return json.Marshal(wire)
}

type ToolCall struct { ID, Name string; RawArguments json.RawMessage; CanonicalArgumentsDigest string }

// CapturedResponse is minted by the bounded parser. Private fields prevent ordinary adapter IPC decoding from constructing it. Parsing and typed mapping alone do not authenticate that the bytes came from OpenRouter; the future trusted broker transport must establish that provenance.
type CapturedResponse struct {
	id, model, provider, systemFingerprint, text, finish, nativeFinish string
	created int64
	usage, raw []byte
	call *ToolCall
}
func (r CapturedResponse) ResponseID() string { return r.id }
func (r CapturedResponse) Model() string { return r.model }
func (r CapturedResponse) Provider() string { return r.provider }
func (r CapturedResponse) SystemFingerprint() string { return r.systemFingerprint }
func (r CapturedResponse) Created() int64 { return r.created }
func (r CapturedResponse) AssistantText() string { return r.text }
func (r CapturedResponse) FinishReason() string { return r.finish }
func (r CapturedResponse) NativeFinishReason() string { return r.nativeFinish }
func (r CapturedResponse) Usage() json.RawMessage { return append(json.RawMessage(nil), r.usage...) }
func (r CapturedResponse) RawResponse() []byte { return append([]byte(nil), r.raw...) }
func (r CapturedResponse) ToolCall() (ToolCall, bool) {
	if r.call == nil { return ToolCall{}, false }
	c := *r.call; c.RawArguments = append(json.RawMessage(nil), c.RawArguments...); return c, true
}

// TrustedCapture maps parser output and broker-assigned issuer, sequence, and generation values. It does not authenticate provider provenance; only trusted broker code may call it after capturing its provider exchange.
func (r CapturedResponse) TrustedCapture(callIssuer, responseIssuer, generation string, sequence uint64) (protocol.TrustedCapture, error) {
	if len(r.raw) == 0 || r.finish != "tool_calls" || r.call == nil { return protocol.TrustedCapture{}, errors.New("response has no completed tool call") }
	if !validText(callIssuer,maxText) || !validText(responseIssuer,maxText) || !validText(generation,128) || sequence == 0 {
		return protocol.TrustedCapture{}, errors.New("invalid broker capture metadata")
	}
	return protocol.TrustedCapture{
		ResponseID:correlation.Identifier{Issuer:responseIssuer,Opaque:r.id},
		ToolCallID:correlation.Identifier{Issuer:callIssuer,Opaque:r.call.ID},
		ToolName:r.call.Name, RawArguments:append(json.RawMessage(nil),r.call.RawArguments...),
		CanonicalizationProfile:protocol.CanonicalizationProfile,
		CanonicalArgumentsDigest:r.call.CanonicalArgumentsDigest, Sequence:sequence, Generation:generation,
	}, nil
}

type assembly struct { id, kind string; name strings.Builder; args []byte }
type stream struct { out CapturedResponse; init, finished, done bool; tool *assembly; events int }

// CaptureSSE reads and bounds the complete body before parsing it, preserving
// exact bytes. This decoder does not authenticate provenance or perform HTTP;
// its caller must be the trusted broker after a real provider exchange. The
// fixed request does not enable stream_options.include_usage, so usage-only
// trailing chunks are outside this profile.
func CaptureSSE(reader io.Reader) (CapturedResponse,error) {
	if reader == nil { return CapturedResponse{}, errors.New("nil response reader") }
	raw,err:=io.ReadAll(io.LimitReader(reader,MaxResponseBytes+1))
	if err!=nil { return CapturedResponse{},fmt.Errorf("read response: %w",err) }
	if len(raw)==0 || len(raw)>MaxResponseBytes { return CapturedResponse{},errors.New("response byte length out of bounds") }
	return decodeSSE(raw)
}

func decodeSSE(raw []byte) (CapturedResponse,error) {
	if !utf8.Valid(raw) { return CapturedResponse{},errors.New("SSE is invalid UTF-8") }
	s:=stream{}; var data []byte; hasData:=false
	for off:=0; off<len(raw); {
		n:=bytes.IndexByte(raw[off:],'\n'); if n<0 { return CapturedResponse{},errors.New("unterminated SSE line") }
		end:=off+n; line:=raw[off:end]; off=end+1
		if len(line)>0 && line[len(line)-1]=='\r' { line=line[:len(line)-1] }
		if bytes.IndexByte(line,'\r')>=0 || len(line)>MaxSSEEventBytes { return CapturedResponse{},errors.New("malformed or oversized SSE line") }
		if len(line)==0 {
			if !hasData { continue }
			s.events++; if s.events>MaxSSEEvents { return CapturedResponse{},errors.New("too many SSE events") }
			if s.done { return CapturedResponse{},errors.New("data after [DONE]") }
			if string(data)=="[DONE]" { s.done=true
			} else {
				if s.finished { return CapturedResponse{},errors.New("chunk after finish_reason") }
				if err:=s.chunk(data); err!=nil { return CapturedResponse{},err }
			}
			data=data[:0]; hasData=false; continue
		}
		if line[0]==':' { continue }
		field,value,ok:=bytes.Cut(line,[]byte{':'}); if !ok { value=nil }
		if !bytes.Equal(field,[]byte("data")) { return CapturedResponse{},errors.New("unsupported SSE field") }
		if len(value)>0 && value[0]==' ' { value=value[1:] }
		add:=len(value); if hasData { add++ }
		if add>MaxSSEEventBytes-len(data) { return CapturedResponse{},errors.New("SSE event exceeds byte limit") }
		if hasData { data=append(data,'\n') }; data=append(data,value...); hasData=true
	}
	if hasData { return CapturedResponse{},errors.New("SSE event is not terminated") }
	if !s.done || !s.init || !s.finished { return CapturedResponse{},errors.New("missing completion, finish_reason, or [DONE]") }
	if err:=s.finalize(); err!=nil { return CapturedResponse{},err }
	s.out.raw=append([]byte(nil),raw...); return s.out,nil
}
func (s *stream) chunk(raw []byte) error {
	if err:=strictJSON(raw); err!=nil { return fmt.Errorf("invalid SSE JSON: %w",err) }
	o,err:=object(raw); if err!=nil { return err }
	if err=keys(o,[]string{"id","model","created","choices"},[]string{"provider","object","system_fingerprint","usage"}); err!=nil { return err }
	id,e1:=str(o,"id"); model,e2:=str(o,"model"); created,e3:=integer(o,"created")
	if e1!=nil || !validText(id,maxText) || e2!=nil || !validText(model,maxText) || e3!=nil || created<0 { return errors.New("invalid response ID, model, or created value") }
	provider,hasProvider,e:=optStr(o,"provider"); if e!=nil || (hasProvider && !validText(provider,maxText)) { return errors.New("invalid provider") }
	if v,ok:=o["object"]; ok { name,e:=strRaw(v); if e!=nil || name!="chat.completion.chunk" { return errors.New("unsupported chunk object") } }
	if v,ok:=o["system_fingerprint"]; ok { fp,e:=strRaw(v); if e!=nil || (s.out.systemFingerprint!="" && fp!=s.out.systemFingerprint) { return errors.New("invalid or changed system fingerprint") }; s.out.systemFingerprint=fp }
	if v,ok:=o["usage"]; ok && !isNull(v) {
		if _,e:=object(v); e!=nil { return errors.New("usage is not an object") }
		if len(s.out.usage)!=0 { return errors.New("repeated usage metadata") }; s.out.usage=append([]byte(nil),v...)
	}
	if !s.init { s.out.id,s.out.model,s.out.created=id,model,created; s.init=true
	} else if id!=s.out.id || model!=s.out.model || created!=s.out.created { return errors.New("response identity changed within stream") }
	if hasProvider { if s.out.provider!="" && s.out.provider!=provider { return errors.New("provider changed within stream") }; s.out.provider=provider }
	choices,e:=array(o["choices"]); if e!=nil || len(choices)!=1 { return errors.New("chunk must have exactly one choice") }
	c,e:=object(choices[0]); if e!=nil { return e }
	if e=keys(c,[]string{"index","delta","finish_reason"},[]string{"logprobs","native_finish_reason"}); e!=nil { return e }
	index,e:=integer(c["index"]); if e!=nil || index!=0 { return errors.New("choice index must be zero") }
	if v,ok:=c["logprobs"]; ok && !isNull(v) { return errors.New("logprobs are unsupported") }
	if v,ok:=c["native_finish_reason"]; ok && !isNull(v) { native,e:=strRaw(v); if e!=nil || (s.out.nativeFinish!="" && native!=s.out.nativeFinish) { return errors.New("invalid or unstable native finish reason") }; s.out.nativeFinish=native }
	if e=s.delta(c["delta"]); e!=nil { return e }
	finishRaw:=c["finish_reason"]; if isNull(finishRaw) { return nil }
	finish,e:=strRaw(finishRaw); if e!=nil || (finish!="stop" && finish!="tool_calls") || s.finished { return errors.New("unsupported or repeated finish reason") }
	s.finished=true; s.out.finish=finish; return nil
}
func (s *stream) delta(raw []byte) error {
	d,e:=object(raw); if e!=nil { return errors.New("choice delta is not an object") }
	if e=keys(d,nil,[]string{"role","content","tool_calls"}); e!=nil { return e }
	if v,ok:=d["role"]; ok { role,e:=strRaw(v); if e!=nil || role!="assistant" { return errors.New("unsupported delta role") } }
	if v,ok:=d["content"]; ok && !isNull(v) {
		text,e:=strRaw(v); if e!=nil || len(text)>MaxAssistantTextBytes-len(s.out.text) { return errors.New("invalid or oversized assistant text") }; s.out.text+=text
	}
	if v,ok:=d["tool_calls"]; ok {
		calls,e:=array(v); if e!=nil || len(calls)>1 { return errors.New("multiple or malformed tool calls") }
		if len(calls)==1 { return s.toolDelta(calls[0]) }
	}
	return nil
}
func (s *stream) toolDelta(raw []byte) error {
	t,e:=object(raw); if e!=nil { return errors.New("tool-call delta is not an object") }
	if e=keys(t,[]string{"index"},[]string{"id","type","function"}); e!=nil { return e }
	index,e:=integer(t["index"]); if e!=nil || index!=0 { return errors.New("tool-call index must be zero") }
	if s.tool==nil { s.tool=&assembly{} }; a:=s.tool
	if v,ok:=t["id"]; ok { id,e:=strRaw(v); if e!=nil || !validText(id,maxText) || (a.id!="" && id!=a.id) { return errors.New("empty or changed tool-call ID") }; a.id=id }
	if v,ok:=t["type"]; ok { kind,e:=strRaw(v); if e!=nil || kind!="function" || (a.kind!="" && a.kind!=kind) { return errors.New("unsupported or changed tool-call type") }; a.kind=kind }
	if v,ok:=t["function"]; ok {
		f,e:=object(v); if e!=nil { return errors.New("tool-call function is not an object") }
		if e=keys(f,nil,[]string{"name","arguments"}); e!=nil { return e }
		if v,ok:=f["name"]; ok { name,e:=strRaw(v); if e!=nil || len(name)>maxText-a.name.Len() { return errors.New("invalid or oversized tool name") }; a.name.WriteString(name) }
		if v,ok:=f["arguments"]; ok { args,e:=strRaw(v); if e!=nil || len(args)>MaxToolArgumentsBytes-len(a.args) { return errors.New("invalid or oversized argument fragment") }; a.args=append(a.args,args...) }
	}
	return nil
}
func (s *stream) finalize() error {
	switch s.out.finish {
	case "stop":
		if s.tool!=nil { return errors.New("stop finish reason conflicts with a tool call") }; return nil
	case "tool_calls":
		if s.tool==nil || s.tool.id=="" || s.tool.kind!="function" || s.tool.name.Len()==0 || len(s.tool.args)==0 { return errors.New("tool call is incomplete") }
		name:=s.tool.name.String(); raw:=append(json.RawMessage(nil),s.tool.args...)
		digest,e:=protocol.CanonicalArgumentsDigest(name,raw); if e!=nil { return fmt.Errorf("tool arguments fail registered schema: %w",e) }
		s.out.call=&ToolCall{ID:s.tool.id,Name:name,RawArguments:raw,CanonicalArgumentsDigest:digest}; return nil
	default: return errors.New("unsupported finish reason")
	}
}

func object(raw []byte) (map[string]json.RawMessage,error) {
	var v map[string]json.RawMessage
	if e:=json.Unmarshal(raw,&v); e!=nil || v==nil { return nil,errors.New("expected JSON object") }; return v,nil
}
func keys(o map[string]json.RawMessage, required, optional []string) error {
	allowed:=map[string]bool{}
	for _,k:=range required { allowed[k]=true; if _,ok:=o[k]; !ok { return fmt.Errorf("missing field %q",k) } }
	for _,k:=range optional { allowed[k]=true }
	for k:=range o { if !allowed[k] { return fmt.Errorf("unsupported field %q",k) } }; return nil
}
func str(o map[string]json.RawMessage,k string)(string,error) { v,ok:=o[k]; if !ok { return "",errors.New("missing string") }; return strRaw(v) }
func optStr(o map[string]json.RawMessage,k string)(string,bool,error) { v,ok:=o[k]; if !ok { return "",false,nil }; s,e:=strRaw(v); return s,true,e }
func strRaw(v []byte)(string,error) { var s string; e:=json.Unmarshal(v,&s); return s,e }
func integer(v []byte)(int64,error) { var n json.Number; d:=json.NewDecoder(bytes.NewReader(v)); d.UseNumber(); if e:=d.Decode(&n); e!=nil { return 0,e }; return strconv.ParseInt(n.String(),10,64) }
func array(v []byte)([]json.RawMessage,error) { var a []json.RawMessage; if e:=json.Unmarshal(v,&a); e!=nil || a==nil { return nil,errors.New("expected JSON array") }; return a,nil }
func isNull(v []byte) bool { return bytes.Equal(bytes.TrimSpace(v),[]byte("null")) }
func validText(s string,limit int) bool {
	if s=="" || len(s)>limit || !utf8.ValidString(s) || strings.TrimSpace(s)!=s { return false }
	for _,r:=range s { if unicode.IsControl(r) { return false } }; return true
}
func strictJSON(raw []byte) error {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return errors.New("empty or invalid UTF-8 JSON")
	}
	if err := rejectSurrogateEscapes(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := scan(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func rejectSurrogateEscapes(raw []byte) error {
	inString := false
	for i := 0; i < len(raw); i++ {
		if !inString {
			if raw[i] == '"' {
				inString = true
			}
			continue
		}
		switch raw[i] {
		case '"':
			inString = false
		case '\\':
			if i+1 >= len(raw) {
				continue
			}
			if raw[i+1] != 'u' {
				i++
				continue
			}
			if i+5 >= len(raw) {
				continue // The JSON decoder reports malformed escapes.
			}
			unit, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
			if err != nil {
				continue
			}
			switch {
			case unit >= 0xd800 && unit <= 0xdbff:
				if i+11 >= len(raw) || raw[i+6] != '\\' || raw[i+7] != 'u' {
					return errors.New("unpaired escaped UTF-16 high surrogate")
				}
				low, err := strconv.ParseUint(string(raw[i+8:i+12]), 16, 16)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return errors.New("unpaired escaped UTF-16 high surrogate")
				}
				i += 11
			case unit >= 0xdc00 && unit <= 0xdfff:
				return errors.New("unpaired escaped UTF-16 low surrogate")
			default:
				i += 5
			}
		}
	}
	return nil
}
func scan(d *json.Decoder,depth int) error {
	t,e:=d.Token(); if e!=nil { return e }
	if n,ok:=t.(json.Number); ok { f,e:=strconv.ParseFloat(string(n),64); if e!=nil || math.IsInf(f,0)||math.IsNaN(f)||(f==0&&math.Signbit(f)) { return errors.New("invalid JSON number") } }
	x,ok:=t.(json.Delim); if !ok { return nil }; if depth>=maxDepth { return errors.New("JSON nesting too deep") }
	switch x {
	case '{':
		seen:=map[string]bool{}
		for d.More() { k,e:=d.Token(); if e!=nil{return e}; key,ok:=k.(string); if !ok||seen[key]{return errors.New("invalid or duplicate JSON key")}; seen[key]=true; if e=scan(d,depth+1);e!=nil{return e} }
		end,e:=d.Token(); if e!=nil||end!=json.Delim('}') { return errors.New("malformed object") }
	case '[':
		for d.More() { if e=scan(d,depth+1);e!=nil{return e} }; end,e:=d.Token(); if e!=nil||end!=json.Delim(']'){return errors.New("malformed array")}
	default:return errors.New("unexpected JSON delimiter")
	}; return nil
}
