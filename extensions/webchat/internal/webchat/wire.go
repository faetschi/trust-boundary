package webchat

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// This file is the gateway's own copy of the product wire contract,
// tbound-product-protocol/v1. The module never imports the supervisor. The
// supervisor generates JSON fixtures for every message, and wire_test.go checks
// that each fixture decodes strictly into these types and re-encodes to the same
// bytes, so a change on either side fails a test. Field order and omitempty tags
// are part of the copy.

const (
	ProductVersion          = "tbound-product-protocol/v1"
	MaxProductRequestBytes  = 64 << 10
	MaxProductResponseBytes = 1 << 20
	// MaxObserveWaitMillis is the protocol's bound on one observe wait.
	MaxObserveWaitMillis = 30000
	// MaxProductEvents bounds the events of one observe response.
	MaxProductEvents = 512
)

// Operations.
const (
	OpCreateSession = "create_session"
	OpSubmitTurn    = "submit_turn"
	OpGetSession    = "get_session"
	OpListSessions  = "list_sessions"
	OpObserve       = "observe"
	OpGetCandidate  = "get_candidate"
	OpCancelTurn    = "cancel_turn"
	OpResolveTurn   = "resolve_turn"
)

// Event kinds.
const (
	EventTurnAdmitted        = "turn_admitted"
	EventTurnLinked          = "turn_linked"
	EventAssistantMessage    = "assistant_message"
	EventToolCall            = "tool_call"
	EventToolResult          = "tool_result"
	EventTurnCancelRequested = "turn_cancel_requested"
	EventTurnAwaitingReview  = "turn_awaiting_review"
	EventTurnResolution      = "turn_resolution_requested"
	EventTurnTerminal        = "turn_terminal"
)

// Response statuses and the two transport-level error codes. Every other code is
// a catalog code.
const (
	StatusOK               = "ok"
	StatusError            = "error"
	CodeProtocolError      = "PROTOCOL_ERROR"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	// CodeBadServiceResponse is the gateway's own code for a response that is not
	// valid protocol. It never comes from the service.
	CodeBadServiceResponse = "BAD_SERVICE_RESPONSE"
)

var errProductFrame = errors.New("product protocol frame is invalid")

type ProductBudget struct {
	HostCalls       int64 `json:"host_calls"`
	Tokens          int64 `json:"tokens"`
	DurationSeconds int64 `json:"duration_seconds"`
}

// ProductRequest is exactly one operation: Op names it and exactly the matching
// payload is set.
type ProductRequest struct {
	Version       string                `json:"version"`
	Op            string                `json:"op"`
	CreateSession *ProductCreateSession `json:"create_session,omitempty"`
	SubmitTurn    *ProductSubmitTurn    `json:"submit_turn,omitempty"`
	GetSession    *ProductSessionRef    `json:"get_session,omitempty"`
	Observe       *ProductObserve       `json:"observe,omitempty"`
	GetCandidate  *ProductTurnRef       `json:"get_candidate,omitempty"`
	CancelTurn    *ProductCancelTurn    `json:"cancel_turn,omitempty"`
	ResolveTurn   *ProductResolveTurn   `json:"resolve_turn,omitempty"`
}

type ProductCreateSession struct {
	ProjectID       string        `json:"project_id"`
	ProviderProfile string        `json:"provider_profile"`
	PublicationMode string        `json:"publication_mode"`
	Budget          ProductBudget `json:"budget"`
	ReadOnly        bool          `json:"read_only,omitempty"`
	RequestKey      string        `json:"request_key"`
}

type ProductSubmitTurn struct {
	SessionID              string `json:"session_id"`
	ExpectedRevision       uint64 `json:"expected_revision"`
	RequestKey             string `json:"request_key"`
	Prompt                 string `json:"prompt"`
	ExpectedBaselineDigest string `json:"expected_baseline_digest,omitempty"`
}

type ProductSessionRef struct {
	SessionID string `json:"session_id"`
}

type ProductTurnRef struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
}

type ProductObserve struct {
	SessionID  string `json:"session_id"`
	Cursor     uint64 `json:"cursor"`
	WaitMillis int64  `json:"wait_ms"`
}

type ProductCancelTurn struct {
	SessionID        string `json:"session_id"`
	TurnID           string `json:"turn_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
}

type ProductResolveTurn struct {
	SessionID        string `json:"session_id"`
	TurnID           string `json:"turn_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
	CandidateDigest  string `json:"candidate_digest"`
	Decision         string `json:"decision"`
}

