package webchat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- pairing -----------------------------------------------------------------

func TestPairingSpendsTheTokenOnFirstUseAndSetsAHardenedCookie(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	response, body := h.do(http.MethodPost, "/api/v1/pair", reqOpts{body: fmt.Sprintf(`{"token":%q}`, h.token)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pair: %d %s", response.StatusCode, body)
	}
	var cookie *http.Cookie
	for _, c := range response.Cookies() {
		if c.Name == SessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no tbound_session cookie")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Errorf("cookie flags: HttpOnly=%v SameSite=%v Path=%q Domain=%q", cookie.HttpOnly, cookie.SameSite, cookie.Path, cookie.Domain)
	}
	if cookie.MaxAge != int((12*time.Hour).Seconds()) || len(cookie.Value) < 40 {
		t.Errorf("cookie MaxAge=%d, value length %d", cookie.MaxAge, len(cookie.Value))
	}
	state := decodeMap(t, body)
	csrf, _ := state["csrf_token"].(string)
	if len(csrf) < 40 || csrf == cookie.Value || strings.Contains(string(body), cookie.Value) {
		t.Errorf("the CSRF token must be fresh and the cookie value must stay out of the body: %s", body)
	}
	if strings.Contains(string(body), h.token) {
		t.Error("the response repeats the pairing token")
	}
	assertGatewayHeaders(t, "pair", response.Header)

	response, body = h.do(http.MethodPost, "/api/v1/pair", reqOpts{body: fmt.Sprintf(`{"token":%q}`, h.token)})
	if response.StatusCode != http.StatusUnauthorized || errorOf(t, body).Code != "UNAUTHENTICATED" || len(response.Cookies()) != 0 {
		t.Errorf("a spent token: %d %s cookies=%v", response.StatusCode, body, response.Cookies())
	}
}

func TestAWrongTokenIsRefusedWithoutSpendingTheRightOne(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	for _, body := range []string{`{"token":"wrong"}`, `{"token":""}`, `{}`} {
		response, data := h.do(http.MethodPost, "/api/v1/pair", reqOpts{body: body})
		if response.StatusCode != http.StatusUnauthorized || len(response.Cookies()) != 0 {
			t.Errorf("%s: %d %s", body, response.StatusCode, data)
		}
	}
	h.pairNow()
}

func TestPairingTokenExpiresAfterTenMinutes(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.clock.advance(PairingLifetime - time.Second)
	h.pairNow() // still valid one second before the deadline

	late := newLiveHarness(t, defaultService())
	late.clock.advance(PairingLifetime)
	response, body := late.do(http.MethodPost, "/api/v1/pair", reqOpts{body: fmt.Sprintf(`{"token":%q}`, late.token)})
	if response.StatusCode != http.StatusUnauthorized || len(response.Cookies()) != 0 {
		t.Errorf("an expired token: %d %s", response.StatusCode, body)
	}
}

func TestBrowserSessionExpiresAfterTwelveHours(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	h.clock.advance(BrowserSessionLifetime - time.Second)
	if response, body := h.api(http.MethodGet, "/api/v1/sessions", ""); response.StatusCode != http.StatusOK {
		t.Fatalf("just before expiry: %d %s", response.StatusCode, body)
	}
	h.clock.advance(time.Second)
	response, body := h.api(http.MethodGet, "/api/v1/sessions", "")
	if response.StatusCode != http.StatusUnauthorized || errorOf(t, body).Code != "UNAUTHENTICATED" {
		t.Errorf("at expiry: %d %s", response.StatusCode, body)
	}
	// The token was spent, so an expired session needs a restart, not a replay.
	if response, _ := h.do(http.MethodPost, "/api/v1/pair", reqOpts{body: fmt.Sprintf(`{"token":%q}`, h.token)}); response.StatusCode != http.StatusUnauthorized {
		t.Errorf("re-pairing with the spent token: %d", response.StatusCode)
	}
}

func TestPairingStateShowsTheCSRFTokenOnlyToTheCookieHolder(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	response, body := h.api(http.MethodGet, "/api/v1/pair", "")
	if response.StatusCode != http.StatusOK || decodeMap(t, body)["csrf_token"] != h.csrf {
		t.Errorf("paired state: %d %s", response.StatusCode, body)
	}
	if response, _ := h.do(http.MethodGet, "/api/v1/pair", reqOpts{}); response.StatusCode != http.StatusUnauthorized {
		t.Errorf("without the cookie: %d", response.StatusCode)
	}
}

// --- authentication, CSRF, Host and Origin -------------------------------------

type routeCase struct {
	name   string
	method string
	path   string
	body   string
}

var allAPIRoutes = []routeCase{
	{"contract", http.MethodGet, "/api/v1/contract", ""},
	{"list", http.MethodGet, "/api/v1/sessions", ""},
	{"create", http.MethodPost, "/api/v1/sessions", `{"project_id":"demo","provider_profile":"prof","publication_mode":"review","budget":{"host_calls":1,"tokens":2,"duration_seconds":3},"request_key":"k1"}`},
	{"get", http.MethodGet, "/api/v1/sessions/psess-t1", ""},
	{"events", http.MethodGet, "/api/v1/sessions/psess-t1/events?cursor=0&wait=0", ""},
	{"submit", http.MethodPost, "/api/v1/sessions/psess-t1/turns", `{"expected_revision":4,"request_key":"k2","prompt":"hello"}`},
	{"candidate", http.MethodGet, "/api/v1/sessions/psess-t1/turns/turn-1/candidate", ""},
	{"cancel", http.MethodPost, "/api/v1/sessions/psess-t1/turns/turn-1/cancel", `{"expected_revision":4,"request_key":"k3"}`},
	{"resolve", http.MethodPost, "/api/v1/sessions/psess-t1/turns/turn-1/resolve", `{"expected_revision":4,"request_key":"k4","candidate_digest":"sha256:cand1","decision":"publish"}`},
}

func TestEveryAPIRequestNeedsTheSessionCookie(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	targets := append([]routeCase{{"pair state", http.MethodGet, "/api/v1/pair", ""}, {"unknown route", http.MethodGet, "/api/v1/nope", ""}}, allAPIRoutes...)
	for _, route := range targets {
		for name, cookie := range map[string]string{"no cookie": "", "wrong cookie": "not-the-cookie", "the CSRF token as cookie": h.csrf} {
			response, body := h.do(route.method, route.path, reqOpts{cookie: cookie, csrf: h.csrf, body: route.body})
			if response.StatusCode != http.StatusUnauthorized || errorOf(t, body).Code != "UNAUTHENTICATED" {
				t.Errorf("%s with %s: %d %s", route.name, name, response.StatusCode, body)
			}
			assertGatewayHeaders(t, route.name, response.Header)
		}
	}
	if n := len(h.service.all()); n != 0 {
		t.Errorf("%d unauthenticated requests reached the service", n)
	}
}

func TestEveryMutationNeedsTheCSRFHeader(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	for _, route := range allAPIRoutes {
		if route.method != http.MethodPost {
			continue
		}
		for name, csrf := range map[string]string{"missing": "", "wrong": "x" + h.csrf, "the cookie value": h.cookie, "prefix": h.csrf[:10]} {
			response, body := h.do(route.method, route.path, reqOpts{cookie: h.cookie, csrf: csrf, body: route.body})
			if response.StatusCode != http.StatusForbidden || errorOf(t, body).Code != "FORBIDDEN" {
				t.Errorf("%s with a %s CSRF header: %d %s", route.name, name, response.StatusCode, body)
			}
		}
	}
	if n := len(h.service.all()); n != 0 {
		t.Fatalf("%d requests without a valid CSRF header reached the service", n)
	}
	for _, route := range allAPIRoutes {
		if route.method == http.MethodPost {
			if response, body := h.do(route.method, route.path, reqOpts{cookie: h.cookie, csrf: h.csrf, body: route.body}); response.StatusCode != http.StatusOK {
				t.Errorf("%s with the right CSRF header: %d %s", route.name, response.StatusCode, body)
			}
		}
	}
}

func TestHostRebindingIsRefusedOnEveryRoute(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	bad := []string{"evil.example:" + h.port, "evil.example", "127.0.0.1:1", "127.0.0.1", "localhost:1", "localhost", "[::1]:" + h.port,
		"127.0.0.1:" + h.port + ".evil.example", "LOCALHOST:" + h.port, "127.0.0.1.evil.example:" + h.port}
	targets := append([]routeCase{{"app", http.MethodGet, "/app/", ""}, {"pair page", http.MethodGet, "/pair", ""}, {"pair", http.MethodPost, "/api/v1/pair", `{"token":"x"}`}}, allAPIRoutes...)
	for _, host := range bad {
		for _, route := range targets {
			response, body := h.do(route.method, route.path, reqOpts{host: host, cookie: h.cookie, csrf: h.csrf, body: route.body})
			if response.StatusCode != http.StatusForbidden {
				t.Errorf("Host %q on %s: %d %s", host, route.name, response.StatusCode, body)
			}
			assertGatewayHeaders(t, "Host "+host, response.Header)
		}
	}
	if n := len(h.service.all()); n != 0 {
		t.Errorf("%d rebinding requests reached the service", n)
	}
	for _, host := range []string{h.host, "localhost:" + h.port} {
		if response, body := h.do(http.MethodGet, "/api/v1/sessions", reqOpts{host: host, cookie: h.cookie}); response.StatusCode != http.StatusOK {
			t.Errorf("Host %q is allowed: %d %s", host, response.StatusCode, body)
		}
	}
}

func TestOriginMustEqualHttpPlusTheHost(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	own := "http://" + h.host
	bad := []struct{ host, origin string }{
		{"", "http://evil.example"}, {"", "null"}, {"", "https://" + h.host}, {"", "http://127.0.0.1:1"}, {"", own + "/"},
		{"", "http://localhost:" + h.port}, // another spelling of the same machine is still another origin
		{"localhost:" + h.port, own},
	}
	for _, c := range bad {
		for _, route := range []routeCase{{"list", http.MethodGet, "/api/v1/sessions", ""}, allAPIRoutes[2], {"app", http.MethodGet, "/app/", ""}} {
			response, body := h.do(route.method, route.path, reqOpts{host: c.host, origin: c.origin, cookie: h.cookie, csrf: h.csrf, body: route.body})
			if response.StatusCode != http.StatusForbidden {
				t.Errorf("Host %q Origin %q on %s: %d %s", c.host, c.origin, route.name, response.StatusCode, body)
			}
		}
	}
	good := []struct{ host, origin string }{{"", ""}, {"", own}, {"localhost:" + h.port, "http://localhost:" + h.port}}
	for _, c := range good {
		response, body := h.do(http.MethodPost, "/api/v1/sessions", reqOpts{host: c.host, origin: c.origin, cookie: h.cookie, csrf: h.csrf, body: allAPIRoutes[2].body})
		if response.StatusCode != http.StatusOK {
			t.Errorf("Host %q Origin %q: %d %s", c.host, c.origin, response.StatusCode, body)
		}
	}
	// A repeated Origin header is refused too.
	request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/v1/sessions", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: h.cookie})
	request.Header.Add("Origin", own)
	request.Header.Add("Origin", own)
	if response, err := noRedirect.Do(request); err != nil || response.StatusCode != http.StatusForbidden {
		t.Errorf("repeated Origin: %v %v", response, err)
	}
}

func TestNoCORSAndOptionsIsNotAPreflightAnswer(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	probes := []reqOpts{{}, {cookie: h.cookie}, {cookie: h.cookie, origin: "http://" + h.host}, {origin: "http://evil.example"},
		{headers: map[string]string{"Access-Control-Request-Method": "POST", "Origin": "http://evil.example"}}}
	for _, o := range probes {
		for _, path := range []string{"/api/v1/sessions", "/api/v1/pair", "/app/", "/pair", "/"} {
			response, _ := h.do(http.MethodOptions, path, o)
			assertGatewayHeaders(t, "OPTIONS "+path, response.Header)
			if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNoContent {
				t.Errorf("OPTIONS %s answered %d", path, response.StatusCode)
			}
		}
	}
	for _, route := range allAPIRoutes {
		response, _ := h.api(route.method, route.path, route.body)
		assertGatewayHeaders(t, route.name, response.Header)
	}
}

// --- bodies --------------------------------------------------------------------

func TestBodiesAreBoundedAndStrict(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	// The limit is spelled out here so changing the constant fails this test.
	const limit = 64 * 1024
	fill := func(total int) string {
		const head, tail = `{"expected_revision":4,"request_key":"k","prompt":"`, `"}`
		return head + strings.Repeat("a", total-len(head)-len(tail)) + tail
	}
	oversize, atLimit, small := fill(limit+1), fill(limit), fill(2000)
	cases := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{"oversize", "/api/v1/sessions/psess-t1/turns", oversize, http.StatusRequestEntityTooLarge},
		{"duplicate key", "/api/v1/sessions/psess-t1/turns", `{"expected_revision":4,"request_key":"k","request_key":"j","prompt":"p"}`, http.StatusBadRequest},
		{"unknown field", "/api/v1/sessions/psess-t1/turns", `{"expected_revision":4,"request_key":"k","prompt":"p","session_id":"psess-other"}`, http.StatusBadRequest},
		{"principal field", "/api/v1/sessions/psess-t1/turns", `{"expected_revision":4,"request_key":"k","prompt":"p","principal":"root"}`, http.StatusBadRequest},
		{"trailing value", "/api/v1/sessions/psess-t1/turns", `{"expected_revision":4,"request_key":"k","prompt":"p"}{}`, http.StatusBadRequest},
		{"wrong type", "/api/v1/sessions/psess-t1/turns", `{"expected_revision":"4","request_key":"k","prompt":"p"}`, http.StatusBadRequest},
		{"negative revision", "/api/v1/sessions/psess-t1/turns", `{"expected_revision":-1,"request_key":"k","prompt":"p"}`, http.StatusBadRequest},
		{"not json", "/api/v1/sessions/psess-t1/turns", `prompt=hello`, http.StatusBadRequest},
		{"array", "/api/v1/sessions/psess-t1/turns", `[]`, http.StatusBadRequest},
		{"empty body", "/api/v1/sessions/psess-t1/turns", ``, http.StatusBadRequest},
		{"no request key", "/api/v1/sessions/psess-t1/turns", `{"expected_revision":4,"prompt":"p"}`, http.StatusBadRequest},
		{"cancel without key", "/api/v1/sessions/psess-t1/turns/turn-1/cancel", `{"expected_revision":4}`, http.StatusBadRequest},
		{"resolve without key", "/api/v1/sessions/psess-t1/turns/turn-1/resolve", `{"expected_revision":4,"candidate_digest":"d","decision":"publish"}`, http.StatusBadRequest},
		{"create without key", "/api/v1/sessions", `{"project_id":"demo"}`, http.StatusBadRequest},
		{"create unknown field", "/api/v1/sessions", `{"project_id":"demo","request_key":"k","read_only":true,"admin":1}`, http.StatusBadRequest},
		{"pair unknown field", "/api/v1/pair", `{"token":"x","extra":1}`, http.StatusBadRequest},
		{"pair duplicate key", "/api/v1/pair", `{"token":"x","token":"y"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		response, body := h.do(http.MethodPost, c.path, reqOpts{cookie: h.cookie, csrf: h.csrf, body: c.body})
		if response.StatusCode != c.status || errorOf(t, body).Code != "INVALID_REQUEST" {
			t.Errorf("%s: %d %s, want %d INVALID_REQUEST", c.name, response.StatusCode, body, c.status)
		}
	}
	if n := len(h.service.all()); n != 0 {
		t.Fatalf("%d refused bodies reached the service", n)
	}
	if response, body := h.do(http.MethodPost, "/api/v1/sessions/psess-t1/turns", reqOpts{cookie: h.cookie, csrf: h.csrf, body: small}); response.StatusCode != http.StatusOK {
		t.Errorf("a body inside the limit: %d %s", response.StatusCode, body)
	}
	// A body of exactly 64 KiB is read, not refused as too large. With the frame envelope
	// added it no longer fits the service's 64 KiB request frame, so it is a protocol error.
	response, body := h.do(http.MethodPost, "/api/v1/sessions/psess-t1/turns", reqOpts{cookie: h.cookie, csrf: h.csrf, body: atLimit})
	if response.StatusCode != http.StatusBadRequest || errorOf(t, body).Code != CodeProtocolError {
		t.Errorf("a body at the limit: %d %s", response.StatusCode, body)
	}
	// A GET carries no body.
	response, body = h.do(http.MethodGet, "/api/v1/sessions", reqOpts{cookie: h.cookie, body: `{"x":1}`})
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("GET with a body: %d %s", response.StatusCode, body)
	}
}

// A prompt that is small in the browser's JSON but large once the service's frame
// escapes it is refused here, not cut.
func TestAPromptThatOutgrowsTheFrameIsAProtocolError(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	prompt := strings.Repeat("<", 20000) // 20000 bytes in, 120000 once <-escaped
	body := fmt.Sprintf(`{"expected_revision":4,"request_key":"k","prompt":%q}`, prompt)
	response, data := h.api(http.MethodPost, "/api/v1/sessions/psess-t1/turns", body)
	if response.StatusCode != http.StatusBadRequest || errorOf(t, data).Code != CodeProtocolError {
		t.Errorf("%d %s", response.StatusCode, data)
	}
}

// --- routes --------------------------------------------------------------------

func TestEachRouteForwardsExactlyItsOperation(t *testing.T) {
	obs := func(cursor uint64, wait int64) ProductRequest {
		return ProductRequest{Version: ProductVersion, Op: OpObserve, Observe: &ProductObserve{SessionID: testSession, Cursor: cursor, WaitMillis: wait}}
	}
	cases := []struct {
		name         string
		method, path string
		body         string
		want         []ProductRequest
		answer       string // a key the JSON answer must have
	}{
		{"list", http.MethodGet, "/api/v1/sessions", "", []ProductRequest{{Version: ProductVersion, Op: OpListSessions}}, "sessions"},
		{"create", http.MethodPost, "/api/v1/sessions",
			`{"project_id":"demo","provider_profile":"prof","publication_mode":"auto","budget":{"host_calls":5,"tokens":6,"duration_seconds":7},"read_only":true,"request_key":"ck-1"}`,
			[]ProductRequest{{Version: ProductVersion, Op: OpCreateSession, CreateSession: &ProductCreateSession{ProjectID: "demo", ProviderProfile: "prof",
				PublicationMode: "auto", Budget: ProductBudget{HostCalls: 5, Tokens: 6, DurationSeconds: 7}, ReadOnly: true, RequestKey: "ck-1"}}}, "session"},
		{"get", http.MethodGet, "/api/v1/sessions/psess-t1", "",
			[]ProductRequest{{Version: ProductVersion, Op: OpGetSession, GetSession: &ProductSessionRef{SessionID: testSession}}, obs(0, 0), obs(8, 0)}, "live"},
		{"events", http.MethodGet, "/api/v1/sessions/psess-t1/events?cursor=3&wait=2500", "", []ProductRequest{obs(3, 2500)}, "items"},
		{"submit", http.MethodPost, "/api/v1/sessions/psess-t1/turns",
			`{"expected_revision":4,"request_key":"ck-2","prompt":"hello","expected_baseline_digest":"sha256:base"}`,
			[]ProductRequest{{Version: ProductVersion, Op: OpSubmitTurn, SubmitTurn: &ProductSubmitTurn{SessionID: testSession, ExpectedRevision: 4,
				RequestKey: "ck-2", Prompt: "hello", ExpectedBaselineDigest: "sha256:base"}}}, "turn"},
		{"candidate short id", http.MethodGet, "/api/v1/sessions/psess-t1/turns/turn-1/candidate", "",
			[]ProductRequest{{Version: ProductVersion, Op: OpGetCandidate, GetCandidate: &ProductTurnRef{SessionID: testSession, TurnID: testTurn}}}, "candidate"},
		{"candidate full id", http.MethodGet, "/api/v1/sessions/psess-t1/turns/" + url.PathEscape(testTurn) + "/candidate", "",
			[]ProductRequest{{Version: ProductVersion, Op: OpGetCandidate, GetCandidate: &ProductTurnRef{SessionID: testSession, TurnID: testTurn}}}, "candidate"},
		{"cancel", http.MethodPost, "/api/v1/sessions/psess-t1/turns/turn-1/cancel", `{"expected_revision":5,"request_key":"ck-3"}`,
			[]ProductRequest{{Version: ProductVersion, Op: OpCancelTurn, CancelTurn: &ProductCancelTurn{SessionID: testSession, TurnID: testTurn, ExpectedRevision: 5, RequestKey: "ck-3"}}}, "turn"},
		{"cancel full id", http.MethodPost, "/api/v1/sessions/psess-t1/turns/" + url.PathEscape(testTurn) + "/cancel", `{"expected_revision":5,"request_key":"ck-3"}`,
			[]ProductRequest{{Version: ProductVersion, Op: OpCancelTurn, CancelTurn: &ProductCancelTurn{SessionID: testSession, TurnID: testTurn, ExpectedRevision: 5, RequestKey: "ck-3"}}}, "turn"},
		{"resolve", http.MethodPost, "/api/v1/sessions/psess-t1/turns/turn-1/resolve",
			`{"expected_revision":6,"request_key":"ck-4","candidate_digest":"sha256:cand1","decision":"discard"}`,
			[]ProductRequest{{Version: ProductVersion, Op: OpResolveTurn, ResolveTurn: &ProductResolveTurn{SessionID: testSession, TurnID: testTurn,
				ExpectedRevision: 6, RequestKey: "ck-4", CandidateDigest: "sha256:cand1", Decision: "discard"}}}, "turn"},
	}
	for _, c := range cases {
		h := newLiveHarness(t, defaultService())
		h.pairNow()
		response, body := h.api(c.method, c.path, c.body)
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s: %d %s", c.name, response.StatusCode, body)
			continue
		}
		if _, ok := decodeMap(t, body)[c.answer]; !ok {
			t.Errorf("%s: the answer has no %q: %s", c.name, c.answer, body)
		}
		if ct := response.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", c.name, ct)
		}
		assertGatewayHeaders(t, c.name, response.Header)
		got := h.service.all()
		if len(got) != len(c.want) {
			t.Errorf("%s: %d service calls, want %d: %+v", c.name, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			requestsEqual(t, fmt.Sprintf("%s call %d", c.name, i), got[i], c.want[i])
		}
	}
}

func TestResolveForwardsTheExactCandidateDigestTheClientDisplayed(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	for _, digest := range []string{"sha256:cand1", "sha256:STALE-diff", "  sha256:with spaces ", "", "sha256:" + strings.Repeat("0", 64)} {
		body := fmt.Sprintf(`{"expected_revision":4,"request_key":"ck-%d","candidate_digest":%q,"decision":"publish"}`, len(digest), digest)
		if response, data := h.api(http.MethodPost, "/api/v1/sessions/psess-t1/turns/turn-1/resolve", body); response.StatusCode != http.StatusOK {
			t.Fatalf("digest %q: %d %s", digest, response.StatusCode, data)
		}
		calls := h.service.calls(OpResolveTurn)
		if got := calls[len(calls)-1].ResolveTurn.CandidateDigest; got != digest {
			t.Errorf("forwarded digest %q, the client sent %q", got, digest)
		}
	}
}

func TestTheGatewayNeverInventsARequestKeyOrRetries(t *testing.T) {
	h := newLiveHarness(t, func(ProductRequest) *ProductResponse { return nil }) // the service drops every connection
	h.pairNow()
	response, body := h.api(http.MethodPost, "/api/v1/sessions/psess-t1/turns", `{"expected_revision":4,"request_key":"ck-fixed","prompt":"p"}`)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
	if n := h.service.connections(); n != 1 {
		t.Errorf("the gateway made %d attempts; a retry is the browser's decision", n)
	}
	for _, request := range h.service.all() {
		if request.SubmitTurn.RequestKey != "ck-fixed" {
			t.Errorf("request key %q", request.SubmitTurn.RequestKey)
		}
	}
}

func TestRoutesAllowOnlyTheirMethods(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	for _, route := range allAPIRoutes {
		want := route.method
		if route.path == "/api/v1/sessions" {
			want = "GET, POST"
		}
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			if strings.Contains(want, method) {
				continue
			}
			response, body := h.do(method, route.path, reqOpts{cookie: h.cookie, csrf: h.csrf, body: ifPost(method, `{}`)})
			if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != want {
				t.Errorf("%s %s: %d Allow=%q %s, want 405 Allow=%q", method, route.path, response.StatusCode, response.Header.Get("Allow"), body, want)
			}
		}
	}
	if n := len(h.service.all()); n != 0 {
		t.Errorf("%d wrong-method requests reached the service", n)
	}
}

func ifPost(method, body string) string {
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		return body
	}
	return ""
}

func TestUnknownRoutesAndMalformedIdentifiersAreNotForwarded(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	cases := []struct {
		path, code string
		status     int
	}{
		{"/api/v1/nope", "NOT_FOUND", 404},
		{"/api/v1/", "NOT_FOUND", 404},
		{"/api/", "NOT_FOUND", 404},
		{"/api/v1/sessions/", "NOT_FOUND", 404},
		{"/api/v1/sessions/psess-t1/", "NOT_FOUND", 404},
		{"/api/v1/sessions//turns", "NOT_FOUND", 404},
		{"/api/v1/sessions/psess-t1/turns/turn-1", "NOT_FOUND", 404},
		{"/api/v1/sessions/psess-t1/turns/turn-1/candidate/extra", "NOT_FOUND", 404},
		{"/api/v1/sessions/bad%20id", "UNKNOWN_SESSION", 404},
		{"/api/v1/sessions/..%2F..%2Fetc", "UNKNOWN_SESSION", 404},
		{"/api/v1/sessions/%00", "UNKNOWN_SESSION", 404},
		{"/api/v1/sessions/psess-t1/turns/turn-1x/candidate", "UNKNOWN_TURN", 404},
		{"/api/v1/sessions/psess-t1/turns/turn-/candidate", "UNKNOWN_TURN", 404},
		{"/api/v1/sessions/psess-t1/turns/psess-other%2Fturn-1/candidate", "UNKNOWN_TURN", 404},
		{"/api/v1/sessions/psess-t1/turns/psess-t1%2F..%2Fturn-1/candidate", "UNKNOWN_TURN", 404},
		{"/api/v1/sessions/psess-t1/turns/psess-t1%2Fturn-1%2Fx/candidate", "UNKNOWN_TURN", 404},
		// The read-only gateway's routes do not exist in live mode.
		{"/api/v1/snapshot", "NOT_FOUND", 404},
		{"/api/v1/events", "NOT_FOUND", 404},
		{"/api/v1/items/x", "NOT_FOUND", 404},
	}
	for _, c := range cases {
		response, body := h.api(http.MethodGet, c.path, "")
		if response.StatusCode != c.status || errorOf(t, body).Code != c.code {
			t.Errorf("GET %s: %d %s, want %d %s", c.path, response.StatusCode, body, c.status, c.code)
		}
	}
	if n := len(h.service.all()); n != 0 {
		t.Errorf("%d malformed requests reached the service", n)
	}
}

func TestLiveContractDescriptorSaysLive(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	response, body := h.api(http.MethodGet, "/api/v1/contract", "")
	descriptor := decodeMap(t, body)
	if response.StatusCode != http.StatusOK || descriptor["mode"] != "live" || descriptor["read_only"] != false || descriptor["csrf_header"] != CSRFHeader {
		t.Errorf("descriptor: %d %s", response.StatusCode, body)
	}
}

// --- errors --------------------------------------------------------------------

func TestServiceErrorsKeepTheirStableCodesWithAStatusMapping(t *testing.T) {
	cases := []struct {
		code   string
		status int
	}{
		{"PROTOCOL_ERROR", 400}, {"INVALID_REQUEST", 400}, {"UNKNOWN_PROJECT", 400}, {"UNKNOWN_PROVIDER_PROFILE", 400},
		{"PUBLICATION_MODE_NOT_PERMITTED", 400}, {"BUDGET_EXCEEDS_LIMIT", 400},
		{"UNAUTHORIZED_PRINCIPAL", 403},
		{"UNKNOWN_SESSION", 404}, {"UNKNOWN_TURN", 404},
		{"REQUEST_CONFLICT", 409}, {"STALE_REVISION", 409}, {"STALE_BASELINE", 409}, {"PROJECT_BUSY", 409}, {"PROJECT_BLOCKED", 409},
		{"TURN_ACTIVE", 409}, {"PENDING_REVIEW", 409}, {"PREDECESSOR_UNRESOLVED", 409}, {"PREDECESSOR_EVIDENCE_INVALID", 409},
		{"WRONG_TURN_STATE", 409}, {"CANDIDATE_MISMATCH", 409}, {"REVIEW_NOT_PENDING", 409}, {"CANCEL_NOT_ALLOWED", 409},
		{"BUDGET_EXHAUSTED", 409}, {"CANCELLED", 409},
		{"CATALOG_CORRUPT", 500},
		{"CANDIDATE_TOO_LARGE", 413},
		{"SERVICE_UNAVAILABLE", 503},
		{"A_FUTURE_CODE", 502}, // unknown, well formed: kept, as a bad gateway
	}
	for _, c := range cases {
		code := c.code
		h := newLiveHarness(t, func(ProductRequest) *ProductResponse { return errResp(code, "detail of "+code) })
		h.pairNow()
		for _, route := range allAPIRoutes[1:] { // every proxied route
			response, body := h.api(route.method, route.path, route.body)
			got := errorOf(t, body)
			if response.StatusCode != c.status || got.Code != code || got.Detail != "detail of "+code || got.OutcomeUnknown {
				t.Errorf("%s on %s: %d %+v, want %d", code, route.name, response.StatusCode, got, c.status)
			}
		}
	}
	h := newLiveHarness(t, func(ProductRequest) *ProductResponse { return errResp("free text <b>with markup</b>", "d") })
	h.pairNow()
	response, body := h.api(http.MethodGet, "/api/v1/sessions", "")
	if got := errorOf(t, body); response.StatusCode != http.StatusBadGateway || got.Code != CodeBadServiceResponse {
		t.Errorf("a malformed code: %d %s", response.StatusCode, body)
	}
}

func TestTransportFailuresAreServiceUnavailableAndMarkUnknownMutations(t *testing.T) {
	// Nobody listens: the request was never sent.
	down := newLiveHarnessOn(t, &fakeService{}, t.TempDir()+"/absent.sock")
	down.pairNow()
	for _, route := range allAPIRoutes[1:] {
		response, body := down.api(route.method, route.path, route.body)
		if got := errorOf(t, body); response.StatusCode != http.StatusServiceUnavailable || got.Code != CodeServiceUnavailable || got.OutcomeUnknown {
			t.Errorf("down, %s: %d %s", route.name, response.StatusCode, body)
		}
	}
	// The service reads the request and drops the connection: a mutation's outcome is unknown.
	h := newLiveHarness(t, func(ProductRequest) *ProductResponse { return nil })
	h.pairNow()
	for _, route := range allAPIRoutes[1:] {
		response, body := h.api(route.method, route.path, route.body)
		got := errorOf(t, body)
		if response.StatusCode != http.StatusServiceUnavailable || got.Code != CodeServiceUnavailable || got.OutcomeUnknown != (route.method == http.MethodPost) {
			t.Errorf("dropped, %s: %d %s", route.name, response.StatusCode, body)
		}
	}
}

func TestAMalformedServiceResponseIsABadGatewayNotAProjection(t *testing.T) {
	good := (&serviceModel{events: sampleEvents()}).respond
	cases := map[string]func(ProductRequest) *ProductResponse{
		"success without its projection": func(r ProductRequest) *ProductResponse {
			if r.Op == OpListSessions || r.Op == OpGetSession {
				return okResp(ProductResponse{})
			}
			return good(r)
		},
		"events that skip a sequence number": func(r ProductRequest) *ProductResponse {
			if r.Op == OpObserve {
				return okResp(ProductResponse{Events: []ProductEvent{{Seq: 5, Kind: EventTurnLinked, TurnID: testTurn}}, NextCursor: 1})
			}
			return good(r)
		},
		"a cursor that does not advance by the events returned": func(r ProductRequest) *ProductResponse {
			if r.Op == OpObserve {
				return okResp(ProductResponse{Events: sampleEvents()[:2], NextCursor: 9})
			}
			return good(r)
		},
	}
	for name, respond := range cases {
		h := newLiveHarness(t, respond)
		h.pairNow()
		for _, path := range []string{"/api/v1/sessions/psess-t1", "/api/v1/sessions/psess-t1/events"} {
			response, body := h.api(http.MethodGet, path, "")
			if response.StatusCode != http.StatusBadGateway && !(name == "success without its projection" && path == "/api/v1/sessions/psess-t1/events") {
				t.Errorf("%s on %s: %d %s", name, path, response.StatusCode, body)
			}
		}
		if response, body := h.api(http.MethodGet, "/api/v1/sessions", ""); name == "success without its projection" {
			if got := decodeMap(t, body); response.StatusCode != http.StatusOK || got["sessions"] == nil {
				t.Errorf("an empty session list is a valid answer and must be [] not null: %d %s", response.StatusCode, body)
			}
		}
	}
}

// --- long poll -----------------------------------------------------------------

func TestObserveBoundsTheLongPoll(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	h.pairNow()
	bad := []string{"wait=25001", "wait=30000", "wait=-1", "wait=abc", "wait=1.5", "cursor=abc", "cursor=-1", "cursor=99999999999999999999",
		"x=1", "cursor=1&cursor=2", "wait=1&wait=2", "cursor=1;wait=2", "cursor=%zz"}
	for _, query := range bad {
		response, body := h.api(http.MethodGet, "/api/v1/sessions/psess-t1/events?"+query, "")
		if response.StatusCode != http.StatusBadRequest || errorOf(t, body).Code != "INVALID_REQUEST" {
			t.Errorf("?%s: %d %s", query, response.StatusCode, body)
		}
	}
	if n := len(h.service.all()); n != 0 {
		t.Fatalf("%d out-of-bounds polls reached the service", n)
	}
	for query, want := range map[string]ProductObserve{
		"":                         {SessionID: testSession},
		"cursor=2":                 {SessionID: testSession, Cursor: 2},
		"wait=25000":               {SessionID: testSession, WaitMillis: 25000},
		"cursor=8&wait=0":          {SessionID: testSession, Cursor: 8},
		"wait=100&cursor=1":        {SessionID: testSession, Cursor: 1, WaitMillis: 100},
		"cursor=18446744073709551": {SessionID: testSession, Cursor: 18446744073709551},
	} {
		h.service.mu.Lock()
		h.service.requests = nil
		h.service.mu.Unlock()
		response, body := h.api(http.MethodGet, "/api/v1/sessions/psess-t1/events?"+query, "")
		calls := h.service.calls(OpObserve)
		if len(calls) != 1 || *calls[0].Observe != want {
			t.Errorf("?%s forwarded %+v, want %+v (status %d %s)", query, calls, want, response.StatusCode, body)
		}
	}
}

func TestObserveWaitsForTheServiceAndMapsEvents(t *testing.T) {
	delay := 1200 * time.Millisecond // longer than the harness read timeout
	model := &serviceModel{events: sampleEvents()}
	h := newLiveHarness(t, func(r ProductRequest) *ProductResponse {
		if r.Op == OpObserve {
			time.Sleep(delay)
		}
		return model.respond(r)
	})
	h.pairNow()
	start := time.Now()
	response, body := h.api(http.MethodGet, "/api/v1/sessions/psess-t1/events?cursor=6&wait=1000", "")
	if elapsed := time.Since(start); elapsed < delay || response.StatusCode != http.StatusOK {
		t.Fatalf("answered %v after %v: %s", response.StatusCode, elapsed, body)
	}
	var answer eventsAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Cursor != 6 || answer.NextCursor != 8 || answer.Gap || len(answer.Events) != 2 || len(answer.Items) != 2 || answer.SessionID != testSession {
		t.Fatalf("answer %+v", answer)
	}
	if answer.Events[0].Kind != EventToolResult || answer.Items[0].Type != ItemResult || answer.Items[0].Sequence != 7 || answer.Items[1].Sequence != 8 {
		t.Errorf("events[i] and items[i] must describe the same observation: %+v %+v", answer.Events, answer.Items)
	}
	// Past the end, the answer is an empty window with arrays, not nulls.
	response, body = h.api(http.MethodGet, "/api/v1/sessions/psess-t1/events?cursor=8", "")
	if got := decodeMap(t, body); response.StatusCode != http.StatusOK || got["events"] == nil || got["items"] == nil || got["next_cursor"] != float64(8) {
		t.Errorf("an empty window: %d %s", response.StatusCode, body)
	}
}

func TestTheGatewayCapsConcurrentServiceCalls(t *testing.T) {
	release := make(chan struct{})
	model := &serviceModel{events: sampleEvents()}
	h := newLiveHarness(t, func(r ProductRequest) *ProductResponse {
		if r.Op == OpObserve {
			<-release
		}
		return model.respond(r)
	})
	h.pairNow()
	var wg sync.WaitGroup
	for i := 0; i < maxInflightCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.api(http.MethodGet, "/api/v1/sessions/psess-t1/events?cursor=8&wait=25000", "")
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(h.gateway.slots) < maxInflightCalls && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	response, body := h.api(http.MethodGet, "/api/v1/sessions", "")
	if response.StatusCode != http.StatusServiceUnavailable || errorOf(t, body).Code != CodeServiceUnavailable {
		t.Errorf("over the cap: %d %s", response.StatusCode, body)
	}
	close(release)
	wg.Wait()
	if response, body := h.api(http.MethodGet, "/api/v1/sessions", ""); response.StatusCode != http.StatusOK {
		t.Errorf("after the polls finished: %d %s", response.StatusCode, body)
	}
}

// --- the snapshot route --------------------------------------------------------

func TestGetSessionRendersTheExistingSnapshotShapeFromGetSessionAndObserve(t *testing.T) {
	model := &serviceModel{events: sampleEvents(), pageSize: 3}
	h := newLiveHarness(t, model.respond)
	h.pairNow()
	response, body := h.api(http.MethodGet, "/api/v1/sessions/psess-t1", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatalf("the snapshot is not the Snapshot shape: %v", err)
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("the snapshot does not validate: %v", err)
	}
	if snapshot.Source != SourceLive || snapshot.Verification != VerificationStructuralAudit || snapshot.Epoch != testSession || snapshot.Cursor != testSession+":8" {
		t.Errorf("source=%s scope=%s epoch=%s cursor=%s", snapshot.Source, snapshot.Verification, snapshot.Epoch, snapshot.Cursor)
	}
	if len(snapshot.Items) != 8 || snapshot.Live == nil || snapshot.Live.Revision != 4 || len(snapshot.Live.Turns) != 1 {
		t.Fatalf("items=%d live=%+v", len(snapshot.Items), snapshot.Live)
	}
	for index, item := range snapshot.Items {
		if item.Sequence != uint64(index+1) || item.Authority != AuthorityObserved || item.Verification != VerificationStructuralAudit {
			t.Errorf("item %d: %+v", index, item)
		}
	}
	if snapshot.Panels.Governance.AllowCount == nil || *snapshot.Panels.Governance.AllowCount != 1 || *snapshot.Panels.Governance.DenyCount != 1 || *snapshot.Panels.Governance.UnknownCount != 0 {
		t.Errorf("governance counts: %+v", snapshot.Panels.Governance)
	}
	if snapshot.Session.Status != SessionPending || *snapshot.Session.TurnCount != 1 || snapshot.Panels.Qualification.G1 || snapshot.Panels.Qualification.ClaimBearing {
		t.Errorf("session %+v qualification %+v", snapshot.Session, snapshot.Panels.Qualification)
	}
	if !strings.Contains(strings.Join(snapshot.Warnings, " "), "occurred_at") {
		t.Errorf("the snapshot must say event times are not recorded: %v", snapshot.Warnings)
	}
	// get_session first, then observe pages from cursor 0 until an empty page.
	var cursors []uint64
	for _, call := range h.service.calls(OpObserve) {
		cursors = append(cursors, call.Observe.Cursor)
		if call.Observe.WaitMillis != 0 {
			t.Errorf("a snapshot must not wait: %+v", call.Observe)
		}
	}
	if fmt.Sprint(cursors) != "[0 3 6 8]" || h.service.all()[0].Op != OpGetSession {
		t.Errorf("observe cursors %v, first call %s", cursors, h.service.all()[0].Op)
	}
}

func TestAVeryLongSessionIsTruncatedAndSaysSo(t *testing.T) {
	events := make([]ProductEvent, MaxItems+52)
	for i := range events {
		events[i] = ProductEvent{Seq: uint64(i), Kind: EventAssistantMessage, TurnID: testTurn, Text: "m"}
	}
	model := &serviceModel{events: events, pageSize: MaxProductEvents}
	h := newLiveHarness(t, model.respond)
	h.pairNow()
	response, body := h.api(http.MethodGet, "/api/v1/sessions/psess-t1", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%d", response.StatusCode)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil || ValidateSnapshot(snapshot) != nil {
		t.Fatalf("snapshot: %v %v", err, ValidateSnapshot(snapshot))
	}
	if len(snapshot.Items) != MaxItems || snapshot.Panels.Governance.AllowCount != nil || snapshot.Streams[0].Health != StreamGap {
		t.Errorf("items=%d counts=%+v stream=%+v", len(snapshot.Items), snapshot.Panels.Governance, snapshot.Streams[0])
	}
	if !strings.Contains(strings.Join(snapshot.Warnings, " "), "more events") {
		t.Errorf("warnings %v", snapshot.Warnings)
	}
}

// --- model text is data ----------------------------------------------------------

const hostile = `<script>alert(1)</script><img src=x onerror=alert(2)>"'&</textarea>{{7*7}}`

func TestAnXSSLookingPromptIsPassedThroughAsData(t *testing.T) {
	events := []ProductEvent{
		{Seq: 0, Kind: EventTurnAdmitted, TurnID: testTurn, Text: hostile},
		{Seq: 1, Kind: EventAssistantMessage, TurnID: testTurn, Text: hostile},
		{Seq: 2, Kind: EventToolCall, TurnID: testTurn, Tool: "bash", ToolCallID: "call_1", Verdict: "DENY", ReasonCode: "policy_rule_deny", Hint: hostile},
	}
	model := &serviceModel{events: events}
	h := newLiveHarness(t, func(r ProductRequest) *ProductResponse {
		if r.Op == OpSubmitTurn || r.Op == OpCreateSession {
			return errResp("INVALID_REQUEST", hostile)
		}
		return model.respond(r)
	})
	h.pairNow()
	response, body := h.api(http.MethodPost, "/api/v1/sessions/psess-t1/turns", fmt.Sprintf(`{"expected_revision":4,"request_key":"k","prompt":%q}`, hostile))
	if got := h.service.calls(OpSubmitTurn); len(got) != 1 || got[0].SubmitTurn.Prompt != hostile {
		t.Errorf("the prompt must reach the service byte for byte: %+v", got)
	}
	targets := map[string][]byte{"submit error": body}
	_, snapshotBody := h.api(http.MethodGet, "/api/v1/sessions/psess-t1", "")
	_, eventsBody := h.api(http.MethodGet, "/api/v1/sessions/psess-t1/events", "")
	targets["snapshot"], targets["events"] = snapshotBody, eventsBody
	for name, data := range targets {
		if bytes.Contains(data, []byte("<script")) || bytes.Contains(data, []byte("<img")) {
			t.Errorf("%s carries raw markup: %s", name, data)
		}
		if !json.Valid(data) {
			t.Errorf("%s is not JSON", name)
		}
	}
	var snapshot Snapshot
	if err := json.Unmarshal(snapshotBody, &snapshot); err != nil || len(snapshot.Items) != 3 {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Items[0].Text != hostile || snapshot.Items[1].Text != hostile || snapshot.Items[2].Summary != hostile {
		t.Errorf("the text must arrive unchanged, as data: %q", snapshot.Items[0].Text)
	}
	if got := errorOf(t, body).Detail; got != hostile {
		t.Errorf("error detail %q", got)
	}
	if ct := response.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("the submit error answers %q", ct)
	}
}

// --- static routes and the pairing page --------------------------------------------

func TestPairingPageIsStaticAndHoldsNoSecret(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	page, pageBody := h.do(http.MethodGet, "/pair", reqOpts{})
	script, scriptBody := h.do(http.MethodGet, "/pair.js", reqOpts{})
	if page.StatusCode != http.StatusOK || script.StatusCode != http.StatusOK {
		t.Fatalf("page %d script %d", page.StatusCode, script.StatusCode)
	}
	if page.Header.Get("Content-Type") != "text/html; charset=utf-8" || script.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Errorf("content types %q %q", page.Header.Get("Content-Type"), script.Header.Get("Content-Type"))
	}
	assertGatewayHeaders(t, "/pair", page.Header)
	assertGatewayHeaders(t, "/pair.js", script.Header)
	html, js := string(pageBody), string(scriptBody)
	if strings.Contains(html, h.token) || strings.Contains(js, h.token) {
		t.Error("the pairing token is served")
	}
	// The CSP forbids inline script, so the page may only load /pair.js.
	if strings.Count(html, "<script") != 1 || !strings.Contains(html, `<script src="/pair.js"></script>`) || strings.Contains(html, " onclick=") {
		t.Errorf("the page must load only /pair.js: %s", html)
	}
	for _, banned := range []string{"innerHTML", "eval(", "document.write", "localStorage", "sessionStorage", "document.cookie"} {
		if strings.Contains(js, banned) {
			t.Errorf("pair.js uses %s", banned)
		}
	}
	if !strings.Contains(js, "location.hash") || !strings.Contains(js, `"/api/v1/pair"`) || !strings.Contains(js, "history.replaceState") {
		t.Error("pair.js must read the fragment, clear it and post to /api/v1/pair")
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if response, _ := h.do(method, "/pair", reqOpts{}); response.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /pair: %d", method, response.StatusCode)
		}
	}
}

func TestLiveModeKeepsServingTheDocumentRoutesUnderTheStrictHost(t *testing.T) {
	h := newLiveHarness(t, defaultService())
	for _, path := range []string{"/", "/app/", "/favicon.ico"} {
		response, _ := h.do(http.MethodGet, path, reqOpts{})
		if response.StatusCode >= 500 || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
			t.Errorf("GET %s: %d", path, response.StatusCode)
		}
		assertGatewayHeaders(t, path, response.Header)
	}
	if response, _ := h.do(http.MethodPost, "/app/", reqOpts{}); response.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /app/: %d", response.StatusCode)
	}
}

