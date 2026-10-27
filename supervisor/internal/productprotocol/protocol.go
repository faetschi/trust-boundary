// Package productprotocol is the versioned wire contract between the TBound product service and
// its clients (terminal now, web gateway later). It carries requests and bounded projections only:
// it never carries a principal, a credential, a host path, or a raw journal record.
package productprotocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"tbound/supervisor/internal/broker/protocol"
)

const (
	Version          = "tbound-product-protocol/v1"
	MaxRequestBytes  = 64 << 10
	MaxResponseBytes = 1 << 20
	// MaxObserveWaitMillis bounds how long one observe request may wait for new events.
	MaxObserveWaitMillis = 30000
	// MaxEvents bounds the events of one observe response.
	MaxEvents = 512
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

// Event kinds, projected only from durable records.
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

// Response statuses.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Projection and transport error codes. Every other code is a catalog code from productsession.
const (
	CodeProtocolError      = "PROTOCOL_ERROR"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	CodeCandidateTooLarge  = "CANDIDATE_TOO_LARGE"
)

var ErrProtocol = errors.New("product protocol frame is invalid")

type Budget struct {
	HostCalls       int64 `json:"host_calls"`
	Tokens          int64 `json:"tokens"`
	DurationSeconds int64 `json:"duration_seconds"`
}

// Request is exactly one operation: Op names it and exactly the matching payload is set.
type Request struct {
	Version       string         `json:"version"`
	Op            string         `json:"op"`
	CreateSession *CreateSession `json:"create_session,omitempty"`
	SubmitTurn    *SubmitTurn    `json:"submit_turn,omitempty"`
	GetSession    *SessionRef    `json:"get_session,omitempty"`
	Observe       *Observe       `json:"observe,omitempty"`
	GetCandidate  *TurnRef       `json:"get_candidate,omitempty"`
	CancelTurn    *CancelTurn    `json:"cancel_turn,omitempty"`
	ResolveTurn   *ResolveTurn   `json:"resolve_turn,omitempty"`
}

type CreateSession struct {
	ProjectID       string `json:"project_id"`
	ProviderProfile string `json:"provider_profile"`
	PublicationMode string `json:"publication_mode"`
	Budget          Budget `json:"budget"`
	ReadOnly        bool   `json:"read_only,omitempty"`
	RequestKey      string `json:"request_key"`
}

type SubmitTurn struct {
	SessionID              string `json:"session_id"`
	ExpectedRevision       uint64 `json:"expected_revision"`
	RequestKey             string `json:"request_key"`
	Prompt                 string `json:"prompt"`
	ExpectedBaselineDigest string `json:"expected_baseline_digest,omitempty"`
}

type SessionRef struct {
	SessionID string `json:"session_id"`
}

type TurnRef struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
}

type Observe struct {
	SessionID  string `json:"session_id"`
	Cursor     uint64 `json:"cursor"`
	WaitMillis int64  `json:"wait_ms"`
}

type CancelTurn struct {
	SessionID        string `json:"session_id"`
	TurnID           string `json:"turn_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
}

type ResolveTurn struct {
	SessionID        string `json:"session_id"`
	TurnID           string `json:"turn_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
	CandidateDigest  string `json:"candidate_digest"`
	Decision         string `json:"decision"`
}

// Response carries Status "ok" with the operation's projection, or "error" with a stable code.
type Response struct {
	Version    string         `json:"version"`
	Status     string         `json:"status"`
	Error      *Error         `json:"error,omitempty"`
	Replayed   bool           `json:"replayed,omitempty"`
	Session    *SessionView   `json:"session,omitempty"`
	Sessions   []SessionView  `json:"sessions,omitempty"`
	Turn       *TurnView      `json:"turn,omitempty"`
	Events     []Event        `json:"events,omitempty"`
	NextCursor uint64         `json:"next_cursor,omitempty"`
	Gap        bool           `json:"gap,omitempty"`
	Candidate  *CandidateView `json:"candidate,omitempty"`
}

// Error codes are the catalog's stable codes and the projection/transport codes above.
type Error struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

type SessionView struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	ProviderProfile string     `json:"provider_profile"`
	PublicationMode string     `json:"publication_mode"`
	ReadOnly        bool       `json:"read_only,omitempty"`
	Revision        uint64     `json:"revision"`
	Blocked         bool       `json:"blocked,omitempty"`
	Budget          Budget     `json:"budget"`
	Consumed        Budget     `json:"consumed"`
	Reserved        Budget     `json:"reserved"`
	TotalTurns      int        `json:"total_turns"`
	TurnsTruncated  bool       `json:"turns_truncated,omitempty"`
	Turns           []TurnView `json:"turns,omitempty"`
}

type TurnView struct {
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

// Event is one observation. Seq is its index in the session's deterministic event list.
type Event struct {
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

type CandidateView struct {
	TurnID          string            `json:"turn_id"`
	CandidateDigest string            `json:"candidate_digest"`
	Changes         []CandidateChange `json:"changes"`
}

type CandidateChange struct {
	Path      string `json:"path"`
	Op        string `json:"op"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Validate checks the request's shape: version, a known operation, exactly its payload, and the
// observe wait bound. The catalog validates field contents.
func (r Request) Validate() error {
	if r.Version != Version {
		return fmt.Errorf("%w: unsupported version", ErrProtocol)
	}
	set := map[string]bool{
		OpCreateSession: r.CreateSession != nil, OpSubmitTurn: r.SubmitTurn != nil,
		OpGetSession: r.GetSession != nil, OpObserve: r.Observe != nil, OpGetCandidate: r.GetCandidate != nil,
		OpCancelTurn: r.CancelTurn != nil, OpResolveTurn: r.ResolveTurn != nil,
	}
	count := 0
	for _, present := range set {
		if present {
			count++
		}
	}
	switch {
	case r.Op == OpListSessions:
		if count != 0 {
			return fmt.Errorf("%w: list_sessions takes no payload", ErrProtocol)
		}
	case !set[r.Op] || count != 1:
		return fmt.Errorf("%w: operation %q needs exactly its own payload", ErrProtocol, r.Op)
	}
	if r.Observe != nil && (r.Observe.WaitMillis < 0 || r.Observe.WaitMillis > MaxObserveWaitMillis) {
		return fmt.Errorf("%w: observe wait_ms is out of range", ErrProtocol)
	}
	return nil
}

// WriteFrame writes v as one length-prefixed JSON frame of at most limit bytes.
func WriteFrame(w io.Writer, v any, limit int) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > limit {
		return fmt.Errorf("%w: frame of %d bytes exceeds %d", ErrProtocol, len(data), limit)
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// ReadFrame reads one length-prefixed frame of at most limit bytes into v: strict JSON, no
// duplicate keys, no unknown fields, nothing after the value.
func ReadFrame(r io.Reader, limit int, v any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || int64(length) > int64(limit) {
		return fmt.Errorf("%w: frame length %d is out of range", ErrProtocol, length)
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return errors.Join(ErrProtocol, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return errors.Join(ErrProtocol, err)
	}
	if decoder.More() {
		return fmt.Errorf("%w: trailing data", ErrProtocol)
	}
	return nil
}
