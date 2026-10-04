package gate

import (
	"encoding/json"
	"reflect"
	"testing"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
)

const (
	callIssuer     = "fixture/provider-tool-call/v1"
	responseIssuer = "fixture/provider-response/v1"
)

func TestPolicyAllowDenyAndDefaultDeny(t *testing.T) {
	proposal, matched := matchedProposal(t, "read", `{"path":"README.md"}`)
	allowPolicy := mustPolicy(t, Rule{Tool: "read", Effect: EffectAllow})
	allow := Evaluate(proposal, matched, allowPolicy)
	if allow.Verdict != Allow || allow.ReasonCode != "policy_rule_allow" || allow.PolicyDigest != allowPolicy.Digest() {
		t.Fatalf("allow decision = %+v", allow)
	}

	denyPolicy := mustPolicy(t, Rule{Tool: "read", Effect: EffectDeny})
	denied := Evaluate(proposal, matched, denyPolicy)
	if denied.Verdict != Deny || denied.ReasonCode != "policy_rule_deny" || denied.PolicyDigest != denyPolicy.Digest() {
		t.Fatalf("deny decision = %+v", denied)
	}

	defaultPolicy := mustPolicy(t)
	defaulted := Evaluate(proposal, matched, defaultPolicy)
	if defaulted.Verdict != Deny || defaulted.ReasonCode != "policy_default_deny" {
		t.Fatalf("unlisted tool did not default deny: %+v", defaulted)
	}
}

func TestArgumentDigestMismatchFailsClosed(t *testing.T) {
	proposal, matched := matchedProposal(t, "write", `{"path":"a","content":"expected"}`)
	proposal.Arguments = json.RawMessage(`{"path":"a","content":"changed"}`)
	decision := Evaluate(proposal, matched, mustPolicy(t, Rule{Tool: "write", Effect: EffectAllow}))
	if decision.Verdict != Deny || decision.ReasonCode != "argument_digest_mismatch" || decision.PolicyDigest == "" {
		t.Fatalf("digest mismatch was not denied: %+v", decision)
	}
}

func TestUnknownToolAndCorrelationBeforePolicy(t *testing.T) {
	policy := mustPolicy(t, Rule{Tool: "bash", Effect: EffectDeny})
	unknown := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-unknown-tool",
		Tool:          "powershell",
		Arguments:     json.RawMessage(`{"command":"whoami"}`),
	}
	unknownReceipt := successfulReceipt(unknown, "digest-placeholder")
	unknownDecision := Evaluate(unknown, unknownReceipt, policy)
	if unknownDecision.Verdict != Deny || unknownDecision.ReasonCode != "unknown_tool" {
		t.Fatalf("unknown tool was not denied: %+v", unknownDecision)
	}

	proposal, _ := matchedProposal(t, "bash", `{"command":"true"}`)
	unmatched := correlation.Decision{
		Phase: correlation.PhaseProposal, Accepted: false,
		ReasonCode: correlation.ReasonArgumentDigestMismatch, StreamClosed: true,
	}
	decision := Evaluate(proposal, unmatched, policy)
	if decision.Verdict != Deny || decision.ReasonCode != "correlation_required" || decision.PolicyDigest != policy.Digest() {
		t.Fatalf("policy was consulted before correlation: %+v", decision)
	}
}

func TestUnknownAndAmbiguousPoliciesAreRejected(t *testing.T) {
	if _, err := NewPolicy(Profile{Version: "tbound-policy/v99"}); err == nil {
		t.Fatal("unknown policy version was accepted")
	}
	if _, err := NewPolicy(Profile{Version: ProfileVersion, Rules: []Rule{
		{Tool: "read", Effect: EffectAllow},
		{Tool: "read", Effect: EffectDeny},
	}}); err == nil {
		t.Fatal("duplicate ambiguous policy rule was accepted")
	}
	if _, err := NewPolicy(Profile{Version: ProfileVersion, Rules: []Rule{{Tool: "exec", Effect: EffectAllow}}}); err == nil {
		t.Fatal("unknown policy tool was accepted")
	}
	if _, err := NewPolicyFromJSON([]byte(`{"version":"tbound-policy/v1","rules":[],"extra":true}`)); err == nil {
		t.Fatal("unknown policy field was accepted")
	}
	if _, err := NewPolicyFromJSON([]byte(`{"version":"tbound-policy/v1","version":"tbound-policy/v1","rules":[]}`)); err == nil {
		t.Fatal("duplicate policy field was accepted")
	}
	if _, err := NewPolicyFromJSON([]byte(`{"version":"tbound-policy/v1"}`)); err == nil {
		t.Fatal("policy with a missing rules field was accepted")
	}
	if _, err := NewPolicyFromJSON([]byte(`{"version":"tbound-policy/v1","rules":null}`)); err == nil {
		t.Fatal("null policy rules were accepted as an empty list")
	}
}

