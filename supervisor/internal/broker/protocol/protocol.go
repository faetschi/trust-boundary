// Package protocol binds Pi's untrusted proxy-tool requests to calls captured
// by a trusted broker. It is a protocol boundary only; it does not intercept
// or authenticate a provider exchange.
package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"tbound/supervisor/internal/broker/correlation"
)

const (
	// ProposalSchemaVersion matches the JSON emitted by the Pi proxy tools.
	ProposalSchemaVersion = "tbound-proposal/v1"
	// CanonicalizationProfile pins RFC 8785 argument digest rules at this boundary.
	CanonicalizationProfile   = "tbound-args-jcs-rfc8785/v1"
	MaxFrameBytes             = 1 << 20
	MaxCapturesPerStream      = 1024
	MaxProposalsPerStream     = 1024
	MaxProposalBytesPerStream = 16 << 20
	maxIdentifierBytes        = 256
	maxGenerationBytes        = 128
	maxJSONDepth              = 64
)

const ReasonResourceLimit correlation.ReasonCode = "protocol_resource_limit"

// TrustedCapture is supplied only by broker code after it has captured a
// provider response. Pi request fields cannot populate these broker facts.
type TrustedCapture struct {
	ResponseID              correlation.Identifier
	ToolCallID              correlation.Identifier
	ToolName                string
	RawArguments            json.RawMessage
	CanonicalizationProfile string
	// CanonicalArgumentsDigest is an asserted broker digest. Capture recomputes
	// it from RawArguments and rejects a mismatch; digest-only evidence is invalid.
	CanonicalArgumentsDigest string
	Sequence                 uint64
	Generation               string
}

// proposalRequest is deliberately the small, actual Pi-side wire shape. Broker
// response IDs, ID issuers, digests, sequence, and generation are unknown fields.
type proposalRequest struct {
	SchemaVersion string          `json:"schema_version"`
	ToolCallID    string          `json:"tool_call_id"`
	Tool          string          `json:"tool"`
	Arguments     json.RawMessage `json:"arguments"`
}

// Proposal is the bounded adapter-facing request carried over session IPC.
// Its fields contain no broker-owned response identity, sequence, or digest.
type Proposal struct {
	SchemaVersion string          `json:"schema_version"`
	ToolCallID    string          `json:"tool_call_id"`
	Tool          string          `json:"tool"`
	Arguments     json.RawMessage `json:"arguments"`
}

// Result is the bounded supervisor reply. Correlation identifiers are copied
// from accepted broker state when available; ToolCallID is never authority.
type Result struct {
	SchemaVersion            string                  `json:"schema_version"`
	ResponseID               *correlation.Identifier `json:"response_id,omitempty"`
	ToolCallID               string                  `json:"tool_call_id"`
	Tool                     string                  `json:"tool"`
	Sequence                 uint64                  `json:"sequence"`
	CanonicalArgumentsDigest string                  `json:"canonical_arguments_digest,omitempty"`
	Verdict                  string                  `json:"verdict"`
	ReasonCode               string                  `json:"reason_code"`
	PolicyDigest             string                  `json:"policy_digest"`
	Output                   json.RawMessage         `json:"output,omitempty"`
}

const ResultSchemaVersion = "tbound-result/v1"

// Stream serializes trusted captures and untrusted Pi proposals. Its mutex
// keeps lookup, digest comparison, and one-time consumption in a single order.
type Stream struct {
	mu             sync.Mutex
	correlator     *correlation.Correlator
	callIssuer     string
	responseIssuer string
	captures       map[string]correlation.BrokerCall
	captureCount   int
	proposalCount  int
	proposalBytes  uint64
	closed         bool
}

// NewStream binds Pi's opaque call ID and trusted response IDs to registered
// provider namespaces. Neither issuer is taken from a Pi proposal.
func NewStream(callIssuer, responseIssuer string) (*Stream, error) {
	if !validText(callIssuer, maxIdentifierBytes) || !validText(responseIssuer, maxIdentifierBytes) {
		return nil, errors.New("invalid registered provider identifier issuer")
	}
	return &Stream{
		correlator:     correlation.New(),
		callIssuer:     callIssuer,
		responseIssuer: responseIssuer,
		captures:       make(map[string]correlation.BrokerCall),
	}, nil
}

