package openrouter

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"
	"time"
)

const (
	DefaultHTTPTimeout = 90 * time.Second
	MaxHTTPTimeout     = 2 * time.Minute
)

// APIKeySource is injected into the transport. Errors returned by a source are
// deliberately hidden so that an implementation cannot accidentally include a
// key in an error, record, or log message.
type APIKeySource func() (string, error)

// HTTPDoer is the production net/http transport for the fixed OpenCode Go
// request. It clones the request before attaching authentication, so the
// broker's pre-transport request snapshot never contains the credential.
type HTTPDoer struct {
	client   *http.Client
	key      APIKeySource
	endpoint string
	// research selects the research header set (Content-Type only, see
	// Route.RequestHeaders). It is set only by NewHTTPDoerForEndpoint.
	research bool
}

func NewHTTPDoer(key APIKeySource, timeout time.Duration) (*HTTPDoer, error) {
	return newHTTPDoer(key, timeout, Endpoint)
}

// testBinary is a variable only so that this package's test can prove the refusal below.
var testBinary = testing.Testing

// WithTestTransport replaces the dialing layer so an offline test can serve the fixed endpoint from a
// loopback fixture. Every request still passes the endpoint, method, header and redirect checks, and the
// key is attached as usual. A binary that is not a Go test binary always refuses: no flag, file, environment
// variable or wire field reaches this method, and no code path of the product service calls it.
func (d *HTTPDoer) WithTestTransport(transport http.RoundTripper) (*HTTPDoer, error) {
	if !testBinary() || d == nil || d.client == nil || transport == nil {
		return nil, errors.New("a test transport is only permitted inside a Go test binary")
	}
	d.client.Transport = transport
	return d, nil
}

// NewHTTPDoerForEndpoint is the transport of a research route. It is the same
// redirect-forbidding client as NewHTTPDoer, bound to one endpoint. The endpoint
// must use HTTPS; plain HTTP is accepted only on a loopback address so that an
// offline fixture can run a fake server. The research route validates the URL
// itself, so this seam does not widen what a deployed service can reach.
func NewHTTPDoerForEndpoint(key APIKeySource, timeout time.Duration, endpoint string) (*HTTPDoer, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || parsed.User != nil || (parsed.Scheme != "https" && !loopbackHTTPEndpoint(endpoint)) {
		return nil, errors.New("research endpoint must use HTTPS or be a loopback fixture")
	}
	doer, err := newHTTPDoer(key, timeout, endpoint)
	if err != nil {
		return nil, err
	}
	doer.research = true
	return doer, nil
}

func newHTTPDoer(key APIKeySource, timeout time.Duration, endpoint string) (*HTTPDoer, error) {
	if key == nil {
		return nil, errors.New("an injected OpenCode Go credential source is required")
	}
	if timeout <= 0 || timeout > MaxHTTPTimeout {
		return nil, errors.New("OpenCode Go HTTP timeout is outside the permitted bound")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("invalid registered OpenCode Go endpoint")
	}
	if endpoint == Endpoint && parsed.Scheme != "https" {
		return nil, errors.New("OpenCode Go endpoint must use HTTPS")
	}

	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: min(timeout, 10*time.Second), KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   min(timeout, 10*time.Second),
		ResponseHeaderTimeout: min(timeout, 30*time.Second),
		ExpectContinueTimeout: min(timeout, time.Second),
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		MaxConnsPerHost:       2,
		IdleConnTimeout:       30 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("OpenCode Go redirects are forbidden")
		},
	}
	return &HTTPDoer{client: client, key: key, endpoint: endpoint}, nil
}

func (d *HTTPDoer) Do(request *http.Request) (*http.Response, error) {
	if d == nil || d.client == nil || d.key == nil {
		return nil, errors.New("OpenCode Go HTTP transport is not configured")
	}
	if request == nil || request.URL == nil || request.Method != http.MethodPost ||
		request.URL.String() != d.endpoint || request.URL.User != nil ||
		(request.Host != "" && request.Host != request.URL.Host) {
		return nil, errors.New("request does not match the registered OpenCode Go endpoint and method")
	}
	if err := validateRegisteredHeaders(request.Header, d.research); err != nil {
		return nil, err
	}

	key, err := d.key()
	if err != nil {
		return nil, errors.New("OpenCode Go credential source failed")
	}
	key, err = normalizeAPIKey(key)
	if err != nil {
		return nil, errors.New("OpenCode Go credential source returned an invalid credential")
	}

	outgoing := request.Clone(request.Context())
	outgoing.Header = request.Header.Clone()
	outgoing.Header.Set("Authorization", "Bearer "+key)

	response, doErr := d.client.Do(outgoing)
	if response != nil && response.Body != nil {
		response.Body = &redactingReadCloser{
			reader: &redactingReader{source: response.Body, secret: []byte(key)},
			closer: response.Body,
		}
	}
	if doErr != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("provider HTTP request failed: %s", transportFailureClass(doErr))
	}
	return response, nil
}

