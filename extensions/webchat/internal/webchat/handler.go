package webchat

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
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
}

type staticRoute struct {
	asset       string
	contentType string
}

// staticRoutes is a closed allowlist. Only these exact paths are served; there
// is no directory listing, path joining, or filesystem access.
var staticRoutes = map[string]staticRoute{
	"/":                      {"assets/index.html", "text/html; charset=utf-8"},
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
		if !authorizeRequest(writer, request, options) {
			return
		}
		path := request.URL.Path
		if route, ok := staticRoutes[path]; ok {
			serveAsset(writer, route)
			return
		}
		if escaped := request.URL.EscapedPath(); escaped == "/app" || strings.HasPrefix(escaped, appPrefix) {
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
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
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

// setSecurityHeadersBase applies a strict, no-inline policy: the client is a set
// of same-origin static modules with no inline script or style.
func setSecurityHeadersBase(writer http.ResponseWriter) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'")
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

func methodNotAllowed(writer http.ResponseWriter) {
	writer.Header().Set("Allow", http.MethodGet)
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
}