// Capture adds a trusted broker call. It recomputes the digest from the raw
// broker-captured arguments and checks the asserted profile/digest. Every
// invalid capture closes this stream.
func (s *Stream) Capture(capture TrustedCapture) correlation.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return correlation.Decision{Phase: correlation.PhaseCapture, ReasonCode: correlation.ReasonSessionClosed, StreamClosed: true}
	}
	if s.captureCount >= MaxCapturesPerStream {
		s.closed = true
		return correlation.Decision{Phase: correlation.PhaseCapture, ReasonCode: ReasonResourceLimit, StreamClosed: true}
	}
	s.captureCount++
	call := correlation.BrokerCall{
		ResponseID: capture.ResponseID,
		ToolCallID: capture.ToolCallID,
		ToolName:   capture.ToolName,
		Sequence:   capture.Sequence,
		Generation: capture.Generation,
	}
	if capture.CanonicalizationProfile != CanonicalizationProfile ||
		capture.ToolCallID.Issuer != s.callIssuer ||
		capture.ResponseID.Issuer != s.responseIssuer ||
		!validIdentifier(capture.ResponseID) || !validIdentifier(capture.ToolCallID) ||
		!validDigest(capture.CanonicalArgumentsDigest) ||
		strings.TrimSpace(capture.ToolName) != capture.ToolName ||
		!validText(capture.Generation, maxGenerationBytes) {
		s.closed = true
		copy := call
		return correlation.Decision{
			Phase: correlation.PhaseCapture, Accepted: false,
			ReasonCode: correlation.ReasonMalformedCapture, StreamClosed: true,
			BrokerCapture: &copy,
		}
	}
	digest, err := CanonicalArgumentsDigest(capture.ToolName, capture.RawArguments)
	if err != nil || digest != capture.CanonicalArgumentsDigest {
		s.closed = true
		copy := call
		return correlation.Decision{
			Phase: correlation.PhaseCapture, Accepted: false,
			ReasonCode: correlation.ReasonMalformedCapture, StreamClosed: true,
			BrokerCapture: &copy,
		}
	}
	call.CanonicalArgumentsDigest = digest

	decision := s.correlator.Capture(call)
	if decision.StreamClosed {
		s.closed = true
		return decision
	}
	s.captures[capture.ToolCallID.Opaque] = call
	return decision
}

// Propose decodes one untrusted Pi request, computes its argument digest, and
// fills broker-owned correlation fields only from a previously accepted capture.
// A malformed, unknown, mismatched, replayed, or out-of-order request closes the
// stream and cannot produce an effect authority.
func (s *Stream) Propose(raw []byte) correlation.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return correlation.Decision{Phase: correlation.PhaseProposal, ReasonCode: correlation.ReasonSessionClosed, StreamClosed: true}
	}
	if s.proposalCount >= MaxProposalsPerStream || uint64(len(raw)) > uint64(MaxProposalBytesPerStream)-s.proposalBytes {
		return s.rejectProposal(ReasonResourceLimit, nil)
	}
	s.proposalCount++
	s.proposalBytes += uint64(len(raw))
	request, err := decodeProposal(raw)
	if err != nil {
		return s.rejectProposal(correlation.ReasonMalformedProposal, nil)
	}
	if !validText(request.ToolCallID, maxIdentifierBytes) {
		return s.rejectProposal(correlation.ReasonMalformedProposal, nil)
	}
	if !registeredTool(request.Tool) {
		return s.rejectProposal(correlation.ReasonUnregisteredTool, nil)
	}
	digest, err := CanonicalArgumentsDigest(request.Tool, request.Arguments)
	if err != nil {
		return s.rejectProposal(correlation.ReasonMalformedProposal, nil)
	}

	call, exists := s.captures[request.ToolCallID]
	if !exists {
		proposal := correlation.Proposal{
			ToolCallID:               correlation.Identifier{Issuer: s.callIssuer, Opaque: request.ToolCallID},
			ToolName:                 request.Tool,
			CanonicalArgumentsDigest: digest,
		}
		return s.rejectProposal(correlation.ReasonUnknownCall, &proposal)
	}
	proposal := correlation.Proposal{
		ResponseID:               call.ResponseID,
		ToolCallID:               call.ToolCallID,
		ToolName:                 request.Tool,
		CanonicalArgumentsDigest: digest,
		Sequence:                 call.Sequence,
		Generation:               call.Generation,
	}
	decision := s.correlator.Validate(proposal)
	if decision.StreamClosed {
		s.closed = true
	}
	return decision
}