// --- demonstration modes are unchanged ----------------------------------------------

func TestDemonstrationModesExposeNoLiveRoutesAndStayGetOnly(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{AllowedHost: "127.0.0.1:8791", AllowedOrigin: "http://127.0.0.1:8791"}, embeddedApp())
	for _, target := range []string{"/pair", "/pair.js", "/api/v1/pair", "/api/v1/sessions", "/api/v1/sessions/psess-t1", "/api/v1/sessions/psess-t1/events"} {
		if recorder := appTestRequest(t, handler, http.MethodGet, target); recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s in a demonstration: %d, want 404", target, recorder.Code)
		}
	}
	for _, target := range []string{"/api/v1/pair", "/api/v1/sessions", "/api/v1/snapshot"} {
		if recorder := appTestRequest(t, handler, http.MethodPost, target); recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s in a demonstration: %d, want 405", target, recorder.Code)
		}
	}
	for _, target := range []string{"/api/v1/snapshot", "/api/v1/contract"} {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s: %d", target, recorder.Code)
		}
		if target == "/api/v1/contract" && (!strings.Contains(recorder.Body.String(), `"read_only":true`) || strings.Contains(recorder.Body.String(), `"mode"`)) {
			t.Errorf("the demonstration contract changed: %s", recorder.Body.String())
		}
	}
	// A demonstration never opens a socket and never takes a cookie.
	if recorder := appTestRequestFrom(t, handler, "/api/v1/snapshot", "localhost:8791", ""); recorder.Code != http.StatusForbidden {
		t.Errorf("a demonstration accepts only its bound Host: %d", recorder.Code)
	}
}

