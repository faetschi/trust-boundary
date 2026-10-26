package webchat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	// socketCallTimeout bounds every call except observe, which adds its own wait.
	socketCallTimeout = 30 * time.Second
	// socketObserveMargin is added to an observe wait, so the service answers first.
	socketObserveMargin = 10 * time.Second
)

// ServiceError is an error response: the service's stable code and bounded detail.
type ServiceError struct{ Code, Detail string }

func (e *ServiceError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// TransportError means no valid response arrived. When Sent is set the service may
// have read the request, so a mutation's outcome is unknown and the caller may
// retry only with the same request key.
type TransportError struct {
	Err  error
	Sent bool
}

func (e *TransportError) Error() string { return "service connection failed: " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// productCaller makes one product protocol call. It is an interface so the HTTP
// layer is tested without a socket; socketClient is the only production
// implementation.
type productCaller interface {
	Call(ctx context.Context, request ProductRequest) (*ProductResponse, error)
}

// socketClient speaks the product protocol over a Unix socket: one connection, one
// request frame and one response frame per call. It never retries. A retry is the
// browser's decision and reuses the browser's request key.
type socketClient struct {
	path string
}

func (c socketClient) Call(ctx context.Context, request ProductRequest) (*ProductResponse, error) {
	request.Version = ProductVersion
	timeout := socketCallTimeout
	if request.Observe != nil {
		timeout = time.Duration(request.Observe.WaitMillis)*time.Millisecond + socketObserveMargin
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connected := false
	fail := func(err error) (*ProductResponse, error) {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		return nil, &TransportError{Err: err, Sent: connected}
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.path)
	if err != nil {
		return fail(err)
	}
	connected = true
	defer conn.Close()
	// Closing the connection on cancel unblocks a read that waits for the service.
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fail(err)
	}
	if err := writeProductFrame(conn, request, MaxProductRequestBytes); err != nil {
		if errors.Is(err, errProductFrame) { // the request outgrew the frame limit once encoded
			return nil, &ServiceError{Code: CodeProtocolError, Detail: "request exceeds the protocol size limit"}
		}
		return fail(err)
	}
	var response ProductResponse
	if err := readProductFrame(conn, MaxProductResponseBytes, &response); err != nil {
		if errors.Is(err, errProductFrame) {
			return nil, &ServiceError{Code: CodeBadServiceResponse, Detail: err.Error()}
		}
		return fail(err)
	}
	switch {
	case response.Version != ProductVersion:
		return nil, &ServiceError{Code: CodeBadServiceResponse, Detail: "unsupported response version"}
	case response.Status == StatusError && response.Error != nil:
		return nil, &ServiceError{Code: response.Error.Code, Detail: response.Error.Detail}
	case response.Status != StatusOK:
		return nil, &ServiceError{Code: CodeBadServiceResponse, Detail: fmt.Sprintf("unexpected status %q", response.Status)}
	}
	return &response, nil
}
