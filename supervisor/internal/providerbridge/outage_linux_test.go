//go:build linux

package providerbridge

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/broker"
)

const subscriptionBody = `{"error":{"type":"forbidden","message":"An active OpenCode Go subscription is required"}}`

// Register D30 as amended 2026-11-18 (owner decision A2): a turn whose last attempt is a provider outage fails
// with ProviderOutage, bound to the hash of its provider_exchange_unknown record. Any other failure does not.
func TestProviderOutageTypesTheTurnFailureWithItsJournalRecord(t *testing.T) {
	for name, tc := range map[string]struct {
		responses []fixtureResponse
		outage    bool
	}{
		"503 after the retries":       {[]fixtureResponse{{failStatus: 503}, {failStatus: 503}, {failStatus: 503}}, true},
		"403 subscription (F24)":      {[]fixtureResponse{{failStatus: 403, failBody: subscriptionBody}}, false},
		"500":                         {[]fixtureResponse{{failStatus: 500, failBody: unavailableBody}}, true},
		"429 without Retry-After":     {[]fixtureResponse{{failStatus: 429, failBody: `{"error":"rate limited"}`}}, true},
		"transport failure":           {nil, true},
		"400":                         {[]fixtureResponse{{failStatus: 400, failBody: `{"error":"bad"}`}}, false},
		"401":                         {[]fixtureResponse{{failStatus: 401}}, false},
		"503 carrying stream bytes":   {[]fixtureResponse{{failStatus: 503, failBody: "data: {\"id\":\"x\"}\n\n"}}, false},
		"200 with no text or tool":    {[]fixtureResponse{{responseID: "r"}}, false},
		"503 with an HTML error page": {[]fixtureResponse{{failStatus: 503, failBody: "<html>Bad Gateway</html>"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			conversation, journal, _, _, _ := retryConversation(t, tc.responses)
			_, err := conversation.NextTurn(context.Background())
			if err == nil {
				t.Fatal("the failed turn succeeded")
			}
			var outage ProviderOutage
			if got := errors.As(err, &outage); got != tc.outage {
				t.Fatalf("outage = %v, want %v (err %v)", got, tc.outage, err)
			}
			if !tc.outage {
				return
			}
			trace, traceErr := journal.Trace()
			if traceErr != nil {
				t.Fatal(traceErr)
			}
			last := trace.Records[len(trace.Records)-1]
			if last.Event.Kind != "provider_exchange_unknown" || outage.RecordHash != last.Hash {
				t.Fatalf("outage record %q, journal ends with %s %q", outage.RecordHash, last.Event.Kind, last.Hash)
			}
			var journaled journaledFailure
			if !errors.As(err, &journaled) {
				t.Fatal("an outage is a journaled disposition")
			}
		})
	}
}

func TestRefusalsBeforeDispatchAndCanceledTurnsAreNoOutage(t *testing.T) {
	conversation, _, doer, _, _ := retryConversation(t, []fixtureResponse{{failStatus: 503}})
	conversation.cfg.ReserveHostCall = func(context.Context, broker.RequestRecord, uint64) (HostCallBinding, error) {
		return HostCallBinding{}, errors.New("host-call budget exhausted")
	}
	_, err := conversation.NextTurn(context.Background())
	var outage ProviderOutage
	if err == nil || errors.As(err, &outage) || doer.call != 0 {
		t.Fatalf("a refused reservation: err %v, attempts %d; want no outage and no request sent", err, doer.call)
	}

	conversation, _, _, _, _ = retryConversation(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conversation.NextTurn(ctx); err == nil || errors.As(err, &outage) {
		t.Fatalf("a canceled turn: err %v, want no outage", err)
	}
}

type timeoutDoer struct{ calls int }

func (d *timeoutDoer) Do(*http.Request) (*http.Response, error) {
	d.calls++
	return nil, &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
}

// The transport's own response timeout, while the turn's context is still live, is an outage (no HTTP response);
// the end of the turn's context during the exchange is not.
func TestTransportTimeoutIsAnOutageAndTurnCancellationIsNot(t *testing.T) {
	cfg := recoveryTestConfig()
	(&retryHooks{}).apply(&cfg)
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "timeout.audit"))
	doer := &timeoutDoer{}
	conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-timeout", "prompt"); err != nil {
		t.Fatal(err)
	}
	var outage ProviderOutage
	if _, err := conversation.NextTurn(context.Background()); !errors.As(err, &outage) || doer.calls != 1 {
		t.Fatalf("transport timeout: err %v, calls %d; want an outage after one attempt", err, doer.calls)
	}

	block, started := make(chan struct{}), make(chan struct{})
	conversation, _, fixture, _, _ := retryConversation(t, nil)
	fixture.block, fixture.started = block, started
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := conversation.NextTurn(ctx); done <- err }()
	<-started
	cancel()
	if err := <-done; err == nil || errors.As(err, &outage) {
		t.Fatalf("a turn canceled during its exchange: err %v, want no outage", err)
	}
	close(block)
}