// --- gateway construction -----------------------------------------------------------

func TestNewLiveGatewayRefusesNonLoopbackAndMissingSocket(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8791", "192.168.1.5:8791", "localhost:8791", "127.0.0.1", ""} {
		if _, _, err := NewLiveGateway(LiveOptions{Socket: "/tmp/x.sock", BoundAddr: addr}); err == nil {
			t.Errorf("bound address %q was accepted", addr)
		}
	}
	if _, _, err := NewLiveGateway(LiveOptions{BoundAddr: "127.0.0.1:8791"}); err == nil {
		t.Error("a gateway without a socket was accepted")
	}
	first, tokenA, _ := NewLiveGateway(LiveOptions{Socket: "/tmp/x.sock", BoundAddr: "[::1]:8791"})
	_, tokenB, _ := NewLiveGateway(LiveOptions{Socket: "/tmp/x.sock", BoundAddr: "[::1]:8791"})
	if tokenA == tokenB || len(tokenA) < 43 {
		t.Errorf("pairing tokens must be 32 random bytes, base64url: %q %q", tokenA, tokenB)
	}
	if !first.hosts["[::1]:8791"] || !first.hosts["localhost:8791"] || len(first.hosts) != 2 {
		t.Errorf("allowed hosts %v", first.hosts)
	}
}
