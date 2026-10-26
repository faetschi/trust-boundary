package webchat

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

//go:embed assets
var assetsFS embed.FS

const responseWriteTimeout = 5 * time.Second

// HandlerOptions configures the presentation gateway. AllowedHost and
// AllowedOrigin are exact values configured after the loopback listener is
// bound. Empty values still require a loopback Host and reject any non-empty
// cross-origin request.
type HandlerOptions struct {
	AllowedHost   string
	AllowedOrigin string
	// Live, when set, replaces the read-only API with the live gateway. It owns the
	// Host and Origin checks, the /api/ routes and the pairing page; the document and
	// asset routes still use this handler.
	Live *LiveGateway
}

type staticRoute struct {
	asset       string
	contentType string
}

// Document routes. "/" redirects to the React app at /app/, and serves the legacy
// workspace only when that app is unavailable. /legacy/ is the deprecated legacy
// workspace. Both are handled in newHandler, not in staticRoutes.
const (
	rootPath    = "/"
	legacyPath  = "/legacy/"
	faviconPath = "/favicon.ico"
)

// staticRoutes is a closed allowlist of the legacy workspace's assets. Only these
// exact paths are served; there is no directory listing, path joining, or
// filesystem access.
var staticRoutes = map[string]staticRoute{
	"/static/styles.css":     {"assets/styles.css", "text/css; charset=utf-8"},
	"/static/app.mjs":        {"assets/app.mjs", "text/javascript; charset=utf-8"},
	"/static/api.mjs":        {"assets/api.mjs", "text/javascript; charset=utf-8"},
	"/static/store.mjs":      {"assets/store.mjs", "text/javascript; charset=utf-8"},
	"/static/render.mjs":     {"assets/render.mjs", "text/javascript; charset=utf-8"},
	"/static/filters.mjs":    {"assets/filters.mjs", "text/javascript; charset=utf-8"},
	"/static/format.mjs":     {"assets/format.mjs", "text/javascript; charset=utf-8"},
	"/static/trajectory.mjs": {"assets/trajectory.mjs", "text/javascript; charset=utf-8"},
	"/static/markdown.mjs":   {"assets/markdown.mjs", "text/javascript; charset=utf-8"},
	"/static/backoff.mjs":    {"assets/backoff.mjs", "text/javascript; charset=utf-8"},
	"/static/stream.mjs":     {"assets/stream.mjs", "text/javascript; charset=utf-8"},
	"/static/anchor.mjs":     {"assets/anchor.mjs", "text/javascript; charset=utf-8"},
}

// NewHandler exposes GET-only snapshots, retained projected items, and cursor
// SSE. It has no session-control, launch, cancel, approval, filesystem, or
// provider routes, and it never reads a request body.
func NewHandler(store *Store, options HandlerOptions) http.Handler {
	return newHandler(store, options, embeddedApp())
}

func newHandler(store *Store, options HandlerOptions, app *appBundle) http.Handler {
	if store == nil || !store.initialized() {
		fallback, err := NewUnavailableStore()
		if err != nil {
			panic(fmt.Sprintf("webchat: unavailable store: %v", err))
		}
		store = fallback
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setSecurityHeadersBase(writer)
		if options.Live != nil {
			if !options.Live.authorizeHostOrigin(writer, request) {
				return
			}
			if options.Live.handles(request) {
				options.Live.serve(writer, request)
				return
			}
			if !authorizeMethodAndBody(writer, request) {
				return
			}
		} else if !authorizeRequest(writer, request, options) {
			return
		}
		path := request.URL.Path
		// The document routes match the escaped path, as /app/ does, so a
		// percent-encoded spelling of "/" or "/legacy/" is not a route.
		escaped := request.URL.EscapedPath()
		switch escaped {
		case rootPath:
			serveRoot(writer, request, app)
			return
		case legacyPath:
			serveLegacy(writer, app)
			return
		case faviconPath:
			// No body and no Content-Type: the base security headers are already set
			// above for every response.
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if route, ok := staticRoutes[path]; ok {
			serveAsset(writer, route)
			return
		}
		if escaped == "/app" || strings.HasPrefix(escaped, appPrefix) {
			serveApp(writer, request, app, escaped)
			return
		}
		switch {
		case path == "/api/v1/snapshot":
			writeJSON(writer, store.Snapshot())
		case path == "/api/v1/contract":
			writeJSON(writer, contractDescriptor())
		case strings.HasPrefix(path, "/api/v1/items/"):
			id := strings.TrimPrefix(path, "/api/v1/items/")
			if !referencePattern.MatchString(id) {
				http.NotFound(writer, request)
				return
			}
			item, found := store.Item(id)
			if !found {
				http.NotFound(writer, request)
				return
			}
			writeJSON(writer, item)
		case path == "/api/v1/events":
			streamEvents(store, writer, request)
		default:
			http.NotFound(writer, request)
		}
	})
}

