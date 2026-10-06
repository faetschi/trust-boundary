package sessioncontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tbound/supervisor/internal/broker/protocol"
)

// HTTPConfig binds one handler to one exact loopback Host value. The caller
// must bind the underlying listener to loopback; this package does not widen
// that boundary for convenience.
type HTTPConfig struct {
	Manager     *Manager
	AllowedHost string
	HTML        []byte
}

func NewHandler(config HTTPConfig) (http.Handler, error) {
	if config.Manager == nil || config.AllowedHost == "" {
		return nil, errors.New("session control HTTP manager and exact host are required")
	}
	if strings.ContainsAny(config.AllowedHost, "\r\n/") {
		return nil, errors.New("invalid exact session control host")
	}
	return &handler{config: config}, nil
}

type handler struct{ config HTTPConfig }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != h.config.AllowedHost {
		h.writeError(w, http.StatusForbidden, ErrForbidden)
		return
	}
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		// Static bootstrap is intentionally unauthenticated. It never grants a
		// session owner cookie; pairing is an explicit authenticated POST.
		h.setStaticHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(h.config.HTML) == 0 {
			_, _ = io.WriteString(w, "<!doctype html><title>tbound chat</title><p>chat UI unavailable</p>")
			return
		}
		_, _ = w.Write(h.config.HTML)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth" {
		if err := h.checkBrowserMutation(r); err != nil {
			h.writeError(w, http.StatusForbidden, err)
			return
		}
		h.pair(w, r)
		return
	}

	principal, err := h.config.Manager.Authenticate(r)
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, err)
		return
	}
	// Bearer clients already present the pairing capability on every request;
	// browser cookie mutations additionally require same-origin CSRF metadata.
	if r.Method == http.MethodPost && principal.Mode == "cookie" {
		if err := h.checkBrowserMutation(r); err != nil {
			h.writeError(w, http.StatusForbidden, err)
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth":
		h.writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "owner": principal.Owner})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
		h.createSession(w, r, principal)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/snapshot"):
		h.snapshot(w, r, principal)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/events"):
		h.events(w, r, principal)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/turns"):
		h.turn(w, r, principal)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/stop"):
		h.stop(w, r, principal)
	default:
		h.writeError(w, http.StatusNotFound, ErrNotFound)
	}
}

func (h *handler) pair(w http.ResponseWriter, r *http.Request) {
	var request *struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &request, "token"); err != nil || request == nil || !secureTokenEqual(request.Token, h.config.Manager.AuthToken()) {
		h.writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	h.setAuthCookie(w)
	h.writeJSON(w, http.StatusCreated, map[string]any{"authenticated": true})
}

