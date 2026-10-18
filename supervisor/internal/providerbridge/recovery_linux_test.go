//go:build linux

package providerbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
)

type replayVerifierFixture struct {
	decisions []ProviderDecisionBinding
	results   []ProviderResultBinding
	denials   []ProviderAdmissionDenialBinding
	err       error
	denialErr error
}

type retirementVerifierFixture struct {
	bindings []ProviderCallRetirementBinding
	err      error
}

type idleRetirementVerifierFixture struct {
	workflowID, conversationID string
	providerSequence           uint64
	previousEpoch, newEpoch    uint64
	calls                      uint64
	err                        error
}

func (v *retirementVerifierFixture) VerifyUnleasedProviderCallRetirement(binding ProviderCallRetirementBinding) error {
	v.bindings = append(v.bindings, binding)
	return v.err
}

func (v *idleRetirementVerifierFixture) VerifyIdleProviderLineageRetirement(workflowID, conversationID string,
	providerSequence, previousOwnerEpoch, retirementOwnerEpoch uint64) error {
	v.workflowID, v.conversationID = workflowID, conversationID
	v.providerSequence, v.previousEpoch, v.newEpoch = providerSequence, previousOwnerEpoch, retirementOwnerEpoch
	v.calls++
	return v.err
}

func (v *replayVerifierFixture) VerifyDecision(binding ProviderDecisionBinding) error {
	v.decisions = append(v.decisions, cloneDecisionBinding(binding))
	return v.err
}

func (v *replayVerifierFixture) VerifyResult(binding ProviderResultBinding) error {
	binding.Decision = cloneDecisionBinding(binding.Decision)
	binding.Result = cloneProtocolResult(binding.Result)
	v.results = append(v.results, binding)
	return v.err
}

func (v *replayVerifierFixture) VerifyAdmissionDenial(binding ProviderAdmissionDenialBinding) error {
	binding.Decision = cloneDecisionBinding(binding.Decision)
	binding.Result = cloneProtocolResult(binding.Result)
	v.denials = append(v.denials, binding)
	if v.denialErr != nil {
		return v.denialErr
	}
	return v.err
}

func TestReplayTranscriptRebuildsAllToolsWithoutReturningHistory(t *testing.T) {
	cfg := recoveryTestConfig()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "provider.audit"))
	doer := &fixtureDoer{responses: []fixtureResponse{
		{responseID: "response-read", callID: "call-read", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)},
		{responseID: "response-write", callID: "call-write", tool: "write", arguments: json.RawMessage(`{"path":"README.md","content":"secret-write-content"}`)},
		{responseID: "response-edit", callID: "call-edit", tool: "edit", arguments: json.RawMessage(`{"path":"README.md","edits":[{"oldText":"secret-old","newText":"secret-new"}]}`)},
		{responseID: "response-bash", callID: "call-bash", tool: "bash", arguments: json.RawMessage(`{"command":"printf '>> & < secret-command'"}`)},
	}}
	conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-replay", "secret host prompt for replay"); err != nil {
		t.Fatal(err)
	}
	policy := recoveryTestPolicy(t, true)
	outputs := []json.RawMessage{
		readOutputFor("README.md", "g0", recoveryTestTree("tree-0"), "secret-read-content"),
		mutationOutput("write", "g1", recoveryTestTree("tree-1"), "transition-1", 1, "effect-1", nil),
		mutationOutput("edit", "g2", recoveryTestTree("tree-2"), "transition-2", 2, "effect-2", nil),
		mutationOutput("bash", "g3", recoveryTestTree("tree-3"), "transition-3", 3, "effect-3", &commandSummary{
			ExitCode: 0, ExitObserved: true, CommandContainmentStatus: "not-established", CommandRunnerProfile: "fixture-runner",
		}),
	}
	executor, err := NewExecutor(conversation, &fixtureExecutor{outputs: outputs})
	if err != nil {
		t.Fatal(err)
	}
	for turnIndex := range outputs {
		turn, err := conversation.NextTurn(context.Background())
		if err != nil || turn.ToolCall == nil {
			t.Fatalf("provider turn %d=%+v err=%v", turnIndex, turn, err)
		}
		proposal := proposalFor(turn)
		matched, err := conversation.Correlate(context.Background(), proposal)
		if err != nil || !matched.Accepted {
			t.Fatalf("correlate turn %d=%+v err=%v", turnIndex, matched, err)
		}
		decision := gate.Evaluate(proposal, matched, policy)
		if err := conversation.RecordDecision(context.Background(), proposal, decision); err != nil {
			t.Fatalf("record decision %d: %v", turnIndex, err)
		}
		if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
			t.Fatalf("execute turn %d: %v", turnIndex, err)
		}
	}
	verifier := &replayVerifierFixture{}
	projection, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if projection.ProviderSequence != 4 || projection.ExchangeCount != 4 || projection.CapturedCallCount != 4 ||
		projection.DecisionCount != 4 || projection.ResultCount != 4 || projection.EventCount != 13 ||
		projection.ProviderGenerationID != "g3" || projection.ProviderTreeDigest != recoveryTestTree("tree-3") ||
		projection.Disposition != DispositionAwaitingProviderExchange || projection.HistoryDigest == "" ||
		projection.SchemaVersion != transcriptProjectionSchema || projection.ProviderJournalRecordCount != projection.EventCount ||
		projection.VerificationScope != ProjectionScopeStoreCrosslink ||
		!VerifyTranscriptProjectionCommitment(projection) ||
		len(verifier.decisions) != 4 || len(verifier.results) != 4 {
		t.Fatalf("projection/crosslinks=%+v decisions=%d results=%d", projection, len(verifier.decisions), len(verifier.results))
	}
	for index, binding := range verifier.decisions {
		if binding.DecisionRecordSequence == 0 || binding.DecisionRecordHash == "" || binding.ExchangeRecordSequence == 0 ||
			binding.ExchangeRecordHash == "" || binding.ProviderSequence != uint64(index+1) {
			t.Fatalf("decision binding %d lacks exact protected record refs: %+v", index, binding)
		}
	}
	for index, binding := range verifier.results {
		if binding.ResultRecordSequence == 0 || binding.ResultRecordHash == "" || binding.ResultDigest == "" ||
			binding.Decision.DecisionRecordSequence != verifier.decisions[index].DecisionRecordSequence {
			t.Fatalf("result binding %d lacks exact decision/result refs: %+v", index, binding)
		}
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	tamperedProjection := projection
	tamperedProjection.ProviderGenerationID = "g9"
	if VerifyTranscriptProjectionCommitment(tamperedProjection) {
		t.Fatal("projection commitment accepted a changed provider tip")
	}
	for _, secret := range []string{"secret host prompt", "secret-read-content", "secret-write-content", "secret-old", "secret-command", ">> & <"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("projection exposed raw transcript content %q", secret)
		}
	}
}

