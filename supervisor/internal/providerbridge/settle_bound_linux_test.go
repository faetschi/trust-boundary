//go:build linux

package providerbridge

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The settlement that runs after a provider record is durable ignores the request's cancellation but not
// forever: a hung ledger commit (here one that waits for its context) fails closed within
// postRecordSettleTimeout. The turn is refused, not released, and its failure is not claimed as disposed,
// so the host call stays reserved for abandon to refuse.
func TestPostRecordSettlementIsBounded(t *testing.T) {
	previous := postRecordSettleTimeout
	postRecordSettleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { postRecordSettleTimeout = previous })
	for name, responses := range map[string][]fixtureResponse{
		"journaled exchange": {{responseID: "response-ok", text: "finished"}},
		"retryable attempt":  {{failStatus: 503}, {responseID: "response-late", text: "late"}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conversation, _, _, hooks, _ := retryConversation(t, responses)
			conversation.cfg.CompleteHostCall = func(ctx context.Context, _ HostCallCompletion) error {
				cancel() // the request deadline lands while the host call settles
				<-ctx.Done()
				return ctx.Err()
			}
			done := make(chan error, 1)
			go func() {
				_, err := conversation.NextTurn(ctx)
				done <- err
			}()
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("a hung host-call settlement kept the turn open past its bound")
			}
			var journaled journaledFailure
			if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &journaled) {
				t.Fatalf("turn = %v, want a refusal from the bounded settlement that claims no disposition", err)
			}
			if len(hooks.reservations) != 1 {
				t.Fatalf("%d host calls were reserved, want 1", len(hooks.reservations))
			}
		})
	}
}
