//go:build linux

package researchroute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/usdledger"
)

// deadlineConversation is a research-route conversation on the real USD ledger whose workflow host-call
// settlement ends the request context first and then refuses an ended context, as the workflow ledger does
// (its progress commit checks the context). It counts the host calls it reserved and settled.
func deadlineConversation(t *testing.T, b *Bundle, cancel context.CancelFunc, reserved, completed *atomic.Int32) *providerbridge.Conversation {
	t.Helper()
	journalDir := t.TempDir()
	if err := os.Chmod(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(journalDir, "provider.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	credentials, err := b.Credentials()
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	c, err := providerbridge.NewFromCredentials(providerbridge.Config{
		ConversationID: "conversation-1", WorkflowID: "workflow-1", ProfileID: "profile-1",
		InitialGenerationID: "generation-1", InitialTreeDigest: "sha256:" + strings.Repeat("0", 64),
		SystemPrompt: "Research system prompt.", DeveloperPrompt: "Research developer prompt.",
		Route: b.Route(), Spend: b,
		SelectCompletionTokenLimit: func(context.Context) (uint64, error) { return 64, nil },
		ReserveHostCall: func(_ context.Context, request broker.RequestRecord, limit uint64) (providerbridge.HostCallBinding, error) {
			n := reserved.Add(1)
			return providerbridge.HostCallBinding{CallID: fmt.Sprintf("call-%d", n), RequestDigest: broker.DigestRequestRecord(request),
				TokenUpperBound: limit}, nil
		},
		CompleteHostCall: func(ctx context.Context, _ providerbridge.HostCallCompletion) error {
			cancel()
			if err := ctx.Err(); err != nil {
				return err
			}
			completed.Add(1)
			return nil
		},
	}, credentials, journal)
	if err != nil {
		t.Fatalf("NewFromCredentials: %v", err)
	}
	if err := c.AdmitPrompt("task-1", "Say ok."); err != nil {
		t.Fatal(err)
	}
	return c
}

// parallelCallSSE answers with two tool calls in one turn, which the bridge refuses and resamples.
func parallelCallSSE(w http.ResponseWriter) {
	chunk := func(delta map[string]any, finish any) string {
		raw, _ := json.Marshal(map[string]any{"id": "resp-parallel", "model": "deepseek/deepseek-v4.1-flash", "created": 1,
			"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		return "data: " + string(raw) + "\n\n"
	}
	call := func(index int, id, name, arguments string) map[string]any {
		return map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"index": index, "id": id,
			"type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(chunk(call(0, "call_00_read", "read", `{"path":"x"}`), nil)))
	_, _ = w.Write([]byte(chunk(call(1, "call_01_bash", "bash", `{"command":"ls"}`), nil)))
	_, _ = w.Write([]byte(chunk(map[string]any{}, "tool_calls")))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

// requireLedgerAfterDeadline checks the USD ledger after the request deadline: every reservation of the turn
// is final in the expected state, none is open (abandon refuses an open unit), each was booked once, and
// the cap arithmetic still holds.
func requireLedgerAfterDeadline(t *testing.T, b *Bundle, state string, units int) {
	t.Helper()
	snap := snapshot(t, b)
	list, err := b.WorkflowUnits("workflow-1", []string{"conversation-1"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != units {
		t.Fatalf("ledger holds %d units for the turn, want %d: %+v", len(list), units, list)
	}
	for _, unit := range list {
		if unit.State != state {
			t.Fatalf("unit %s is %s, want %s: %+v", unit.ID, unit.State, state, list)
		}
	}
	if snap.OpenMicroUSD != 0 || snap.CommittedMicroUSD != snap.SettledMicroUSD+snap.UncertainMicroUSD ||
		snap.RemainingMicroUSD != snap.CapMicroUSD-snap.CommittedMicroUSD {
		t.Fatalf("ledger accounting after the deadline: %+v", snap)
	}
}

// The request deadline lands after the provider exchange is journaled, while its spend and host call are
// settled (#156). The unit is settled once to the provider's usage, the host call is settled, and the turn
// is still refused, not released.
func TestResearchExchangeAtTheDeadlineSettlesItsSpendAndHostCallOnce(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) {
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reserved, completed atomic.Int32
	c := deadlineConversation(t, bundle, cancel, &reserved, &completed)
	if _, err := c.NextTurn(ctx); err == nil {
		t.Fatal("a turn was released after the request deadline")
	}
	if server.requests.Load() != 1 || reserved.Load() != 1 || completed.Load() != 1 {
		t.Fatalf("requests=%d reserved=%d settled=%d host calls, want 1 each", server.requests.Load(), reserved.Load(), completed.Load())
	}
	requireLedgerAfterDeadline(t, bundle, usdledger.UnitStateSettled, 1)
	if snap := snapshot(t, bundle); snap.SettledMicroUSD != 2 {
		t.Fatalf("the unit was not settled to the provider's cost: %+v", snap)
	}
}

// The request deadline lands after a refused parallel-call attempt is journaled as a retryable failure,
// while its host call is settled (retry.go): the research route's retry window. The refused attempt's unit
// stays uncertain at its full reservation, booked once; its host call is settled; no further attempt is
// reserved or sent; and the turn is not released.
func TestResearchResampleAtTheDeadlineBooksTheRefusedAttemptOnce(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) { parallelCallSSE(w) }
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reserved, completed atomic.Int32
	c := deadlineConversation(t, bundle, cancel, &reserved, &completed)
	if _, err := c.NextTurn(ctx); err == nil {
		t.Fatal("a turn was released after the request deadline")
	}
	if server.requests.Load() != 1 || reserved.Load() != 1 || completed.Load() != 1 {
		t.Fatalf("requests=%d reserved=%d settled=%d host calls, want 1 each", server.requests.Load(), reserved.Load(), completed.Load())
	}
	requireLedgerAfterDeadline(t, bundle, usdledger.UnitStateUncertain, 1)
	if snap := snapshot(t, bundle); snap.UncertainMicroUSD != requestReserve {
		t.Fatalf("the refused attempt is not booked at its full reservation: %+v", snap)
	}
}
