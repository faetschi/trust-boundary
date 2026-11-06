package webchat

import (
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// The root redirects to the React app for GET and HEAD. The Location is the
// fixed string /app/, so nothing from the request can reach it.
func TestRootRedirectsToTheReactAppForGetAndHead(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		recorder := appTestRequest(t, handler, method, "/")
		if recorder.Code != http.StatusFound {
			t.Errorf("%s / status=%d, want 302", method, recorder.Code)
		}
		if got := recorder.Header().Get("Location"); got != "/app/" {
			t.Errorf("%s / Location=%q, want /app/", method, got)
		}
		assertSecurityHeaders(t, method+" /", recorder)
	}
}

func TestRootRejectsOtherMethodsAndAdvertisesGetAndHead(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		recorder := appTestRequest(t, handler, method, "/")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s / status=%d, want 405", method, recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s / Allow=%q, want %q", method, got, "GET, HEAD")
		}
		if got := recorder.Header().Get("Location"); got != "" {
			t.Errorf("%s / must not redirect, Location=%q", method, got)
		}
		assertSecurityHeaders(t, method+" /", recorder)
	}
}

// Whatever the query says, the redirect target stays /app/.
func TestRootRedirectIgnoresTheQueryString(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	for _, target := range []string{"/?x=//evil.com", "/?next=https://evil.example/", "/?/evil.example", "/?"} {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/app/" {
			t.Errorf("GET %s: status=%d Location=%q, want 302 to /app/", target, recorder.Code, recorder.Header().Get("Location"))
		}
		assertSecurityHeaders(t, target, recorder)
	}
}

// Only the exact "/" redirects. Every other spelling is not a route: it is 404 and
// carries no Location, so none of them can become an open redirect.
func TestOnlyTheExactRootRedirects(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	attempts := []string{
		"//", "//evil.example/", "/%2f", "/%2F", "/.", "/./", "/%2e",
		"/index.html", "/static/..", "/static/../", "/legacy", "/legacy//",
		"/legacy/.", "/legacy/index.html", "/%6cegacy/", "/app",
	}
	for _, target := range attempts {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s status=%d, want 404", target, recorder.Code)
		}
		if got := recorder.Header().Get("Location"); got != "" {
			t.Errorf("GET %s redirected to %q; only the exact / may redirect", target, got)
		}
		assertSecurityHeaders(t, target, recorder)
	}
}

// When the React app cannot be derived, / still redirects to /app/, and /app/ answers
// 503 with the reason as plain text. Nothing falls back to another client.
func TestRootStillRedirectsWhenTheAppIsUnavailable(t *testing.T) {
	missing := reachableFixture()
	delete(missing, ".vite/manifest.json")
	cases := []struct {
		name   string
		source fstest.MapFS
		reason string
	}{
		{"missing manifest", missing, "is missing"},
		{"corrupt manifest", fixtureWithManifest(`{"index.html": `), "is corrupt"},
		{"hostile manifest reason", fixtureWithManifest(`{"index.html": {"file": "assets/<img src=x onerror=alert(1)>.js", "isEntry": true}}`), "<img src=x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := loadAppBundle(tc.source)
			if app.err == nil {
				t.Fatal("app should be unavailable")
			}
			handler := newHandler(appTestStore(t), HandlerOptions{}, app)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				recorder := appTestRequest(t, handler, method, "/")
				if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/app/" {
					t.Errorf("%s / status=%d Location=%q, want 302 to /app/", method, recorder.Code, recorder.Header().Get("Location"))
				}
				assertSecurityHeaders(t, method+" / (app unavailable)", recorder)
			}
			recorder := appTestRequest(t, handler, http.MethodGet, "/app/")
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("GET /app/ status=%d, want 503", recorder.Code)
			}
			if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
				t.Errorf("GET /app/ 503 content-type=%q, want text/plain, so the manifest reason is never parsed as markup", got)
			}
			if !strings.Contains(recorder.Body.String(), tc.reason) {
				t.Errorf("GET /app/ 503 body %q does not state the reason %q", recorder.Body.String(), tc.reason)
			}
			assertSecurityHeaders(t, "/app/ (503)", recorder)
		})
	}
}

