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
			if !strings.Contains(recorder.Body.String(), "web chat app unavailable") || !strings.Contains(recorder.Body.String(), tc.wantReason) {
				t.Errorf("GET /app/ body %q does not explain the failure", recorder.Body.String())
			}
			for _, target := range []string{"/app/assets/app.js", "/app/index.html"} {
				if recorder := appTestRequest(t, handler, http.MethodGet, target); recorder.Code != http.StatusNotFound {
					t.Errorf("GET %s status=%d, want 404 while the app is unavailable", target, recorder.Code)
				}
			}
			if recorder := appTestRequest(t, handler, http.MethodGet, "/"); recorder.Code != http.StatusOK {
				t.Errorf("GET / status=%d, want 200: the vanilla UI must keep working", recorder.Code)
			}
			if recorder := appTestRequest(t, handler, http.MethodGet, "/api/v1/contract"); recorder.Code != http.StatusOK {
				t.Errorf("GET /api/v1/contract status=%d, want 200", recorder.Code)
			}
		})
	}
}

var (
	scriptElement   = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	scriptSource    = regexp.MustCompile(`\ssrc="(/app/[^"]+)"`)
	inlineStyleAttr = regexp.MustCompile(`(?i)\sstyle\s*=`)
	eventAttr       = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	urlAttr         = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*"([^"]*)"`)
	cssImport       = regexp.MustCompile(`(?i)@import\b`)
	cssExternalURL  = regexp.MustCompile(`(?i)url\(\s*['"]?\s*(https?:|data:|//|javascript:)`)
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

func checkShell(t *testing.T, target, text string, routes map[string]appRoute) {
	t.Helper()
	lower := strings.ToLower(text)
	scripts := scriptElement.FindAllStringSubmatch(text, -1)
	if strings.Count(lower, "<script") != len(scripts) {
		t.Errorf("%s has a <script> that is not a closed element", target)
	}
	for _, match := range scripts {
		if strings.TrimSpace(match[2]) != "" {
			t.Errorf("%s has an inline <script> body", target)
		}
		if !scriptSource.MatchString(match[1]) {
			t.Errorf("%s has a <script> without a same-origin /app/ src", target)
		}
	}
	if strings.Contains(lower, "<style") {
		t.Errorf("%s has an inline <style> element", target)
	}
	if inlineStyleAttr.MatchString(text) {
		t.Errorf("%s has a style= attribute", target)
	}
	if eventAttr.MatchString(text) || strings.Contains(lower, "javascript:") {
		t.Errorf("%s has an inline event handler or javascript: URL", target)
	}
	for _, match := range urlAttr.FindAllStringSubmatch(text, -1) {
		reference := match[1]
		if !strings.HasPrefix(reference, appPrefix) || strings.HasPrefix(reference, "//") {
			t.Errorf("%s references %q, which is not a same-origin /app/ path", target, reference)
			continue
		}
		if _, ok := routes[reference]; !ok {
			t.Errorf("%s references %q, which is not a derived route", target, reference)
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
