// Package ipc provides bounded, session-bound proposal/result framing. The
// binding token is an additional frame-binding check, not peer authentication.
package ipc

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"tbound/supervisor/internal/broker/protocol"
)

const (
	WireVersion           = "tbound-ipc/v1"
	PrefixBytes           = 4
	MaxMessageBytes       = protocol.MaxFrameBytes
	MaxFrameBytes         = PrefixBytes + MaxMessageBytes
	MaxFramesPerDirection = protocol.MaxProposalsPerStream
	MaxStreamBytes        = protocol.MaxProposalBytesPerStream
	BindingTokenBytes     = 32
	BindingTokenHexBytes  = BindingTokenBytes * 2
)

var (
	ErrClosed          = errors.New("IPC endpoint is closed")
	ErrMalformedFrame  = errors.New("malformed IPC frame")
	ErrFrameTooLarge   = errors.New("IPC frame exceeds configured limit")
	ErrBindingMismatch = errors.New("IPC session binding token mismatch")
	ErrReplay          = errors.New("IPC sequence is duplicate, stale, or out of order")
	ErrUnexpectedKind  = errors.New("IPC message kind is not valid in this direction")
	ErrInvalidToken    = errors.New("IPC binding token must encode 32 bytes as lowercase hex")
	ErrUnsupportedKind = errors.New("unknown IPC message kind")
)

type Kind string

const (
	ProposalKind Kind = "proposal"
	ResultKind   Kind = "result"
)

type wireFrame struct {
	Version      string          `json:"version"`
	BindingToken string          `json:"binding_token"`
	Sequence     uint64          `json:"sequence"`
	Kind         Kind            `json:"kind"`
	Payload      json.RawMessage `json:"payload"`
}

type endpoint struct {
	conn    net.Conn
	token   string
	mu      sync.Mutex
	readMu  sync.Mutex
	writeMu sync.Mutex

	closed      bool
	failed      bool
	readNext    uint64
	writeNext   uint64
	readFrames  uint64
	writeFrames uint64
	readBytes   uint64
	writeBytes  uint64
}

// Client is the adapter-facing half of a proposal/result stream.
type Client struct{ endpoint *endpoint }

// Server is the supervisor-facing half of a proposal/result stream.
type Server struct{ endpoint *endpoint }

// NewBindingToken returns a fresh, session-local 256-bit token.
func NewBindingToken() (string, error) {
	bytes := make([]byte, BindingTokenBytes)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate IPC binding token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func NewClient(conn net.Conn, bindingToken string) (*Client, error) {
	e, err := newEndpoint(conn, bindingToken)
	if err != nil {
		return nil, err
	}
	return &Client{endpoint: e}, nil
}

func NewServer(conn net.Conn, bindingToken string) (*Server, error) {
	e, err := newEndpoint(conn, bindingToken)
	if err != nil {
		return nil, err
	}
	return &Server{endpoint: e}, nil
}

// NewPipe returns a connected client/server pair for portable tests and
// in-process use. Each direction has its own monotonically increasing sequence.
func NewPipe(bindingToken string) (*Client, *Server, error) {
	if err := validateBindingToken(bindingToken); err != nil {
		return nil, nil, err
	}
	clientConn, serverConn := net.Pipe()
	client, err := NewClient(clientConn, bindingToken)
	if err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		return nil, nil, err
	}
	server, err := NewServer(serverConn, bindingToken)
	if err != nil {
		_ = client.Close()
		_ = serverConn.Close()
		return nil, nil, err
	}
	return client, server, nil
}

func newEndpoint(conn net.Conn, bindingToken string) (*endpoint, error) {
	if conn == nil {
		return nil, errors.New("IPC connection is required")
	}
	if err := validateBindingToken(bindingToken); err != nil {
		return nil, err
	}
	return &endpoint{conn: conn, token: bindingToken, readNext: 1, writeNext: 1}, nil
}

