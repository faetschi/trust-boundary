// Package sessioncontrol owns the small authenticated control boundary for a
// browser-controlled session. It is deliberately not the observability
// viewer: mutations, worker ownership, admission, and bounded live events live
// here, while the existing viewer remains unchanged.
package sessioncontrol

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	ModeFixture = "fixture"
	ModeReal    = "real"

	TrustFixture = "fixture"
	TrustServer  = "server"

	MaxPromptBytes   = 16 << 10
	MaxSessions      = 4
	MaxTurns         = 16
	MaxEvents        = 256
	MaxEventText     = 64 << 10
	MaxEventBytes    = 512 << 10
	MaxJSONBytes     = 128 << 10
	MaxSSEEvents     = 128
	SSEIdleTimeout   = 15 * time.Second
	WorkerStopBudget = 3 * time.Second
)

var (
	ErrUnauthorized      = errors.New("session control authentication required")
	ErrForbidden         = errors.New("session control request is not permitted")
	ErrNotFound          = errors.New("session control session not found")
	ErrBusy              = errors.New("session is already processing a turn")
	ErrCapacity          = errors.New("session control capacity exceeded")
	ErrRealLaunchRefused = errors.New("real Pi launch is refused until closure, containment, source, offline-profile, verifier, and cleanup gates are reviewed")
	ErrMalformedRequest  = errors.New("malformed session control request")
)

// WorkerEvent is intentionally content-only. All correlation and trust fields
// in the public event projection are assigned by Manager, never by a worker or
// browser client.
type WorkerEvent struct {
	Kind  string
	Text  string
	Error string
}

// SessionIdentity is server-owned identity passed to a trusted factory.
type SessionIdentity struct {
	ID         string
	Generation string
}

// ToolDecision is emitted by the server-side IPC/gate bridge. A worker cannot
// supply public correlation references; Manager derives those references from
// this registered backend decision and the session identity.
type ToolDecision struct {
	Generation string
	Sequence   uint64
	ResponseID string
	ToolCallID string
	Tool       string
	Verdict    string
	Reason     string
	Status     string
}

type WorkerCallbacks struct {
	Event        func(WorkerEvent)
	ToolDecision func(ToolDecision)
}

// Worker is the only session-control worker seam. Prompt must return after the
// requested turn settles or the context is canceled. Stop must close admission
// before attempting transport/process cancellation; uncertain cleanup is
// represented as UNKNOWN by Manager.
type Worker interface {
	Prompt(context.Context, string) error
	Stop(context.Context) error
}

// WorkerFactory is trusted server configuration. It is not populated from an
// HTTP request. The fixture factory is the only factory registered by the
// current command, and it must have no real provider or filesystem effects.
type WorkerFactory func(context.Context, SessionIdentity, WorkerCallbacks) (Worker, error)

type Config struct {
	AuthToken      string
	Owner          string
	FixtureFactory WorkerFactory
	MaxSessions    int
	Lifetime       context.Context
}

type Manager struct {
	mu             sync.Mutex
	owner          string
	token          string
	fixture        WorkerFactory
	maxSessions    int
	lifetime       context.Context
	lifetimeCancel context.CancelFunc
	closed         bool
	reservations   int
	sessions       map[string]*session
	nextID         uint64
	nextEvent      uint64
	changed        chan struct{}
}

type session struct {
	id         string
	owner      string
	mode       string
	state      string
	generation string
	worker     Worker
	turns      int
	busy       bool
	turnCancel context.CancelFunc
	stopDone   chan struct{}
	stopErr    error
	events     []Event
	eventBytes int
	firstEvent uint64
}

type Session struct {
	ID         string `json:"session_id"`
	Mode       string `json:"mode"`
	State      string `json:"state"`
	TrustLevel string `json:"trust_level"`
	Generation string `json:"generation_id"`
	Turns      int    `json:"turns"`
}

