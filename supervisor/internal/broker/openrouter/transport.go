package openrouter

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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

// HTTPDoer is the production net/http transport for a registered OpenRouter
// request. It clones the request before attaching authentication, so the
// broker's pre-transport request snapshot never contains the credential.
type HTTPDoer struct {
	client   *http.Client
	key      APIKeySource
	endpoint string
}

func NewHTTPDoer(key APIKeySource, timeout time.Duration) (*HTTPDoer, error) {
	return newHTTPDoer(key, timeout, Endpoint)
}

func newHTTPDoer(key APIKeySource, timeout time.Duration, endpoint string) (*HTTPDoer, error) {
	if key == nil {
		return nil, errors.New("an injected OpenRouter credential source is required")
	}
	if timeout <= 0 || timeout > MaxHTTPTimeout {
		return nil, errors.New("OpenRouter HTTP timeout is outside the permitted bound")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("invalid registered OpenRouter endpoint")
	}
	if endpoint == Endpoint && parsed.Scheme != "https" {
		return nil, errors.New("OpenRouter endpoint must use HTTPS")
	}

	shorter := func(limit time.Duration) time.Duration {
		if timeout < limit {
			return timeout
		}
		return limit
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: shorter(10 * time.Second), KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   shorter(10 * time.Second),
		ResponseHeaderTimeout: shorter(30 * time.Second),
		ExpectContinueTimeout: shorter(time.Second),
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		MaxConnsPerHost:       2,
		IdleConnTimeout:       30 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("OpenRouter redirects are forbidden")
		},
	}
	return &HTTPDoer{client: client, key: key, endpoint: endpoint}, nil
}

func (d *HTTPDoer) Do(request *http.Request) (*http.Response, error) {
	if d == nil || d.client == nil || d.key == nil {
		return nil, errors.New("OpenRouter HTTP transport is not configured")
	}
	if request == nil || request.URL == nil || request.Method != http.MethodPost ||
		request.URL.String() != d.endpoint || request.URL.User != nil ||
		(request.Host != "" && request.Host != request.URL.Host) {
		return nil, errors.New("request does not match the registered OpenRouter endpoint and method")
	}

	key, err := d.key()
	if err != nil {
		return nil, errors.New("OpenRouter credential source failed")
	}
	key, err = normalizeAPIKey(key)
	if err != nil {
		return nil, errors.New("OpenRouter credential source returned an invalid credential")
	}

	outgoing := request.Clone(request.Context())
	outgoing.Header = request.Header.Clone()
	if outgoing.Header == nil {
		outgoing.Header = make(http.Header)
	}
	for name := range outgoing.Header {
		if strings.EqualFold(name, "Authorization") {
			delete(outgoing.Header, name)
		}
	}
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
		return nil, errors.New("OpenRouter HTTP request failed")
	}
	return response, nil
}

type redactingReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func (r *redactingReadCloser) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if err != nil && err != io.EOF {
		return n, errors.New("OpenRouter response body read failed")
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
			return 0, errors.New("OpenRouter response body read failed")
		}
	}
	n := copy(buffer, r.output)
	r.output = r.output[n:]
	return n, nil
}
