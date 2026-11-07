package openrouter

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestHTTPDoerAddsOneBearerHeaderAndDoesNotMutateRequest(t *testing.T) {
	const key = "test-provider-key-123"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		values := request.Header.Values("Authorization")
		if len(values) != 1 || values[0] != "Bearer "+key {
			t.Error("Authorization was not exactly one expected Bearer value")
		}
		_, _ = io.WriteString(writer, "provider echoed "+key)
	}))
	defer server.Close()

	doer, err := newHTTPDoer(func() (string, error) { return key, nil }, time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	setRegisteredHeaders(request, "fixture-session")
	response, err := doer.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close response: %v / %v", readErr, closeErr)
	}
	if bytes.Contains(body, []byte(key)) || !bytes.Contains(body, []byte("[REDACTED]")) {
		t.Fatal("response credential was not redacted")
	}
	if got := request.Header.Values("Authorization"); len(got) != 0 ||
		request.Header.Get(SessionHeader) != "fixture-session" || request.Header.Get("User-Agent") != UserAgent {
		t.Fatalf("transport mutated the registered request headers: %q", got)
	}
	transport, ok := doer.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("OpenCode Go transport must not use an environment proxy")
	}
}

func TestHTTPDoerRejectsUnexpectedHeadersBeforeCredentialRead(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	var credentialReads atomic.Int32
	doer, err := newHTTPDoer(func() (string, error) {
		credentialReads.Add(1)
		return "test-key-123", nil
	}, time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest(t, server.URL)
	setRegisteredHeaders(request, "fixture-session")
	request.Header.Set("X-Extra", "not-approved")
	if response, err := doer.Do(request); err == nil || response != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		t.Fatal("unexpected header was accepted")
	}
	if requests.Load() != 0 || credentialReads.Load() != 0 {
		t.Fatalf("invalid request reached transport or credential source: requests=%d credentialReads=%d", requests.Load(), credentialReads.Load())
	}
}

func TestHTTPDoerRefusesRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/target" {
			targetRequests.Add(1)
			writer.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(writer, request, server.URL+"/target", http.StatusFound)
	}))
	defer server.Close()
	doer, err := newHTTPDoer(func() (string, error) { return "test-key-123", nil }, time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := doer.Do(testRequest(t, server.URL))
	if err == nil || response != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		t.Fatal("redirect was followed or not rejected")
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target received %d requests", targetRequests.Load())
	}
	if strings.Contains(err.Error(), "test-key-123") {
		t.Fatal("transport error exposed the credential")
	}
}

func TestHTTPDoerAppliesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(writer, "late")
	}))
	defer server.Close()
	doer, err := newHTTPDoer(func() (string, error) { return "test-key-123", nil }, 35*time.Millisecond, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := doer.Do(testRequest(t, server.URL))
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("slow response did not time out")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("configured timeout was not bounded: elapsed %s", elapsed)
	}
	if strings.Contains(err.Error(), "test-key-123") {
		t.Fatal("timeout error exposed the credential")
	}
}

func TestHTTPDoerRedactsCredentialAcrossResponseReadBoundaries(t *testing.T) {
	const key = "boundary-sensitive-key-987"
	content := strings.Repeat("x", (32<<10)-3) + key + "-tail"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, content)
	}))
	defer server.Close()
	doer, err := newHTTPDoer(func() (string, error) { return key, nil }, time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := doer.Do(testRequest(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if bytes.Contains(body, []byte(key)) || !bytes.Contains(body, []byte("[REDACTED]")) {
		t.Fatal("credential crossing a response read boundary was not redacted")
	}
}

func TestLoadCredentialsEnvironmentPrecedesFileAndRedactsRepresentations(t *testing.T) {
	const secret = "env-openrouter-secret-987"
	credentials, err := LoadCredentialsFrom(mapLookup(map[string]string{
		APIKeyEnv:     secret,
		APIKeyFileEnv: filepath.Join(t.TempDir(), "must-not-be-opened"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if credentials.ModelID() != ApprovedModel {
		t.Fatalf("model = %q", credentials.ModelID())
	}
	for name, rendered := range map[string]string{
		"string":   credentials.String(),
		"gostring": fmt.Sprintf("%#v", credentials),
		"json":     string(mustJSON(t, credentials)),
	} {
		if strings.Contains(rendered, secret) || !strings.Contains(rendered, "[REDACTED]") {
			t.Errorf("%s representation was not redacted", name)
		}
	}
	if got := credentials.Redact("response=" + secret); strings.Contains(got, secret) || !strings.Contains(got, "[REDACTED]") {
		t.Fatal("Redact did not replace the credential")
	}
}

func TestLoadCredentialsRequiresCredential(t *testing.T) {
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{})); err == nil || strings.Contains(err.Error(), "some-key") {
		t.Fatal("missing credential was not rejected")
	}
	if _, err := LoadCredentialsFrom(nil); err == nil {
		t.Fatal("nil environment lookup was accepted")
	}
}

func TestLoadCredentialsFromSecureFileAndRejectsUnsafeFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file credentials are validated through the file owner and DACL; POSIX mode fixtures are not portable")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "openrouter-key")
	if err := os.WriteFile(path, []byte("file-openrouter-secret-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := LoadCredentialsFrom(mapLookup(map[string]string{APIKeyFileEnv: path}))
	if err != nil {
		t.Fatal(err)
	}
	if got := credentials.Redact("file-openrouter-secret-123"); got != "[REDACTED]" {
		t.Fatal("file credential was not loaded and redacted")
	}

	insecure := filepath.Join(directory, "insecure-key")
	if err := os.WriteFile(insecure, []byte("insecure-secret-123"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecure, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{APIKeyFileEnv: insecure})); err == nil || strings.Contains(err.Error(), "insecure-secret-123") {
		t.Fatal("group/world-readable credential file was accepted or leaked")
	}

	symlink := filepath.Join(directory, "symlink-key")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{APIKeyFileEnv: symlink})); err == nil {
		t.Fatal("symlink credential file was accepted")
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{APIKeyFileEnv: filepath.Join(directory, "missing")})); err == nil {
		t.Fatal("missing credential file was accepted")
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{APIKeyFileEnv: directory})); err == nil {
		t.Fatal("directory credential file was accepted")
	}
}

func TestLoadCredentialsRejectsOversizedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows private-file fixtures require a custom owner-only DACL")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "large-key")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", maxCredentialBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{APIKeyFileEnv: path})); err == nil {
		t.Fatal("oversized credential file was accepted")
	}
}

