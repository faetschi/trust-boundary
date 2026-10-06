package webview

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

//go:embed index.html
var page embed.FS

func newHTTPHandler(buffer *Buffer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		content, err := page.ReadFile("index.html")
		if err != nil {
			http.Error(w, "embedded page unavailable", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(content)
	})
	mux.HandleFunc("/records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(buffer.Snapshot()); err != nil {
			return
		}
	})
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, buffer.snapshot())
	})
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, buffer.manifest())
	})
	mux.HandleFunc("/tests", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, buffer.testCatalog())
	})
	mux.HandleFunc("/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, buffer.retainedRunCatalog())
	})
	mux.HandleFunc("/runs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/runs/")
		if !runIDPattern.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		run, ok := buffer.retainedRun(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, run)
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		streamEvents(buffer, w, r)
	})
	return mux
}

func streamEvents(buffer *Buffer, w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("cursor")
	}
	subscription, gap := buffer.SubscribeAfter(cursor)
	defer subscription.Close()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, ": ready\n\n"); err != nil {
		return
	}
	flusher.Flush()
	if gap != nil {
		if writeSSEGap(w, flusher, *gap) != nil {
			return
		}
	}
	for _, record := range subscription.Initial {
		if r.Context().Err() != nil || writeSSE(w, flusher, buffer.Epoch(), record) != nil {
			return
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case record, open := <-subscription.Records:
			if !open || writeSSE(w, flusher, buffer.Epoch(), record) != nil {
				return
			}
		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w io.Writer, flusher http.Flusher, epoch string, record Record) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\ndata: %s\n\n", formatCursor(epoch, record.ID), encoded); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEGap(w io.Writer, flusher http.Flusher, gap ReplayGap) error {
	encoded, err := json.Marshal(gap)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: gap\ndata: %s\n\n", encoded); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", http.MethodGet)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
