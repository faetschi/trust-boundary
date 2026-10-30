//go:build linux

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
)

func startSoftRefusalWorkflow(t *testing.T, turns []providerbridge.ScriptTurn) *workflowVerticalFixture {
	t.Helper()
	f := prepareWorkflowVertical(t, workflowVerticalOptions{launchProof: true, maxHostCalls: 12, maxTokens: 12288})
	f.config.ProductSoftRefusals = true
	f.startConversation(t, func(cfg providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		return providerbridge.NewScriptedConversation(cfg, journal, &productDriverScript{turns: turns})
	})
	return f
}

func recordSoftRefusal(t *testing.T, f *workflowVerticalFixture) {
	t.Helper()
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("capture refusal call: %+v %v", turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID, Tool: turn.ToolCall.Name, Arguments: turn.ToolCall.Arguments}
	matched, err := f.conversation.Correlate(f.ctx, proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate refusal: %+v %v", matched, err)
	}
	refusal, err := f.durable.productPreflight(proposal)
	if err != nil || refusal == nil {
		t.Fatalf("preflight refusal: %+v %v", refusal, err)
	}
	if err := f.conversation.RecordSoftRefusal(f.ctx, proposal, refusal.ArgumentDigest, refusal.Code, refusal.Hint, nil); err != nil {
		t.Fatal(err)
	}
}

func softRefusalReplayPins(t *testing.T, f *workflowVerticalFixture) GovernedReplayPins {
	t.Helper()
	bundle, err := f.store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	return GovernedReplayPins{
		Provider: providerbridge.RecoveryPins{Config: f.config, Model: f.config.Route.Resolved().Model, Policy: f.policy, RequireHostCallReservations: true},
		Profile: broker.Profile{ID: f.config.ProfileID, Model: f.config.Route.Resolved().Model, Route: f.config.Route,
			Messages:     providerbridge.InitialPromptMessages(f.config.Route.Resolved(), f.config.SystemPrompt, f.config.DeveloperPrompt),
			ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer, ResponseIDIssuer: governedResponseIssuer, Generation: bundle.Baseline.ID},
		Baseline: bundle.Baseline, StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
	}
}

func rewriteSoftRefusalJournal(t *testing.T, journal *audit.Journal, mutate func(*providerbridge.SoftRefusalEvidence)) *audit.Journal {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	rewritten, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "rewritten.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rewritten.Close() })
	changed := false
	for _, record := range trace.Records {
		if record.Event.Kind == providerbridge.SoftRefusalEvent {
			var evidence providerbridge.SoftRefusalEvidence
			if err := json.Unmarshal(record.Event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			mutate(&evidence)
			evidence.Result = providerbridge.SoftRefusalText(evidence.Code, evidence.Hint)
			record.Event.Data, err = json.Marshal(evidence)
			if err != nil {
				t.Fatal(err)
			}
			changed = true
		}
		if _, err := rewritten.Append(record.Event); err != nil {
			t.Fatal(err)
		}
	}
	if !changed {
		t.Fatal("no refusal to rewrite")
	}
	return rewritten
}

func TestProductTurnSoftRefusalReplayRecomputesWithoutCheckpoint(t *testing.T) {
	f := startSoftRefusalWorkflow(t, []providerbridge.ScriptTurn{{ResponseID: "soft-response", CallID: "soft-call", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)}})
	recordSoftRefusal(t, f)
	pins := softRefusalReplayPins(t, f)
	if _, err := ReplayGovernedTranscript(f.providerJournal, f.storeJournal, f.store, pins); err != nil {
		t.Fatalf("valid refusal replay: %v", err)
	}
	for _, field := range []string{"code", "hint", "digest"} {
		t.Run(field, func(t *testing.T) {
			journal := rewriteSoftRefusalJournal(t, f.providerJournal, func(e *providerbridge.SoftRefusalEvidence) {
				switch field {
				case "code":
					e.Code = "edit_old_text_absent"
				case "hint":
					e.Hint = "Use a different hint."
				case "digest":
					e.TypedArgumentDigest = "sha256:" + strings.Repeat("a", 64)
				}
			})
			if _, err := ReplayGovernedTranscript(journal, f.storeJournal, f.store, pins); err == nil || !strings.Contains(err.Error(), "soft refusal differs from recomputed sealed-generation preflight") {
				t.Fatalf("want recomputed preflight rejection, got %v", err)
			}
			trace, err := journal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			checkpointed := pins
			checkpointed.Provider.CheckpointRecordCount = uint64(len(trace.Records))
			checkpointed.Provider.CheckpointRecordHead = auditHeadHash(trace.Records)
			if _, err := ReplayGovernedTranscript(journal, f.storeJournal, f.store, checkpointed); err == nil || !strings.Contains(err.Error(), "soft refusal differs from recomputed sealed-generation preflight") {
				t.Fatalf("restart replay skipped checkpointed refusal recompute: %v", err)
			}
		})
	}
}

