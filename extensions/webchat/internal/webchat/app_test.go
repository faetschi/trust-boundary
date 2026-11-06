package webchat

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// wantCSP is the Content-Security-Policy as it shipped before the React scaffold.
// The React client needs no new source, so this string must not change.
const wantCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"

// The smoke script reads contentSecurityPolicy from handler.go. This pins that
// constant to the shipped string, so a change to the policy is a deliberate
// change here as well.
func TestContentSecurityPolicyConstantIsPinned(t *testing.T) {
	if contentSecurityPolicy != wantCSP {
		t.Fatalf("contentSecurityPolicy = %q, want %q", contentSecurityPolicy, wantCSP)
	}
}

// reachableFixture is a small build. assets/orphan.js is in the build but no
// manifest entry reaches it. chunk.js is reached through a static import and
// dyn.js through a dynamic import.
func reachableFixture() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {Data: []byte(`<!doctype html><html><head>` +
			`<link rel="stylesheet" crossorigin href="/app/assets/app.css">` +
			`<script type="module" crossorigin src="/app/assets/app.js"></script>` +
			`</head><body><div id="root"></div></body></html>`)},
		".vite/manifest.json": {Data: []byte(`{
  "index.html": {"file": "assets/app.js", "src": "index.html", "isEntry": true,
    "css": ["assets/app.css"], "imports": ["src/chunk.ts"], "dynamicImports": ["src/dyn.ts"]},
  "src/chunk.ts": {"file": "assets/chunk.js", "src": "src/chunk.ts"},
  "src/dyn.ts": {"file": "assets/dyn.js", "src": "src/dyn.ts", "isDynamicEntry": true}
}`)},
		"assets/app.js":    {Data: []byte("console.log('app')")},
		"assets/app.css":   {Data: []byte("body{margin:0}")},
		"assets/chunk.js":  {Data: []byte("export const chunk = 1")},
		"assets/dyn.js":    {Data: []byte("export const dyn = 2")},
		"assets/orphan.js": {Data: []byte("console.log('not in the manifest')")},
		"secret.txt":       {Data: []byte("not part of the build")},
	}
}

func fixtureWithManifest(manifest string) fstest.MapFS {
	source := reachableFixture()
	source[".vite/manifest.json"] = &fstest.MapFile{Data: []byte(manifest)}
	return source
}

func appTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	return store
}

func appTestRequest(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	request.Host = "127.0.0.1:8791"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func assertSecurityHeaders(t *testing.T, target string, recorder *httptest.ResponseRecorder) {
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
		if got := recorder.Header().Get(name); got != value {
			t.Errorf("GET %s %s=%q, want %q", target, name, got, value)
		}
	}
}

func TestAppServesManifestDerivedFilesWithTypesAndHeaders(t *testing.T) {
	if err := AppStatus(); err != nil {
		t.Fatalf("embedded app should derive from the committed build: %v", err)
	}
	app := embeddedApp()
	if _, ok := app.routes["/app/"]; !ok {
		t.Fatal("embedded app has no /app/ route")
	}
	handler := newHandler(appTestStore(t), HandlerOptions{}, app)
	for target, route := range app.routes {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s status=%d, want 200", target, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != route.contentType {
			t.Errorf("GET %s content-type=%q, want %q", target, got, route.contentType)
		}
		want, err := fs.ReadFile(app.source, route.name)
		if err != nil {
			t.Fatalf("read %s: %v", route.name, err)
		}
		if !bytes.Equal(recorder.Body.Bytes(), want) {
			t.Errorf("GET %s body differs from build file %s", target, route.name)
		}
		assertSecurityHeaders(t, target, recorder)
	}
	if recorder := appTestRequest(t, handler, http.MethodPost, "/app/"); recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /app/ status=%d, want 405", recorder.Code)
	} else {
		assertSecurityHeaders(t, "POST /app/", recorder)
	}
}