func serveAsset(writer http.ResponseWriter, route staticRoute) {
	serveFile(writer, assetsFS, route.asset, route.contentType)
}

// serveRoot answers "/". When the React app derives, it redirects to appPrefix.
// That target is a constant: no part of the request (query, path, Host, Origin)
// reaches Location, so "/" cannot be an open redirect. When the app does not
// derive, "/" serves the legacy workspace with a fallback notice instead of a
// redirect into the app's 503.
func serveRoot(writer http.ResponseWriter, request *http.Request, app *appBundle) {
	if app.err != nil {
		serveLegacy(writer, app)
		return
	}
	http.Redirect(writer, request, appPrefix, http.StatusFound)
}

// serveLegacy serves the deprecated legacy workspace. The shipped assets do not
// change: the page is served with a notice inserted above <main>, so the legacy
// tests keep reading exactly the files on disk.
func serveLegacy(writer http.ResponseWriter, app *appBundle) {
	page, err := legacyPage(app)
	if err != nil {
		http.Error(writer, "legacy UI unavailable", http.StatusInternalServerError)
		return
	}
	setSecurityHeaders(writer, "text/html; charset=utf-8")
	_ = boundedWrite(writer, func() error {
		_, err := writer.Write(page)
		return err
	})
}

// legacyAnchor is where the legacy notice is inserted. It must occur exactly once
// in the shipped shell; legacyPage refuses to serve the page otherwise.
const legacyAnchor = `<main class="layout" id="layout">`

func legacyPage(app *appBundle) ([]byte, error) {
	shell, err := fs.ReadFile(assetsFS, "assets/index.html")
	if err != nil {
		return nil, err
	}
	if bytes.Count(shell, []byte(legacyAnchor)) != 1 {
		return nil, errors.New("the legacy shell has no unique insertion point for its notice")
	}
	return bytes.Replace(shell, []byte(legacyAnchor), []byte(legacyNotice(app)+legacyAnchor), 1), nil
}

// The notices reuse the gateway's notice classes, so they need no inline style
// under the Content-Security-Policy. Only the reason in the fallback is variable,
// and it is escaped before it is inserted.
const (
	legacyDeprecatedNotice = `<div class="notices legacy-notice"><div class="notice" data-variant="gap" role="note"><span class="notice-icon" aria-hidden="true">!</span><span><span class="notice-kind">Deprecated: legacy workspace — use /app/</span><span class="notice-text">This page stays until the React workspace is signed off, then it is removed. <a href="/app/">Open the React app</a>.</span></span></div></div>`
	legacyFallbackNotice   = `<div class="notices legacy-notice"><div class="notice" data-variant="error" role="note"><span class="notice-icon" aria-hidden="true">!</span><span><span class="notice-kind">Fallback: legacy workspace</span><span class="notice-text">The React app at /app/ is unavailable (%s). This deprecated page is the fallback until the app is restored.</span></span></div></div>`
)

func legacyNotice(app *appBundle) string {
	if app.err != nil {
		return fmt.Sprintf(legacyFallbackNotice, html.EscapeString(app.err.Error()))
	}
	return legacyDeprecatedNotice
}

// serveApp serves /app/ only from the manifest-derived table. The lookup uses the
// escaped path, so a percent-encoded spelling of an app path is not a route.
// When the table could not be derived, /app/ answers 503 with the reason; every
// other path under /app is 404.
func serveApp(writer http.ResponseWriter, request *http.Request, app *appBundle, escaped string) {
	if app.err != nil {
		if escaped == appPrefix {
			http.Error(writer, "web chat app unavailable: "+app.err.Error(), http.StatusServiceUnavailable)
			return
		}
		http.NotFound(writer, request)
		return
	}
	route, ok := app.routes[escaped]
	if !ok {
		http.NotFound(writer, request)
		return
	}
	serveFile(writer, app.source, route.name, route.contentType)
}

