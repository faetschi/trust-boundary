package webview

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTailerEmitsAppendedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.ndjson")
	if err := os.WriteFile(path, []byte(`{"ignored":"startup history"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	tailer := newTestTailer(t, path, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	startTestTailer(t, tailer, ctx)
	defer stopTestTailer(t, cancel, tailer)

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, `{"proposal":{"tool":"read"},"correlation":{},"decision":{},"result":{}}`+"\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	records := waitForRecords(t, buffer, 1)
	if len(records) != 1 {
		t.Fatalf("records = %d; want only the appended line", len(records))
	}
	if got, want := strings.Join(records[0].Types, ","), "proposal,correlation,decision,result"; got != want {
		t.Fatalf("types = %q; want %q", got, want)
	}
	if !strings.Contains(string(records[0].Record), `"tool":"read"`) {
		t.Fatalf("record did not retain parsed JSON line: %s", records[0].Record)
	}
}

func TestEventsReplayExistingBufferedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.ndjson")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	tailer := newTestTailer(t, path, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	startTestTailer(t, tailer, ctx)
	defer stopTestTailer(t, cancel, tailer)

	appendLine(t, path, `{"proposal":{"tool":"edit"},"correlation":{},"decision":{},"result":{}}`)
	waitForRecords(t, buffer, 1)

	server := httptest.NewServer(NewHandler(buffer))
	defer server.Close()
	response, err := http.Get(server.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", response.StatusCode)
	}
	event := readSSEData(t, response.Body)
	var replayed Record
	if err := json.Unmarshal([]byte(event), &replayed); err != nil {
		t.Fatalf("decode replayed event: %v", err)
	}
	if replayed.ID != 1 || !strings.Contains(string(replayed.Record), `"tool":"edit"`) {
		t.Fatalf("unexpected replayed record: %+v", replayed)
	}
}

func TestTailerSurfacesMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.ndjson")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	tailer := newTestTailer(t, path, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	startTestTailer(t, tailer, ctx)
	defer stopTestTailer(t, cancel, tailer)

	appendLine(t, path, `not-json <script>alert(1)</script>`)
	records := waitForRecords(t, buffer, 1)
	if !strings.Contains(strings.Join(records[0].Types, ","), "malformed") {
		t.Fatalf("malformed line types = %v", records[0].Types)
	}
	if records[0].RawLine != `not-json <script>alert(1)</script>` || records[0].ParseError == "" {
		t.Fatalf("malformed line was not retained honestly: %+v", records[0])
	}
}

func TestTailerSurfacesOversizedLinesWithBoundedPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.ndjson")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	tailer := newTestTailer(t, path, buffer, TailerOptions{
		PollInterval: 5 * time.Millisecond,
		MaxLineBytes: 16,
	})
	ctx, cancel := context.WithCancel(context.Background())
	startTestTailer(t, tailer, ctx)
	defer stopTestTailer(t, cancel, tailer)

	appendLine(t, path, strings.Repeat("x", 1024))
	records := waitForRecords(t, buffer, 1)
	if !strings.Contains(records[0].ParseError, "configured maximum of 16 bytes") {
		t.Fatalf("oversized line error = %q", records[0].ParseError)
	}
	if len(records[0].RawLine) > previewBytes {
		t.Fatalf("oversized line preview has %d bytes; max %d", len(records[0].RawLine), previewBytes)
	}
}

func TestTailerReopensAfterTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.ndjson")
	if err := os.WriteFile(path, []byte("initial line longer than the replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	tailer := newTestTailer(t, path, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	startTestTailer(t, tailer, ctx)
	defer stopTestTailer(t, cancel, tailer)

	if err := os.WriteFile(path, []byte(`{"result":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	records := waitForRecords(t, buffer, 1)
	if len(records) != 1 || !strings.Contains(string(records[0].Record), `"result":true`) {
		t.Fatalf("tailer did not read replacement after truncation: %+v", records)
	}
}

func TestTailerOpensSourceReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	original := []byte(`{"version":1,"sequence":1}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	buffer := NewBuffer(8)
	tailer := newTestTailer(t, path, buffer, TailerOptions{PollInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	startTestTailer(t, tailer, ctx)
	stopTestTailer(t, cancel, tailer)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("tailer modified its source: content=%q before=%v after=%v", got, before.ModTime(), after.ModTime())
	}
}

func TestEventsClientDisconnectIsClean(t *testing.T) {
	buffer := NewBuffer(4)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		NewHandler(buffer).ServeHTTP(w, r)
		close(done)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE readiness: %v", err)
		}
		if line == "\n" {
			break
		}
	}
	cancel()
	_ = response.Body.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop after client disconnect")
	}
}

func TestRecordsEndpointIsBounded(t *testing.T) {
	buffer := NewBuffer(2)
	for _, line := range []string{`{"n":1}`, `{"n":2}`, `{"n":3}`} {
		buffer.publish(Record{Source: "test", Types: []string{"record"}, Record: json.RawMessage(line)})
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/records", nil)
	NewHandler(buffer).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", response.Code)
	}
	var records []Record
	if err := json.Unmarshal(response.Body.Bytes(), &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != 2 || records[1].ID != 3 {
		t.Fatalf("records endpoint did not return the bounded recent window: %+v", records)
	}
}

func TestIndexServesEmbeddedUntrustedPresentationPage(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	NewHandler(NewBuffer(4)).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", response.Code)
	}
	page := response.Body.String()
	if !strings.Contains(page, "untrusted presentation client — read-only; not in the claim path") {
		t.Fatal("embedded page is missing the untrusted/read-only banner")
	}
	if strings.Contains(page, "https://") || strings.Contains(page, "http://") {
		t.Fatal("embedded page unexpectedly references an external asset or service")
	}
}

func newTestTailer(t *testing.T, path string, buffer *Buffer, options TailerOptions) *Tailer {
	t.Helper()
	tailer, err := NewTailer([]Source{{Name: "test transcript", Path: path, Kind: "transcript"}}, buffer, options)
	if err != nil {
		t.Fatal(err)
	}
	return tailer
}

func startTestTailer(t *testing.T, tailer *Tailer, ctx context.Context) {
	t.Helper()
	if err := tailer.Start(ctx); err != nil {
		t.Fatal(err)
	}
}

func stopTestTailer(t *testing.T, cancel context.CancelFunc, tailer *Tailer) {
	t.Helper()
	cancel()
	select {
	case err := <-tailer.Done():
		if err != nil {
			t.Fatalf("tailer stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tailer did not stop after cancellation")
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, line+"\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitForRecords(t *testing.T, buffer *Buffer, count int) []Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		records := buffer.Snapshot()
		if len(records) >= count {
			return records
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d records; got %d", count, len(buffer.Snapshot()))
	return nil
}

func readSSEData(t *testing.T, body io.Reader) string {
	t.Helper()
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE stream: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
}