func (h *handler) createSession(w http.ResponseWriter, r *http.Request, principal Principal) {
	var request *struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(w, r, &request, "mode"); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if request == nil {
		h.writeError(w, http.StatusBadRequest, ErrMalformedRequest)
		return
	}
	if request.Mode == ModeReal {
		h.writeError(w, http.StatusServiceUnavailable, ErrRealLaunchRefused)
		return
	}
	session, err := h.config.Manager.Create(r.Context(), principal.Owner, request.Mode)
	if err != nil {
		h.writeManagerError(w, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, session)
}

func (h *handler) snapshot(w http.ResponseWriter, r *http.Request, principal Principal) {
	id, ok := pathID(r.URL.Path, "/snapshot")
	if !ok {
		h.writeError(w, http.StatusNotFound, ErrNotFound)
		return
	}
	after, err := parseAfter(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	snapshot, err := h.config.Manager.Snapshot(principal.Owner, id, after)
	if err != nil {
		h.writeManagerError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, snapshot)
}

func (h *handler) events(w http.ResponseWriter, r *http.Request, principal Principal) {
	id, ok := pathID(r.URL.Path, "/events")
	if !ok {
		h.writeError(w, http.StatusNotFound, ErrNotFound)
		return
	}
	after, err := parseAfter(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := h.config.Manager.Snapshot(principal.Owner, id, after); err != nil {
		h.writeManagerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(SSEIdleTimeout))
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, errors.New("SSE is unavailable"))
		return
	}
	deadline := time.NewTimer(SSEIdleTimeout)
	defer deadline.Stop()
	sent := 0
	for sent < MaxSSEEvents {
		snapshot, err := h.config.Manager.Snapshot(principal.Owner, id, after)
		if err != nil {
			return
		}
		if snapshot.Gap {
			if !writeSSEGap(w, gapEvent{SessionID: id, RequestedCursor: after, ResumeCursor: snapshot.ResumeCursor}) {
				return
			}
			sent++
			after = snapshot.ResumeCursor
			snapshot, err = h.config.Manager.Snapshot(principal.Owner, id, after)
			if err != nil {
				return
			}
		}
		for _, event := range snapshot.Events {
			if !writeSSE(w, event) {
				return
			}
			after = event.EventID
			sent++
			if sent >= MaxSSEEvents {
				break
			}
		}
		if sent > 0 {
			flusher.Flush()
			// One bounded batch per connection keeps slow clients from retaining
			// unbounded server-side state. EventSource reconnects with the cursor.
			return
		}
		waitCtx, cancel := context.WithCancel(r.Context())
		waitDone := make(chan error, 1)
		go func() { waitDone <- h.config.Manager.WaitForChange(waitCtx) }()
		select {
		case err := <-waitDone:
			cancel()
			if err != nil {
				return
			}
		case <-deadline.C:
			cancel()
			return
		case <-r.Context().Done():
			cancel()
			return
		}
	}
}

func (h *handler) turn(w http.ResponseWriter, r *http.Request, principal Principal) {
	id, ok := pathID(r.URL.Path, "/turns")
	if !ok {
		h.writeError(w, http.StatusNotFound, ErrNotFound)
		return
	}
	var request struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(w, r, &request, "text"); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	session, err := h.config.Manager.Submit(r.Context(), principal.Owner, id, request.Text)
	if err != nil {
		h.writeManagerError(w, err)
		return
	}
	h.writeJSON(w, http.StatusAccepted, session)
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request, principal Principal) {
	id, ok := pathID(r.URL.Path, "/stop")
	if !ok {
		h.writeError(w, http.StatusNotFound, ErrNotFound)
		return
	}
	session, err := h.config.Manager.Stop(r.Context(), principal.Owner, id)
	if err != nil {
		h.writeManagerError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, session)
}

func (h *handler) setAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "tbound_auth", Value: h.config.Manager.AuthToken(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (h *handler) setStaticHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func (h *handler) checkBrowserMutation(r *http.Request) error {
	if r.Header.Get("Origin") != "http://"+h.config.AllowedHost {
		return ErrForbidden
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return ErrForbidden
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" && mode != "same-origin" && mode != "cors" {
		return ErrForbidden
	}
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" && dest != "empty" {
		return ErrForbidden
	}
	return nil
}

func (h *handler) writeManagerError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrUnauthorized):
		status = http.StatusUnauthorized
	case errors.Is(err, ErrBusy), errors.Is(err, ErrCapacity):
		status = http.StatusConflict
	case errors.Is(err, ErrRealLaunchRefused):
		status = http.StatusServiceUnavailable
	case errors.Is(err, ErrMalformedRequest):
		status = http.StatusBadRequest
	}
	h.writeError(w, status, err)
}

func (h *handler) writeError(w http.ResponseWriter, status int, err error) {
	h.writeJSON(w, status, map[string]string{"error": safeError(err)})
}

func (h *handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, allowed ...string) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("%w: read body", ErrMalformedRequest)
	}
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedRequest, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("%w: body must be an object", ErrMalformedRequest)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	if len(fields) != len(allowedSet) {
		return fmt.Errorf("%w: unexpected or missing field", ErrMalformedRequest)
	}
	for key := range fields {
		if _, ok := allowedSet[key]; !ok {
			return fmt.Errorf("%w: unexpected field", ErrMalformedRequest)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedRequest, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrMalformedRequest)
	}
	return nil
}

func parseAfter(r *http.Request) (uint64, error) {
	value := r.URL.Query().Get("after")
	if value == "" {
		if value = r.Header.Get("Last-Event-ID"); value == "" {
			return 0, nil
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, errors.New("event cursor must be an unsigned integer")
	}
	return parsed, nil
}

func pathID(path, suffix string) (string, bool) {
	prefix := "/api/v1/sessions/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.ContainsAny(id, "/?\r\n") {
		return "", false
	}
	return id, true
}

func writeSSE(w http.ResponseWriter, event Event) bool {
	encoded, err := json.Marshal(event)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.EventID, encoded)
	return err == nil
}

type gapEvent struct {
	Kind            string `json:"kind"`
	SessionID       string `json:"session_id"`
	RequestedCursor uint64 `json:"requested_cursor"`
	ResumeCursor    uint64 `json:"resume_cursor"`
	TrustLevel      string `json:"trust_level"`
}

func writeSSEGap(w http.ResponseWriter, event gapEvent) bool {
	encoded, err := json.Marshal(event)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "event: gap\ndata: %s\n\n", encoded)
	return err == nil
}