func validateBindingToken(token string) error {
	if len(token) != BindingTokenHexBytes {
		return ErrInvalidToken
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != BindingTokenBytes || hex.EncodeToString(decoded) != token {
		return ErrInvalidToken
	}
	return nil
}

func (c *Client) SendProposal(proposal protocol.Proposal) error {
	if c == nil || c.endpoint == nil {
		return ErrClosed
	}
	payload, err := protocol.MarshalProposal(proposal)
	if err != nil {
		return c.endpoint.fail(fmt.Errorf("%w: invalid proposal: %v", ErrMalformedFrame, err))
	}
	return c.endpoint.write(ProposalKind, payload)
}

func (c *Client) ReceiveResult() (protocol.Result, error) {
	if c == nil || c.endpoint == nil {
		return protocol.Result{}, ErrClosed
	}
	payload, err := c.endpoint.read(ResultKind)
	if err != nil {
		return protocol.Result{}, err
	}
	result, err := protocol.DecodeResult(payload)
	if err != nil {
		return protocol.Result{}, c.endpoint.fail(fmt.Errorf("%w: invalid result payload: %v", ErrMalformedFrame, err))
	}
	return result, nil
}

func (c *Client) Close() error {
	if c == nil || c.endpoint == nil {
		return nil
	}
	return c.endpoint.close()
}

func (s *Server) ReceiveProposal() (protocol.Proposal, error) {
	if s == nil || s.endpoint == nil {
		return protocol.Proposal{}, ErrClosed
	}
	payload, err := s.endpoint.read(ProposalKind)
	if err != nil {
		return protocol.Proposal{}, err
	}
	proposal, err := protocol.DecodeProposal(payload)
	if err != nil {
		return protocol.Proposal{}, s.endpoint.fail(fmt.Errorf("%w: invalid proposal payload: %v", ErrMalformedFrame, err))
	}
	return proposal, nil
}

func (s *Server) SendResult(result protocol.Result) error {
	if s == nil || s.endpoint == nil {
		return ErrClosed
	}
	payload, err := protocol.MarshalResult(result)
	if err != nil {
		return s.endpoint.fail(fmt.Errorf("%w: invalid result: %v", ErrMalformedFrame, err))
	}
	return s.endpoint.write(ResultKind, payload)
}

func (s *Server) Close() error {
	if s == nil || s.endpoint == nil {
		return nil
	}
	return s.endpoint.close()
}

func (e *endpoint) write(kind Kind, payload []byte) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if err := e.checkOpen(); err != nil {
		return err
	}
	if kind != ProposalKind && kind != ResultKind {
		return e.fail(ErrUnsupportedKind)
	}
	if e.writeFrames >= MaxFramesPerDirection || uint64(len(payload)) > MaxMessageBytes || e.writeBytes > MaxStreamBytes-uint64(len(payload)) {
		return e.fail(ErrFrameTooLarge)
	}
	frame := wireFrame{
		Version: WireVersion, BindingToken: e.token,
		Sequence: e.writeNext, Kind: kind, Payload: payload,
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return e.fail(fmt.Errorf("%w: encode frame: %v", ErrMalformedFrame, err))
	}
	if len(encoded) == 0 || len(encoded) > MaxMessageBytes {
		return e.fail(ErrFrameTooLarge)
	}
	var prefix [PrefixBytes]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(encoded)))
	if err := writeFull(e.conn, prefix[:]); err != nil {
		return e.fail(fmt.Errorf("write IPC length prefix: %w", err))
	}
	if err := writeFull(e.conn, encoded); err != nil {
		return e.fail(fmt.Errorf("write IPC frame: %w", err))
	}
	e.mu.Lock()
	e.writeFrames++
	e.writeBytes += uint64(len(encoded))
	e.writeNext++
	e.mu.Unlock()
	return nil
}

func (e *endpoint) read(want Kind) ([]byte, error) {
	e.readMu.Lock()
	defer e.readMu.Unlock()
	if err := e.checkOpen(); err != nil {
		return nil, err
	}
	var prefix [PrefixBytes]byte
	n, err := io.ReadFull(e.conn, prefix[:])
	if err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			_ = e.close()
			return nil, ErrClosed
		}
		return nil, e.fail(fmt.Errorf("%w: truncated length prefix: %v", ErrMalformedFrame, err))
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > MaxMessageBytes {
		return nil, e.fail(ErrFrameTooLarge)
	}
	e.mu.Lock()
	if e.readFrames >= MaxFramesPerDirection || e.readBytes > MaxStreamBytes-uint64(length) {
		e.mu.Unlock()
		return nil, e.fail(ErrFrameTooLarge)
	}
	e.mu.Unlock()
	encoded := make([]byte, int(length))
	if _, err := io.ReadFull(e.conn, encoded); err != nil {
		return nil, e.fail(fmt.Errorf("%w: truncated frame payload: %v", ErrMalformedFrame, err))
	}
	frame, err := decodeFrame(encoded)
	if err != nil {
		return nil, e.fail(err)
	}
	if subtle.ConstantTimeCompare([]byte(frame.BindingToken), []byte(e.token)) != 1 {
		return nil, e.fail(ErrBindingMismatch)
	}
	if frame.Sequence != e.readNext {
		return nil, e.fail(fmt.Errorf("%w: got %d, want %d", ErrReplay, frame.Sequence, e.readNext))
	}
	if frame.Kind != ProposalKind && frame.Kind != ResultKind {
		return nil, e.fail(fmt.Errorf("%w: %q", ErrUnsupportedKind, frame.Kind))
	}
	if frame.Kind != want {
		return nil, e.fail(ErrUnexpectedKind)
	}
	if frame.Payload == nil || len(frame.Payload) == 0 {
		return nil, e.fail(fmt.Errorf("%w: missing payload", ErrMalformedFrame))
	}
	e.mu.Lock()
	e.readFrames++
	e.readBytes += uint64(length)
	e.readNext++
	e.mu.Unlock()
	return append([]byte(nil), frame.Payload...), nil
}

func decodeFrame(encoded []byte) (wireFrame, error) {
	var frame wireFrame
	if err := protocol.ValidateStrictJSON(encoded); err != nil {
		return frame, fmt.Errorf("%w: %v", ErrMalformedFrame, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return frame, fmt.Errorf("%w: %v", ErrMalformedFrame, err)
	}
	if len(fields) != 5 {
		return frame, fmt.Errorf("%w: envelope must contain exactly five fields", ErrMalformedFrame)
	}
	for _, key := range []string{"version", "binding_token", "sequence", "kind", "payload"} {
		if _, ok := fields[key]; !ok {
			return frame, fmt.Errorf("%w: missing field %q", ErrMalformedFrame, key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame); err != nil {
		return frame, fmt.Errorf("%w: %v", ErrMalformedFrame, err)
	}
	if frame.Version != WireVersion || frame.Sequence == 0 || frame.BindingToken == "" || !jsonObject(frame.Payload) {
		return frame, fmt.Errorf("%w: invalid envelope value", ErrMalformedFrame)
	}
	return frame, nil
}

func jsonObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 1 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func (e *endpoint) checkOpen() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.failed {
		return ErrClosed
	}
	return nil
}

func (e *endpoint) fail(err error) error {
	e.mu.Lock()
	e.failed = true
	e.closed = true
	e.mu.Unlock()
	_ = e.conn.Close()
	return err
}

func (e *endpoint) close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.mu.Unlock()
	return e.conn.Close()
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if n < 0 || n > len(data) {
			return fmt.Errorf("invalid write count %d", n)
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