func TestReplayTranscriptPendingAllowKeepsProviderPredecessorTip(t *testing.T) {
	cfg := recoveryTestConfig()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "provider.audit"))
	conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{
		{responseID: "response-allow-pending", callID: "call-allow-pending", tool: "write", arguments: json.RawMessage(`{"path":"x","content":"new"}`)},
	}}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-pending", "create x"); err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.NextTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	matched, err := conversation.Correlate(context.Background(), proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate=%+v err=%v", matched, err)
	}
	policy := recoveryTestPolicy(t, true)
	decision := gate.Evaluate(proposal, matched, policy)
	if err := conversation.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("ALLOW replay without Store verifier = %v", err)
	}
	verifier := &replayVerifierFixture{}
	projection, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Disposition != DispositionPendingProviderResult || projection.ProviderGenerationID != "g0" ||
		projection.ProviderTreeDigest != recoveryTestTree("tree-0") || projection.ResultCount != 0 || len(verifier.decisions) != 1 || len(verifier.results) != 0 {
		t.Fatalf("pending ALLOW was promoted past its provider result: projection=%+v verifier=%+v", projection, verifier)
	}
	if projection.VerificationScope != ProjectionScopeStoreCrosslink {
		t.Fatalf("pending projection omitted Store verifier scope: %+v", projection)
	}
}