type Event struct {
	EventID      uint64 `json:"event_id"`
	SessionID    string `json:"session_id"`
	TurnID       string `json:"turn_id,omitempty"`
	GenerationID string `json:"generation_id,omitempty"`
	Kind         string `json:"kind"`
	Text         string `json:"text,omitempty"`
	Error        string `json:"error,omitempty"`
	Status       string `json:"status,omitempty"`
	Tool         string `json:"tool,omitempty"`
	TrustLevel   string `json:"trust_level"`
	ProviderRef  string `json:"provider_ref,omitempty"`
	PiRef        string `json:"pi_ref,omitempty"`
	ProposalRef  string `json:"proposal_ref,omitempty"`
	DecisionRef  string `json:"decision_ref,omitempty"`
	ResultRef    string `json:"result_ref,omitempty"`
	EffectRef    string `json:"effect_ref,omitempty"`
}

type Snapshot struct {
	Session      Session `json:"session"`
	Events       []Event `json:"events"`
	Gap          bool    `json:"gap"`
	ResumeCursor uint64  `json:"resume_cursor,omitempty"`
}

type Principal struct {
	Owner string
	Mode  string // "bearer" or "cookie"
}

func New(config Config) (*Manager, error) {
	if config.Owner == "" {
		config.Owner = "local-owner"
	}
	if strings.TrimSpace(config.Owner) != config.Owner || len(config.Owner) > 128 {
		return nil, errors.New("invalid session control owner")
	}
	if config.AuthToken == "" {
		var bytes [32]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return nil, fmt.Errorf("generate session control token: %w", err)
		}
		config.AuthToken = hex.EncodeToString(bytes[:])
	}
	if len(config.AuthToken) != 64 || strings.Trim(config.AuthToken, "0123456789abcdef") != "" {
		return nil, errors.New("session control token must be 32 bytes encoded as lowercase hex")
	}
	if config.MaxSessions <= 0 || config.MaxSessions > MaxSessions {
		config.MaxSessions = MaxSessions
	}
	lifetime := config.Lifetime
	if lifetime == nil {
		lifetime = context.Background()
	}
	lifetime, lifetimeCancel := context.WithCancel(lifetime)
	return &Manager{
		owner: config.Owner, token: config.AuthToken, fixture: config.FixtureFactory,
		maxSessions: config.MaxSessions, lifetime: lifetime, lifetimeCancel: lifetimeCancel,
		sessions: make(map[string]*session),
		changed:  make(chan struct{}),
	}, nil
}

// AuthToken is for a trusted launcher to place in an owner-only metadata file
// or a private API client. Callers must not log or expose the returned value.
func (m *Manager) AuthToken() string {
	if m == nil {
		return ""
	}
	return m.token
}

