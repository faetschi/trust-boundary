package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"tbound/supervisor/internal/productprotocol"
)

const (
	chatCallTimeout    = 30 * time.Second
	chatObserveMargin  = 10 * time.Second
	chatDefaultAttempt = 3
	chatDefaultBackoff = 200 * time.Millisecond
)

// chatServiceError is an error response: a stable code and a bounded detail.
type chatServiceError struct{ Code, Detail string }

func (e *chatServiceError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// chatTransportError means no complete response arrived. After a connection was made, a mutating
// operation may still have been applied, so its outcome is unknown.
type chatTransportError struct {
	err            error
	sent           bool // the connection was made, so the service may have read the request
	unknownOutcome bool
}

func (e *chatTransportError) Error() string { return "service connection failed: " + e.err.Error() }
func (e *chatTransportError) Unwrap() error { return e.err }

// chatClient speaks the product protocol: one request per connection, one response frame back.
type chatClient struct {
	socket   string
	attempts int
	backoff  time.Duration
}

var chatMutating = map[string]bool{
	productprotocol.OpCreateSession: true, productprotocol.OpSubmitTurn: true,
	productprotocol.OpCancelTurn: true, productprotocol.OpResolveTurn: true,
}

// call sends req, retrying a transport failure with the identical request (so the same request
// key). A service error is final. An error response is returned as *chatServiceError.
func (c *chatClient) call(ctx context.Context, req productprotocol.Request, timeout time.Duration) (*productprotocol.Response, error) {
	req.Version = productprotocol.Version
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < max(c.attempts, 1); attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(c.backoff * time.Duration(attempt)):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		resp, err := c.roundTrip(ctx, req, timeout)
		var transport *chatTransportError
		if !errors.As(err, &transport) {
			return resp, err
		}
		transport.unknownOutcome = chatMutating[req.Op] && transport.sent
		last = transport
	}
	return nil, last
}

func (c *chatClient) roundTrip(ctx context.Context, req productprotocol.Request, timeout time.Duration) (*productprotocol.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connected := false // set once the dial succeeded
	// A cancelled context closes the connection, so I/O errors after it are not transport failures.
	fail := func(err error) (*productprotocol.Response, error) {
		if parent := ctx.Err(); errors.Is(parent, context.Canceled) {
			return nil, parent
		}
		if errors.Is(err, productprotocol.ErrProtocol) {
			return nil, &chatServiceError{Code: "PROTOCOL_ERROR", Detail: err.Error()}
		}
		return nil, &chatTransportError{err: err, sent: connected}
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return fail(err)
	}
	connected = true
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fail(err)
	}
	if err := productprotocol.WriteFrame(conn, req, productprotocol.MaxRequestBytes); err != nil {
		return fail(err)
	}
	var resp productprotocol.Response
	if err := productprotocol.ReadFrame(conn, productprotocol.MaxResponseBytes, &resp); err != nil {
		return fail(err)
	}
	switch {
	case resp.Version != productprotocol.Version:
		return nil, &chatServiceError{Code: "PROTOCOL_ERROR", Detail: "unsupported response version"}
	case resp.Status == "error" && resp.Error != nil:
		return nil, &chatServiceError{Code: resp.Error.Code, Detail: resp.Error.Detail}
	case resp.Status != "ok":
		return nil, &chatServiceError{Code: "PROTOCOL_ERROR", Detail: fmt.Sprintf("unexpected status %q", resp.Status)}
	}
	return &resp, nil
}
