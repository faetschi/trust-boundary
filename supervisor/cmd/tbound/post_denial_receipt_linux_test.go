//go:build linux

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

func TestReceiptProposalsPreservesCommandFailedStatus(t *testing.T) {
	result := protocol.Result{SchemaVersion: protocol.ResultSchemaVersion, ToolCallID: "failed-call", Tool: "bash",
		Sequence: 1, Verdict: "ALLOW", ReasonCode: "policy_rule_allow", PolicyDigest: "sha256:" + strings.Repeat("b", 64),
		ResponseID:               &correlation.Identifier{Issuer: "tbound-provider", Opaque: "response-1"},
		CanonicalArgumentsDigest: protocol.CanonicalizationProfile + ":sha256:" + strings.Repeat("a", 64),
		Status:                   protocol.ResultStatusCommandFailed}
	rawResult, err := protocol.MarshalResult(result)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := json.Marshal(gate.Decision{ToolCallID: result.ToolCallID, Tool: result.Tool, Verdict: gate.Allow, ReasonCode: result.ReasonCode})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal(providerResultEvidence{ToolCallID: result.ToolCallID, Result: rawResult})
	if err != nil {
		t.Fatal(err)
	}
	trace := audit.Trace{Records: []audit.Record{
		{Sequence: 1, Event: audit.Event{Kind: "provider_gate_decision", Data: decision}},
		{Sequence: 2, Event: audit.Event{Kind: "provider_tool_result", Data: evidence}},
	}}
	facts, err := receiptProposals(trace)
	if err != nil || len(facts) != 1 || facts[0].Verdict != "ALLOW" || facts[0].Status != "command_failed" || facts[0].ReasonCode != "policy_rule_allow" {
		t.Fatalf("facts = %+v, %v", facts, err)
	}
	trace.Records[1].Event.Data = []byte(`{"result":"not base64"}`)
	if _, err := receiptProposals(trace); err == nil {
		t.Fatal("malformed result silently discarded")
	}
}