func TestAppServesExactlyTheManifestReachableFiles(t *testing.T) {
	app := loadAppBundle(reachableFixture())
	if app.err != nil {
		t.Fatalf("fixture should derive: %v", app.err)
	}
	handler := newHandler(appTestStore(t), HandlerOptions{}, app)

	served := map[string]string{
		"/app/":                "text/html; charset=utf-8",
		"/app/assets/app.js":   "text/javascript; charset=utf-8",
		"/app/assets/app.css":  "text/css; charset=utf-8",
		"/app/assets/chunk.js": "text/javascript; charset=utf-8",
		"/app/assets/dyn.js":   "text/javascript; charset=utf-8",
	}
	for target, contentType := range served {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s status=%d, want 200 (reachable from the manifest)", target, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != contentType {
			t.Errorf("GET %s content-type=%q, want %q", target, got, contentType)
		}
	}

	notServed := []string{
		"/app/assets/orphan.js",    // present in the build, not reachable from the manifest
		"/app/index.html",          // the shell is served at /app/ only
		"/app/.vite/manifest.json", // build metadata is never served
		"/app/secret.txt",
		"/app/assets/",
		"/app/assets",
		"/app",
	}
	for _, target := range notServed {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s status=%d, want 404", target, recorder.Code)
		}
		assertSecurityHeaders(t, target, recorder)
		if strings.Contains(recorder.Body.String(), "console.log") || strings.Contains(recorder.Body.String(), "not part of the build") {
			t.Errorf("GET %s leaked build content", target)
		}
	}
}

// Traversal, dot-segment, percent-encoding and double-encoding attempts are not
// routes. The lookup uses the escaped path, so a percent-encoded spelling of a
// real app path is also 404: there is no normalisation.
func TestAppTraversalAndEncodingAttemptsAreNotFound(t *testing.T) {
	handler := newHandler(appTestStore(t), HandlerOptions{}, loadAppBundle(reachableFixture()))
	if recorder := appTestRequest(t, handler, http.MethodGet, "/app/assets/app.js"); recorder.Code != http.StatusOK {
		t.Fatalf("positive control GET /app/assets/app.js status=%d, want 200", recorder.Code)
	}
	attempts := []string{
		"/app/../secret.txt",
		"/app/assets/../secret.txt",
		"/app/assets/../assets/app.js",
		"/app/./index.html",
		"/app//index.html",
		"/app/assets/./app.js",
		"/app/%2e%2e/secret.txt",
		"/app/%2E%2E/secret.txt",
		"/app/assets/%2e%2e/secret.txt",
		"/app/%2e%2e%2fsecret.txt",
		"/app/%252e%252e/secret.txt",
		"/app/%252e%252e%252fsecret.txt",
		"/app/assets%2fapp.js",
		"/app/assets%5capp.js",
		"/app/%61ssets/app.js",
		"/app/assets/app.js/",
		"/app/assets/app.js%00",
		"/app/assets/APP.JS",
		"/app/assets/app.js%2F",
	}
	for _, target := range attempts {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s status=%d, want 404", target, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "console.log") || strings.Contains(recorder.Body.String(), "not part of the build") {
			t.Errorf("GET %s leaked build content", target)
		}
	}
}

