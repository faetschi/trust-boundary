package governanceview

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed index.html
var page embed.FS

type HandlerOptions struct {
	// AllowedHost and AllowedOrigin are exact values configured after the
	// loopback listener is bound. Empty values still require a loopback Host and
	// reject non-empty cross-origin requests.
	AllowedHost   string
	AllowedOrigin string
}

const responseWriteTimeout = 5 * time.Second

// NewHandler exposes GET-only snapshots, retained registered records, and SSE.
// It has no session-control, launch, stop, approval, filesystem, or provider
// routes.
func NewHandler(store *Store, options HandlerOptions) http.Handler {
	if store == nil {
		fallback, _ := NewStore(NewUnavailableSnapshot(), DefaultRecordCapacity)
		store = fallback
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if !authorizeRequest(writer, request, options) {
			return
		}
		if request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		setSecurityHeaders(writer, "text/html; charset=utf-8")
		content, err := page.ReadFile("index.html")
		if err != nil {
			http.Error(writer, "embedded companion unavailable", http.StatusInternalServerError)
			return
		}
		_ = boundedWrite(writer, func() error {
			_, err := writer.Write(content)
			return err
		})
	})
	mux.HandleFunc("/api/v1/snapshot", func(writer http.ResponseWriter, request *http.Request) {
		if !authorizeGET(writer, request, options) {
			return
		}
		writeJSON(writer, store.Snapshot())
	})
	mux.HandleFunc("/api/v1/contract", func(writer http.ResponseWriter, request *http.Request) {
		if !authorizeGET(writer, request, options) {
			return
		}
		writeJSON(writer, map[string]any{
			"schema_version": ContractVersion,
			"max_records":    MaxRecords,
			"max_sources":    MaxSources,
			"modes":          []Mode{ModeFixture, ModeLive, ModeReplay, ModeUnavailable},
			"states":         []State{StatePending, StateDenied, StateFailed, StateWithheld, StateUnknown, StateCompleted},
		})
	})
	mux.HandleFunc("/api/v1/records/", func(writer http.ResponseWriter, request *http.Request) {
		if !authorizeGET(writer, request, options) {
			return
		}
		id := strings.TrimPrefix(request.URL.Path, "/api/v1/records/")
		if !identifierPattern.MatchString(id) {
			http.NotFound(writer, request)
			return
		}
		record, ok := store.Record(id)
		if !ok {
			http.NotFound(writer, request)
			return
		}
		writeJSON(writer, record)
	})
	mux.HandleFunc("/api/v1/events", func(writer http.ResponseWriter, request *http.Request) {
		if !authorizeGET(writer, request, options) {
			return
		}
		streamEvents(store, writer, request)
	})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setSecurityHeadersBase(writer)
		mux.ServeHTTP(writer, request)
	})
}

func authorizeGET(writer http.ResponseWriter, request *http.Request, options HandlerOptions) bool {
	if !authorizeRequest(writer, request, options) {
		return false
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return false
	}
	return true
}

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
	flusher, ok := writer.(http.Flusher)
	if !ok {
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
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	if err := boundedWrite(writer, func() error {
		_, err := io.WriteString(writer, ": ready\n\n")
		return err
	}); err != nil {
		return
	}
	flusher.Flush()
	if gap != nil {
		if writeSSEGap(writer, flusher, *gap) != nil {
			return
		}
	}
	for _, record := range subscription.Initial {
		if request.Context().Err() != nil || writeSSE(writer, flusher, store.Snapshot(), record) != nil {
			return
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case record, open := <-subscription.Records:
			if !open || writeSSE(writer, flusher, store.Snapshot(), record) != nil {
				return
			}
		case <-ticker.C:
			if err := boundedWrite(writer, func() error {
				_, err := io.WriteString(writer, ": keepalive\n\n")
				return err
			}); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(writer http.ResponseWriter, flusher http.Flusher, snapshot Snapshot, record Record) error {
	event := Event{SchemaVersion: ContractVersion, Cursor: formatCursor(snapshot.Epoch, record.Sequence), Record: record, Sources: append([]Source(nil), snapshot.Sources...)}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := boundedWrite(writer, func() error {
		_, err := fmt.Fprintf(writer, "id: %s\nevent: observation\ndata: %s\n\n", event.Cursor, encoded)
		return err
	}); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEGap(writer io.Writer, flusher http.Flusher, gap ReplayGap) error {
	encoded, err := json.Marshal(gap)
	if err != nil {
		return err
	}
	responseWriter, ok := writer.(http.ResponseWriter)
	if !ok {
		return fmt.Errorf("gap writer does not support bounded writes")
	}
	if err := boundedWrite(responseWriter, func() error {
		_, err := fmt.Fprintf(responseWriter, "event: gap\ndata: %s\n\n", encoded)
		return err
	}); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func setSecurityHeaders(writer http.ResponseWriter, contentType string) {
	setSecurityHeadersBase(writer)
	writer.Header().Set("Content-Type", contentType)
}

func setSecurityHeadersBase(writer http.ResponseWriter) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
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

func boundedWrite(writer http.ResponseWriter, write func() error) error {
	controller := http.NewResponseController(writer)
	if err := controller.SetWriteDeadline(time.Now().Add(responseWriteTimeout)); err != nil {
		// httptest.ResponseRecorder and other in-memory writers do not expose a
		// socket deadline. Real net/http connections do; tests still get the
		// same bounded payload path without pretending a recorder is a socket.
		if !errors.Is(err, http.ErrNotSupported) {
			return fmt.Errorf("bounded response writes unavailable: %w", err)
		}
		return write()
	}
	err := write()
	_ = controller.SetWriteDeadline(time.Time{})
	return err
}

func methodNotAllowed(writer http.ResponseWriter) {
	writer.Header().Set("Allow", http.MethodGet)
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
}
