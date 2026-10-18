//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

// denyingExecutor fails the first call with a delta-admission denial and answers the others from outputs.
type denyingExecutor struct {
	outputs []json.RawMessage
	call    int
	deny    map[int]*protocol.AdmissionDeniedError
}

func (e *denyingExecutor) Execute(_ context.Context, _ protocol.Proposal, _ gate.Decision) (json.RawMessage, error) {
	index := e.call
	e.call++
	if denied, ok := e.deny[index]; ok {
		return nil, fmt.Errorf("durable write: %w", denied)
	}
	if index >= len(e.outputs) {
		return nil, errors.New("fixture executor output budget exhausted")
	}
	return append(json.RawMessage(nil), e.outputs[index]...), nil
}

const denialTestReason = "scope_protected_path"

func protectedWriteDenial(effect string) *protocol.AdmissionDeniedError {
	return &protocol.AdmissionDeniedError{Tool: "write", ReasonCode: denialTestReason, EffectID: effect,
		Err: errors.New("candidate transition was denied by output-scope admission")}
}

// driveDenialTurn runs one proposal through correlation and the gate and, with execute, the executor; it
// returns the executor error.
func driveDenialTurn(t *testing.T, conversation *Conversation, executor *ResultingExecutor, policy gate.Policy, execute bool) (protocol.Proposal, gate.Decision, error) {
	t.Helper()
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("provider turn=%+v err=%v", turn, err)
	}
	proposal := proposalFor(turn)
	matched, err := conversation.Correlate(context.Background(), proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate=%+v err=%v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, policy)
	if err := conversation.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != gate.Allow || !execute {
		return proposal, decision, nil
	}
	_, err = executor.Execute(context.Background(), proposal, decision)
	return proposal, decision, err
}

func newDenialFixture(t *testing.T, allow bool, responses []fixtureResponse, executor Executor) (Config, gate.Policy, *audit.Journal, *Conversation, *ResultingExecutor, *fixtureDoer) {
	t.Helper()
	cfg := recoveryTestConfig()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "provider.audit"))
	doer := &fixtureDoer{responses: responses}
	conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-denial", "write the protected file"); err != nil {
		t.Fatal(err)
	}
	wrapped, err := NewExecutor(conversation, executor)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, recoveryTestPolicy(t, allow), journal, conversation, wrapped, doer
}

var protectedWriteThenRead = []fixtureResponse{
	{responseID: "response-write", callID: "call-write", tool: "write", arguments: json.RawMessage(`{"path":"task.txt","content":"x"}`)},
	{responseID: "response-read", callID: "call-read", tool: "read", arguments: json.RawMessage(`{"path":"task.txt"}`)},
}