func validateRegisteredHeaders(header http.Header, research bool) error {
	if research {
		if len(header) != 1 || len(header["Content-Type"]) != 1 || header.Get("Content-Type") != "application/json" {
			return errors.New("request headers do not match the fixed research profile")
		}
		return nil
	}
	if len(header) != 4 {
		return errors.New("request headers do not match the fixed OpenCode Go profile")
	}
	for _, name := range []string{"Accept", "Content-Type", "User-Agent", SessionHeader} {
		values, ok := header[name]
		if !ok || len(values) != 1 {
			return errors.New("request headers do not match the fixed OpenCode Go profile")
		}
	}
	if header.Get("Accept") != "text/event-stream" || header.Get("Content-Type") != "application/json" ||
		header.Get("User-Agent") != UserAgent || !validText(header.Get(SessionHeader), 256) {
		return errors.New("request headers do not match the fixed OpenCode Go profile")
	}
	return nil
}

type redactingReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func (r *redactingReadCloser) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if err != nil && err != io.EOF {
		// The class is the whole text: the raw error can carry a URL or an address.
		return n, errors.New(transportFailureClass(err))
	}
	return n, err
}

func (r *redactingReadCloser) Close() error {
	if err := r.closer.Close(); err != nil {
		return errors.New("OpenRouter response body close failed")
	}
	return nil
}

// redactingReader buffers at most one source read plus the secret overlap. It
// replaces a key even when the provider response splits it across reads.
type redactingReader struct {
	source  io.Reader
	secret  []byte
	pending []byte
	output  []byte
	eof     bool
}

func (r *redactingReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for len(r.output) == 0 {
		if index := bytes.Index(r.pending, r.secret); index >= 0 {
			r.output = append(r.output, r.pending[:index]...)
			r.output = append(r.output, "[REDACTED]"...)
			r.pending = append(r.pending[:0], r.pending[index+len(r.secret):]...)
			continue
		}
		if r.eof {
			r.output = append(r.output, r.pending...)
			r.pending = nil
			if len(r.output) == 0 {
				return 0, io.EOF
			}
			break
		}
		keep := len(r.secret) - 1
		if safe := len(r.pending) - keep; safe > 0 {
			r.output = append(r.output, r.pending[:safe]...)
			r.pending = append(r.pending[:0], r.pending[safe:]...)
			break
		}
		chunk := make([]byte, 32<<10)
		n, err := r.source.Read(chunk)
		if n > 0 {
			r.pending = append(r.pending, chunk[:n]...)
		}
		if err == io.EOF {
			r.eof = true
		} else if err != nil {
			return 0, err
		}
	}
	n := copy(buffer, r.output)
	r.output = r.output[n:]
	return n, nil
}

// WinSock numbers for a timed-out, reset or refused socket. On Windows the syscall ETIMEDOUT,
// ECONNRESET and ECONNREFUSED values are invented and do not match these; Linux never returns them.
const (
	winsockTimedOut    = syscall.Errno(10060)
	winsockConnReset   = syscall.Errno(10054)
	winsockConnRefused = syscall.Errno(10061)
)

// transportFailureClass returns one fixed class for a transport error, never its text. The checks run in
// this order and the first match wins: timeout comes first because a timed-out dial or lookup also matches
// the generic net errors below; then canceled, dns, tls and connection_reset. Anything else is other.
func transportFailureClass(err error) string {
	var netErr net.Error
	var dnsErr *net.DNSError
	var recordErr tls.RecordHeaderError
	var alertErr tls.AlertError
	var verifyErr *tls.CertificateVerificationError
	var authorityErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var certErr x509.CertificateInvalidError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, winsockTimedOut),
		errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.As(err, &recordErr), errors.As(err, &alertErr), errors.As(err, &verifyErr),
		errors.As(err, &authorityErr), errors.As(err, &hostnameErr), errors.As(err, &certErr):
		return "tls"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, winsockConnReset), errors.Is(err, winsockConnRefused),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection_reset"
	}
	return "other"
}
