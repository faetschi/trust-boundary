//go:build linux

package providerbridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/runend"
)

// The bounds the agent's own use drives on the provider path end the turn with runend.ErrCeiling, so the guest
// answers run-budget-exhausted and the host never reruns the run (thesis §7.5, register D50 Outcomes).
func TestAgentDrivenProviderBoundsCarryTheCeilingMarker(t *testing.T) {
	t.Run("reply cut at the per-call completion cap", func(t *testing.T) {
		cfg := recoveryTestConfig()
		hooks := &retryHooks{}
		hooks.apply(&cfg)
		journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "length.audit"))
		raw := sseChunks("resp-1", openrouter.ApprovedModel, "length", map[string]any{"role": "assistant", "content": "I will now read every file and"})
		conversation, err := newWithDoer(cfg, ApprovedModel, &rawDoer{bodies: [][]byte{raw}}, journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := conversation.AdmitPrompt("task-length", "do the task"); err != nil {
			t.Fatal(err)
		}
		_, err = conversation.NextTurn(context.Background())
		var journaled journaledFailure
		var outage ProviderOutage
		if !errors.Is(err, runend.ErrCeiling) || !errors.As(err, &journaled) || errors.As(err, &outage) || len(hooks.reservations) != 1 {
			t.Fatalf("a reply cut at the cap is a journaled ceiling, no outage: %v", err)
		}
	})
	t.Run("history over the request bound", func(t *testing.T) {
		conversation, _, _, _, _ := retryConversation(t, nil)
		big := strings.Repeat("x", openrouter.MaxRequestBytes)
		if _, err := conversation.boundedHistoryLocked(openrouter.Message{Role: openrouter.User, Content: &big}); !errors.Is(err, runend.ErrCeiling) {
			t.Fatalf("a history over the request bound: %v", err)
		}
		text := "x"
		many := make([]openrouter.Message, MaxTranscriptTurns*3+3)
		for i := range many {
			many[i] = openrouter.Message{Role: openrouter.User, Content: &text}
		}
		if _, err := conversation.boundedHistoryLocked(many...); !errors.Is(err, runend.ErrCeiling) {
			t.Fatalf("a history over the message limit: %v", err)
		}
	})
	t.Run("history over the durable exchange budget", func(t *testing.T) {
		conversation, _, doer, _, _ := retryConversation(t, []fixtureResponse{{failStatus: 503}})
		big := strings.Repeat("x", broker.MaxDurableExchangeBytes)
		conversation.history = append(conversation.history, openrouter.Message{Role: openrouter.User, Content: &big})
		if _, err := conversation.NextTurn(context.Background()); doer.call != 0 || !errors.Is(err, runend.ErrCeiling) {
			t.Fatalf("a request that leaves no durable response budget: calls %d, %v", doer.call, err)
		}
	})
	t.Run("turn projection over its evidence budget", func(t *testing.T) {
		if err := validateTurnProjectionBudget(Turn{AssistantText: strings.Repeat("x", MaxTurnProjectionBytes)}); !errors.Is(err, runend.ErrCeiling) {
			t.Fatalf("an oversized projection: %v", err)
		}
	})
	t.Run("turn budget", func(t *testing.T) {
		conversation, _, _, _, _ := retryConversation(t, nil)
		conversation.turns = MaxTranscriptTurns
		if _, err := conversation.NextTurn(context.Background()); !errors.Is(err, runend.ErrCeiling) {
			t.Fatalf("the turn budget: %v", err)
		}
	})
}
