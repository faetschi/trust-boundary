package sessioncontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type testWorker struct {
	mu       sync.Mutex
	event    func(WorkerEvent)
	stopped  bool
	blocking bool
	stopErr  error
}

type coordinatedStopWorker struct {
	mu       sync.Mutex
	started  chan struct{}
	release  chan struct{}
	stopCall int
}

func (w *coordinatedStopWorker) Prompt(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func (w *coordinatedStopWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	w.stopCall++
	if w.stopCall == 1 {
		close(w.started)
	}
	w.mu.Unlock()
	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *testWorker) Prompt(ctx context.Context, text string) error {
	if w.blocking {
		<-ctx.Done()
		return ctx.Err()
	}
	w.event(WorkerEvent{Kind: "text_delta", Text: text})
	return nil
}

func (w *testWorker) Stop(context.Context) error {
	w.mu.Lock()
	w.stopped = true
	err := w.stopErr
	w.mu.Unlock()
	return err
}

func newTestManager(t *testing.T, blocking bool) (*Manager, WorkerFactory) {
	t.Helper()
	factory := func(_ context.Context, _ SessionIdentity, callbacks WorkerCallbacks) (Worker, error) {
		return &testWorker{event: callbacks.Event, blocking: blocking}, nil
	}
	manager, err := New(Config{AuthToken: strings.Repeat("a", 64), Owner: "test-owner", FixtureFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	return manager, factory
}

func TestSessionControlRejectsUnauthorizedHostOriginAndRealLaunch(t *testing.T) {
	manager, _ := newTestManager(t, false)
	handler, err := NewHandler(HTTPConfig{Manager: manager, AllowedHost: "127.0.0.1:8788", HTML: []byte("<html>fixture</html>")})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8788/api/v1/sessions", strings.NewReader(`{"mode":"fixture"}`))
	unauthorized.Host = "127.0.0.1:8788"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", response.Code)
	}

	root := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/", nil)
	root.Host = "127.0.0.1:8788"
	rootResponse := httptest.NewRecorder()
	handler.ServeHTTP(rootResponse, root)
	if len(rootResponse.Result().Cookies()) != 0 {
		t.Fatal("unauthenticated root issued an owner cookie")
	}
	if csp := rootResponse.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("bootstrap CSP is not browser-usable and isolated: %q", csp)
	}
	pair := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8788/api/v1/auth", strings.NewReader(`{"token":"`+manager.AuthToken()+`"}`))
	pair.Host = "127.0.0.1:8788"
	pair.Header.Set("Origin", "http://127.0.0.1:8788")
	pairResponse := httptest.NewRecorder()
	handler.ServeHTTP(pairResponse, pair)
	if pairResponse.Code != http.StatusCreated {
		t.Fatalf("pair status = %d: %s", pairResponse.Code, pairResponse.Body.String())
	}
	cookies := pairResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].HttpOnly != true || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("pair did not issue strict HttpOnly cookie: %#v", cookies)
	}
	cookie := cookies[0]

	badOrigin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8788/api/v1/sessions", strings.NewReader(`{"mode":"fixture"}`))
	badOrigin.Host = "127.0.0.1:8788"
	badOrigin.AddCookie(cookie)
	badOrigin.Header.Set("Origin", "http://evil.invalid")
	badOriginResponse := httptest.NewRecorder()
	handler.ServeHTTP(badOriginResponse, badOrigin)
	if badOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("bad origin status = %d", badOriginResponse.Code)
	}

	production := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8788/api/v1/sessions", strings.NewReader(`{"mode":"real"}`))
	production.Host = "127.0.0.1:8788"
	production.AddCookie(cookie)
	production.Header.Set("Origin", "http://127.0.0.1:8788")
	productionResponse := httptest.NewRecorder()
	handler.ServeHTTP(productionResponse, production)
	if productionResponse.Code != http.StatusServiceUnavailable || !strings.Contains(productionResponse.Body.String(), "real Pi launch is refused") {
		t.Fatalf("production launch was not refused: %d %s", productionResponse.Code, productionResponse.Body.String())
	}

	wrongHost := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9999/api/v1/auth", nil)
	wrongHost.Host = "127.0.0.1:9999"
	wrongHostResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongHostResponse, wrongHost)
	if wrongHostResponse.Code != http.StatusForbidden {
		t.Fatalf("wrong host status = %d", wrongHostResponse.Code)
	}
}