func TestAdmissionDenialIsAJournaledDenyResultAndTheConversationContinues(t *testing.T) {
	executor := &denyingExecutor{deny: map[int]*protocol.AdmissionDeniedError{0: protectedWriteDenial("effect-denied")},
		outputs: []json.RawMessage{nil, readOutputFor("task.txt", "g0", recoveryTestTree("tree-0"), "baseline")}}
	cfg, policy, journal, conversation, wrapped, doer := newDenialFixture(t, true, protectedWriteThenRead, executor)

	proposal, decision, err := driveDenialTurn(t, conversation, wrapped, policy, true)
	var denied *protocol.AdmissionDeniedError
	if !errors.As(err, &denied) || denied.ReasonCode != denialTestReason || denied.EffectID != "effect-denied" {
		t.Fatalf("the wrapper did not hand the denial to the supervisor: %v", err)
	}
	if errors.Is(err, ErrResultWithheld) || errors.Is(err, ErrClosed) {
		t.Fatalf("a denial is not a withheld result or a closed conversation: %v", err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	last := trace.Records[len(trace.Records)-1]
	if last.Event.Kind != "provider_admission_denied" {
		t.Fatalf("last record is %q, want provider_admission_denied", last.Event.Kind)
	}
	var evidence admissionDeniedEvidence
	if err := unmarshalCanonicalEvent(last.Event.Data, &evidence); err != nil || evidence.ReasonCode != denialTestReason ||
		evidence.EffectID != "effect-denied" || evidence.GenerationFrom != evidence.GenerationTo || evidence.TreeDigestFrom != evidence.TreeDigestTo ||
		evidence.TransitionID != "" || evidence.TransitionSeq != 0 {
		t.Fatalf("denial evidence = %+v, %v", evidence, err)
	}
	result, err := protocol.DecodeResult(evidence.Result)
	if err != nil || result.Verdict != "DENY" || result.ReasonCode != denialTestReason || len(result.Output) != 0 ||
		result.PolicyDigest != decision.PolicyDigest || result.ToolCallID != proposal.ToolCallID {
		t.Fatalf("released result = %+v, %v", result, err)
	}

	// The conversation proceeds: the next turn goes out carrying the deny Result, and its read succeeds.
	if _, _, err := driveDenialTurn(t, conversation, wrapped, policy, true); err != nil {
		t.Fatalf("turn after the denial: %v", err)
	}
	if len(doer.requests) != 2 || !strings.Contains(string(doer.requests[1]), denialTestReason) ||
		!strings.Contains(string(doer.requests[1]), `DENY`) {
		t.Fatalf("the next provider request does not carry the deny Result: %d requests", len(doer.requests))
	}

	verifier := &replayVerifierFixture{}
	projection, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}, verifier)
	if err != nil {
		t.Fatalf("replay of allow -> admission deny -> next turn: %v", err)
	}
	if projection.DecisionCount != 2 || projection.ResultCount != 2 || projection.ProviderGenerationID != cfg.InitialGenerationID ||
		projection.Disposition != DispositionAwaitingProviderExchange || len(verifier.denials) != 1 || len(verifier.results) != 1 {
		t.Fatalf("projection=%+v denials=%d results=%d", projection, len(verifier.denials), len(verifier.results))
	}
	binding := verifier.denials[0]
	if binding.ReasonCode != denialTestReason || binding.EffectID != "effect-denied" || binding.Result.Verdict != "DENY" ||
		binding.Decision.GateDecision.Verdict != gate.Allow || binding.ResultRecordSequence == 0 || binding.ResultRecordHash == "" {
		t.Fatalf("denial binding = %+v", binding)
	}
}

func TestAdmissionDenialEffectCannotBeReused(t *testing.T) {
	executor := &denyingExecutor{deny: map[int]*protocol.AdmissionDeniedError{
		0: protectedWriteDenial("effect-denied"), 1: protectedWriteDenial("effect-denied")}}
	_, policy, journal, conversation, wrapped, _ := newDenialFixture(t, true, protectedWriteThenRead, executor)
	if _, _, err := driveDenialTurn(t, conversation, wrapped, policy, true); err == nil {
		t.Fatal("the first denial was not reported")
	}
	_, _, err := driveDenialTurn(t, conversation, wrapped, policy, true)
	var denied *protocol.AdmissionDeniedError
	if errors.As(err, &denied) || !errors.Is(err, ErrResultWithheld) {
		t.Fatalf("a replayed Store effect was accepted as a second denial: %v", err)
	}
	trace, _ := journal.Trace()
	if trace.Records[len(trace.Records)-1].Event.Kind != "provider_tool_result_unknown" {
		t.Fatalf("a replayed denial effect must end UNKNOWN, last record %q", trace.Records[len(trace.Records)-1].Event.Kind)
	}
}

