package webchat

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake product service on a temp Unix socket speaks the real framing, and the
// live gateway runs on a real loopback listener, so the tests go through the socket
// client and the HTTP stack, not around them.

const (
	testSession = "psess-t1"
	testTurn    = "psess-t1/turn-1"
)

func okResp(response ProductResponse) *ProductResponse {
	response.Version, response.Status = ProductVersion, StatusOK
	return &response
}

func errResp(code, detail string) *ProductResponse {
	return &ProductResponse{Version: ProductVersion, Status: StatusError, Error: &ProductError{Code: code, Detail: detail}}
}

type fakeService struct {
	t        *testing.T
	path     string
	listener net.Listener
	respond  func(ProductRequest) *ProductResponse

	mu       sync.Mutex
	requests []ProductRequest
	conns    int
}

func startFakeService(t *testing.T, respond func(ProductRequest) *ProductResponse) *fakeService {
	t.Helper()
	dir, err := os.MkdirTemp("", "tbs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "p.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets are unavailable here: %v", err)
	}
	service := &fakeService{t: t, path: path, listener: listener, respond: respond}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			service.mu.Lock()
			service.conns++
			service.mu.Unlock()
			go service.serve(conn)
		}
	}()
	return service
}

func (f *fakeService) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	var request ProductRequest
	if err := readProductFrame(conn, MaxProductRequestBytes, &request); err != nil {
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	if response := f.respond(request); response != nil {
		_ = writeProductFrame(conn, response, MaxProductResponseBytes)
	}
}

func (f *fakeService) calls(op string) []ProductRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ProductRequest
	for _, request := range f.requests {
		if request.Op == op {
			out = append(out, request)
		}
	}
	return out
}

func (f *fakeService) all() []ProductRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProductRequest(nil), f.requests...)
}

func (f *fakeService) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

// serviceModel answers every operation with a plausible projection.
type serviceModel struct {
	events   []ProductEvent
	pageSize int
}

func sampleEvents() []ProductEvent {
	return []ProductEvent{
		{Seq: 0, Kind: EventTurnAdmitted, TurnID: testTurn, Text: "fix the failing test"},
		{Seq: 1, Kind: EventTurnLinked, TurnID: testTurn},
		{Seq: 2, Kind: EventAssistantMessage, TurnID: testTurn, Text: "I will read the file."},
		{Seq: 3, Kind: EventToolCall, TurnID: testTurn, Tool: "bash", ToolCallID: "call_1", Verdict: "DENY", ReasonCode: "policy_rule_deny", Hint: "use read"},
		{Seq: 4, Kind: EventToolResult, TurnID: testTurn, Tool: "bash", ToolCallID: "call_1", Verdict: "DENY", ReasonCode: "policy_rule_deny", Text: "denied"},
		{Seq: 5, Kind: EventToolCall, TurnID: testTurn, Tool: "read", ToolCallID: "call_2", Verdict: "ALLOW", ReasonCode: "policy_rule_allow"},
		{Seq: 6, Kind: EventToolResult, TurnID: testTurn, Tool: "read", ToolCallID: "call_2", Verdict: "ALLOW", ReasonCode: "policy_rule_allow", Text: "ok, 12 bytes of output"},
		{Seq: 7, Kind: EventTurnAwaitingReview, TurnID: testTurn, Text: "1 changed paths", CandidateDigest: "sha256:cand1", Paths: []string{"a.go"}},
	}
}

func testSessionView() ProductSession {
	return ProductSession{ID: testSession, ProjectID: "demo", ProviderProfile: "prof", PublicationMode: "review", Revision: 4,
		Budget:   ProductBudget{HostCalls: 10, Tokens: 1000, DurationSeconds: 60},
		Consumed: ProductBudget{HostCalls: 1, Tokens: 10, DurationSeconds: 2},
		Turns:    []ProductTurn{{ID: testTurn, Ordinal: 1, State: "awaiting_review", Prompt: "fix the failing test", CandidateDigest: "sha256:cand1"}}}
}

func (m *serviceModel) respond(request ProductRequest) *ProductResponse {
	turn := testSessionView().Turns[0]
	switch request.Op {
	case OpCreateSession:
		view := testSessionView()
		view.Turns = nil
		view.ProjectID = request.CreateSession.ProjectID
		return okResp(ProductResponse{Session: &view})
	case OpListSessions:
		view := testSessionView()
		view.Turns = nil
		return okResp(ProductResponse{Sessions: []ProductSession{view}})
	case OpGetSession:
		view := testSessionView()
		return okResp(ProductResponse{Session: &view})
	case OpObserve:
		cursor := request.Observe.Cursor
		if cursor > uint64(len(m.events)) {
			return errResp("INVALID_REQUEST", "cursor is past the end")
		}
		end := len(m.events)
		if m.pageSize > 0 && int(cursor)+m.pageSize < end {
			end = int(cursor) + m.pageSize
		}
		page := m.events[cursor:end]
		return okResp(ProductResponse{Events: page, NextCursor: cursor + uint64(len(page))})
	case OpSubmitTurn, OpCancelTurn, OpResolveTurn:
		return okResp(ProductResponse{Turn: &turn})
	case OpGetCandidate:
		return okResp(ProductResponse{Candidate: &ProductCandidate{TurnID: testTurn, CandidateDigest: "sha256:cand1",
			Changes: []ProductChange{{Path: "a.go", Op: "modify", Diff: "-a\n+b\n"}}}})
	}
	return errResp(CodeProtocolError, "unknown operation")
}