func TestAppUnavailableWithClearErrorWhileTheRestOfTheServerWorks(t *testing.T) {
	missing := reachableFixture()
	delete(missing, ".vite/manifest.json")

	noShellEntry := fixtureWithManifest(`{"src/x.ts": {"file": "assets/app.js", "isEntry": true}}`)
	danglingImport := fixtureWithManifest(`{
  "index.html": {"file": "assets/app.js", "isEntry": true, "imports": ["src/missing.ts"]}
}`)
	unsupported := fixtureWithManifest(`{
  "index.html": {"file": "assets/app.js", "isEntry": true, "assets": ["assets/logo.png"]}
}`)
	traversalName := fixtureWithManifest(`{
  "index.html": {"file": "../secret.txt", "isEntry": true}
}`)
	listedMissing := reachableFixture()
	delete(listedMissing, "assets/chunk.js")
	shellExtra := reachableFixture()
	shellExtra["index.html"] = &fstest.MapFile{Data: []byte(`<!doctype html><script type="module" src="/app/assets/evil.js"></script>`)}

	cases := []struct {
		name       string
		source     fstest.MapFS
		wantReason string
	}{
		{"missing manifest", missing, "is missing"},
		{"corrupt manifest", fixtureWithManifest(`{"index.html": `), "is corrupt"},
		{"no shell entry", noShellEntry, "has no entry for index.html"},
		{"dangling import", danglingImport, "which has no entry"},
		{"listed file missing", listedMissing, "which is missing from the build"},
		{"unsupported asset type", unsupported, "not a permitted JavaScript or CSS asset path"},
		{"traversal in manifest", traversalName, "not a permitted JavaScript or CSS asset path"},
		{"shell references unlisted file", shellExtra, "which the manifest does not derive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := loadAppBundle(tc.source)
			if app.err == nil {
				t.Fatal("app should be unavailable")
			}
			if !strings.Contains(app.err.Error(), tc.wantReason) {
				t.Fatalf("error %q does not say %q", app.err, tc.wantReason)
			}
			handler := newHandler(appTestStore(t), HandlerOptions{}, app)

			recorder := appTestRequest(t, handler, http.MethodGet, "/app/")
			if recorder.Code != http.StatusServiceUnavailable {
				t.Errorf("GET /app/ status=%d, want 503", recorder.Code)
			}
			assertSecurityHeaders(t, "/app/ (unavailable)", recorder)
			if !strings.Contains(recorder.Body.String(), "web chat app unavailable") || !strings.Contains(recorder.Body.String(), tc.wantReason) {
				t.Errorf("GET /app/ body %q does not explain the failure", recorder.Body.String())
			}
			for _, target := range []string{"/app/assets/app.js", "/app/index.html"} {
				if recorder := appTestRequest(t, handler, http.MethodGet, target); recorder.Code != http.StatusNotFound {
					t.Errorf("GET %s status=%d, want 404 while the app is unavailable", target, recorder.Code)
				} else {
					assertSecurityHeaders(t, target, recorder)
				}
			}
			if recorder := appTestRequest(t, handler, http.MethodGet, "/"); recorder.Code != http.StatusFound {
				t.Errorf("GET / status=%d, want 302: / still redirects to /app/", recorder.Code)
			}
			if recorder := appTestRequest(t, handler, http.MethodGet, "/api/v1/contract"); recorder.Code != http.StatusOK {
				t.Errorf("GET /api/v1/contract status=%d, want 200", recorder.Code)
			}
		})
	}
}

var (
	cssImport      = regexp.MustCompile(`(?i)@import\b`)
	cssExternalURL = regexp.MustCompile(`(?i)url\(\s*['"]?\s*(https?:|data:|//|javascript:)`)
)

func TestAppShellAndStylesHaveNoInlineOrExternalReferences(t *testing.T) {
	app := embeddedApp()
	if app.err != nil {
		t.Fatalf("embedded app: %v", app.err)
	}
	checked := 0
	for target, route := range app.routes {
		body, err := fs.ReadFile(app.source, route.name)
		if err != nil {
			t.Fatalf("read %s: %v", route.name, err)
		}
		text := string(body)
		switch {
		case strings.HasSuffix(route.name, ".html"):
			checked++
			checkShell(t, target, text, app.routes)
		case strings.HasSuffix(route.name, ".css"):
			checked++
			if cssImport.MatchString(text) {
				t.Errorf("%s uses @import; the stylesheet must be self-contained", target)
			}
			if cssExternalURL.MatchString(text) {
				t.Errorf("%s references an external or data: URL", target)
			}
		}
	}
	if checked < 2 {
		t.Fatalf("only %d shell/stylesheet file(s) checked; the check would be vacuous", checked)
	}
}