// The legacy client is retired. Its document route and each asset path it used to
// serve are refused exactly as any other unknown path: the same status, body and
// Allow header, with the gateway's security headers and no redirect.
func TestRetiredLegacyPathsAreRefusedLikeAnyUnknownPath(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	retired := []string{
		"/legacy/", "/legacy", "/legacy/index.html",
		"/static/styles.css", "/static/app.mjs", "/static/api.mjs", "/static/store.mjs", "/static/render.mjs",
		"/static/filters.mjs", "/static/format.mjs", "/static/trajectory.mjs", "/static/markdown.mjs",
		"/static/backoff.mjs", "/static/stream.mjs", "/static/anchor.mjs", "/static/stop-sequence.mjs",
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
		unknown := appTestRequest(t, handler, method, "/no-such-path")
		for _, target := range retired {
			recorder := appTestRequest(t, handler, method, target)
			if recorder.Code != unknown.Code {
				t.Errorf("%s %s status=%d, an unknown path gives %d", method, target, recorder.Code, unknown.Code)
			}
			if recorder.Body.String() != unknown.Body.String() {
				t.Errorf("%s %s body=%q, an unknown path gives %q", method, target, recorder.Body.String(), unknown.Body.String())
			}
			if recorder.Header().Get("Allow") != unknown.Header().Get("Allow") {
				t.Errorf("%s %s Allow=%q, an unknown path gives %q", method, target, recorder.Header().Get("Allow"), unknown.Header().Get("Allow"))
			}
			if recorder.Header().Get("Location") != "" {
				t.Errorf("%s %s redirected to %q", method, target, recorder.Header().Get("Location"))
			}
			assertSecurityHeaders(t, method+" "+target, recorder)
		}
	}
	if recorder := appTestRequest(t, handler, http.MethodGet, "/legacy/"); recorder.Code != http.StatusNotFound {
		t.Errorf("GET /legacy/ status=%d, want 404", recorder.Code)
	}
}

// csp-smoke.mjs audits the document routes listed in GATEWAY_ROUTES. That list must
// be the Go route table: the same paths, the same kind, and the landing each one
// reaches. Otherwise the smoke could pass while a route it never checks is broken.
var smokeRouteEntry = regexp.MustCompile(`\{ path: '([^']+)', kind: '([^']+)', landing: '([^']+)'`)

func TestSmokeScriptRoutesMatchTheGoRouteTable(t *testing.T) {
	source, err := os.ReadFile("../../scripts/csp-smoke.mjs")
	if err != nil {
		t.Fatalf("read csp-smoke.mjs: %v", err)
	}
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	var paths []string
	for _, entry := range smokeRouteEntry.FindAllStringSubmatch(string(source), -1) {
		path, kind, landing := entry[1], entry[2], entry[3]
		paths = append(paths, path)
		if kind == "legacy" {
			t.Errorf("csp-smoke.mjs audits %s as the retired kind %q", path, kind)
		}
		recorder := appTestRequest(t, handler, http.MethodGet, path)
		switch recorder.Code {
		case http.StatusFound:
			if got := recorder.Header().Get("Location"); got != landing {
				t.Errorf("%s redirects to %q; csp-smoke expects %q", path, got, landing)
			}
		case http.StatusOK:
			if landing != path {
				t.Errorf("%s answers 200; csp-smoke expects it to land on %q", path, landing)
			}
		default:
			t.Errorf("%s status=%d; csp-smoke audits it, so it must be served", path, recorder.Code)
		}
	}
	sort.Strings(paths)
	if want := []string{"/", "/app/"}; !slices.Equal(paths, want) {
		t.Errorf("csp-smoke.mjs audits %q, the Go route table serves %q", paths, want)
	}
}