func TestAdmissionDenialWithoutAStableReasonOrEffectIsUnknown(t *testing.T) {
	for name, denial := range map[string]*protocol.AdmissionDeniedError{
		"no reason":     {Tool: "write", EffectID: "effect-1", Err: errors.New("denied")},
		"unstable code": {Tool: "write", ReasonCode: "Not A Code", EffectID: "effect-1", Err: errors.New("denied")},
		"no effect":     {Tool: "write", ReasonCode: denialTestReason, Err: errors.New("denied")},
	} {
		executor := &denyingExecutor{deny: map[int]*protocol.AdmissionDeniedError{0: denial}}
		_, policy, journal, conversation, wrapped, _ := newDenialFixture(t, true, protectedWriteThenRead, executor)
		_, _, err := driveDenialTurn(t, conversation, wrapped, policy, true)
		if !errors.Is(err, ErrResultWithheld) {
			t.Errorf("%s: %v", name, err)
		}
		trace, _ := journal.Trace()
		for _, record := range trace.Records {
			if record.Event.Kind == "provider_admission_denied" {
				t.Errorf("%s: an unrecordable denial was journaled", name)
			}
		}
	}
}

// forgeDenial appends a provider_admission_denied record built by hand after the gate decision of the
// pending call, so a verifier sees what an attacker with journal access could write. tweak edits it.
func forgeDenial(t *testing.T, journal *audit.Journal, cfg Config, proposal protocol.Proposal, decision gate.Decision,
	tweak func(*admissionDeniedEvidence)) {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	result := admissionDeniedResult(proposal, decision, denialTestReason)
	encoded, err := protocol.MarshalResult(result)
	if err != nil {
		t.Fatal(err)
	}
	resultID := stableResultID(cfg.WorkflowID, cfg.ConversationID, decision.ResponseID.Opaque, proposal.ToolCallID)
	evidence := admissionDeniedEvidence{resultEvidence: resultEvidence{
		WorkflowID: cfg.WorkflowID, ConversationID: cfg.ConversationID, ResponseIDIssuer: responseIssuer, ToolCallIDIssuer: callIssuer,
		ResponseID: decision.ResponseID.Opaque, ToolCallID: proposal.ToolCallID, Tool: proposal.Tool,
		ArgumentDigest: decision.CanonicalArgumentsDigest, Sequence: decision.Sequence,
		GenerationFrom: cfg.InitialGenerationID, GenerationTo: cfg.InitialGenerationID,
		TreeDigestFrom: cfg.InitialTreeDigest, TreeDigestTo: cfg.InitialTreeDigest,
		ResultID: resultID, ResultDigest: digestBytes(encoded), Result: encoded, EffectID: "effect-forged"},
		ReasonCode: denialTestReason}
	if tweak != nil {
		tweak(&evidence)
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(audit.Event{Kind: "provider_admission_denied",
		ID: fmt.Sprintf("%s:%s:%d", cfg.ConversationID, resultID, len(trace.Records)+1), Data: data}); err != nil {
		t.Fatal(err)
	}
}

// setResult replaces the released Result of a forged record and keeps its digest consistent.
func setResult(evidence *admissionDeniedEvidence, result protocol.Result) {
	encoded, _ := protocol.MarshalResult(result)
	evidence.Result, evidence.ResultDigest = encoded, digestBytes(encoded)
}

func TestReplayRejectsForgedAdmissionDenials(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow bool
		tweak func(*admissionDeniedEvidence)
		want  bool
	}{
		{name: "well-formed record (control)", allow: true, want: true},
		{name: "reason in the record differs from the Result", allow: true,
			tweak: func(e *admissionDeniedEvidence) { e.ReasonCode = "scope_path_not_covered" }},
		{name: "Result carries another reason", allow: true, tweak: func(e *admissionDeniedEvidence) {
			result, _ := protocol.DecodeResult(e.Result)
			result.ReasonCode = "scope_path_not_covered"
			setResult(e, result)
		}},
		{name: "Result is ALLOW", allow: true, tweak: func(e *admissionDeniedEvidence) {
			result, _ := protocol.DecodeResult(e.Result)
			result.Verdict, result.ReasonCode = "ALLOW", "policy_rule_allow"
			setResult(e, result)
			e.ReasonCode = "policy_rule_allow"
		}},
		{name: "Result carries output", allow: true, tweak: func(e *admissionDeniedEvidence) {
			result, _ := protocol.DecodeResult(e.Result)
			result.Output = json.RawMessage(`{"outcome":"success"}`)
			if encoded, err := json.Marshal(result); err == nil {
				e.Result, e.ResultDigest = encoded, digestBytes(encoded)
			}
		}},
		{name: "Result digest differs", allow: true, tweak: func(e *admissionDeniedEvidence) { e.ResultDigest = digestBytes([]byte("other")) }},
		{name: "promotes the provider generation", allow: true, tweak: func(e *admissionDeniedEvidence) { e.GenerationTo = "g1" }},
		{name: "changes the tree digest", allow: true, tweak: func(e *admissionDeniedEvidence) { e.TreeDigestTo = recoveryTestTree("tree-1") }},
		{name: "claims a transition", allow: true, tweak: func(e *admissionDeniedEvidence) { e.TransitionID, e.TransitionSeq = "transition-1", 1 }},
		{name: "has no Store effect", allow: true, tweak: func(e *admissionDeniedEvidence) { e.EffectID = "" }},
		{name: "reason is not a stable code", allow: true, tweak: func(e *admissionDeniedEvidence) { e.ReasonCode = "Not A Code" }},
		{name: "another tool call", allow: true, tweak: func(e *admissionDeniedEvidence) { e.ToolCallID = "call-other" }},
		{name: "another argument digest", allow: true, tweak: func(e *admissionDeniedEvidence) { e.ArgumentDigest = "x" }},
		{name: "after a gate DENY", allow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, policy, journal, conversation, wrapped, _ := newDenialFixture(t, tc.allow, protectedWriteThenRead[:1], &denyingExecutor{})
			proposal, decision, err := driveDenialTurn(t, conversation, wrapped, policy, false)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.allow {
				// a DENY decision already released its own result; forge a second record for the same call
				trace, _ := journal.Trace()
				if trace.Records[len(trace.Records)-1].Event.Kind != "provider_tool_result" {
					t.Fatalf("control: DENY decision did not release a result: %v", trace.Records[len(trace.Records)-1].Event.Kind)
				}
			}
			forgeDenial(t, journal, cfg, proposal, decision, tc.tweak)
			_, err = ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}, &replayVerifierFixture{})
			if tc.want && err != nil {
				t.Fatalf("well-formed record rejected: %v", err)
			}
			if !tc.want && !errors.Is(err, ErrTranscriptReplay) {
				t.Fatalf("forged variant accepted or failed for another reason: %v", err)
			}
		})
	}
}

