//go:build linux

package providerbridge

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// failingUncertainSpend is a spend accountant whose uncertain mark is not durable.
type failingUncertainSpend struct{ spendRecorder }

func (s *failingUncertainSpend) UncertainSpend(context.Context, string) error {
	return errors.New("fixture spend ledger failure")
}

// serveOneTurn serves one `next` request on the channel and returns what the bridge returned.
func serveOneTurn(t *testing.T, conversation *Conversation) error {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(context.Background(), serverConn, conversation) }()
	if err := writeChannelTestFrame(clientConn, map[string]any{
		"version": bridgeSchemaVersion, "sequence": 1, "kind": "next", "request_id": "request-disposition",
	}); err != nil {
		t.Fatal(err)
	}
	if frame, err := readChannelTestFrame(clientConn); err != nil || frame["kind"] != "error" {
		t.Fatalf("failed turn frame=%v err=%v", frame, err)
	}
	select {
	case err := <-serveDone:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("provider bridge did not stop after the failed turn")
		return nil
	}
}

// ErrProviderTurnFailed promises the failed exchange is disposed of in the provider journal. Only a
// failure that left that disposition is typed; a failure whose journal write or host-call
// completion also failed stays untyped, so cleanup stays quarantined and abandon refuses.
func TestOnlyAJournaledProviderTurnFailureIsTyped(t *testing.T) {
	body, err := os.ReadFile("../broker/openrouter/testdata/opencode-go-g1-turn1.sse")
	if err != nil {
		t.Fatal(err)
	}
	cut := string(bytes.TrimSuffix(body, []byte("data: [DONE]\n\n")))
	unknown := fixtureResponse{failStatus: http.StatusOK, failBody: cut}
	good := fixtureResponse{responseID: "response-1", text: "ok"}

	conversationWith := func(t *testing.T, journal eventJournal, response fixtureResponse, hooks func(*Config)) *Conversation {
		t.Helper()
		cfg := recoveryTestConfig()
		if hooks != nil {
			hooks(&cfg)
		}
		c, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{response}}, journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AdmitPrompt("task-disposition", "trusted host-admitted prompt"); err != nil {
			t.Fatal(err)
		}
		return c
	}
	completeFails := func(cfg *Config) {
		(&retryHooks{}).apply(cfg)
		cfg.CompleteHostCall = func(context.Context, HostCallCompletion) error { return errors.New("fixture host-call store failure") }
	}
	hostCalls := func(cfg *Config) { (&retryHooks{}).apply(cfg) }

	for name, tc := range map[string]struct {
		build func(t *testing.T) *Conversation
		typed bool
	}{
		"unknown exchange journaled": {func(t *testing.T) *Conversation {
			return conversationWith(t, &memoryJournal{}, unknown, nil)
		}, true},
		"unknown exchange journaled with a host call": {func(t *testing.T) *Conversation {
			return conversationWith(t, &memoryJournal{}, unknown, hostCalls)
		}, true},
		"unknown record not journaled": {func(t *testing.T) *Conversation {
			return conversationWith(t, &memoryJournal{failKind: "provider_exchange_unknown"}, unknown, nil)
		}, false},
		"exchange record not journaled": {func(t *testing.T) *Conversation {
			return conversationWith(t, &memoryJournal{failKind: "provider_exchange"}, good, nil)
		}, false},
		"host-call completion failed": {func(t *testing.T) *Conversation {
			return conversationWith(t, &memoryJournal{}, good, completeFails)
		}, false},
		"spend uncertain mark failed after a journaled unknown": {func(t *testing.T) *Conversation {
			doer := &spendDoer{fail: errors.New("connection reset")}
			c := newSpendConversation(t, &failingUncertainSpend{}, doer)
			admitSpendPrompt(t, c)
			return c
		}, false},
		"spend uncertain mark succeeded after a journaled unknown": {func(t *testing.T) *Conversation {
			doer := &spendDoer{fail: errors.New("connection reset")}
			c := newSpendConversation(t, &spendRecorder{doer: nil}, doer)
			admitSpendPrompt(t, c)
			return c
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			serveErr := serveOneTurn(t, tc.build(t))
			if serveErr == nil {
				t.Fatal("the failed turn did not close the bridge with an error")
			}
			if got := errors.Is(serveErr, ErrProviderTurnFailed); got != tc.typed {
				t.Fatalf("typed as a provider turn failure = %t, want %t (err=%v)", got, tc.typed, serveErr)
			}
		})
	}
}
