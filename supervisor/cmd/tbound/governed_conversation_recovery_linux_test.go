//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
)

func TestNewProviderConversationRequiresFreshProviderJournal(t *testing.T) {
	// These are valid audit frames, not semantic conversation fixtures. The
	// interim constructor deliberately refuses every nonempty journal rather
	// than attempting to restore history or classify a completed conversation.
	for _, tc := range []struct {
		name   string
		events []string
		want   bool
	}{
		{name: "empty"},
		{name: "admitted prompt frame", events: []string{"provider_user_admitted"}, want: true},
		{name: "terminal result frames", events: []string{"provider_user_admitted", "provider_exchange", "provider_tool_result"}, want: true},
		{name: "pending result frames", events: []string{"provider_user_admitted", "provider_exchange", "provider_gate_decision"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateGovernedTestRoot(t)
			storeJournal, err := audit.Open(filepath.Join(root, "store-audit.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer storeJournal.Close()
			providerJournal, err := audit.Open(filepath.Join(root, "provider-audit.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer providerJournal.Close()
			for i, kind := range tc.events {
				if _, err := providerJournal.Append(audit.Event{Kind: kind, ID: fmt.Sprintf("conversation:%d", i+1), Data: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "read", Effect: gate.EffectAllow}}})
			if err != nil {
				t.Fatal(err)
			}
			authority, err := NewSessionOperationAuthority(storeJournal, providerJournal, "recovery-workflow", "recovery-conversation", "recovery-profile",
				policy, governedCrashPolicy, governedCrashMetadata, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = authority.NewProviderConversation(providerbridge.Config{WorkflowID: "recovery-workflow", ConversationID: "recovery-conversation", ProfileID: "recovery-profile"})
			if errors.Is(err, ErrProviderRecoveryRequired) != tc.want {
				t.Fatalf("constructor err=%v, recovery-required=%v want %v", err, errors.Is(err, ErrProviderRecoveryRequired), tc.want)
			}
			if !tc.want && (err == nil || err.Error() != "verified D06 source and durable provenance must be bound before provider/tool activity") {
				t.Fatalf("fresh journal should reach the missing-source gate before credentials: %v", err)
			}
		})
	}
}

func TestFreshProviderConversationFailsClosedOnUnavailableTrace(t *testing.T) {
	if err := requireFreshProviderConversation(nil); !errors.Is(err, ErrProviderRecoveryRequired) {
		t.Fatalf("nil journal accepted: %v", err)
	}
	for _, tc := range []string{"closed", "corrupt"} {
		t.Run(tc, func(t *testing.T) {
			path := filepath.Join(privateGovernedTestRoot(t), "provider-audit.jsonl")
			journal, err := audit.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			if tc == "closed" {
				if err := journal.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte("{truncated\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			err = requireFreshProviderConversation(journal)
			if !errors.Is(err, ErrProviderRecoveryRequired) || err == ErrProviderRecoveryRequired {
				t.Fatalf("trace failure was not retained in recovery refusal: %v", err)
			}
			if tc == "closed" && !errors.Is(err, audit.ErrClosed) {
				t.Fatalf("closed journal cause lost: %v", err)
			}
			if tc == "corrupt" && !errors.Is(err, audit.ErrCorrupt) {
				t.Fatalf("corrupt journal cause lost: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("fresh-conversation refusal altered retained journal")
			}
		})
	}
}