func TestReplayRequiresTheStoreToConfirmTheDenial(t *testing.T) {
	cfg, policy, journal, conversation, wrapped, _ := newDenialFixture(t, true, protectedWriteThenRead[:1], &denyingExecutor{})
	proposal, decision, err := driveDenialTurn(t, conversation, wrapped, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	forgeDenial(t, journal, cfg, proposal, decision, nil)
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}
	if _, err := ReplayTranscript(journal, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("denial replayed without a Store verifier: %v", err)
	}
	if _, err := ReplayTranscript(journal, pins, &replayVerifierFixture{denialErr: errors.New("Store holds no such denial")}); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("denial accepted although the Store refuses it: %v", err)
	}
}

func TestReplayRejectsAnAdmissionDenialAfterAReleasedResultAndTwoResults(t *testing.T) {
	cfg, policy, journal, conversation, wrapped, _ := newDenialFixture(t, true, protectedWriteThenRead[:1],
		&denyingExecutor{outputs: []json.RawMessage{mutationOutput("write", "g1", recoveryTestTree("tree-1"), "transition-1", 1, "effect-1", nil)}})
	proposal, decision, err := driveDenialTurn(t, conversation, wrapped, policy, true)
	if err != nil {
		t.Fatal(err)
	}
	// the call already has its promoted result; a later denial for the same call has no pending decision
	forgeDenial(t, journal, cfg, proposal, decision, nil)
	if _, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}, &replayVerifierFixture{}); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("a denial after a promoted result was accepted: %v", err)
	}
}