func testRequest(t *testing.T, endpoint string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	setRegisteredHeaders(request, "fixture-session")
	return request
}

func setRegisteredHeaders(request *http.Request, conversationID string) {
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", UserAgent)
	request.Header.Set(SessionHeader, conversationID)
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Error("the test transport saw a request that the doer should have refused")
	return nil, fmt.Errorf("unexpected request")
}

func TestWithTestTransportIsRefusedOutsideATestBinaryAndKeepsTheEndpointChecks(t *testing.T) {
	newDoer := func() *HTTPDoer {
		doer, err := newHTTPDoer(func() (string, error) { return "test-key-123", nil }, time.Second, Endpoint)
		if err != nil {
			t.Fatal(err)
		}
		return doer
	}
	original := testBinary
	defer func() { testBinary = original }()
	testBinary = func() bool { return false }
	if _, err := newDoer().WithTestTransport(failingTransport{t}); err == nil {
		t.Fatal("a test transport was accepted outside a test binary")
	}
	testBinary = original
	if _, err := newDoer().WithTestTransport(nil); err == nil {
		t.Fatal("a nil test transport was accepted")
	}
	doer, err := newDoer().WithTestTransport(failingTransport{t})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:1/other", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if _, err := doer.Do(request); err == nil {
		t.Fatal("a request for another URL was sent through the test transport")
	}
}

func TestTransportFailureClass(t *testing.T) {
	endpoint := "https://api.provider.test/v1/chat"
	wrap := func(cause error) error {
		return &url.Error{Op: http.MethodPost, URL: endpoint, Err: &net.OpError{Op: "dial", Net: "tcp", Err: cause}}
	}
	cases := []struct {
		name  string
		cause error
		want  string
	}{
		{"dns lookup failure", &net.DNSError{Err: "no such host", Name: "api.provider.test", IsNotFound: true}, "dns"},
		{"dns lookup timeout is a timeout", &net.DNSError{Err: "i/o timeout", Name: "api.provider.test", IsTimeout: true}, "timeout"},
		{"context deadline", context.DeadlineExceeded, "timeout"},
		{"dial deadline", os.ErrDeadlineExceeded, "timeout"},
		{"winsock timed out", syscall.Errno(10060), "timeout"},
		{"context canceled", context.Canceled, "canceled"},
		{"connection reset", syscall.ECONNRESET, "connection_reset"},
		{"connection refused", syscall.ECONNREFUSED, "connection_reset"},
		{"broken pipe", syscall.EPIPE, "connection_reset"},
		{"winsock reset", syscall.Errno(10054), "connection_reset"},
		{"winsock refused", syscall.Errno(10061), "connection_reset"},
		{"unexpected EOF", io.ErrUnexpectedEOF, "connection_reset"},
		{"EOF", io.EOF, "connection_reset"},
		{"TLS record header", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, "tls"},
		{"TLS alert", tls.AlertError(40), "tls"},
		{"certificate verification", &tls.CertificateVerificationError{Err: x509.CertificateInvalidError{Reason: x509.Expired}}, "tls"},
		{"x509 unknown authority", x509.UnknownAuthorityError{}, "tls"},
		{"x509 hostname mismatch", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "api.provider.test"}, "tls"},
		{"unclassified", errors.New("unclassified"), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transportFailureClass(wrap(tc.cause)); got != tc.want {
				t.Fatalf("class = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHTTPDoerClassifiesResponseHeaderTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-release:
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	doer, err := newHTTPDoer(func() (string, error) { return "test-key-123", nil }, time.Minute, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doer.WithTestTransport(&http.Transport{ResponseHeaderTimeout: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	response, err := doer.Do(testRequest(t, server.URL))
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err == nil || err.Error() != "provider HTTP request failed: timeout" {
		t.Fatalf("response header timeout error = %v", err)
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatal("transport error contains the endpoint")
	}
}

func TestHTTPDoerClassifiesRefusedConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + address
	doer, err := newHTTPDoer(func() (string, error) { return "test-key-123", nil }, time.Second, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	response, err := doer.Do(testRequest(t, endpoint))
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	// Linux returns ECONNREFUSED and Windows WSAECONNREFUSED for a closed loopback port. connection_reset
	// groups both: each fails at the connection, before any response exists.
	if err == nil || err.Error() != "provider HTTP request failed: connection_reset" {
		t.Fatalf("refused connection error = %v", err)
	}
	if strings.Contains(err.Error(), address) {
		t.Fatal("transport error contains the endpoint")
	}
}

func TestBodyReadFailureIsReportedAsItsClassOnly(t *testing.T) {
	body := &redactingReadCloser{
		reader: &redactingReader{
			source: failingReader{&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}},
			secret: []byte("test-key-123"),
		},
		closer: io.NopCloser(strings.NewReader("")),
	}
	if _, err := io.ReadAll(body); err == nil || err.Error() != "connection_reset" {
		t.Fatalf("body read error = %v, want the connection_reset class", err)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