func TestSessionControlMalformedSizeUnknownAndOwnership(t *testing.T) {
	manager, _ := newTestManager(t, false)
	ctx := context.Background()
	session, err := manager.Create(ctx, "test-owner", ModeFixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Snapshot("other-owner", session.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ownership error = %v", err)
	}
	if _, err := manager.Submit(ctx, "test-owner", "unknown", "hello"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session error = %v", err)
	}
	if _, err := manager.Submit(ctx, "test-owner", session.ID, strings.Repeat("x", MaxPromptBytes+1)); !errors.Is(err, ErrMalformedRequest) {
		t.Fatalf("oversize prompt error = %v", err)
	}

	handler, err := NewHandler(HTTPConfig{Manager: manager, AllowedHost: "127.0.0.1:8788"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8788/api/v1/sessions", strings.NewReader(`{"mode":"fixture","extra":true}`))
	request.Host = "127.0.0.1:8788"
	request.Header.Set("Authorization", "Bearer "+manager.AuthToken())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", response.Code)
	}
	duplicate := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8788/api/v1/sessions", strings.NewReader(`{"mode":"fixture","mode":"fixture"}`))
	duplicate.Host = "127.0.0.1:8788"
	duplicate.Header.Set("Authorization", "Bearer "+manager.AuthToken())
	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusBadRequest {
		t.Fatalf("duplicate field status = %d", duplicateResponse.Code)
	}
}

func TestSessionControlReservesCapacityBeforeWorkerFactory(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	factory := func(_ context.Context, _ SessionIdentity, callbacks WorkerCallbacks) (Worker, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return &testWorker{event: callbacks.Event}, nil
	}
	manager, err := New(Config{AuthToken: strings.Repeat("b", 64), Owner: "test-owner", FixtureFactory: factory, MaxSessions: 1})
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		_, createErr := manager.Create(context.Background(), "test-owner", ModeFixture)
		first <- createErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker factory did not start")
	}
	if _, err := manager.Create(context.Background(), "test-owner", ModeFixture); !errors.Is(err, ErrCapacity) {
		t.Fatalf("concurrent create capacity error = %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestSessionControlConcurrentStopSharesTerminationAndIsIdempotent(t *testing.T) {
	worker := &coordinatedStopWorker{started: make(chan struct{}), release: make(chan struct{})}
	factory := func(_ context.Context, _ SessionIdentity, _ WorkerCallbacks) (Worker, error) { return worker, nil }
	manager, err := New(Config{AuthToken: strings.Repeat("c", 64), Owner: "test-owner", FixtureFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.Create(context.Background(), "test-owner", ModeFixture)
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		_, stopErr := manager.Stop(context.Background(), "test-owner", session.ID)
		first <- stopErr
	}()
	select {
	case <-worker.started:
	case <-time.After(time.Second):
		t.Fatal("first stop did not reach worker")
	}
	second := make(chan error, 1)
	go func() {
		_, stopErr := manager.Stop(context.Background(), "test-owner", session.ID)
		second <- stopErr
	}()
	time.Sleep(20 * time.Millisecond)
	worker.mu.Lock()
	callsWhileBlocked := worker.stopCall
	worker.mu.Unlock()
	if callsWhileBlocked != 1 {
		t.Fatalf("concurrent stop invoked worker %d times", callsWhileBlocked)
	}
	close(worker.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stop(context.Background(), "test-owner", session.ID); err != nil {
		t.Fatal(err)
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopCall != 1 {
		t.Fatalf("idempotent stop invoked worker %d times", worker.stopCall)
	}
}

func TestSessionControlRequestCancellationDoesNotCancelAdmittedTurn(t *testing.T) {
	manager, _ := newTestManager(t, true)
	session, err := manager.Create(context.Background(), "test-owner", ModeFixture)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, submitErr := manager.Submit(ctx, "test-owner", session.ID, "slow")
	if submitErr != nil {
		t.Fatal(submitErr)
	}
	cancel()
	snapshot, err := manager.Snapshot("test-owner", session.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.State != "RUNNING" {
		t.Fatalf("request cancellation changed session state = %s", snapshot.Session.State)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if stopped, err := manager.Stop(stopCtx, "test-owner", session.ID); err != nil || stopped.State != "STOPPED" {
		t.Fatalf("explicit stop = %#v, %v", stopped, err)
	}
}

func TestSessionControlSSEIsBoundedAndTreatsHostileOutputAsData(t *testing.T) {
	manager, _ := newTestManager(t, false)
	handler, err := NewHandler(HTTPConfig{Manager: manager, AllowedHost: "127.0.0.1:8788"})
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8788/api/v1/sessions", strings.NewReader(`{"mode":"fixture"}`))
	create.Host = "127.0.0.1:8788"
	create.Header.Set("Authorization", "Bearer "+manager.AuthToken())
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	var session Session
	if err := json.Unmarshal(createResponse.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(context.Background(), "test-owner", session.ID, `<img src=x onerror=alert(1)>`); err != nil {
		t.Fatal(err)
	}
	after := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/sessions/"+session.ID+"/events", nil)
	after.Host = "127.0.0.1:8788"
	after.Header.Set("Authorization", "Bearer "+manager.AuthToken())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, after)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `\u003cimg`) {
		t.Fatalf("SSE did not preserve hostile output as JSON data: %d %s", response.Code, response.Body.String())
	}
}

func TestSessionControlSlowSSEStopsWhenClientDisconnects(t *testing.T) {
	manager, _ := newTestManager(t, false)
	session, err := manager.Create(context.Background(), "test-owner", ModeFixture)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HTTPConfig{Manager: manager, AllowedHost: "127.0.0.1:8788"})
	if err != nil {
		t.Fatal(err)
	}
	gapRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/sessions/"+session.ID+"/events?after=999", nil)
	gapRequest.Host = "127.0.0.1:8788"
	gapRequest.Header.Set("Authorization", "Bearer "+manager.AuthToken())
	gapResponse := httptest.NewRecorder()
	handler.ServeHTTP(gapResponse, gapRequest)
	if !strings.Contains(gapResponse.Body.String(), "event: gap") || strings.Contains(gapResponse.Body.String(), "id: 1000") {
		t.Fatalf("gap event has an unsafe synthetic cursor: %s", gapResponse.Body.String())
	}
	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/sessions/"+session.ID+"/events?after=1", nil).WithContext(requestContext)
	request.Host = "127.0.0.1:8788"
	request.Header.Set("Authorization", "Bearer "+manager.AuthToken())
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow SSE handler did not stop after client disconnect")
	}
}
