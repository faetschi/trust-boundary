//go:build linux

package providerbridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

type failingBody struct {
	data []byte
	err  error
}

func (b *failingBody) Read(p []byte) (int, error) {
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	return 0, b.err
}
func (b *failingBody) Close() error { return nil }

// A status that looks retryable is not an eligible failure unless the response was received whole:
// a read failure, a missing body or an endpoint the transport changed leave the outcome UNKNOWN.
func TestIncompleteOrPolicyViolatingResponsesAreNeverRetried(t *testing.T) {
	for name, respond := range map[string]func(*http.Request) (*http.Response, error){
		"truncated body after some bytes": func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Request: r,
				Body: &failingBody{data: []byte(`{"error":{"message":"Endpo`), err: io.ErrUnexpectedEOF}}, nil
		},
		"unexpected EOF with an empty body": func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Request: r, Body: &failingBody{err: io.ErrUnexpectedEOF}}, nil
		},
		"connection reset while reading": func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 502, Header: make(http.Header), Request: r,
				Body: &failingBody{data: []byte(unavailableBody), err: syscall.ECONNRESET}}, nil
		},
		"nil body": func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Request: r}, nil
		},
		"endpoint changed by the transport": func(r *http.Request) (*http.Response, error) {
			other := *r
			other.URL = &url.URL{Scheme: "https", Host: "elsewhere.example", Path: "/v1/chat/completions"}
			return &http.Response{StatusCode: 503, Header: make(http.Header), Request: &other,
				Body: io.NopCloser(strings.NewReader(unavailableBody))}, nil
		},
		"method changed by the transport": func(r *http.Request) (*http.Response, error) {
			other := *r
			other.Method = http.MethodGet
			return &http.Response{StatusCode: 503, Header: make(http.Header), Request: &other,
				Body: io.NopCloser(strings.NewReader(unavailableBody))}, nil
		},
		"transport error": func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") },
	} {
		attempts := 0
		cfg := recoveryTestConfig()
		hooks := &retryHooks{}
		hooks.apply(&cfg)
		journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "incomplete.audit"))
		conversation, err := newWithDoer(cfg, ApprovedModel, doerFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			return respond(r)
		}), journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
			t.Fatal(err)
		}
		if _, err := conversation.NextTurn(context.Background()); err == nil {
			t.Fatalf("%s: the turn succeeded", name)
		}
		kinds := journalKinds(t, journal)
		if attempts != 1 || len(hooks.reservations) != 1 || len(hooks.completions) != 0 ||
			kinds[len(kinds)-1] != "provider_exchange_unknown" {
			t.Errorf("%s: attempts=%d reservations=%d completions=%d kinds=%v, want one attempt ending UNKNOWN",
				name, attempts, len(hooks.reservations), len(hooks.completions), kinds)
		}
		for _, kind := range kinds {
			if kind == exchangeRetryKind {
				t.Errorf("%s: an incomplete response was journaled as retryable evidence", name)
			}
		}
	}
}

// The same retryable status with a complete body is retried, whatever its length (even empty).
func TestCompleteEmptyAndJSONErrorResponsesAreRetried(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "json error": unavailableBody} {
		attempts := 0
		cfg := recoveryTestConfig()
		hooks := &retryHooks{}
		hooks.apply(&cfg)
		journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "complete.audit"))
		success := &fixtureDoer{responses: []fixtureResponse{{responseID: "r", text: "done"}}}
		conversation, err := newWithDoer(cfg, ApprovedModel, doerFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{StatusCode: 503, Header: make(http.Header), Request: r,
					Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			return success.Do(r)
		}), journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
			t.Fatal(err)
		}
		if _, err := conversation.NextTurn(context.Background()); err != nil || attempts != 2 || len(hooks.reservations) != 2 {
			t.Fatalf("%s: err=%v attempts=%d reservations=%d, want a retry to success", name, err, attempts, len(hooks.reservations))
		}
	}
}

// A 503 that announces server-sent events and then carries model output is not a retryable error,
// on the live path and on replay of a forged retry record.
func TestSSEContentIsNotRetriedLiveNorAcceptedByReplay(t *testing.T) {
	sse := "event: message\ndata: {\"choices\":[{\"delta\":{\"content\":\"model output\"}}]}\n\n"
	cfg := recoveryTestConfig()
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "sse.audit"))
	attempts := 0
	conversation, err := newWithDoer(cfg, ApprovedModel, doerFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		header := make(http.Header)
		header.Set("Content-Type", "text/event-stream")
		return &http.Response{StatusCode: 503, Header: header, Request: r, Body: io.NopCloser(strings.NewReader(sse))}, nil
	}), journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err == nil || attempts != 1 {
		t.Fatalf("an SSE body on a 503 was retried: err=%v attempts=%d", err, attempts)
	}

	good, goodJournal, _, _, goodCfg := retryConversation(t, []fixtureResponse{{failStatus: 503, failBody: unavailableBody}, {responseID: "r", text: "ok"}})
	if _, err := good.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	pins := RecoveryPins{Config: goodCfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}
	for name, mutate := range map[string]func(*exchangeRetryEvidence){
		"SSE body":         func(e *exchangeRetryEvidence) { e.Response = []byte(sse) },
		"SSE content type": func(e *exchangeRetryEvidence) { e.ContentType = "text/event-stream" },
		"escaped choices":  func(e *exchangeRetryEvidence) { e.Response = []byte(`{"choices":[]}`) },
	} {
		if _, err := ReplayTranscript(tamperedRetryJournal(t, goodJournal, mutate), pins, nil); !errors.Is(err, ErrTranscriptReplay) {
			t.Errorf("%s: forged retry record replayed: %v", name, err)
		}
	}
}