func defaultService() func(ProductRequest) *ProductResponse {
	model := &serviceModel{events: sampleEvents()}
	return model.respond
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type liveHarness struct {
	t       *testing.T
	gateway *LiveGateway
	server  *httptest.Server
	service *fakeService
	clock   *testClock
	token   string
	host    string
	port    string
	cookie  string
	csrf    string
}

// newLiveHarness runs the gateway against a fake service on a fresh socket.
func newLiveHarness(t *testing.T, respond func(ProductRequest) *ProductResponse) *liveHarness {
	t.Helper()
	service := startFakeService(t, respond)
	return newLiveHarnessOn(t, service, service.path)
}

func newLiveHarnessOn(t *testing.T, service *fakeService, socket string) *liveHarness {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	// Item times are checked against the real clock, so the fake one starts in the past.
	clock := &testClock{now: time.Now().UTC().Add(-time.Hour)}
	host := server.Listener.Addr().String()
	gateway, token, err := NewLiveGateway(LiveOptions{Socket: socket, BoundAddr: host, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = gateway.Handler()
	// A short read timeout, as the real server has, must not cut a long poll or a slow mutation.
	server.Config.ReadTimeout = 700 * time.Millisecond
	server.Start()
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(host)
	return &liveHarness{t: t, gateway: gateway, server: server, service: service, clock: clock, token: token, host: host, port: port}
}

type reqOpts struct {
	host    string
	origin  string
	cookie  string // sent as the session cookie; "" sends none
	csrf    string
	body    string
	headers map[string]string
}

var noRedirect = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     &http.Transport{DisableKeepAlives: true},
	Timeout:       30 * time.Second,
}

func (h *liveHarness) do(method, target string, o reqOpts) (*http.Response, []byte) {
	h.t.Helper()
	var body io.Reader
	if o.body != "" {
		body = strings.NewReader(o.body)
	}
	request, err := http.NewRequest(method, h.server.URL+target, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if o.host != "" {
		request.Host = o.host
	}
	if o.origin != "" {
		request.Header.Set("Origin", o.origin)
	}
	if o.cookie != "" {
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: o.cookie})
	}
	if o.csrf != "" {
		request.Header.Set(CSRFHeader, o.csrf)
	}
	for name, value := range o.headers {
		request.Header.Set(name, value)
	}
	response, err := noRedirect.Do(request)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, target, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return response, data
}

// pairNow pairs with the real token and keeps the cookie and CSRF token.
func (h *liveHarness) pairNow() {
	h.t.Helper()
	response, body := h.do(http.MethodPost, "/api/v1/pair", reqOpts{body: fmt.Sprintf(`{"token":%q}`, h.token)})
	if response.StatusCode != http.StatusOK {
		h.t.Fatalf("pairing: status %d %s", response.StatusCode, body)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == SessionCookieName {
			h.cookie = cookie.Value
		}
	}
	var state pairingState
	if err := json.Unmarshal(body, &state); err != nil || state.CSRFToken == "" || h.cookie == "" {
		h.t.Fatalf("pairing gave no cookie or CSRF token: %s", body)
	}
	h.csrf = state.CSRFToken
}

// api sends an authenticated request. A mutation carries the CSRF token.
func (h *liveHarness) api(method, target, body string) (*http.Response, []byte) {
	h.t.Helper()
	o := reqOpts{cookie: h.cookie, body: body}
	if method == http.MethodPost {
		o.csrf = h.csrf
	}
	return h.do(method, target, o)
}

func decodeMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, body)
	}
	return out
}

func errorOf(t *testing.T, body []byte) apiErrorDetail {
	t.Helper()
	var wrapped apiError
	if err := json.Unmarshal(body, &wrapped); err != nil || wrapped.Error.Code == "" {
		t.Fatalf("not an error body: %v: %s", err, body)
	}
	return wrapped.Error
}

func assertGatewayHeaders(t *testing.T, label string, header http.Header) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy":      wantCSP,
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cache-Control":                "no-store",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cross-Origin-Opener-Policy":   "same-origin",
	}
	for name, value := range want {
		if got := header.Get(name); got != value {
			t.Errorf("%s: %s=%q, want %q", label, name, got, value)
		}
	}
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			t.Errorf("%s: sends the CORS header %s", label, name)
		}
	}
}

func requestsEqual(t *testing.T, label string, got, want ProductRequest) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Errorf("%s forwarded\n got %s\nwant %s", label, gotJSON, wantJSON)
	}
}