func (s *Stream) rejectProposal(reason correlation.ReasonCode, proposal *correlation.Proposal) correlation.Decision {
	s.closed = true
	return correlation.Decision{
		Phase:        correlation.PhaseProposal,
		Accepted:     false,
		ReasonCode:   reason,
		StreamClosed: true,
		Proposal:     proposal,
	}
}

func decodeProposal(raw []byte) (proposalRequest, error) {
	var request proposalRequest
	if len(raw) == 0 || len(raw) > MaxFrameBytes {
		return request, errors.New("proposal frame has invalid size")
	}
	if err := checkStrictJSON(raw); err != nil {
		return request, err
	}
	if err := checkExactProposalKeys(raw); err != nil {
		return request, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if request.SchemaVersion != ProposalSchemaVersion || request.ToolCallID == "" || request.Tool == "" || len(request.Arguments) == 0 {
		return request, errors.New("proposal is missing a required field or has the wrong schema version")
	}
	arguments := bytes.TrimSpace(request.Arguments)
	if len(arguments) == 0 || arguments[0] != '{' {
		return request, errors.New("tool arguments must be an object")
	}
	return request, nil
}

// DecodeProposal applies the same strict, exact-shape validation used by the
// broker correlation stream and returns a defensive copy of the arguments.
func DecodeProposal(raw []byte) (Proposal, error) {
	request, err := decodeProposal(raw)
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{
		SchemaVersion: request.SchemaVersion,
		ToolCallID:    request.ToolCallID,
		Tool:          request.Tool,
		Arguments:     append(json.RawMessage(nil), request.Arguments...),
	}, nil
}

// MarshalProposal encodes a typed proposal after validating its exact wire
// shape. Canonicalization and broker correlation still happen in Stream.Propose.
func MarshalProposal(proposal Proposal) ([]byte, error) {
	if proposal.SchemaVersion != ProposalSchemaVersion ||
		!validText(proposal.ToolCallID, maxIdentifierBytes) ||
		!validText(proposal.Tool, maxIdentifierBytes) || len(proposal.Arguments) == 0 || len(proposal.Arguments) > MaxFrameBytes {
		return nil, errors.New("proposal fields exceed the protocol bounds")
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return nil, err
	}
	if _, err := DecodeProposal(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

// ValidateStrictJSON exposes the protocol's duplicate-key, UTF-8, number, and
// nesting checks to the session IPC framing layer. It does not validate a
// particular message schema.
func ValidateStrictJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxFrameBytes {
		return errors.New("JSON message has invalid size")
	}
	return checkStrictJSON(raw)
}

// DecodeResult validates a supervisor result before it is released to the
// adapter. Unknown fields and duplicate JSON keys are rejected.
func DecodeResult(raw []byte) (Result, error) {
	var result Result
	if err := ValidateStrictJSON(raw); err != nil {
		return result, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return result, err
	}
	allowed := map[string]bool{
		"schema_version": true, "response_id": true, "tool_call_id": true,
		"tool": true, "sequence": true, "canonical_arguments_digest": true,
		"verdict": true, "reason_code": true, "policy_digest": true, "output": true,
	}
	for key := range object {
		if !allowed[key] {
			return result, fmt.Errorf("unknown result field %q", key)
		}
	}
	for _, key := range []string{"schema_version", "tool_call_id", "tool", "sequence", "verdict", "reason_code", "policy_digest"} {
		if _, exists := object[key]; !exists {
			return result, fmt.Errorf("result is missing required field %q", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Result{}, err
	}
	if result.SchemaVersion != ResultSchemaVersion ||
		!validText(result.ToolCallID, maxIdentifierBytes) ||
		!validText(result.Tool, maxIdentifierBytes) ||
		(result.Verdict != "ALLOW" && result.Verdict != "DENY") ||
		!validText(result.ReasonCode, maxIdentifierBytes) ||
		!validText(result.PolicyDigest, maxIdentifierBytes) {
		return Result{}, errors.New("result has invalid required values")
	}
	if result.ResponseID != nil && !validIdentifier(*result.ResponseID) {
		return Result{}, errors.New("result has invalid response identity")
	}
	if result.CanonicalArgumentsDigest != "" && !validDigest(result.CanonicalArgumentsDigest) {
		return Result{}, errors.New("result has invalid canonical-arguments digest")
	}
	if result.Verdict == "ALLOW" && (result.ResponseID == nil || result.Sequence == 0 ||
		result.CanonicalArgumentsDigest == "" || !registeredTool(result.Tool) || result.ReasonCode != "policy_rule_allow") {
		return Result{}, errors.New("allow result is not bound to a matched registered proposal")
	}
	if result.Verdict == "DENY" && len(result.Output) != 0 {
		return Result{}, errors.New("denial result may not contain executor output")
	}
	if len(result.Output) > 0 {
		if err := ValidateStrictJSON(result.Output); err != nil {
			return Result{}, fmt.Errorf("result output: %w", err)
		}
	}
	result.Output = append(json.RawMessage(nil), result.Output...)
	return result, nil
}

// MarshalResult validates and encodes a result using the same schema accepted
// by DecodeResult.
func MarshalResult(result Result) ([]byte, error) {
	if len(result.ToolCallID) > maxIdentifierBytes || len(result.Tool) > maxIdentifierBytes ||
		len(result.ReasonCode) > maxIdentifierBytes || len(result.PolicyDigest) > maxIdentifierBytes ||
		len(result.CanonicalArgumentsDigest) > len(CanonicalizationProfile)+len(":sha256:")+sha256.Size*2 ||
		len(result.Output) > MaxFrameBytes {
		return nil, errors.New("result fields exceed the protocol bounds")
	}
	if result.ResponseID != nil && (len(result.ResponseID.Issuer) > maxIdentifierBytes || len(result.ResponseID.Opaque) > maxIdentifierBytes) {
		return nil, errors.New("result response identity exceeds the protocol bounds")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err := DecodeResult(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func checkExactProposalKeys(raw []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	if len(object) != 4 {
		return errors.New("proposal must contain exactly four top-level fields")
	}
	for _, key := range []string{"schema_version", "tool_call_id", "tool", "arguments"} {
		if _, exists := object[key]; !exists {
			return fmt.Errorf("proposal is missing exact field name %q", key)
		}
	}
	return nil
}

func checkStrictJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("JSON is not valid UTF-8")
	}
	if err := rejectSurrogateEscapes(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("JSON has trailing content")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if number, ok := token.(json.Number); ok {
		value, err := strconv.ParseFloat(string(number), 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return fmt.Errorf("JSON number is not a finite IEEE-754 binary64 value: %q", number)
		}
		// RFC Editor verified Erratum 7920 recommends rejecting negative zero
		// before canonicalization because JCS serializes it as ordinary zero.
		if value == 0 && math.Signbit(value) {
			return errors.New("negative zero is rejected under verified RFC 8785 erratum 7920")
		}
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	if depth >= maxJSONDepth {
		return errors.New("JSON nesting exceeds the protocol limit")
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("malformed JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

// Escaped UTF-16 surrogate pairs are accepted as Unicode scalar values, while
// unpaired high or low surrogates are rejected before encoding/json can repair
// malformed strings.
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
				continue // The JSON decoder reports the malformed escape.
			}
			unit, ok := escapedCodeUnit(raw[i+2 : i+6])
			if !ok {
				continue // The JSON decoder reports the malformed hex digits.
			}
			switch {
			case unit >= 0xd800 && unit <= 0xdbff:
				if i+11 >= len(raw) || raw[i+6] != '\\' || raw[i+7] != 'u' {
					return errors.New("unpaired escaped UTF-16 high surrogate")
				}
				low, ok := escapedCodeUnit(raw[i+8 : i+12])
				if !ok || low < 0xdc00 || low > 0xdfff {
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

func escapedCodeUnit(raw []byte) (uint16, bool) {
	decoded, err := hex.DecodeString(string(raw))
	if err != nil || len(decoded) != 2 {
		return 0, false
	}
	return uint16(decoded[0])<<8 | uint16(decoded[1]), true
}

func validIdentifier(id correlation.Identifier) bool {
	return validText(id.Issuer, maxIdentifierBytes) && validText(id.Opaque, maxIdentifierBytes)
}

func validText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	const prefix = CanonicalizationProfile + ":sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	encoded := value[len(prefix):]
	if strings.ToLower(encoded) != encoded {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func registeredTool(tool string) bool {
	switch tool {
	case "read", "write", "edit", "bash":
		return true
	default:
		return false
	}
}
