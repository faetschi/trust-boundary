package webchat

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The live gateway holds no authority. It pairs one browser, checks Host, Origin,
// the session cookie and the CSRF header, forwards each request as one product
// protocol call over the user's socket, and renders the answer. The service sees
// the user's UID as the principal; the browser gains nothing beyond that.

const (
	SessionCookieName      = "tbound_session"
	CSRFHeader             = "X-TBound-CSRF"
	PairingLifetime        = 10 * time.Minute
	BrowserSessionLifetime = 12 * time.Hour
	// MaxBodyBytes bounds a browser request body.
	MaxBodyBytes = 64 << 10
	// MaxBrowserObserveWaitMillis is the longest wait a browser may request. The
	// protocol allows 30 s; the gateway stops at 25 s so a proxy or tunnel does not cut first.
	MaxBrowserObserveWaitMillis = 25000
	// maxInflightCalls bounds the product socket calls open at once, long polls included.
	maxInflightCalls = 32
	// snapshotMaxPages bounds the observe calls that build one snapshot.
	snapshotMaxPages = 16
	maxErrorDetail   = 512
)

var (
	turnSuffixPattern = regexp.MustCompile(`^turn-[0-9]{1,9}$`)
	turnIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}/turn-[0-9]{1,9}$`)
	errorCodePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// LiveOptions configures a live gateway.
type LiveOptions struct {
	// Socket is the product service's Unix socket.
	Socket string
	// BoundAddr is the listener's literal loopback host:port. The only Host values
	// accepted are BoundAddr and localhost on the same port.
	BoundAddr string
	// Now defaults to time.Now.
	Now func() time.Time
	// caller replaces the socket client in tests.
	caller productCaller
}

// LiveGateway is the browser-facing side of the product protocol.
type LiveGateway struct {
	caller productCaller
	hosts  map[string]bool
	now    func() time.Time
	slots  chan struct{}

	mu          sync.Mutex
	pairHash    [sha256.Size]byte
	pairExpires time.Time
	pairSpent   bool
	paired      *browserSession
}

// browserSession is the one paired browser. The pairing token is single-use, so a
// gateway process pairs at most one browser; a new pairing needs a restart.
type browserSession struct {
	cookieHash [sha256.Size]byte
	csrf       string
	expires    time.Time
}

// NewLiveGateway creates the gateway and its one-time pairing token. The token is
// returned for the terminal and kept only as a hash.
func NewLiveGateway(options LiveOptions) (*LiveGateway, string, error) {
	host, port, err := net.SplitHostPort(options.BoundAddr)
	if err != nil {
		return nil, "", fmt.Errorf("live gateway needs the bound host:port: %w", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, "", errors.New("live gateway binds a literal loopback address only")
	}
	caller := options.caller
	if caller == nil {
		if strings.TrimSpace(options.Socket) == "" {
			return nil, "", errors.New("live gateway needs a product socket path")
		}
		caller = socketClient{path: options.Socket}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	token, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	gateway := &LiveGateway{
		caller:      caller,
		hosts:       map[string]bool{options.BoundAddr: true, "localhost:" + port: true},
		now:         now,
		slots:       make(chan struct{}, maxInflightCalls),
		pairHash:    sha256.Sum256([]byte(token)),
		pairExpires: now().Add(PairingLifetime),
	}
	return gateway, token, nil
}

// Handler serves the live API and the pairing page, and the document and asset
// routes of the read-only gateway.
func (g *LiveGateway) Handler() http.Handler {
	return newHandler(nil, HandlerOptions{Live: g}, embeddedApp())
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("random source: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// authorizeHostOrigin is the DNS-rebinding and cross-origin defence. Host must be
// the bound address or localhost on its port. An Origin header, when present, must
// be exactly http:// plus that Host. No CORS header is ever sent.
func (g *LiveGateway) authorizeHostOrigin(writer http.ResponseWriter, request *http.Request) bool {
	if !g.hosts[request.Host] {
		writeAPIError(writer, http.StatusForbidden, "FORBIDDEN", "host not allowed", false)
		return false
	}
	if origins := request.Header.Values("Origin"); len(origins) > 0 && (len(origins) > 1 || origins[0] != "http://"+request.Host) {
		writeAPIError(writer, http.StatusForbidden, "FORBIDDEN", "origin not allowed", false)
		return false
	}
	return true
}

func (g *LiveGateway) handles(request *http.Request) bool {
	path := request.URL.EscapedPath()
	return strings.HasPrefix(path, "/api/") || path == pairPagePath || path == pairScriptPath
}

// liveRoute is one matched API route.
type liveRoute struct {
	name    string
	methods []string
	session string
	turn    string
}

func matchLiveRoute(segments []string) (liveRoute, bool) {
	get, post, both := []string{http.MethodGet}, []string{http.MethodPost}, []string{http.MethodGet, http.MethodPost}
	switch {
	case len(segments) == 1 && segments[0] == "pair":
		return liveRoute{name: "pair", methods: both}, true
	case len(segments) == 1 && segments[0] == "contract":
		return liveRoute{name: "contract", methods: get}, true
	case len(segments) == 1 && segments[0] == "sessions":
		return liveRoute{name: "sessions", methods: both}, true
	case len(segments) == 2 && segments[0] == "sessions":
		return liveRoute{name: "session", methods: get, session: segments[1]}, true
	case len(segments) == 3 && segments[0] == "sessions" && segments[2] == "events":
		return liveRoute{name: "events", methods: get, session: segments[1]}, true
	case len(segments) == 3 && segments[0] == "sessions" && segments[2] == "turns":
		return liveRoute{name: "turns", methods: post, session: segments[1]}, true
	case len(segments) == 5 && segments[0] == "sessions" && segments[2] == "turns":
		switch segments[4] {
		case "candidate":
			return liveRoute{name: "candidate", methods: get, session: segments[1], turn: segments[3]}, true
		case "cancel", "resolve":
			return liveRoute{name: segments[4], methods: post, session: segments[1], turn: segments[3]}, true
		}
	}
	return liveRoute{}, false
}

// serve answers the pairing page and every /api/ request. Order: cookie, route and
// method, CSRF for a mutation, then the body.
func (g *LiveGateway) serve(writer http.ResponseWriter, request *http.Request) {
	escaped := request.URL.EscapedPath()
	if escaped == pairPagePath || escaped == pairScriptPath {
		servePairAsset(writer, request, escaped)
		return
	}
	segments, ok := apiSegments(escaped)
	route, matched := liveRoute{}, false
	if ok {
		route, matched = matchLiveRoute(segments)
	}
	isPairPost := matched && route.name == "pair" && request.Method == http.MethodPost
	var browser *browserSession
	if !isPairPost {
		if browser = g.authenticate(request); browser == nil {
			writeAPIError(writer, http.StatusUnauthorized, "UNAUTHENTICATED", "a paired session cookie is required", false)
			return
		}
	}
	if !matched {
		writeAPIError(writer, http.StatusNotFound, "NOT_FOUND", "no such route", false)
		return
	}
	if !containsString(route.methods, request.Method) {
		writer.Header().Set("Allow", strings.Join(route.methods, ", "))
		writeAPIError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
		return
	}
	if request.Method == http.MethodPost && !isPairPost {
		got := request.Header.Get(CSRFHeader)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(browser.csrf)) != 1 {
			writeAPIError(writer, http.StatusForbidden, "FORBIDDEN", "a valid "+CSRFHeader+" header is required", false)
			return
		}
	}
	if request.Method != http.MethodPost && (request.ContentLength != 0 || len(request.TransferEncoding) > 0) {
		writer.Header().Set("Connection", "close")
		writeAPIError(writer, http.StatusBadRequest, "INVALID_REQUEST", "request body not allowed", false)
		return
	}
	if route.session != "" && !identifierPattern.MatchString(route.session) {
		writeAPIError(writer, http.StatusNotFound, "UNKNOWN_SESSION", "no such session", false)
		return
	}
	if route.turn != "" {
		turn, ok := fullTurnID(route.session, route.turn)
		if !ok {
			writeAPIError(writer, http.StatusNotFound, "UNKNOWN_TURN", "no such turn", false)
			return
		}
		route.turn = turn
	}
	switch route.name {
	case "pair":
		if request.Method == http.MethodPost {
			g.pair(writer, request)
		} else {
			writeJSON(writer, pairingState{CSRFToken: browser.csrf, ExpiresAt: browser.expires.UTC()})
		}
	case "contract":
		writeJSON(writer, liveContractDescriptor())
	case "sessions":
		if request.Method == http.MethodPost {
			g.createSession(writer, request)
		} else {
			g.listSessions(writer, request)
		}
	case "session":
		g.getSession(writer, request, route.session)
	case "events":
		g.observe(writer, request, route.session)
	case "turns":
		g.submitTurn(writer, request, route.session)
	case "candidate":
		g.getCandidate(writer, request, route.session, route.turn)
	case "cancel":
		g.cancelTurn(writer, request, route.session, route.turn)
	case "resolve":
		g.resolveTurn(writer, request, route.session, route.turn)
	}
}

// apiSegments splits an escaped /api/v1/ path into unescaped segments. A percent
// encoded slash stays inside its segment, so a turn ID such as psess-x/turn-1 can
// travel as one segment. An empty segment is not a route.
func apiSegments(escaped string) ([]string, bool) {
	rest, ok := strings.CutPrefix(escaped, "/api/v1/")
	if !ok || rest == "" {
		return nil, false
	}
	parts := strings.Split(rest, "/")
	for index, part := range parts {
		unescaped, err := url.PathUnescape(part)
		if err != nil || unescaped == "" {
			return nil, false
		}
		parts[index] = unescaped
	}
	return parts, true
}

// fullTurnID accepts the short turn-N form or the full ID of a turn of this session.
func fullTurnID(session, segment string) (string, bool) {
	if turnSuffixPattern.MatchString(segment) {
		return session + "/" + segment, true
	}
	if turnIDPattern.MatchString(segment) && strings.HasPrefix(segment, session+"/") {
		return segment, true
	}
	return "", false
}

func containsString(list []string, value string) bool {
	for _, entry := range list {
		if entry == value {
			return true
		}
	}
	return false
}

// authenticate returns the paired browser whose cookie the request carries, or nil.
// An expired session is dropped.
func (g *LiveGateway) authenticate(request *http.Request) *browserSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paired == nil {
		return nil
	}
	if !g.now().Before(g.paired.expires) {
		g.paired = nil
		return nil
	}
	for _, cookie := range request.Cookies() {
		if cookie.Name != SessionCookieName {
			continue
		}
		digest := sha256.Sum256([]byte(cookie.Value))
		if subtle.ConstantTimeCompare(digest[:], g.paired.cookieHash[:]) == 1 {
			return g.paired
		}
	}
	return nil
}

type pairBody struct {
	Token string `json:"token"`
}

type pairingState struct {
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// pair spends the pairing token and sets the session cookie. The token is spent by
// the first correct use. A wrong or late token changes nothing.
func (g *LiveGateway) pair(writer http.ResponseWriter, request *http.Request) {
	var body pairBody
	if !readBody(writer, request, &body) {
		return
	}
	digest := sha256.Sum256([]byte(body.Token))
	now := g.now()
	cookie, csrf, err := "", "", error(nil)
	g.mu.Lock()
	valid := !g.pairSpent && now.Before(g.pairExpires) && subtle.ConstantTimeCompare(digest[:], g.pairHash[:]) == 1
	var browser *browserSession
	if valid {
		g.pairSpent = true
		if cookie, err = randomToken(); err == nil {
			csrf, err = randomToken()
		}
		if err == nil {
			browser = &browserSession{cookieHash: sha256.Sum256([]byte(cookie)), csrf: csrf, expires: now.Add(BrowserSessionLifetime)}
			g.paired = browser
		}
	}
	g.mu.Unlock()
	switch {
	case !valid:
		writeAPIError(writer, http.StatusUnauthorized, "UNAUTHENTICATED", "the pairing token is invalid, already used or expired", false)
	case err != nil:
		writeAPIError(writer, http.StatusInternalServerError, "INTERNAL", "could not create a session", false)
	default:
		http.SetCookie(writer, &http.Cookie{
			Name: SessionCookieName, Value: cookie, Path: "/", Expires: browser.expires, MaxAge: int(BrowserSessionLifetime / time.Second),
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		writeJSON(writer, pairingState{CSRFToken: csrf, ExpiresAt: browser.expires.UTC()})
	}
}

// readBody reads at most MaxBodyBytes and decodes strictly into v. It writes the
// refusal itself.
func readBody(writer http.ResponseWriter, request *http.Request, v any) bool {
	data, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, MaxBodyBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeAPIError(writer, http.StatusRequestEntityTooLarge, "INVALID_REQUEST", fmt.Sprintf("request body exceeds %d bytes", MaxBodyBytes), false)
		return false
	case err != nil:
		writeAPIError(writer, http.StatusBadRequest, "INVALID_REQUEST", "request body could not be read", false)
		return false
	case len(data) == 0:
		writeAPIError(writer, http.StatusBadRequest, "INVALID_REQUEST", "a JSON body is required", false)
		return false
	}
	if err := strictDecode(data, v); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "INVALID_REQUEST", "body is not strict JSON for this route: "+err.Error(), false)
		return false
	}
	return true
}

// Browser bodies are the protocol payloads without the fields the path supplies.
// Every field is the client's. The gateway fills in nothing, and it refuses a
// missing request key rather than inventing one.
type submitBody struct {
	ExpectedRevision       uint64 `json:"expected_revision"`
	RequestKey             string `json:"request_key"`
	Prompt                 string `json:"prompt"`
	ExpectedBaselineDigest string `json:"expected_baseline_digest,omitempty"`
}

type cancelBody struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
}

type resolveBody struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
	CandidateDigest  string `json:"candidate_digest"`
	Decision         string `json:"decision"`
}

func requireKey(writer http.ResponseWriter, key string) bool {
	if key == "" {
		writeAPIError(writer, http.StatusBadRequest, "INVALID_REQUEST", "request_key is required; the client chooses it and reuses it to retry", false)
		return false
	}
	return true
}

func (g *LiveGateway) createSession(writer http.ResponseWriter, request *http.Request) {
	var body ProductCreateSession
	if !readBody(writer, request, &body) || !requireKey(writer, body.RequestKey) {
		return
	}
	response, ok := g.forward(writer, request, ProductRequest{Op: OpCreateSession, CreateSession: &body})
	if !ok || response.Session == nil {
		missingPayload(writer, ok)
		return
	}
	writeJSON(writer, struct {
		Session  *ProductSession `json:"session"`
		Replayed bool            `json:"replayed,omitempty"`
	}{response.Session, response.Replayed})
}

func (g *LiveGateway) listSessions(writer http.ResponseWriter, request *http.Request) {
	response, ok := g.forward(writer, request, ProductRequest{Op: OpListSessions})
	if !ok {
		return
	}
	sessions := response.Sessions
	if sessions == nil {
		sessions = []ProductSession{}
	}
	writeJSON(writer, struct {
		Sessions []ProductSession `json:"sessions"`
	}{sessions})
}

func (g *LiveGateway) getSession(writer http.ResponseWriter, request *http.Request, session string) {
	snapshot, ok := g.snapshot(writer, request, session)
	if ok {
		writeJSON(writer, snapshot)
	}
}

type turnAnswer struct {
	Turn     *ProductTurn `json:"turn"`
	Replayed bool         `json:"replayed,omitempty"`
}

func (g *LiveGateway) submitTurn(writer http.ResponseWriter, request *http.Request, session string) {
	var body submitBody
	if !readBody(writer, request, &body) || !requireKey(writer, body.RequestKey) {
		return
	}
	g.forwardTurn(writer, request, ProductRequest{Op: OpSubmitTurn, SubmitTurn: &ProductSubmitTurn{
		SessionID: session, ExpectedRevision: body.ExpectedRevision, RequestKey: body.RequestKey,
		Prompt: body.Prompt, ExpectedBaselineDigest: body.ExpectedBaselineDigest}})
}

func (g *LiveGateway) cancelTurn(writer http.ResponseWriter, request *http.Request, session, turn string) {
	var body cancelBody
	if !readBody(writer, request, &body) || !requireKey(writer, body.RequestKey) {
		return
	}
	g.forwardTurn(writer, request, ProductRequest{Op: OpCancelTurn, CancelTurn: &ProductCancelTurn{
		SessionID: session, TurnID: turn, ExpectedRevision: body.ExpectedRevision, RequestKey: body.RequestKey}})
}

// resolveTurn forwards the candidate digest exactly as the client gives it, so a
// stale diff cannot authorize a changed candidate. The service compares it.
func (g *LiveGateway) resolveTurn(writer http.ResponseWriter, request *http.Request, session, turn string) {
	var body resolveBody
	if !readBody(writer, request, &body) || !requireKey(writer, body.RequestKey) {
		return
	}
	g.forwardTurn(writer, request, ProductRequest{Op: OpResolveTurn, ResolveTurn: &ProductResolveTurn{
		SessionID: session, TurnID: turn, ExpectedRevision: body.ExpectedRevision, RequestKey: body.RequestKey,
		CandidateDigest: body.CandidateDigest, Decision: body.Decision}})
}

func (g *LiveGateway) forwardTurn(writer http.ResponseWriter, request *http.Request, call ProductRequest) {
	response, ok := g.forward(writer, request, call)
	if !ok || response.Turn == nil {
		missingPayload(writer, ok)
		return
	}
	writeJSON(writer, turnAnswer{Turn: response.Turn, Replayed: response.Replayed})
}

func (g *LiveGateway) getCandidate(writer http.ResponseWriter, request *http.Request, session, turn string) {
	response, ok := g.forward(writer, request, ProductRequest{Op: OpGetCandidate, GetCandidate: &ProductTurnRef{SessionID: session, TurnID: turn}})
	if !ok || response.Candidate == nil {
		missingPayload(writer, ok)
		return
	}
	writeJSON(writer, struct {
		Candidate *ProductCandidate `json:"candidate"`
	}{response.Candidate})
}

// eventsAnswer pairs each product event with the item the existing UI renders for
// it. events[i] and items[i] describe the same observation, so the client reads the
// lifecycle kind from the event and the transcript line from the item.
type eventsAnswer struct {
	SchemaVersion string         `json:"schema_version"`
	SessionID     string         `json:"session_id"`
	Cursor        uint64         `json:"cursor"`
	NextCursor    uint64         `json:"next_cursor"`
	Gap           bool           `json:"gap"`
	Events        []ProductEvent `json:"events"`
	Items         []Item         `json:"items"`
}

func (g *LiveGateway) observe(writer http.ResponseWriter, request *http.Request, session string) {
	cursor, wait, err := parseObserveQuery(request.URL.RawQuery)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), false)
		return
	}
	response, ok := g.forward(writer, request, ProductRequest{Op: OpObserve, Observe: &ProductObserve{SessionID: session, Cursor: cursor, WaitMillis: wait}})
	if !ok {
		return
	}
	if err := checkEvents(response.Events, cursor, response.NextCursor); err != nil {
		writeAPIError(writer, http.StatusBadGateway, CodeBadServiceResponse, err.Error(), false)
		return
	}
	events := response.Events
	if events == nil {
		events = []ProductEvent{}
	}
	writeJSON(writer, eventsAnswer{
		SchemaVersion: ContractVersion, SessionID: session, Cursor: cursor, NextCursor: response.NextCursor, Gap: response.Gap,
		Events: events, Items: eventItems(session, events, g.now().UTC()),
	})
}

// parseObserveQuery accepts only cursor and wait, each once. wait is in
// milliseconds and at most MaxBrowserObserveWaitMillis.
func parseObserveQuery(raw string) (cursor uint64, wait int64, err error) {
	values, parseErr := url.ParseQuery(raw)
	if parseErr != nil {
		return 0, 0, errors.New("query is not valid")
	}
	for key, list := range values {
		if (key != "cursor" && key != "wait") || len(list) != 1 {
			return 0, 0, fmt.Errorf("query parameter %q is not allowed or is repeated", key)
		}
	}
	if text := values.Get("cursor"); text != "" {
		if cursor, err = strconv.ParseUint(text, 10, 64); err != nil {
			return 0, 0, errors.New("cursor must be a non-negative integer")
		}
	}
	if text := values.Get("wait"); text != "" {
		if wait, err = strconv.ParseInt(text, 10, 64); err != nil || wait < 0 || wait > MaxBrowserObserveWaitMillis {
			return 0, 0, fmt.Errorf("wait must be an integer from 0 to %d milliseconds", MaxBrowserObserveWaitMillis)
		}
	}
	return cursor, wait, nil
}

// checkEvents refuses a window whose sequence numbers do not continue the cursor,
// because the gateway would otherwise label items with positions the service never
// gave.
func checkEvents(events []ProductEvent, cursor, next uint64) error {
	if len(events) > MaxProductEvents || next != cursor+uint64(len(events)) {
		return errors.New("the observe window does not continue the cursor")
	}
	for index, event := range events {
		if event.Seq != cursor+uint64(index) {
			return errors.New("an event has an unexpected sequence number")
		}
	}
	return nil
}

// snapshot reads the session, then its events from the start, and renders the
// existing snapshot shape. The events are read after the session, so a stale
// revision in the view fails safe: the service refuses it.
func (g *LiveGateway) snapshot(writer http.ResponseWriter, request *http.Request, session string) (Snapshot, bool) {
	response, ok := g.forward(writer, request, ProductRequest{Op: OpGetSession, GetSession: &ProductSessionRef{SessionID: session}})
	if !ok || response.Session == nil {
		missingPayload(writer, ok)
		return Snapshot{}, false
	}
	view := response.Session
	var events []ProductEvent
	var cursor uint64
	truncated := true
	for page := 0; page < snapshotMaxPages; page++ {
		window, ok := g.forward(writer, request, ProductRequest{Op: OpObserve, Observe: &ProductObserve{SessionID: session, Cursor: cursor}})
		if !ok {
			return Snapshot{}, false
		}
		if err := checkEvents(window.Events, cursor, window.NextCursor); err != nil {
			writeAPIError(writer, http.StatusBadGateway, CodeBadServiceResponse, err.Error(), false)
			return Snapshot{}, false
		}
		if len(window.Events) == 0 {
			truncated = false
			break
		}
		room := MaxItems - len(events)
		if room == 0 { // this window is the probe: events exist beyond the cap
			break
		}
		if len(window.Events) > room {
			window.Events = window.Events[:room]
		}
		events = append(events, window.Events...)
		cursor += uint64(len(window.Events))
	}
	snapshot, err := renderSnapshot(view, events, truncated, g.now().UTC())
	if err != nil {
		writeAPIError(writer, http.StatusBadGateway, CodeBadServiceResponse, "the session cannot be projected: "+err.Error(), false)
		return Snapshot{}, false
	}
	return snapshot, true
}

var mutatingOps = map[string]bool{OpCreateSession: true, OpSubmitTurn: true, OpCancelTurn: true, OpResolveTurn: true}

// forward makes one product call and maps a failure to its HTTP answer. It reports
// whether the call succeeded. A client that went away gets no answer.
func (g *LiveGateway) forward(writer http.ResponseWriter, request *http.Request, call ProductRequest) (*ProductResponse, bool) {
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		writeAPIError(writer, http.StatusServiceUnavailable, CodeServiceUnavailable, "too many calls to the product service are open", false)
		return nil, false
	}
	response, err := g.caller.Call(request.Context(), call)
	if err == nil {
		return response, true
	}
	var service *ServiceError
	var transport *TransportError
	switch {
	case errors.As(err, &service):
		writeAPIError(writer, statusForCode(service.Code), publicCode(service.Code), service.Detail, false)
	case errors.As(err, &transport):
		detail := "the product service is not reachable"
		if mutatingOps[call.Op] && transport.Sent {
			detail = "the connection failed after the request was sent; the outcome is unknown, so retry only with the same request_key"
		}
		writeAPIError(writer, http.StatusServiceUnavailable, CodeServiceUnavailable, detail, mutatingOps[call.Op] && transport.Sent)
	case request.Context().Err() != nil:
		// the browser left; there is nobody to answer
	default:
		writeAPIError(writer, http.StatusServiceUnavailable, CodeServiceUnavailable, "the product service call failed", false)
	}
	return nil, false
}

// missingPayload answers a success response that lacks its projection. When the
// call itself failed forward already answered.
func missingPayload(writer http.ResponseWriter, answered bool) {
	if answered {
		writeAPIError(writer, http.StatusBadGateway, CodeBadServiceResponse, "the service response has no projection", false)
	}
}

var codeStatuses = map[string]int{
	CodeProtocolError: http.StatusBadRequest, "INVALID_REQUEST": http.StatusBadRequest,
	"UNKNOWN_PROJECT": http.StatusBadRequest, "UNKNOWN_PROVIDER_PROFILE": http.StatusBadRequest,
	"PUBLICATION_MODE_NOT_PERMITTED": http.StatusBadRequest, "BUDGET_EXCEEDS_LIMIT": http.StatusBadRequest,
	"UNAUTHORIZED_PRINCIPAL": http.StatusForbidden,
	"UNKNOWN_SESSION":        http.StatusNotFound, "UNKNOWN_TURN": http.StatusNotFound,
	"REQUEST_CONFLICT": http.StatusConflict, "STALE_REVISION": http.StatusConflict, "STALE_BASELINE": http.StatusConflict,
	"PROJECT_BUSY": http.StatusConflict, "PROJECT_BLOCKED": http.StatusConflict, "TURN_ACTIVE": http.StatusConflict,
	"PENDING_REVIEW": http.StatusConflict, "PREDECESSOR_UNRESOLVED": http.StatusConflict,
	"PREDECESSOR_EVIDENCE_INVALID": http.StatusConflict, "WRONG_TURN_STATE": http.StatusConflict,
	"CANDIDATE_MISMATCH": http.StatusConflict, "REVIEW_NOT_PENDING": http.StatusConflict,
	"CANCEL_NOT_ALLOWED": http.StatusConflict, "BUDGET_EXHAUSTED": http.StatusConflict, "CANCELLED": http.StatusConflict,
	"CATALOG_CORRUPT":      http.StatusInternalServerError,
	CodeServiceUnavailable: http.StatusServiceUnavailable,
	CodeBadServiceResponse: http.StatusBadGateway,
}

// statusForCode maps a service code to its HTTP status. A code this gateway does
// not know is a bad gateway: the gateway cannot tell whether the request failed.
func statusForCode(code string) int {
	if status, ok := codeStatuses[code]; ok {
		return status
	}
	return http.StatusBadGateway
}

// publicCode keeps a well-formed service code as is and replaces any other string,
// so a service cannot put free text in the code field.
func publicCode(code string) string {
	if errorCodePattern.MatchString(code) {
		return code
	}
	return CodeBadServiceResponse
}

type apiError struct {
	Error apiErrorDetail `json:"error"`
}

type apiErrorDetail struct {
	Code           string `json:"code"`
	Detail         string `json:"detail,omitempty"`
	OutcomeUnknown bool   `json:"outcome_unknown,omitempty"`
}

func writeAPIError(writer http.ResponseWriter, status int, code, detail string, outcomeUnknown bool) {
	if len(detail) > maxErrorDetail {
		detail, _ = cleanText(detail, maxErrorDetail)
	}
	writeJSONStatus(writer, status, apiError{apiErrorDetail{Code: code, Detail: detail, OutcomeUnknown: outcomeUnknown}})
}

// writeJSONStatus writes a JSON body under the gateway's headers.
func writeJSONStatus(writer http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		http.Error(writer, "response encoding failed", http.StatusInternalServerError)
		return
	}
	setSecurityHeaders(writer, "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = boundedWrite(writer, func() error {
		_, err := writer.Write(append(encoded, '\n'))
		return err
	})
}

// liveContractDescriptor is the contract descriptor of a live gateway. The mode
// key is how a client tells a live gateway from a demonstration.
func liveContractDescriptor() map[string]any {
	descriptor := contractDescriptor()
	descriptor["mode"] = "live"
	descriptor["read_only"] = false
	descriptor["controls"] = []string{OpCreateSession, OpSubmitTurn, OpCancelTurn, OpResolveTurn}
	descriptor["csrf_header"] = CSRFHeader
	descriptor["session_cookie"] = SessionCookieName
	descriptor["observe_wait_max_ms"] = MaxBrowserObserveWaitMillis
	descriptor["protocol"] = ProductVersion
	descriptor["note"] = "live projection of the product service; the gateway forwards requests under the user's principal and holds no policy, effect, or publication authority"
	return descriptor
}

const (
	pairPagePath   = "/pair"
	pairScriptPath = "/pair.js"
)

// The pairing page reads the token from the URL fragment, which the browser never
// sends to a server, posts it, and moves on to the app. Both files are fixed text.
const pairPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>TBound web chat: pairing</title></head>
<body><main><h1>TBound web chat pairing</h1><p id="pair-status" role="status">Pairing.</p><noscript><p>This page needs JavaScript to pair.</p></noscript></main><script src="/pair.js"></script></body>
</html>
`

const pairScript = `(async () => {
  const status = document.getElementById("pair-status");
  const token = location.hash.slice(1);
  history.replaceState(null, "", location.pathname);
  if (!token) {
    status.textContent = "No pairing token. Open the pairing URL that tbound-webchat printed in the terminal.";
    return;
  }
  try {
    const response = await fetch("/api/v1/pair", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token }),
    });
    if (!response.ok) {
      status.textContent = "Pairing was refused (HTTP " + response.status + "). A token works once and for 10 minutes. Restart tbound-webchat for a new one.";
      return;
    }
    status.textContent = "Paired. Opening the chat.";
    location.replace("/app/");
  } catch (error) {
    status.textContent = "The gateway could not be reached.";
  }
})();
`

func servePairAsset(writer http.ResponseWriter, request *http.Request, path string) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeAPIError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
		return
	}
	contentType, content := "text/html; charset=utf-8", pairPage
	if path == pairScriptPath {
		contentType, content = "text/javascript; charset=utf-8", pairScript
	}
	setSecurityHeaders(writer, contentType)
	_ = boundedWrite(writer, func() error {
		_, err := io.WriteString(writer, content)
		return err
	})
}
