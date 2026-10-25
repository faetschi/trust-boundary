//go:build linux

package productprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type providerDoerFunc func(*http.Request) (*http.Response, error)

func (f providerDoerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureSSE(model string, usage any) string {
	chunk, _ := json.Marshal(map[string]any{
		"id": "response-" + sentinelKey, "model": model, "created": 1,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "OK " + sentinelKey}, "finish_reason": "stop"}},
		"usage":   usage,
	})
	return "data: " + string(chunk) + "\n\ndata: [DONE]\n\n"
}

func TestProviderLiveCheckOfflineFixtures(t *testing.T) {
	goodUsage := map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}
	for _, scenario := range []struct {
		name               string
		status             int
		model, contentType string
		usage              any
		want               string
	}{
		{"success", 200, "deepseek-v4.1-flash", "text/event-stream", goodUsage, ""},
		{"redirect", 307, "", "text/event-stream", nil, "redirect refused"},
		{"wrong-model", 200, "wrong-" + sentinelKey, "text/event-stream", goodUsage, "model identity mismatch"},
		{"missing-usage", 200, "deepseek-v4.1-flash", "text/event-stream", nil, "usage evidence missing"},
		{"empty-usage", 200, "deepseek-v4.1-flash", "text/event-stream", map[string]any{}, "usage evidence missing"},
		{"bad-usage", 200, "deepseek-v4.1-flash", "text/event-stream", map[string]any{"prompt_tokens": 1, "completion_tokens": 99, "total_tokens": 100}, "usage evidence missing"},
		{"401", 401, "", "text/event-stream", nil, "authentication failed"},
		{"403", 403, "", "text/event-stream", nil, "authentication failed"},
		{"429", 429, "", "text/event-stream", nil, "quota or rate limit"},
		{"402", 402, "", "text/event-stream", nil, "quota or rate limit"},
		{"500", 500, "", "text/event-stream", nil, "HTTP failure"},
		{"wrong-protocol", 200, "deepseek-v4.1-flash", "application/json", goodUsage, "unsupported provider protocol"},
		{"malformed-sse", 200, "deepseek-v4.1-flash", "text/event-stream", goodUsage, "unsupported or malformed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+sentinelKey {
					t.Error("unexpected request")
				}
				var body struct {
					Model    string `json:"model"`
					Cap      uint64 `json:"max_completion_tokens"`
					Stream   bool   `json:"stream"`
					Parallel bool   `json:"parallel_tool_calls"`
					Thinking struct {
						Type string `json:"type"`
					} `json:"thinking"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "deepseek-v4.1-flash" || body.Cap != 16 || !body.Stream || body.Parallel || body.Thinking.Type != "disabled" {
					t.Errorf("outbound request: %+v %v", body, err)
				}
				w.Header().Set("Content-Type", scenario.contentType)
				if scenario.status == 307 {
					w.Header().Set("Location", serverRedirectTarget(sentinelKey))
				}
				w.WriteHeader(scenario.status)
				if scenario.name == "malformed-sse" {
					_, _ = fmt.Fprint(w, "data: "+sentinelKey+"\n\n")
					return
				}
				_, _ = fmt.Fprint(w, fixtureSSE(scenario.model, scenario.usage))
			}))
			defer server.Close()
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			// The sole test seam maps the fixed compiled URL to loopback. It is not
			// reachable from the product CLI or any production configuration.
			doer := providerDoerFunc(func(r *http.Request) (*http.Response, error) {
				mapped := r.Clone(r.Context())
				mapped.URL, _ = url.Parse(server.URL)
				mapped.Host = mapped.URL.Host
				mapped.Header = r.Header.Clone()
				mapped.Header.Set("Authorization", "Bearer "+sentinelKey)
				response, err := client.Do(mapped)
				if response != nil {
					response.Request = r
				}
				return response, err
			})
			p, _ := Lookup(OpenCodeGo)
			result, err := checkWithDoer(context.Background(), p, 16, doer)
			if scenario.want == "" {
				if err != nil || !result.ModelMatch || !result.UsagePresent {
					t.Fatalf("check: %+v %v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("want %q got %v", scenario.want, err)
			}
			if calls.Load() != 1 || result.Calls != 1 {
				t.Fatalf("expected one call, got %d (%d records)", calls.Load(), result.Calls)
			}
			assertRedacted(t, result, err)
		})
	}
}

func serverRedirectTarget(key string) string { return "http://127.0.0.1:1/never-follow/" + key }

func TestProviderLiveCheckNetworkAndWrappedErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("closed server called") }))
	endpoint := server.URL
	server.Close()
	for _, network := range []bool{true, false} {
		t.Run(fmt.Sprint(network), func(t *testing.T) {
			p, _ := Lookup(OpenCodeGo)
			d := providerDoerFunc(func(r *http.Request) (*http.Response, error) {
				if !network {
					return nil, fmt.Errorf("outer %s: %w", sentinelKey, fmt.Errorf("inner: %w", errors.New(sentinelKey)))
				}
				mapped := r.Clone(r.Context())
				mapped.URL, _ = url.Parse(endpoint)
				mapped.Host = mapped.URL.Host
				return http.DefaultClient.Do(mapped)
			})
			result, err := checkWithDoer(context.Background(), p, 16, d)
			if err == nil || !strings.Contains(err.Error(), "network failure") || result.Calls != 1 {
				t.Fatalf("network check: %+v %v", result, err)
			}
			assertRedacted(t, result, err)
		})
	}
}

func TestProviderCheckRefusesBeforeDispatch(t *testing.T) {
	p, _ := Lookup(OpenCodeGo)
	d := providerDoerFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected dispatch"); return nil, nil })
	for _, cap := range []uint64{0, p.MaxCompletionTokens + 1} {
		if _, err := checkWithDoer(context.Background(), p, cap, d); err == nil {
			t.Fatal("invalid cap accepted")
		}
	}
	if _, err := testStore(t).Check(context.Background(), OpenCodeGo, 16); !errors.Is(err, ErrCredential) {
		t.Fatal("missing credential accepted")
	}
}
