package webview

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	subscription := buffer.Subscribe()
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
	for _, record := range subscription.Initial {
		if r.Context().Err() != nil || writeSSE(w, flusher, record) != nil {
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
			if !open || writeSSE(w, flusher, record) != nil {
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

func writeSSE(w io.Writer, flusher http.Flusher, record Record) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", record.ID, encoded); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", http.MethodGet)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