func TestPolicyDigestAndDecisionAreDeterministic(t *testing.T) {
	first := mustPolicy(t,
		Rule{Tool: "write", Effect: EffectAllow},
		Rule{Tool: "read", Effect: EffectDeny},
	)
	second := mustPolicy(t,
		Rule{Tool: "read", Effect: EffectDeny},
		Rule{Tool: "write", Effect: EffectAllow},
	)
	if first.Digest() != second.Digest() {
		t.Fatalf("semantically identical profiles have different digests: %q != %q", first.Digest(), second.Digest())
	}
	proposal, matched := matchedProposal(t, "write", `{"path":"a","content":"b"}`)
	baseline := Evaluate(proposal, matched, first)
	for i := 0; i < 5; i++ {
		if got := Evaluate(proposal, matched, first); !reflect.DeepEqual(got, baseline) {
			t.Fatalf("decision changed on repetition: got %+v, want %+v", got, baseline)
		}
	}
	if baseline.PolicyDigest != first.Digest() {
		t.Fatalf("decision omitted policy identity: %+v", baseline)
	}
}

func TestMissingPolicyFailsClosedAfterCorrelation(t *testing.T) {
	proposal, matched := matchedProposal(t, "read", `{"path":"a"}`)
	decision := Evaluate(proposal, matched, Policy{})
	if decision.Verdict != Deny || decision.ReasonCode != "invalid_policy" {
		t.Fatalf("missing policy did not fail closed: %+v", decision)
	}
}

func mustPolicy(t *testing.T, rules ...Rule) Policy {
	t.Helper()
	policy, err := NewPolicy(Profile{Version: ProfileVersion, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func matchedProposal(t *testing.T, tool, args string) (protocol.Proposal, correlation.Decision) {
	t.Helper()
	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-1",
		Tool:          tool,
		Arguments:     json.RawMessage(args),
	}
	digest, err := protocol.CanonicalArgumentsDigest(tool, proposal.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := protocol.NewStream(callIssuer, responseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	capture := protocol.TrustedCapture{
		ResponseID: correlation.Identifier{Issuer: responseIssuer, Opaque: "response-1"},
		ToolCallID: correlation.Identifier{Issuer: callIssuer, Opaque: proposal.ToolCallID},
		ToolName:   tool, RawArguments: json.RawMessage(args),
		CanonicalizationProfile:  protocol.CanonicalizationProfile,
		CanonicalArgumentsDigest: digest, Sequence: 1, Generation: "g0",
	}
	if decision := stream.Capture(capture); !decision.Accepted {
		t.Fatalf("capture failed: %+v", decision)
	}
	encoded, err := protocol.MarshalProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	matched := stream.Propose(encoded)
	if !matched.Accepted {
		t.Fatalf("proposal did not correlate: %+v", matched)
	}
	return proposal, matched
}

func successfulReceipt(proposal protocol.Proposal, digest string) correlation.Decision {
	response := correlation.Identifier{Issuer: responseIssuer, Opaque: "response-1"}
	call := correlation.Identifier{Issuer: callIssuer, Opaque: proposal.ToolCallID}
	return correlation.Decision{
		Phase: correlation.PhaseProposal, Accepted: true, ReasonCode: correlation.ReasonMatched,
		Proposal: &correlation.Proposal{
			ResponseID: response, ToolCallID: call, ToolName: proposal.Tool,
			CanonicalArgumentsDigest: digest, Sequence: 1, Generation: "g0",
		},
		BrokerCapture: &correlation.BrokerCall{
			ResponseID: response, ToolCallID: call, ToolName: proposal.Tool,
			CanonicalArgumentsDigest: digest, Sequence: 1, Generation: "g0",
		},
	}
}