func TestAppendUnleasedProviderCallRetirementRequiresNewEpochAndPinsExactCall(t *testing.T) {
	cfg := recoveryTestConfig()
	path := filepath.Join(t.TempDir(), "provider-retirement.audit")
	journal := openRecoveryTestJournal(t, path)
	conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{
		{responseID: "response-retire", callID: "call-retire", tool: "write", arguments: json.RawMessage(`{"path":"secret.txt","content":"never execute"}`)},
	}}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-retire", "write the secret"); err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("captured call=%+v err=%v", turn, err)
	}
	policy := recoveryTestPolicy(t, true)
	retirementVerifier := &retirementVerifierFixture{}
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy,
		PreviousOwnerEpoch: 6, RetirementOwnerEpoch: 9, RetirementVerifier: retirementVerifier}
	projection, err := AppendUnleasedProviderCallRetirement(journal, pins, nil)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Disposition != DispositionRetiredUnexecuted || projection.PendingCall != nil || projection.RetiredCall == nil ||
		projection.RetiredCall.OwnerEpoch != 9 || projection.RetiredCall.RetirementKind != retirementKind ||
		projection.RetiredCall.WorkflowID != cfg.WorkflowID || projection.RetiredCall.ConversationID != cfg.ConversationID ||
		projection.RetiredCall.ProfileID != cfg.ProfileID || projection.RetiredCall.ToolCallID != "call-retire" ||
		projection.RetiredCall.ArgumentsDigest != digestBytes([]byte(`{"path":"secret.txt","content":"never execute"}`)) ||
		len(retirementVerifier.bindings) != 2 {
		t.Fatalf("retirement projection=%+v verifier=%+v", projection, retirementVerifier)
	}
	if projection.ProviderGenerationID != cfg.InitialGenerationID || projection.ProviderTreeDigest != cfg.InitialTreeDigest ||
		projection.DecisionCount != 0 || projection.ResultCount != 0 || projection.EventCount != 3 {
		t.Fatalf("retirement advanced provider/store execution state: %+v", projection)
	}
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) != 3 || trace.Records[2].Event.Kind != retirementEventKind ||
		trace.Records[2].Event.ID != cfg.ConversationID+":lineage_retired:3" {
		t.Fatalf("retirement journal=%+v err=%v", trace, err)
	}
	for _, secret := range []string{"never execute", "secret.txt", "write the secret"} {
		encoded, _ := json.Marshal(projection)
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("retirement projection leaked raw content %q", secret)
		}
	}

	// Once the exact retirement event is covered by the externally authenticated
	// checkpoint prefix, future owners replay its recorded epoch pair while still
	// rechecking that no Store/effect/lease fact ever appeared for the call.
	pinned := pins
	pinned.PreviousOwnerEpoch = 9
	pinned.RetirementOwnerEpoch = 11
	pinned.CheckpointRecordCount = projection.ProviderJournalRecordCount
	pinned.CheckpointRecordHead = projection.ProviderJournalHead
	replayed, err := ReplayTranscript(journal, pinned, nil)
	if err != nil || replayed.Disposition != DispositionRetiredUnexecuted || replayed.RetiredCall == nil ||
		replayed.RetiredCall.OwnerEpoch != 9 || len(retirementVerifier.bindings) != 3 {
		t.Fatalf("checkpointed retirement replay=%+v verifier=%+v err=%v", replayed, retirementVerifier, err)
	}
}

func TestAppendUnleasedProviderCallRetirementRejectsMissingDrainEpochOrAuthorityEvidence(t *testing.T) {
	cfg := recoveryTestConfig()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "provider-retirement-refused.audit"))
	conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{
		{responseID: "response-retire-refused", callID: "call-retire-refused", tool: "write", arguments: json.RawMessage(`{"path":"x","content":"x"}`)},
	}}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-retire-refused", "write x"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	policy := recoveryTestPolicy(t, true)
	verifier := &retirementVerifierFixture{err: errors.New("retained Store has an authorization or effect for this call")}
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy,
		PreviousOwnerEpoch: 7, RetirementOwnerEpoch: 8, RetirementVerifier: verifier}
	before, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendUnleasedProviderCallRetirement(journal, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("retirement accepted unverified authority absence: %v", err)
	}
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) != len(before.Records) ||
		len(trace.Records) == 0 || trace.Records[len(trace.Records)-1].Hash != before.Records[len(before.Records)-1].Hash {
		t.Fatalf("failed retirement changed the provider journal: before=%d after=%d err=%v", len(before.Records), len(trace.Records), err)
	}
	pins.RetirementOwnerEpoch = pins.PreviousOwnerEpoch
	if _, err := AppendUnleasedProviderCallRetirement(journal, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("retirement accepted an unchanged owner epoch: %v", err)
	}
}