// ProductResponse carries Status "ok" with the operation's projection, or
// "error" with a stable code.
type ProductResponse struct {
	Version    string            `json:"version"`
	Status     string            `json:"status"`
	Error      *ProductError     `json:"error,omitempty"`
	Replayed   bool              `json:"replayed,omitempty"`
	Session    *ProductSession   `json:"session,omitempty"`
	Sessions   []ProductSession  `json:"sessions,omitempty"`
	Turn       *ProductTurn      `json:"turn,omitempty"`
	Events     []ProductEvent    `json:"events,omitempty"`
	NextCursor uint64            `json:"next_cursor,omitempty"`
	Gap        bool              `json:"gap,omitempty"`
	Candidate  *ProductCandidate `json:"candidate,omitempty"`
}

type ProductError struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// ProductSession is the service's session view. The gateway also carries it, as
// is, in Snapshot.Live, so a client has the revision it must echo back.
type ProductSession struct {
	ID              string        `json:"id"`
	ProjectID       string        `json:"project_id"`
	ProviderProfile string        `json:"provider_profile"`
	PublicationMode string        `json:"publication_mode"`
	ReadOnly        bool          `json:"read_only,omitempty"`
	Revision        uint64        `json:"revision"`
	Blocked         bool          `json:"blocked,omitempty"`
	Budget          ProductBudget `json:"budget"`
	Consumed        ProductBudget `json:"consumed"`
	Reserved        ProductBudget `json:"reserved"`
	Turns           []ProductTurn `json:"turns,omitempty"`
}

type ProductTurn struct {
	ID                   string `json:"id"`
	Ordinal              int    `json:"ordinal"`
	State                string `json:"state"`
	Prompt               string `json:"prompt"`
	Outcome              string `json:"outcome,omitempty"`
	TerminalReason       string `json:"terminal_reason,omitempty"`
	CancelRequested      bool   `json:"cancel_requested,omitempty"`
	Resolution           string `json:"resolution,omitempty"`
	CandidateDigest      string `json:"candidate_digest,omitempty"`
	ReviewTimeoutSeconds int64  `json:"review_timeout_seconds,omitempty"`
}

// ProductEvent is one observation. Seq is its index in the session's event list.
type ProductEvent struct {
	Seq             uint64   `json:"seq"`
	Kind            string   `json:"kind"`
	TurnID          string   `json:"turn_id"`
	Text            string   `json:"text,omitempty"`
	Tool            string   `json:"tool,omitempty"`
	ToolCallID      string   `json:"tool_call_id,omitempty"`
	Verdict         string   `json:"verdict,omitempty"`
	ReasonCode      string   `json:"reason_code,omitempty"`
	Hint            string   `json:"hint,omitempty"`
	Outcome         string   `json:"outcome,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	CandidateDigest string   `json:"candidate_digest,omitempty"`
	Paths           []string `json:"paths,omitempty"`
}

type ProductCandidate struct {
	TurnID          string          `json:"turn_id"`
	CandidateDigest string          `json:"candidate_digest"`
	Changes         []ProductChange `json:"changes"`
}

type ProductChange struct {
	Path      string `json:"path"`
	Op        string `json:"op"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

const maxJSONDepth = 32

// strictDecode decodes one JSON value into v: valid UTF-8, no duplicate object
// keys, bounded nesting, no unknown fields and nothing after the value. It is the
// only decoder for bytes that cross a trust boundary: browser bodies and service
// frames.
func strictDecode(data []byte, v any) error {
	if !utf8.Valid(data) {
		return errors.New("JSON is not valid UTF-8")
	}
	scan := json.NewDecoder(bytes.NewReader(data))
	if err := scanStrict(scan, 0); err != nil {
		return err
	}
	if _, err := scan.Token(); err != io.EOF {
		return errors.New("JSON has trailing content")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

func scanStrict(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	if depth >= maxJSONDepth {
		return errors.New("JSON nesting is too deep")
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, _ := keyToken.(string)
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanStrict(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanStrict(decoder, depth+1); err != nil {
				return err
			}
		}
	}
	_, err = decoder.Token() // the closing delimiter
	return err
}

// writeProductFrame writes v as one 4-byte big-endian length-prefixed JSON frame
// of at most limit bytes.
func writeProductFrame(w io.Writer, v any, limit int) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > limit {
		return fmt.Errorf("%w: frame of %d bytes exceeds %d", errProductFrame, len(data), limit)
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if _, err := w.Write(append(prefix[:], data...)); err != nil {
		return err
	}
	return nil
}

// readProductFrame reads one frame of at most limit bytes into v, strictly.
func readProductFrame(r io.Reader, limit int, v any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || int64(length) > int64(limit) {
		return fmt.Errorf("%w: frame length %d is out of range", errProductFrame, length)
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if err := strictDecode(data, v); err != nil {
		return errors.Join(errProductFrame, err)
	}
	return nil
}