// TokenDigest is safe metadata for diagnostics; the token itself is never
// returned by an HTTP endpoint and is not included in errors or events.
func (m *Manager) TokenDigest() string {
	if m == nil {
		return ""
	}
	digest := sha256.Sum256([]byte(m.token))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (m *Manager) Authenticate(r *http.Request) (Principal, error) {
	if m == nil || r == nil {
		return Principal{}, ErrUnauthorized
	}
	if header := r.Header.Get("Authorization"); header != "" {
		if !strings.HasPrefix(header, "Bearer ") || !secureTokenEqual(strings.TrimPrefix(header, "Bearer "), m.token) {
			return Principal{}, ErrUnauthorized
		}
		return Principal{Owner: m.owner, Mode: "bearer"}, nil
	}
	cookie, err := r.Cookie("tbound_auth")
	if err != nil || !secureTokenEqual(cookie.Value, m.token) {
		return Principal{}, ErrUnauthorized
	}
	return Principal{Owner: m.owner, Mode: "cookie"}, nil
}

func (m *Manager) Create(ctx context.Context, owner, mode string) (Session, error) {
	if m == nil || ctx == nil || owner == "" {
		return Session{}, ErrForbidden
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if mode == ModeReal {
		return Session{}, ErrRealLaunchRefused
	}
	if mode != ModeFixture {
		return Session{}, ErrMalformedRequest
	}
	if m.fixture == nil {
		return Session{}, ErrRealLaunchRefused
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Session{}, ErrForbidden
	}
	if len(m.sessions)+m.reservations >= m.maxSessions {
		m.mu.Unlock()
		return Session{}, ErrCapacity
	}
	m.reservations++
	m.nextID++
	id := fmt.Sprintf("sess-%016x", m.nextID)
	generation := fmt.Sprintf("gen-%016x", m.nextID)
	m.mu.Unlock()

	// The HTTP request only admits the session. Its cancellation must not tear
	// down a successfully admitted worker when the create response is written;
	// turn/stop contexts own the worker after admission.
	identity := SessionIdentity{ID: id, Generation: generation}
	callbacks := WorkerCallbacks{
		Event:        func(event WorkerEvent) { m.workerEvent(id, generation, event) },
		ToolDecision: func(decision ToolDecision) { m.toolDecision(id, generation, decision) },
	}
	worker, err := m.fixture(m.lifetime, identity, callbacks)
	if err != nil {
		m.mu.Lock()
		m.reservations--
		m.mu.Unlock()
		return Session{}, fmt.Errorf("create fixture worker: %w", err)
	}
	if worker == nil {
		m.mu.Lock()
		m.reservations--
		m.mu.Unlock()
		return Session{}, errors.New("create fixture worker: factory returned nil worker")
	}
	m.mu.Lock()
	if m.closed {
		m.reservations--
		m.mu.Unlock()
		_ = stopWorkerWithBudget(worker)
		return Session{}, ErrForbidden
	}
	m.reservations--
	entry := &session{id: id, owner: owner, mode: ModeFixture, state: "RUNNING", generation: generation, worker: worker}
	m.sessions[id] = entry
	m.appendLocked(entry, Event{Kind: "session_started", TrustLevel: TrustFixture})
	result := copySession(entry)
	m.mu.Unlock()
	return result, nil
}

func (m *Manager) Submit(ctx context.Context, owner, id, text string) (Session, error) {
	if m == nil || ctx == nil || owner == "" {
		return Session{}, ErrForbidden
	}
	if len([]byte(text)) == 0 || len([]byte(text)) > MaxPromptBytes || strings.IndexByte(text, 0) >= 0 {
		return Session{}, ErrMalformedRequest
	}
	m.mu.Lock()
	entry, ok := m.sessions[id]
	if !ok || entry.owner != owner {
		m.mu.Unlock()
		return Session{}, ErrNotFound
	}
	if entry.state != "RUNNING" {
		m.mu.Unlock()
		return Session{}, ErrForbidden
	}
	if entry.busy {
		m.mu.Unlock()
		return Session{}, ErrBusy
	}
	if entry.turns >= MaxTurns {
		m.mu.Unlock()
		return Session{}, ErrCapacity
	}
	entry.busy = true
	entry.turns++
	turnID := fmt.Sprintf("turn-%016x", uint64(entry.turns))
	worker := entry.worker
	generation := entry.generation
	turnContext, turnCancel := context.WithCancel(m.lifetime)
	entry.turnCancel = turnCancel
	m.appendLocked(entry, Event{Kind: "user_message", Text: text, TrustLevel: TrustServer, TurnID: turnID, GenerationID: generation})
	result := copySession(entry)
	m.mu.Unlock()
	go m.runTurn(entry, worker, turnContext, turnCancel, text, turnID, generation)
	return result, nil
}

func (m *Manager) runTurn(entry *session, worker Worker, ctx context.Context, cancel context.CancelFunc, text, turnID, generation string) {
	err := worker.Prompt(ctx, text)
	cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	entry.busy = false
	entry.turnCancel = nil
	if err != nil && entry.state == "RUNNING" {
		m.appendLocked(entry, Event{Kind: "turn_error", Error: safeError(err), TrustLevel: TrustServer, TurnID: turnID, GenerationID: generation})
	} else if entry.state == "RUNNING" {
		m.appendLocked(entry, Event{Kind: "turn_end", TrustLevel: TrustServer, TurnID: turnID, GenerationID: generation})
	}
}

func (m *Manager) Stop(ctx context.Context, owner, id string) (Session, error) {
	if m == nil || ctx == nil || owner == "" {
		return Session{}, ErrForbidden
	}
	m.mu.Lock()
	entry, ok := m.sessions[id]
	if !ok || entry.owner != owner {
		m.mu.Unlock()
		return Session{}, ErrNotFound
	}
	if entry.state == "STOPPED" || entry.state == "UNKNOWN" {
		result := copySession(entry)
		m.mu.Unlock()
		return result, nil
	}
	if entry.state == "STOPPING" {
		done := entry.stopDone
		m.mu.Unlock()
		select {
		case <-done:
			m.mu.Lock()
			entry, ok := m.sessions[id]
			if !ok || entry.owner != owner {
				m.mu.Unlock()
				return Session{}, ErrNotFound
			}
			result := copySession(entry)
			m.mu.Unlock()
			return result, nil
		case <-ctx.Done():
			return Session{}, ctx.Err()
		}
	}
	entry.state = "STOPPING"
	entry.stopDone = make(chan struct{})
	worker := entry.worker
	turnCancel := entry.turnCancel
	m.appendLocked(entry, Event{Kind: "stop_requested", TrustLevel: TrustServer, GenerationID: entry.generation})
	m.mu.Unlock()
	if turnCancel != nil {
		turnCancel()
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), WorkerStopBudget)
	stopErr := worker.Stop(cleanupCtx)
	cleanupCancel()
	m.mu.Lock()
	entry.stopErr = stopErr
	if stopErr != nil {
		entry.state = "UNKNOWN"
		m.appendLocked(entry, Event{Kind: "session_unknown", Error: "worker cleanup did not settle with certainty", TrustLevel: TrustServer, GenerationID: entry.generation})
	} else {
		entry.state = "STOPPED"
		m.appendLocked(entry, Event{Kind: "session_stopped", TrustLevel: TrustServer, GenerationID: entry.generation})
	}
	result := copySession(entry)
	close(entry.stopDone)
	m.mu.Unlock()
	if stopErr != nil {
		return result, stopErr
	}
	return result, nil
}

// Close is the manager-owned shutdown path. It does not depend on a browser
// request and waits for every admitted worker within the supplied budget.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil || ctx == nil {
		return ErrForbidden
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.lifetimeCancel()
	}
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var first error
	for _, id := range ids {
		if _, err := m.Stop(ctx, m.owner, id); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *Manager) Snapshot(owner, id string, after uint64) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.sessions[id]
	if !ok || entry.owner != owner {
		return Snapshot{}, ErrNotFound
	}
	events, gap, resume := eventsAfter(entry, after)
	return Snapshot{Session: copySession(entry), Events: events, Gap: gap, ResumeCursor: resume}, nil
}