func TestAppendIdleProviderLineageRetirementRequiresNoPendingCall(t *testing.T) {
	cfg := recoveryTestConfig()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "provider-idle-retirement.audit"))
	conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-idle-retirement", "a task with no provider exchange yet"); err != nil {
		t.Fatal(err)
	}
	policy := recoveryTestPolicy(t, true)
	idleVerifier := &idleRetirementVerifierFixture{}
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy,
		PreviousOwnerEpoch: 4, RetirementOwnerEpoch: 7, IdleRetirementVerifier: idleVerifier}
	projection, err := AppendIdleProviderLineageRetirement(journal, pins, nil)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Disposition != DispositionRetired || projection.RetiredCall != nil || projection.PendingCall != nil ||
		projection.CapturedCallCount != 0 || idleVerifier.workflowID != cfg.WorkflowID || idleVerifier.conversationID != cfg.ConversationID ||
		idleVerifier.providerSequence != 0 || idleVerifier.previousEpoch != 4 || idleVerifier.newEpoch != 7 || idleVerifier.calls != 1 {
		t.Fatalf("idle lineage projection=%+v verifier=%+v", projection, idleVerifier)
	}
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) != 2 || trace.Records[1].Event.Kind != retirementEventKind {
		t.Fatalf("idle retirement journal=%+v err=%v", trace, err)
	}
	pinned := pins
	pinned.PreviousOwnerEpoch = 7
	pinned.RetirementOwnerEpoch = 8
	pinned.CheckpointRecordCount = projection.ProviderJournalRecordCount
	pinned.CheckpointRecordHead = projection.ProviderJournalHead
	replayed, err := ReplayTranscript(journal, pinned, nil)
	if err != nil || replayed.Disposition != DispositionRetired || replayed.RetiredCall != nil || idleVerifier.calls != 2 {
		t.Fatalf("checkpointed idle retirement replay=%+v verifier=%+v err=%v", replayed, idleVerifier, err)
	}
}

func TestReplayTranscriptDenialRequiresNoStoreResultAndLeavesTip(t *testing.T) {
	cfg := recoveryTestConfig()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "provider.audit"))
	conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{
		{responseID: "response-deny", callID: "call-deny", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)},
	}}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-deny", "read file"); err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.NextTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	matched, err := conversation.Correlate(context.Background(), proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate=%+v err=%v", matched, err)
	}
	policy := recoveryTestPolicy(t, false)
	decision := gate.Evaluate(proposal, matched, policy)
	if decision.Verdict != gate.Deny {
		t.Fatalf("fixture decision=%+v", decision)
	}
	if err := conversation.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	verifier := &replayVerifierFixture{}
	projection, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if projection.ProviderGenerationID != "g0" || projection.ProviderTreeDigest != recoveryTestTree("tree-0") || projection.ResultCount != 1 ||
		len(verifier.decisions) != 1 || verifier.decisions[0].GateDecision.Verdict != gate.Deny || len(verifier.results) != 0 {
		t.Fatalf("DENY projection or Store-auth absence check=%+v verifier=%+v", projection, verifier)
	}
	providerOnly, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}, nil)
	if err != nil || providerOnly.ProviderGenerationID != "g0" || providerOnly.VerificationScope != ProjectionScopeProviderOnly {
		t.Fatalf("provider-only DENY projection=%+v err=%v", providerOnly, err)
	}
}

func TestReplayTranscriptRejectsRequestNotMatchingRebuiltHistory(t *testing.T) {
	cfg := recoveryTestConfig()
	original := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "original.audit"))
	conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{
		{responseID: "response-tamper", callID: "call-tamper", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)},
	}}, original)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-tamper", "pinned prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	trace, err := original.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	tampered := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "tampered.audit"))
	for _, record := range trace.Records {
		event := record.Event
		if event.Kind == "provider_exchange" {
			var evidence exchangeEvidence
			if err := json.Unmarshal(event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			evidence.Request.Body = []byte(`{"model":"different"}`)
			event.Data, err = json.Marshal(evidence)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tampered.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReplayTranscript(tampered, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true)}, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("replay accepted a request body that differs from rebuilt host history: %v", err)
	}
}

