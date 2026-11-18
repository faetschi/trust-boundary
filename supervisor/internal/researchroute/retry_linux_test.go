//go:build linux

package researchroute

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/usdledger"
)

// openRouterFailure answers a pre-stream OpenRouter error: a JSON error object and no completion byte.
func openRouterFailure(status int, retryAfter, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

var (
	openRouter503 = openRouterFailure(http.StatusServiceUnavailable, "", `{"error":{"code":503,"message":"Provider returned error"}}`)
	openRouter429 = openRouterFailure(http.StatusTooManyRequests, "0", `{"error":{"code":429,"message":"Rate limit exceeded"}}`)
)

// failingThen answers the first failures requests with fail and every later one with a completion that costs
// 2 micro-USD.
func failingThen(failures int32, fail func(http.ResponseWriter)) func(*providerServer) func(http.ResponseWriter, *http.Request) {
	return func(server *providerServer) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, _ *http.Request) {
			if server.requests.Load() <= failures {
				fail(w)
				return
			}
			sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002})
		}
	}
}

func journalKindCount(t *testing.T, journal *audit.Journal, kind string) (count int, last string) {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind == kind {
			count++
		}
		last = record.Event.Kind
	}
	return count, last
}

// requireUnitStates checks how many of the turn's USD units are uncertain and settled, and that none is open.
func requireUnitStates(t *testing.T, b *Bundle, uncertain, settled int) {
	t.Helper()
	list, err := b.WorkflowUnits("workflow-1", []string{"conversation-1"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, unit := range list {
		counts[unit.State]++
	}
	if len(list) != uncertain+settled || counts[usdledger.UnitStateUncertain] != uncertain || counts[usdledger.UnitStateSettled] != settled {
		t.Fatalf("turn units %+v, want %d uncertain and %d settled", list, uncertain, settled)
	}
	snap := snapshot(t, b)
	if snap.OpenUnits != 0 || snap.UncertainMicroUSD != int64(uncertain)*requestReserve {
		t.Fatalf("ledger after the turn: %+v, want no open unit and %d uncertain at the full reservation", snap, uncertain)
	}
}

// A pre-stream OpenRouter 503, or a 429 with a short Retry-After, is retried on the research route as on the
// engineering route: the failed attempt is journaled as a retryable failure (the receipt's provider_retries), its
// USD unit stays booked uncertain at its full reservation, the retry is its own unit and host call, and the turn
// is released with the retry's answer, settled to its cost.
func TestResearchRetryableFailureIsRetriedOnItsOwnUSDUnit(t *testing.T) {
	for name, fail := range map[string]func(http.ResponseWriter){"503": openRouter503, "429 with Retry-After 0": openRouter429} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
			f.genesis()
			var bundle *Bundle
			server := newProviderServer(t, &bundle)
			server.handler = failingThen(1, fail)(server)
			bundle = f.open(server.URL + "/api/v1/chat/completions")
			var reserved, completed atomic.Int32
			c, journal := deadlineConversation(t, bundle, nil, &reserved, &completed)
			turn, err := c.NextTurn(context.Background())
			if err != nil || turn.AssistantText != "ok" {
				t.Fatalf("turn after one retryable failure = %+v, %v; want the retry's answer", turn, err)
			}
			if server.requests.Load() != 2 || reserved.Load() != 2 || completed.Load() != 2 {
				t.Fatalf("requests=%d reserved=%d settled=%d host calls, want 2 each", server.requests.Load(), reserved.Load(), completed.Load())
			}
			if retries, last := journalKindCount(t, journal, "provider_exchange_retryable_failure"); retries != 1 || last != "provider_exchange" {
				t.Fatalf("journal has %d retryable failures and ends at %s, want 1 and the exchange", retries, last)
			}
			requireUnitStates(t, bundle, 1, 1)
			if snap := snapshot(t, bundle); snap.SettledMicroUSD != 2 {
				t.Fatalf("the retry was not settled to its cost: %+v", snap)
			}
		})
	}
}

// The bound is D54's: at most 2 extra attempts per turn. After the third failure the turn ends UNKNOWN on the
// existing failure path, and every attempt's unit stays booked uncertain.
func TestResearchRetriesStopAtTheSharedBound(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = failingThen(1_000, openRouter503)(server)
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	var reserved, completed atomic.Int32
	c, journal := deadlineConversation(t, bundle, nil, &reserved, &completed)
	if _, err := c.NextTurn(context.Background()); err == nil {
		t.Fatal("a turn was released after three provider failures")
	}
	if server.requests.Load() != 3 || reserved.Load() != 3 || completed.Load() != 2 {
		t.Fatalf("requests=%d reserved=%d settled=%d host calls, want 3, 3 and 2", server.requests.Load(), reserved.Load(), completed.Load())
	}
	if retries, last := journalKindCount(t, journal, "provider_exchange_retryable_failure"); retries != 2 || last != "provider_exchange_unknown" {
		t.Fatalf("journal has %d retryable failures and ends at %s, want 2 and the unknown exchange", retries, last)
	}
	requireUnitStates(t, bundle, 3, 0)
}

// A retry the USD ledger cannot reserve is never sent: the cap fits the first attempt only, so after its failure
// the turn ends without a second request, and the failed attempt stays booked uncertain.
func TestResearchRetryTheLedgerCannotReserveIsNotSent(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(requestReserve+1_000, 1_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = failingThen(1, openRouter503)(server)
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	var reserved, completed atomic.Int32
	c, _ := deadlineConversation(t, bundle, nil, &reserved, &completed)
	if _, err := c.NextTurn(context.Background()); err == nil {
		t.Fatal("a turn was released although its retry could not be reserved")
	}
	if server.requests.Load() != 1 || reserved.Load() != 1 || completed.Load() != 1 {
		t.Fatalf("requests=%d reserved=%d settled=%d host calls, want 1 each", server.requests.Load(), reserved.Load(), completed.Load())
	}
	requireUnitStates(t, bundle, 1, 0)
}
