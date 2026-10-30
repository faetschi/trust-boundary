package webchat

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// faviconTestRequest is appTestRequestFrom with the method under the caller's
// control. An empty origin sends no Origin header.
func faviconTestRequest(t *testing.T, handler http.Handler, method, target, host, origin string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	request.Host = host
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// The browser requests /favicon.ico on every page load. The gateway answers it with
// 204 No Content so that request is not a console error.
func TestFaviconAnswersNoContentForGetAndHead(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		recorder := appTestRequest(t, handler, method, "/favicon.ico")
		if recorder.Code != http.StatusNoContent {
			t.Errorf("%s /favicon.ico status=%d, want 204", method, recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("%s /favicon.ico body=%q, want empty", method, recorder.Body.String())
		}
		assertSecurityHeaders(t, method+" /favicon.ico", recorder)
	}
}

func TestFaviconRejectsOtherMethodsAndAdvertisesGetAndHead(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		recorder := appTestRequest(t, handler, method, "/favicon.ico")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /favicon.ico status=%d, want 405", method, recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s /favicon.ico Allow=%q, want %q", method, got, "GET, HEAD")
		}
		assertSecurityHeaders(t, method+" /favicon.ico", recorder)
	}
}

// Only the exact path is the favicon route. Lookalikes and percent-encoded spellings
// stay as they were: GET is 404, and HEAD is refused with 405 and Allow: GET, so the
// HEAD allowance does not leak to them.
func TestOnlyTheExactFaviconPathIsARoute(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	for _, target := range []string{"/favicon.ico/", "/favicon.ico.bak", "/%66avicon.ico"} {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s status=%d, want 404", target, recorder.Code)
		}
		assertSecurityHeaders(t, target, recorder)

		head := appTestRequest(t, handler, http.MethodHead, target)
		if head.Code != http.StatusMethodNotAllowed {
			t.Errorf("HEAD %s status=%d, want 405", target, head.Code)
		}
		if got := head.Header().Get("Allow"); got != "GET" {
			t.Errorf("HEAD %s Allow=%q, want GET", target, got)
		}
	}
}

// authorizeRequest runs before routing, so the favicon route gets the same Host and
// Origin pinning as every other route, for GET and HEAD alike.
func TestFaviconRefusesForeignHostAndOrigin(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	refused := []struct {
		name, host, origin string
	}{
		{"foreign Host", "evil.example", ""},
		{"loopback-looking Host", "127.0.0.1.evil.example", ""},
		{"foreign Origin", "127.0.0.1:8791", "http://evil.example"},
		{"null Origin", "127.0.0.1:8791", "null"},
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, r := range refused {
			recorder := faviconTestRequest(t, handler, method, "/favicon.ico", r.host, r.origin)
			if recorder.Code != http.StatusForbidden {
				t.Errorf("%s /favicon.ico with %s status=%d, want 403", method, r.name, recorder.Code)
			}
			assertSecurityHeaders(t, method+" /favicon.ico ("+r.name+")", recorder)
		}
	}

	configured := newHandler(appTestStore(t), HandlerOptions{AllowedHost: "127.0.0.1:8791", AllowedOrigin: "http://127.0.0.1:8791"}, embeddedApp())
	if recorder := faviconTestRequest(t, configured, http.MethodGet, "/favicon.ico", "127.0.0.1:8791", "http://127.0.0.1:8791"); recorder.Code != http.StatusNoContent {
		t.Errorf("configured host and origin: GET /favicon.ico status=%d, want 204", recorder.Code)
	}
	if recorder := faviconTestRequest(t, configured, http.MethodGet, "/favicon.ico", "127.0.0.1:9999", ""); recorder.Code != http.StatusForbidden {
		t.Errorf("configured host mismatch: GET /favicon.ico status=%d, want 403", recorder.Code)
	}
}