func TestProductTurnSoftRefusalReplayRejectsStoreAuthority(t *testing.T) {
	f := startSoftRefusalWorkflow(t, []providerbridge.ScriptTurn{{ResponseID: "soft-response", CallID: "soft-call", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)}})
	recordSoftRefusal(t, f)
	pins := softRefusalReplayPins(t, f)
	if _, err := ReplayGovernedTranscript(f.providerJournal, f.storeJournal, f.store, pins); err != nil {
		t.Fatalf("valid refusal replay: %v", err)
	}
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	last := trace.Records[len(trace.Records)-1]
	auth := governedOperationAdmissionRecord{SchemaVersion: "tbound-governed-operation-admission/v2", ConversationID: f.config.ConversationID,
		EffectID: "forged-effect", ProviderDecisionSequence: last.Sequence, ProviderDecisionRecordHash: last.Hash,
		GateDecision: gate.Decision{ToolCallID: "soft-call", Verdict: gate.Allow}}
	data, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.storeJournal.Append(audit.Event{Kind: governedOperationAuthorizationKind, ID: "governed-operation:" + auth.EffectID, Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayGovernedTranscript(f.providerJournal, f.storeJournal, f.store, pins); err == nil || !strings.Contains(err.Error(), "proven-unexecuted call has Store authority") {
		t.Fatalf("want refused-call Store authority rejection, got %v", err)
	}
}

func TestRefusalReplayRejectsTheOtherRefusalRecordShape(t *testing.T) {
	thesis := newNoEffectReplayFixture(t)
	product := startSoftRefusalWorkflow(t, []providerbridge.ScriptTurn{{ResponseID: "soft-response", CallID: "soft-call", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)}})
	recordSoftRefusal(t, product)
	productPins := softRefusalReplayPins(t, product)
	for _, check := range []struct {
		name, kind, otherKind               string
		journal, storeJournal, otherJournal *audit.Journal
		pins                                GovernedReplayPins
	}{
		{"no_effect_as_preflight", "provider_tool_result", providerbridge.SoftRefusalEvent, thesis.providerJournal, thesis.storeJournal, product.providerJournal, thesis.pins},
		{"preflight_as_no_effect", providerbridge.SoftRefusalEvent, "provider_tool_result", product.providerJournal, product.storeJournal, thesis.providerJournal, productPins},
	} {
		t.Run(check.name, func(t *testing.T) {
			store := thesis.store
			if check.kind == providerbridge.SoftRefusalEvent {
				store = product.store
			}
			if _, err := ReplayGovernedTranscript(check.journal, check.storeJournal, store, check.pins); err != nil {
				t.Fatalf("valid refusal: %v", err)
			}
			other, err := check.otherJournal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			var data []byte
			for _, record := range other.Records {
				if record.Event.Kind == check.otherKind {
					data = record.Event.Data
					break
				}
			}
			if len(data) == 0 {
				t.Fatal("missing other refusal shape")
			}
			trace, err := check.journal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			journal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "wrong-shape.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = journal.Close() })
			changed := false
			for _, record := range trace.Records {
				event := record.Event
				if event.Kind == check.kind && !changed {
					event.Data = data
					changed = true
				}
				if _, err := journal.Append(event); err != nil {
					t.Fatal(err)
				}
			}
			if !changed {
				t.Fatal("missing original refusal")
			}
			if _, err := ReplayGovernedTranscript(journal, check.storeJournal, store, check.pins); err == nil {
				t.Fatal("accepted the other refusal's record shape")
			}
		})
	}
}
