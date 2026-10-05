package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
)

type oneShotTestDoer struct {
	response []byte
	err      error
	calls    int
}

func (d *oneShotTestDoer) Do(request *http.Request) (*http.Response, error) {
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	if request.Method != http.MethodPost || request.URL.String() != openrouter.Endpoint {
		return nil, errors.New("test received a non-registered request")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(d.response)),
		Request:    request,
	}, nil
}

func TestRunOneShotPrintsOnlyRedactedBoundedSummary(t *testing.T) {
	const key = "summary-private-key-987"
	credentials, err := openrouter.LoadCredentialsFrom(func(name string) (string, bool) {
		values := map[string]string{openrouter.APIKeyEnv: key, openrouter.ModelEnv: "vendor/model"}
		value, ok := values[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	response := append(sseChunk(t, key, "vendor/model", map[string]any{"role": "assistant", "content": key}, nil),
		sseChunk(t, key, "vendor/model", map[string]any{}, "stop")...)
	response = append(response, []byte("data: [DONE]\n\n")...)
	doer := &oneShotTestDoer{response: response}
	var output bytes.Buffer
	if err := runOneShot(context.Background(), credentials, doer, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Contains(text, key) || strings.Contains(text, "Bearer ") || strings.Contains(text, "assistant") {
		t.Fatal("summary leaked credential, auth header, or raw response")
	}
	for _, field := range []string{
		"redacted_summary=true", "status_code=200", "response_id=\"[REDACTED]\"",
		"response_model=\"vendor/model\"", "captured_tool_calls=0",
		"ordered_four_tool_manifest_respected=true", "response_bytes=", "containment=not-established", "g1=false",
	} {
		if !strings.Contains(text, field) {
			t.Errorf("summary omitted %q: %s", field, text)
		}
	}
	if strings.Count(text, "status_code=") != 1 || doer.calls != 1 {
		t.Fatal("one-shot exchange did not produce exactly one summary and transport call")
	}
}

func TestRunOneShotRedactsCredentialFromErrors(t *testing.T) {
	const key = "one-shot-error-secret-654"
	credentials, err := openrouter.LoadCredentialsFrom(func(name string) (string, bool) {
		if name == openrouter.APIKeyEnv {
			return key, true
		}
		if name == openrouter.ModelEnv {
			return "vendor/model", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runOneShot(context.Background(), credentials, &oneShotTestDoer{err: errors.New("failure: " + key)}, &output)
	if err == nil || strings.Contains(err.Error(), key) || strings.Contains(output.String(), key) {
		t.Fatal("one-shot error or output leaked credential")
	}
}

func sseChunk(t *testing.T, responseID, model string, delta any, finish any) []byte {
	t.Helper()
	chunk := map[string]any{
		"id": responseID, "model": model, "created": 123,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(fmt.Sprintf("data: %s\n\n", encoded))
}
