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

// When the React app cannot be derived, / and /legacy/ serve the legacy workspace
// with a fallback notice. / never redirects into the app's 503.
func TestRootFallsBackToTheLegacyWorkspaceWhenTheAppIsUnavailable(t *testing.T) {
	missing := reachableFixture()
	delete(missing, ".vite/manifest.json")
	cases := []struct {
		name   string
		source fstest.MapFS
		reason string
	}{
		{"missing manifest", missing, "is missing"},
		{"corrupt manifest", fixtureWithManifest(`{"index.html": `), "is corrupt"},
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
				if recorder.Code != http.StatusOK {
					t.Errorf("%s / status=%d, want 200 (the legacy fallback, not a redirect into a 503)", method, recorder.Code)
				}
				if got := recorder.Header().Get("Location"); got != "" {
					t.Errorf("%s / redirected to %q while the app is unavailable", method, got)
				}
				assertSecurityHeaders(t, method+" / (fallback)", recorder)
			}
			for _, target := range []string{"/", "/legacy/"} {
				body := appTestRequest(t, handler, http.MethodGet, target).Body.String()
				if !strings.Contains(body, "Fallback: legacy workspace") || !strings.Contains(body, tc.reason) {
					t.Errorf("GET %s does not show the fallback notice with its reason %q", target, tc.reason)
				}
				if !strings.Contains(body, `<main class="layout" id="layout">`) {
					t.Errorf("GET %s is not the legacy workspace", target)
				}
			}
			if recorder := appTestRequest(t, handler, http.MethodGet, "/app/"); recorder.Code != http.StatusServiceUnavailable {
				t.Errorf("GET /app/ status=%d, want 503", recorder.Code)
			}
		})
	}
}

// The reason comes from the build manifest, so it is escaped before it is shown.
func TestFallbackNoticeEscapesTheManifestReason(t *testing.T) {
	hostile := fixtureWithManifest(`{"index.html": {"file": "assets/<img src=x onerror=alert(1)>.js", "isEntry": true}}`)
	app := loadAppBundle(hostile)
	if app.err == nil {
		t.Fatal("hostile manifest should be unavailable")
	}
	body := appTestRequest(t, newHandler(appTestStore(t), HandlerOptions{}, app), http.MethodGet, "/").Body.String()
	if strings.Contains(body, "<img src=x") {
		t.Fatal("the manifest reason reached the page unescaped")
	}
	if !strings.Contains(body, "&lt;img src=x") {
		t.Fatalf("the escaped reason is missing from the fallback notice: %q", body)
	}
}

// /legacy/ is the deprecated workspace. The notice sits above <main>, and the rest
// of the served page is byte-identical to the shipped assets/index.html.
func TestLegacyRouteShowsTheDeprecationNoticeAndNothingElseChanges(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	recorder := appTestRequest(t, handler, http.MethodGet, "/legacy/")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /legacy/ status=%d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("GET /legacy/ content-type=%q", got)
	}
	assertSecurityHeaders(t, "/legacy/", recorder)
	body := recorder.Body.String()
	if !strings.Contains(body, "Deprecated: legacy workspace — use /app/") {
		t.Fatal("GET /legacy/ has no deprecation notice")
	}
	shell, err := assetsFS.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(body, `<div class="notices legacy-notice">`)
	mainAt := strings.Index(body, `<main class="layout" id="layout">`)
	if start < 0 || mainAt < start {
		t.Fatal("the notice must sit before <main>")
	}
	if body[:start]+body[mainAt:] != string(shell) {
		t.Error("the served legacy page differs from assets/index.html by more than the notice")
	}
	for _, method := range []string{http.MethodPost, http.MethodHead} {
		recorder := appTestRequest(t, handler, method, "/legacy/")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /legacy/ status=%d, want 405", method, recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "GET" {
			t.Errorf("%s /legacy/ Allow=%q, want GET", method, got)
		}
		assertSecurityHeaders(t, method+" /legacy/", recorder)
	}
}

// The deprecated page is still under the closed asset allowlist: every /static/
// reference it serves is an allowlisted route.
func TestLegacyRouteReferencesOnlyAllowlistedAssets(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	body := appTestRequest(t, handler, http.MethodGet, "/legacy/").Body.String()
	references := shellReferencePattern.FindAllStringSubmatch(body, -1)
	if len(references) < 2 {
		t.Fatalf("only %d /static/ reference(s) on /legacy/; the check would be vacuous", len(references))
	}
	for _, match := range references {
		if _, ok := staticRoutes[match[1]]; !ok {
			t.Errorf("/legacy/ references %s, which the allowlist does not serve", match[1])
		}
	}
}

// Every legacy route carries the gateway's security headers: the deprecated page and
// each allowlisted asset under /static/.
func TestLegacyRoutesCarrySecurityHeaders(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	targets := []string{legacyPath}
	for path := range staticRoutes {
		targets = append(targets, path)
	}
	for _, target := range targets {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s status=%d, want 200", target, recorder.Code)
		}
		assertSecurityHeaders(t, target, recorder)
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
		if (kind == "legacy") != (path == "/legacy/") {
			t.Errorf("csp-smoke.mjs audits %s as kind %q", path, kind)
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
	if want := []string{"/", "/app/", "/legacy/"}; !slices.Equal(paths, want) {
		t.Errorf("csp-smoke.mjs audits %q, the Go route table serves %q", paths, want)
	}
}