// checkShell reads the shell with the same tokenizer as the derivation, so the
// test and the gateway agree on what the browser loads.
func checkShell(t *testing.T, target, text string, routes map[string]appRoute) {
	t.Helper()
	tags, err := parseShellTags([]byte(text))
	if err != nil {
		t.Errorf("%s: %v", target, err)
		return
	}
	if strings.Contains(strings.ToLower(text), "javascript:") {
		t.Errorf("%s has a javascript: URL", target)
	}
	for _, tag := range tags {
		if tag.name == "style" {
			t.Errorf("%s has an inline <style> element", target)
		}
		hasAppSource := false
		for _, attr := range tag.attrs {
			if attr.name == "style" {
				t.Errorf("%s has a style= attribute", target)
			}
			if strings.HasPrefix(attr.name, "on") {
				t.Errorf("%s has an inline event handler %s=", target, attr.name)
			}
			var references []string
			switch {
			case attr.name == "srcset" || attr.name == "imagesrcset":
				for _, candidate := range strings.Split(attr.value, ",") {
					if fields := strings.Fields(candidate); len(fields) > 0 {
						references = append(references, fields[0])
					}
				}
			case shellURLAttributes[attr.name]:
				references = []string{attr.value}
			}
			for _, reference := range references {
				if !strings.HasPrefix(reference, appPrefix) || strings.HasPrefix(reference, "//") {
					t.Errorf("%s references %q, which is not a same-origin /app/ path", target, reference)
					continue
				}
				if _, ok := routes[reference]; !ok {
					t.Errorf("%s references %q, which is not a derived route", target, reference)
				}
				hasAppSource = hasAppSource || (tag.name == "script" && attr.name == "src")
			}
		}
		if tag.name == "script" {
			if strings.TrimSpace(tag.body) != "" {
				t.Errorf("%s has an inline <script> body", target)
			}
			if !hasAppSource {
				t.Errorf("%s has a <script> without a same-origin /app/ src", target)
			}
		}
	}
}

