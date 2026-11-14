//go:build linux

package providerbridge

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A request context that ends once a retryable attempt is journaled ends the turn as a journaled failure
// (Serve then reports ErrProviderTurnFailed): the attempt's host call is settled once, and no further attempt
// is reserved or sent. The context ends either while the attempt's host call is settled (which refuses an
// ended context, like the workflow ledger) or during the rate-limit wait.
func TestRequestEndAfterARetryableAttemptEndsTheTurnDisposed(t *testing.T) {
	for name, tc := range map[string]struct {
		response  fixtureResponse
		endInWait bool
	}{
		"while the host call settles": {response: fixtureResponse{failStatus: 503}},
		"during the rate-limit wait":  {response: fixtureResponse{failStatus: 429, retryAfter: "3"}, endInWait: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := retryWait
			retryWait = func(ctx context.Context, delay time.Duration) error {
				if tc.endInWait {
					cancel()
				}
				return original(ctx, delay)
			}
			t.Cleanup(func() { retryWait = original })
			conversation, journal, doer, hooks, _ := retryConversation(t, []fixtureResponse{tc.response, {responseID: "late", text: "late"}})
			if !tc.endInWait {
				complete := conversation.cfg.CompleteHostCall
				conversation.cfg.CompleteHostCall = func(ctx context.Context, completion HostCallCompletion) error {
					cancel()
					if err := ctx.Err(); err != nil {
						return err
					}
					return complete(ctx, completion)
				}
			}
			_, err := conversation.NextTurn(ctx)
			var journaled journaledFailure
			if !errors.As(err, &journaled) || !errors.Is(err, context.Canceled) {
				t.Fatalf("turn = %v, want a journaled failure from the ended context", err)
			}
			kinds := journalKinds(t, journal)
			if doer.call != 1 || len(hooks.reservations) != 1 || len(hooks.completions) != 1 ||
				kinds[len(kinds)-1] != "provider_exchange_retryable_failure" {
				t.Fatalf("attempts=%d reservations=%d settled=%d kinds=%v, want one attempt settled and journaled",
					doer.call, len(hooks.reservations), len(hooks.completions), kinds)
			}
		})
	}
}