func (m *Manager) WaitForChange(ctx context.Context) error {
	if m == nil || ctx == nil {
		return ErrForbidden
	}
	m.mu.Lock()
	changed := m.changed
	m.mu.Unlock()
	select {
	case <-changed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) workerEvent(id, generation string, event WorkerEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.sessions[id]
	if !ok || entry.state == "STOPPED" {
		return
	}
	if len([]byte(event.Text)) > MaxEventText {
		event.Text = truncateUTF8(event.Text, MaxEventText) + " [truncated]"
	}
	if len([]byte(event.Error)) > MaxEventText {
		event.Error = truncateUTF8(event.Error, MaxEventText) + " [truncated]"
	}
	trust := TrustFixture
	if event.Kind == "error" {
		trust = TrustServer
	}
	if event.Kind != "text_delta" && event.Kind != "error" {
		event.Kind = "worker_event"
	}
	m.appendLocked(entry, Event{
		Kind: event.Kind, Text: event.Text, Error: event.Error, TrustLevel: trust, GenerationID: generation,
	})
}

func (m *Manager) toolDecision(id, generation string, decision ToolDecision) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.sessions[id]
	if !ok || entry.state == "STOPPED" || decision.Generation != generation || decision.Sequence == 0 ||
		decision.ResponseID == "" || decision.ToolCallID == "" || decision.Tool == "" {
		if ok && entry.state != "STOPPED" && decision.Generation == generation && decision.Status == "failed" && decision.Reason != "" {
			m.appendLocked(entry, Event{Kind: "tool_failed", Status: "failed", Text: decision.Reason, TrustLevel: TrustFixture, GenerationID: generation})
		}
		return
	}
	providerRef := "fixture-provider-response-" + decision.ResponseID
	piRef := "pi-" + id
	proposalRef := fmt.Sprintf("proposal-%d-%s", decision.Sequence, decision.ToolCallID)
	status := decision.Status
	if status == "" {
		if decision.Verdict == "ALLOW" {
			status = "completed"
		} else {
			status = "denied"
		}
	}
	if status != "pending" && status != "completed" && status != "denied" && status != "failed" {
		status = "failed"
	}
	decisionRef := fmt.Sprintf("decision-%d", decision.Sequence)
	if status == "pending" {
		decisionRef = fmt.Sprintf("decision-pending-%d", decision.Sequence)
	}
	resultRef := fmt.Sprintf("result-%d", decision.Sequence)
	effectRef := ""
	if decision.Verdict == "ALLOW" {
		effectRef = fmt.Sprintf("effect-fixture-not-executed-%d", decision.Sequence)
	}
	kind := "tool_" + status
	m.appendLocked(entry, Event{Kind: kind, Text: decision.Reason, Status: status, Tool: decision.Tool, TrustLevel: TrustFixture,
		GenerationID: generation, ProviderRef: providerRef, PiRef: piRef, ProposalRef: proposalRef,
		DecisionRef: decisionRef, ResultRef: resultRef, EffectRef: effectRef})
}