// Every file in the committed build must be served, or be the manifest itself.
// A stale file left in dist/ by an earlier build would otherwise sit in the
// binary unserved and unnoticed.
func TestAppBuildHasNoUnreachableFiles(t *testing.T) {
	app := embeddedApp()
	if app.err != nil {
		t.Fatalf("embedded app: %v", app.err)
	}
	accounted := map[string]bool{appManifest: true}
	for _, route := range app.routes {
		accounted[route.name] = true
	}
	err := fs.WalkDir(app.source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !accounted[path] {
			t.Errorf("dist/%s is in the build but neither served nor the manifest; regenerate dist/", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dist: %v", err)
	}
}

// fixtureWithShell is reachableFixture with index.html replaced.
func fixtureWithShell(shell string) fstest.MapFS {
	source := reachableFixture()
	source["index.html"] = &fstest.MapFile{Data: []byte(shell)}
	return source
}

// appTestRequestFrom is appTestRequest with Host and Origin under the caller's
// control. An empty origin sends no Origin header.
func appTestRequestFrom(t *testing.T, handler http.Handler, target, host, origin string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Host = host
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// authorizeRequest runs before routing. A foreign Host or Origin must be refused on
// every document route (/ and /app/), with the gateway's security
// headers. Mutation check: skipping authorizeRequest for any of them must fail
// this test.
func TestAppRefusesForeignHostAndOrigin(t *testing.T) {
	fixture := newHandler(appTestStore(t), HandlerOptions{}, loadAppBundle(reachableFixture()))
	embedded := newHandler(appTestStore(t), HandlerOptions{}, embeddedApp())
	fallback := newHandler(appTestStore(t), HandlerOptions{}, loadAppBundle(fixtureWithManifest(`{`)))
	targets := []struct {
		name    string
		handler http.Handler
		target  string
	}{
		{"fixture shell", fixture, "/app/"},
		{"fixture asset", fixture, "/app/assets/app.js"},
		{"fixture stylesheet", fixture, "/app/assets/app.css"},
		{"fixture unreachable file", fixture, "/app/assets/orphan.js"},
		{"embedded shell", embedded, "/app/"},
		{"root redirect", embedded, "/"},
		{"root redirect with the app unavailable", fallback, "/"},
		{"retired legacy path", embedded, "/legacy/"},
		{"retired static path", embedded, "/static/app.mjs"},
	}
	refused := []struct {
		name, host, origin string
	}{
		{"foreign Host", "evil.example", ""},
		{"loopback-looking Host", "127.0.0.1.evil.example", ""},
		{"foreign Origin", "127.0.0.1:8791", "http://evil.example"},
		{"null Origin", "127.0.0.1:8791", "null"},
		{"foreign Origin with loopback Host", "localhost:8791", "https://127.0.0.1:8791"},
	}
	for _, tc := range targets {
		for _, r := range refused {
			t.Run(tc.name+" with "+r.name, func(t *testing.T) {
				recorder := appTestRequestFrom(t, tc.handler, tc.target, r.host, r.origin)
				if recorder.Code != http.StatusForbidden {
					t.Errorf("GET %s with %s status=%d, want 403", tc.target, r.name, recorder.Code)
				}
				assertSecurityHeaders(t, tc.target, recorder)
			})
		}
	}

	configured := newHandler(appTestStore(t), HandlerOptions{AllowedHost: "127.0.0.1:8791", AllowedOrigin: "http://127.0.0.1:8791"}, loadAppBundle(reachableFixture()))
	if recorder := appTestRequestFrom(t, configured, "/app/assets/app.js", "127.0.0.1:8791", "http://127.0.0.1:8791"); recorder.Code != http.StatusOK {
		t.Errorf("configured host and origin: GET /app/assets/app.js status=%d, want 200", recorder.Code)
	}
	for _, r := range []struct{ host, origin string }{{"127.0.0.1:9999", ""}, {"127.0.0.1:8791", "http://evil.example"}} {
		recorder := appTestRequestFrom(t, configured, "/app/assets/app.js", r.host, r.origin)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("configured gateway: host %q origin %q status=%d, want 403", r.host, r.origin, recorder.Code)
		}
		assertSecurityHeaders(t, "/app/assets/app.js", recorder)
	}
}

// The shell's URLs are read by a tokenizer that follows the browser's rules for
// quotes, unquoted values, whitespace around '=', srcset and comments. Anything
// the browser could load that is not an admitted route, and any tag the
// derivation cannot classify, must fail the derivation. Nothing is skipped.
func TestAppShellFailsClosedOnAnyUnadmittedURL(t *testing.T) {
	const notAdmitted = "which the manifest does not derive"
	cases := []struct {
		name  string
		shell string
		want  string
	}{
		{"single-quoted evil script", `<script type="module" src='https://evil.example/x.js'></script>`, notAdmitted},
		{"unquoted evil script", `<script type=module src=https://evil.example/x.js></script>`, notAdmitted},
		{"whitespace around equals", `<script type="module" src = "/app/assets/evil.js"></script>`, notAdmitted},
		{"uppercase attribute name", `<script type="module" SRC="/app/assets/evil.js"></script>`, notAdmitted},
		{"duplicate src, evil second", `<script type="module" src="/app/assets/app.js" src="https://evil.example/x.js"></script>`, notAdmitted},
		{"slash before attribute", `<script/src="https://evil.example/x.js"></script>`, notAdmitted},
		{"srcset with evil candidate", `<img srcset="/app/assets/app.css 1x, //evil.example/x.png 2x">`, notAdmitted},
		{"modulepreload of unlisted file", `<link rel="modulepreload" href="/app/assets/evil.js">`, notAdmitted},
		{"entity-encoded path", `<script type="module" src="&#47;app&#47;assets&#47;evil.js"></script>`, notAdmitted},
		{"single-quoted javascript URL", `<a href='javascript:alert(1)'>x</a>`, notAdmitted},
		{"base element", `<base href="https://evil.example/">`, "has a <base> element"},
		{"meta refresh", `<meta http-equiv="refresh" content="0;url=https://evil.example/">`, "has a <meta http-equiv>"},
		{"inline script body", `<script type="module" src="/app/assets/app.js">alert(1)</script>`, "has an inline <script>"},
		{"script without src", `<script type="module">alert(1)</script>`, "has an inline <script>"},
		{"data-src is not src", `<script type="module" data-src="/app/assets/app.js"></script>`, "has an inline <script>"},
		{"unterminated start tag", `<script src="/app/assets/app.js"`, "unterminated"},
		{"unterminated quoted value", `<script src="/app/assets/app.js></script>`, "unterminated"},
		{"unterminated comment", `<!-- <script src="/app/assets/app.js"></script>`, "unterminated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := loadAppBundle(fixtureWithShell(tc.shell))
			if app.err == nil {
				t.Fatalf("shell should fail closed; it derived %d routes", len(app.routes))
			}
			if !strings.Contains(app.err.Error(), tc.want) {
				t.Fatalf("error %q does not say %q", app.err, tc.want)
			}
		})
	}

	admitted := []struct {
		name  string
		shell string
	}{
		{"double-quoted", `<script type="module" src="/app/assets/app.js"></script><link rel="stylesheet" href="/app/assets/app.css">`},
		{"single-quoted", `<script type='module' src='/app/assets/app.js'></script><link rel='stylesheet' href='/app/assets/app.css'>`},
		{"unquoted and whitespace around equals", `<script type=module src = /app/assets/app.js></script><link rel=stylesheet href=/app/assets/app.css>`},
		{"srcset and modulepreload of admitted files", `<script type="module" src="/app/assets/app.js"></script><link rel="stylesheet" href="/app/assets/app.css"><link rel="modulepreload" href="/app/assets/chunk.js"><img srcset="/app/assets/app.css 1x, /app/assets/dyn.js 2x">`},
		{"evil markup inside a comment is inert", `<!-- <script src="https://evil.example/x.js"></script> --><script type="module" src="/app/assets/app.js"></script><link rel="stylesheet" href="/app/assets/app.css">`},
	}
	for _, tc := range admitted {
		t.Run("admits "+tc.name, func(t *testing.T) {
			if app := loadAppBundle(fixtureWithShell(tc.shell)); app.err != nil {
				t.Fatalf("admitted shell rejected: %v", app.err)
			}
		})
	}
}

// The theme script is the one file outside the manifest that the shell may load. The browser runs it before the
// first paint, so the shell may load it only as its first script: a bare classic script in <head>, before any
// module script.
const (
	themeBootTag   = `<script src="/app/theme-boot.js"></script>`
	themeModuleTag = `<script type="module" crossorigin src="/app/assets/app.js"></script>`
	themeBootShell = `<!doctype html><html><head>` + themeBootTag + themeModuleTag +
		`<link rel="stylesheet" crossorigin href="/app/assets/app.css"></head><body><div id="root"></div></body></html>`
	themeBootBody = "(function () {})();"
)

// themeFixture is the fixture with the theme script in the build, as the frontend emits it.
func themeFixture(shell string) fstest.MapFS {
	source := fixtureWithShell(shell)
	source["theme-boot.js"] = &fstest.MapFile{Data: []byte(themeBootBody)}
	return source
}

// Mutation check: admitting the theme script wherever a shell names it, or whenever the file is in the build, must
// fail the refusals in this test.
func TestAppAdmitsTheThemeScriptOnlyAsTheFirstClassicScript(t *testing.T) {
	app := loadAppBundle(themeFixture(themeBootShell))
	if app.err != nil {
		t.Fatalf("a first, classic theme script should derive: %v", app.err)
	}
	handler := newHandler(appTestStore(t), HandlerOptions{}, app)
	recorder := appTestRequest(t, handler, http.MethodGet, "/app/theme-boot.js")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /app/theme-boot.js status=%d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("GET /app/theme-boot.js content-type=%q, want text/javascript", got)
	}
	if !bytes.Equal(recorder.Body.Bytes(), []byte(themeBootBody)) {
		t.Error("GET /app/theme-boot.js does not serve the build file")
	}
	assertSecurityHeaders(t, "/app/theme-boot.js", recorder)

	const notFirst = "loads /app/theme-boot.js other than as its first, classic script before <body>"
	refused := []struct{ name, shell, want string }{
		{"after the module script", `<script type="module" src="/app/assets/app.js"></script>` + themeBootTag, notFirst},
		{"as a module", `<script type="module" src="/app/theme-boot.js"></script>` + themeModuleTag, notFirst},
		{"deferred", `<script defer src="/app/theme-boot.js"></script>` + themeModuleTag, notFirst},
		{"async", `<script async src="/app/theme-boot.js"></script>` + themeModuleTag, notFirst},
		{"nomodule", `<script nomodule src="/app/theme-boot.js"></script>` + themeModuleTag, notFirst},
		{"with a type attribute", `<script type="text/javascript" src="/app/theme-boot.js"></script>` + themeModuleTag, notFirst},
		{"with an extra attribute", `<script data-x="1" src="/app/theme-boot.js"></script>` + themeModuleTag, notFirst},
		{"src first, then defer", `<script src="/app/theme-boot.js" defer></script>` + themeModuleTag, notFirst},
		{"src first, then async", `<script src="/app/theme-boot.js" async></script>` + themeModuleTag, notFirst},
		{"src first, then nomodule", `<script src="/app/theme-boot.js" nomodule></script>` + themeModuleTag, notFirst},
		{"src first, then a type", `<script src="/app/theme-boot.js" type="text/javascript"></script>` + themeModuleTag, notFirst},
		{"src first, then an extra attribute", `<script src="/app/theme-boot.js" data-x="1"></script>` + themeModuleTag, notFirst},
		{"after <body>", `<body>` + themeBootTag + themeModuleTag + `</body>`, notFirst},
		{"as a preload, not a script", `<link rel="preload" as="script" href="/app/theme-boot.js">` + themeBootTag + themeModuleTag, notFirst},
		{"entity-encoded", `<script src="&#47;app&#47;theme-boot.js"></script>` + themeModuleTag, "which the manifest does not derive"},
	}
	for _, tc := range refused {
		t.Run("refuses the theme script "+tc.name, func(t *testing.T) {
			app := loadAppBundle(themeFixture(tc.shell))
			if app.err == nil {
				t.Fatal("the shell should fail closed")
			}
			if !strings.Contains(app.err.Error(), tc.want) {
				t.Fatalf("error %q does not say %q", app.err, tc.want)
			}
		})
	}
	t.Run("refuses a theme script that the build does not contain", func(t *testing.T) {
		err := loadAppBundle(fixtureWithShell(themeBootShell)).err
		if err == nil || !strings.Contains(err.Error(), "theme-boot.js, which is missing from the build") {
			t.Fatalf("err=%v, want a missing theme script", err)
		}
	})
}