func serveFile(writer http.ResponseWriter, source fs.FS, name, contentType string) {
	content, err := fs.ReadFile(source, name)
	if err != nil {
		http.Error(writer, "embedded asset unavailable", http.StatusInternalServerError)
		return
	}
	setSecurityHeaders(writer, contentType)
	_ = boundedWrite(writer, func() error {
		_, err := writer.Write(content)
		return err
	})
}

func contractDescriptor() map[string]any {
	return map[string]any{
		"schema_version":   ContractVersion,
		"document_version": DocumentVersion,
		"gap_version":      GapVersion,
		"source_classes":   []SourceClass{SourceFixture, SourceLive, SourceReplay, SourceUnavailable},
		"item_types":       []ItemType{ItemMessage, ItemActivity, ItemDecision, ItemResult},
		"result_states":    []ResultState{ResultPending, ResultSucceeded, ResultDenied, ResultFailed, ResultUnknown, ResultLost},
		"max_items":        MaxItems,
		"max_streams":      MaxStreams,
		"max_text_bytes":   MaxTextBytes,
		"max_detail_bytes": MaxDetailBytes,
		"controls":         []string{},
		"read_only":        true,
		"note":             "presentation projection only; the gateway holds no policy, effect, or publication authority",
	}
}

// authorizeRequest enforces a loopback/exact host, an exact or absent Origin,
// the GET method, and the absence of a request body. Rejecting bodies removes
// the slow-body request surface entirely rather than reading and bounding it.
func authorizeRequest(writer http.ResponseWriter, request *http.Request, options HandlerOptions) bool {
	if options.AllowedHost != "" {
		if request.Host != options.AllowedHost {
			http.Error(writer, "host not allowed", http.StatusForbidden)
			return false
		}
	} else if !loopbackHost(request.Host) {
		http.Error(writer, "loopback host required", http.StatusForbidden)
		return false
	}
	if origin := request.Header.Get("Origin"); origin != "" {
		if options.AllowedOrigin == "" || origin != options.AllowedOrigin {
			http.Error(writer, "origin not allowed", http.StatusForbidden)
			return false
		}
	}
	return authorizeMethodAndBody(writer, request)
}

// authorizeMethodAndBody applies the read-only gateway's method policy and refuses
// any request body.
func authorizeMethodAndBody(writer http.ResponseWriter, request *http.Request) bool {
	if methods := allowedMethods(request.URL.EscapedPath()); !slices.Contains(methods, request.Method) {
		methodNotAllowed(writer, methods)
		return false
	}
	if request.ContentLength != 0 || len(request.TransferEncoding) > 0 {
		// Close the connection instead of letting net/http drain an unread body,
		// so a slow body cannot hold the handler open.
		writer.Header().Set("Connection", "close")
		http.Error(writer, "request body not allowed", http.StatusBadRequest)
		return false
	}
	return true
}

