package webview

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
)

func BenchmarkBufferPublishEventBatch(b *testing.B) {
	buffer := NewBuffer(64)
	record := Record{Source: "benchmark", Types: []string{"record"}, Record: json.RawMessage(`{"payload":"bounded"}`)}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		buffer.publish(record)
	}
}

func BenchmarkPublishLineJSONPayload(b *testing.B) {
	tailer := &Tailer{buffer: NewBuffer(64), options: TailerOptions{MaxLineBytes: DefaultMaxLineBytes}}
	line := []byte(`{"proposal":{"tool":"read"},"correlation":{},"decision":{},"result":{}}`)
	source := Source{Name: "benchmark", Kind: "transcript"}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		tailer.publishLine(source, line, "epoch")
	}
}

func BenchmarkHistoryFlushEventBatch(b *testing.B) {
	path := filepath.Join(b.TempDir(), "history.json")
	buffer, err := NewHistoryBuffer(path, 64)
	if err != nil {
		b.Fatal(err)
	}
	record := Record{Source: "benchmark", Types: []string{"record"}, Record: json.RawMessage(`{"payload":"bounded"}`)}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		buffer.beginHistoryBatch()
		for event := 0; event < 8; event++ {
			buffer.publish(record)
		}
		buffer.endHistoryBatch()
	}
}

func BenchmarkManifestEndpointAllocations(b *testing.B) {
	buffer := NewBuffer(64)
	buffer.setSources([]SourceInfo{{Name: "benchmark", Kind: "transcript"}})
	for index := 0; index < 64; index++ {
		buffer.publish(Record{Source: "benchmark", Types: []string{"record"}, Record: json.RawMessage(`{"index":1}`)})
	}
	handler := NewHandler(buffer)
	request := httptest.NewRequest(http.MethodGet, "/manifest", nil)
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
	}
}

func BenchmarkTestCatalogEndpointAllocations(b *testing.B) {
	buffer := NewBuffer(64)
	for index := 0; index < 64; index++ {
		if err := buffer.applyGoTestEvent(goTestEvent{Action: "pass", Package: "benchmark", Test: testName(index)}, "benchmark", "epoch"); err != nil {
			b.Fatal(err)
		}
	}
	for index := 0; index < 64; index++ {
		buffer.publish(Record{Source: "benchmark", Types: []string{"record"}, Record: json.RawMessage(`{"index":1}`)})
	}
	handler := NewHandler(buffer)
	request := httptest.NewRequest(http.MethodGet, "/tests", nil)
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
	}
}

func testName(index int) string {
	return "TestBenchmark" + strconv.Itoa(index)
}