// Admitting the theme script opens no neighbour: a file beside it, a path under it or a case variant stays 404, and
// so does the script itself when the shell does not load it.
func TestAppThemeScriptAdmissionOpensNoNeighbours(t *testing.T) {
	source := themeFixture(themeBootShell)
	source["theme-boot.js.map"] = &fstest.MapFile{Data: []byte("neighbour")}
	source["theme-boot-copy.js"] = &fstest.MapFile{Data: []byte("neighbour")}
	source["assets/theme-boot.js"] = &fstest.MapFile{Data: []byte("neighbour")}
	handler := newHandler(appTestStore(t), HandlerOptions{}, loadAppBundle(source))
	for _, target := range []string{
		"/app/theme-boot.js.map", "/app/theme-boot-copy.js", "/app/assets/theme-boot.js",
		"/app/theme-boot.js/", "/app/theme-boot.js%00", "/app/theme-boot.JS", "/theme-boot.js",
	} {
		recorder := appTestRequest(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s status=%d, want 404", target, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "neighbour") {
			t.Errorf("GET %s leaked a neighbour", target)
		}
	}

	unloaded := reachableFixture()
	unloaded["theme-boot.js"] = &fstest.MapFile{Data: []byte(themeBootBody)}
	app := loadAppBundle(unloaded)
	if app.err != nil {
		t.Fatalf("a build with an unloaded theme script should still derive: %v", app.err)
	}
	if recorder := appTestRequest(t, newHandler(appTestStore(t), HandlerOptions{}, app), http.MethodGet, "/app/theme-boot.js"); recorder.Code != http.StatusNotFound {
		t.Errorf("GET /app/theme-boot.js status=%d, want 404: the shell does not load it", recorder.Code)
	}
}