func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport == "localhost"
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func streamEvents(store *Store, writer http.ResponseWriter, request *http.Request) {
	if _, ok := writer.(http.Flusher); !ok {
		http.Error(writer, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	cursor := request.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = request.URL.Query().Get("cursor")
	}
	subscription, gap := store.SubscribeAfter(cursor)
	defer subscription.Close()
	setSecurityHeaders(writer, "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store, no-cache, no-transform")
	writer.Header().Set("Connection", "keep-alive")
	writer.WriteHeader(http.StatusOK)
	if err := boundedWrite(writer, func() error {
		_, err := io.WriteString(writer, ": ready\n\n")
		return err
	}); err != nil {
		return
	}
	if gap != nil {
		if writeGap(writer, *gap) != nil {
			return
		}
	}
	// Send the non-item projection captured at subscription time before the item
	// replay, so a session/panel/stream change that happened between the client's
	// snapshot fetch and this subscription cannot be missed.
	header := subscription.Session
	panels := subscription.Panels
	streams := append([]StreamStatus(nil), subscription.Streams...)
	if writeEvent(writer, Event{
		SchemaVersion: ContractVersion,
		Cursor:        formatCursor(store.epoch, store.next),
		Kind:          "session",
		Session:       &header,
		Panels:        &panels,
		Streams:       streams,
	}) != nil {
		return
	}
	for _, item := range subscription.Initial {
		if request.Context().Err() != nil {
			return
		}
		if writeItem(writer, store, item) != nil {
			return
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-subscription.Events:
			if !open || writeEvent(writer, event) != nil {
				return
			}
		case <-ticker.C:
			if err := boundedWrite(writer, func() error {
				_, err := io.WriteString(writer, ": keepalive\n\n")
				return err
			}); err != nil {
				return
			}
		}
	}
}

func writeItem(writer http.ResponseWriter, store *Store, item Item) error {
	event := Event{SchemaVersion: ContractVersion, Cursor: formatCursor(store.epoch, item.Sequence), Kind: "item", Item: itemPointer(item)}
	return writeEvent(writer, event)
}

func writeEvent(writer http.ResponseWriter, event Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	name := "item"
	if event.Kind == "session" {
		name = "session"
	}
	if err := boundedWrite(writer, func() error {
		_, err := fmt.Fprintf(writer, "id: %s\nevent: %s\ndata: %s\n\n", event.Cursor, name, encoded)
		return err
	}); err != nil {
		return err
	}
	return nil
}

func writeGap(writer http.ResponseWriter, gap ReplayGap) error {
	encoded, err := json.Marshal(gap)
	if err != nil {
		return err
	}
	return boundedWrite(writer, func() error {
		_, err := fmt.Fprintf(writer, "event: gap\ndata: %s\n\n", encoded)
		return err
	})
}

func setSecurityHeaders(writer http.ResponseWriter, contentType string) {
	setSecurityHeadersBase(writer)
	writer.Header().Set("Content-Type", contentType)
}

// contentSecurityPolicy is sent on every response, including the /app/ shell, the
// / redirect, the /legacy/ page and the 403, 404, 405 and 503 answers.
// scripts/csp-smoke.mjs reads this line from this file as its expected value, and
// app_test.go pins it.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"

// setSecurityHeadersBase applies a strict, no-inline policy: the client is a set
// of same-origin static modules with no inline script or style.
func setSecurityHeadersBase(writer http.ResponseWriter) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Content-Security-Policy", contentSecurityPolicy)
}

func writeJSON(writer http.ResponseWriter, value any) {
	setSecurityHeaders(writer, "application/json; charset=utf-8")
	encoded, err := json.Marshal(value)
	if err != nil {
		http.Error(writer, "response encoding failed", http.StatusInternalServerError)
		return
	}
	_ = boundedWrite(writer, func() error {
		_, err := writer.Write(append(encoded, '\n'))
		return err
	})
}

// boundedWrite bounds a response write with a deadline that also covers the
// flush. The deadline is only cleared after the bytes have been handed to the
// socket, so a stalled client cannot make a write outlive its deadline.
func boundedWrite(writer http.ResponseWriter, write func() error) error {
	controller := http.NewResponseController(writer)
	deadlineErr := controller.SetWriteDeadline(time.Now().Add(responseWriteTimeout))
	bounded := deadlineErr == nil
	if deadlineErr != nil && !errors.Is(deadlineErr, http.ErrNotSupported) {
		return fmt.Errorf("bounded response writes unavailable: %w", deadlineErr)
	}
	err := write()
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	if bounded {
		_ = controller.SetWriteDeadline(time.Time{})
	}
	return err
}

// allowedMethods is the method policy for a path. "/" and /favicon.ico also answer
// HEAD, so a HEAD probe sees the same answer as GET: the redirect or fallback at "/",
// and the 204 at /favicon.ico. HEAD is refused everywhere else, so it cannot open the
// event stream.
func allowedMethods(escaped string) []string {
	if escaped == rootPath || escaped == faviconPath {
		return []string{http.MethodGet, http.MethodHead}
	}
	return []string{http.MethodGet}
}

func methodNotAllowed(writer http.ResponseWriter, methods []string) {
	writer.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
}