func (m *Manager) appendLocked(entry *session, event Event) {
	if event.TurnID == "" && entry.busy && (event.Kind == "text_delta" || event.Kind == "error" || strings.HasPrefix(event.Kind, "tool_")) {
		event.TurnID = fmt.Sprintf("turn-%016x", uint64(entry.turns))
	}
	m.nextEvent++
	event.EventID = m.nextEvent
	event.SessionID = entry.id
	if event.GenerationID == "" {
		event.GenerationID = entry.generation
	}
	encoded, _ := json.Marshal(event)
	entry.events = append(entry.events, event)
	entry.eventBytes += len(encoded)
	if entry.firstEvent == 0 {
		entry.firstEvent = event.EventID
	}
	for len(entry.events) > MaxEvents || entry.eventBytes > MaxEventBytes {
		if len(entry.events) == 0 {
			break
		}
		removed := entry.events[0]
		entry.events = entry.events[1:]
		removedBytes, _ := json.Marshal(removed)
		entry.eventBytes -= len(removedBytes)
		if len(entry.events) > 0 {
			entry.firstEvent = entry.events[0].EventID
		}
	}
	old := m.changed
	m.changed = make(chan struct{})
	close(old)
}

func eventsAfter(entry *session, after uint64) ([]Event, bool, uint64) {
	if len(entry.events) == 0 {
		return nil, false, after
	}
	latest := entry.events[len(entry.events)-1].EventID
	gap := after > latest || (entry.firstEvent > 0 && after < entry.firstEvent-1)
	resume := after
	if gap {
		if after > latest {
			resume = latest
		} else {
			resume = entry.firstEvent - 1
		}
	}
	start := 0
	for start < len(entry.events) && entry.events[start].EventID <= resume {
		start++
	}
	return append([]Event(nil), entry.events[start:]...), gap, resume
}

func copySession(entry *session) Session {
	return Session{ID: entry.id, Mode: entry.mode, State: entry.state, TrustLevel: TrustFixture, Generation: entry.generation, Turns: entry.turns}
}

func stopWorkerWithBudget(worker Worker) error {
	ctx, cancel := context.WithTimeout(context.Background(), WorkerStopBudget)
	defer cancel()
	return worker.Stop(ctx)
}

func secureTokenEqual(left, right string) bool {
	if len(left) != len(right) || len(left) != 64 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 512 {
		return message[:512] + " [truncated]"
	}
	return message
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