func TestReplayTranscriptRejectsPinSequenceIdentityAndRecordDrift(t *testing.T) {
	cfg, policy, journal, trace := newSingleAllowReplayFixture(t)
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}
	for _, tc := range []struct {
		name   string
		change func(*RecoveryPins)
	}{
		{name: "system prompt drift", change: func(p *RecoveryPins) { p.Config.SystemPrompt += " changed" }},
		{name: "profile label drift", change: func(p *RecoveryPins) { p.Config.ProfileID += "-changed" }},
		{name: "initial generation drift", change: func(p *RecoveryPins) { p.Config.InitialGenerationID = "g9" }},
		{name: "initial tree drift", change: func(p *RecoveryPins) { p.Config.InitialTreeDigest = recoveryTestTree("other-tree") }},
		{name: "policy drift", change: func(p *RecoveryPins) { p.Policy = recoveryTestPolicy(t, false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := pins
			tc.change(&changed)
			if _, err := ReplayTranscript(journal, changed, &replayVerifierFixture{}); !errors.Is(err, ErrTranscriptReplay) {
				t.Fatalf("replay accepted changed host pin: %v", err)
			}
		})
	}

	for _, tc := range []struct {
		name    string
		mutate  func(int, *audit.Event) bool
		reorder bool
		dup     bool
	}{
		{name: "exchange turn sequence", mutate: func(_ int, event *audit.Event) bool {
			if event.Kind != "provider_exchange" {
				return false
			}
			var evidence exchangeEvidence
			if err := json.Unmarshal(event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			evidence.Turn++
			event.Data, _ = json.Marshal(evidence)
			return true
		}},
		{name: "response identity mismatch", mutate: func(_ int, event *audit.Event) bool {
			if event.Kind != "provider_exchange" {
				return false
			}
			var evidence exchangeEvidence
			if err := json.Unmarshal(event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			evidence.ResponseID = "other-response"
			event.Data, _ = json.Marshal(evidence)
			return true
		}},
		{name: "tool call identity mismatch", mutate: func(_ int, event *audit.Event) bool {
			if event.Kind != "provider_exchange" {
				return false
			}
			var evidence exchangeEvidence
			if err := json.Unmarshal(event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			evidence.ToolCall.ID = "other-call"
			event.Data, _ = json.Marshal(evidence)
			return true
		}},
		{name: "result sequence counter", mutate: func(_ int, event *audit.Event) bool {
			if event.Kind != "provider_tool_result" {
				return false
			}
			var evidence resultEvidence
			if err := json.Unmarshal(event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			evidence.Sequence++
			event.Data, _ = json.Marshal(evidence)
			return true
		}},
		{name: "reordered records", reorder: true},
		{name: "duplicate exchange record", dup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make([]audit.Event, len(trace.Records))
			for i, record := range trace.Records {
				events[i] = record.Event
				if tc.mutate != nil {
					tc.mutate(i, &events[i])
				}
			}
			if tc.reorder {
				events[0], events[1] = events[1], events[0]
			}
			if tc.dup {
				duplicate := events[1]
				duplicate.ID = cfg.ConversationID + ":exchange:3"
				events = append(events, duplicate)
			}
			mutated := rechainRecoveryEvents(t, events)
			if _, err := ReplayTranscript(mutated, pins, &replayVerifierFixture{}); !errors.Is(err, ErrTranscriptReplay) {
				t.Fatalf("replay accepted %s: %v", tc.name, err)
			}
		})
	}
}

func recoveryTestConfig() Config {
	return Config{
		ConversationID: "conversation-replay", WorkflowID: "workflow-replay", ProfileID: "profile-replay",
		InitialGenerationID: "g0", InitialTreeDigest: recoveryTestTree("tree-0"),
		SystemPrompt: "pinned system prompt", DeveloperPrompt: "pinned developer prompt",
	}
}

func newSingleAllowReplayFixture(t *testing.T) (Config, gate.Policy, *audit.Journal, audit.Trace) {
	t.Helper()
	cfg := recoveryTestConfig()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "single-provider.audit"))
	conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{
		{responseID: "response-single", callID: "call-single", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)},
	}}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-single", "host task"); err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.NextTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	matched, err := conversation.Correlate(context.Background(), proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("single fixture correlate=%+v err=%v", matched, err)
	}
	policy := recoveryTestPolicy(t, true)
	decision := gate.Evaluate(proposal, matched, policy)
	if err := conversation.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(conversation, &fixtureExecutor{outputs: []json.RawMessage{
		readOutputFor("README.md", "g0", recoveryTestTree("tree-0"), "single result"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return cfg, policy, journal, trace
}

func rechainRecoveryEvents(t *testing.T, events []audit.Event) *audit.Journal {
	t.Helper()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "replayed-provider.audit"))
	for _, event := range events {
		if _, err := journal.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	return journal
}

func recoveryTestTree(label string) string {
	digest := sha256.Sum256([]byte(label))
	return delta.TreeDigestProfile + hex.EncodeToString(digest[:])
}

func recoveryTestPolicy(t *testing.T, allow bool) gate.Policy {
	t.Helper()
	rules := []gate.Rule{}
	if allow {
		rules = []gate.Rule{
			{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow},
			{Tool: "edit", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow},
		}
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func openRecoveryTestJournal(t *testing.T, path string) *audit.Journal {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	return journal
}

var _ StoreReplayVerifier = (*replayVerifierFixture)(nil)
