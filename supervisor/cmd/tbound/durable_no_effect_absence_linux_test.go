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
	"tbound/supervisor/internal/sessionrepo"
)

// A refused read of one call, and a retained Store bundle whose only generation is the input tip.
func absenceProofFixture(t *testing.T, storeRecord string) (protocol.Proposal, gate.Decision, sessionrepo.EvidenceBundle) {
	t.Helper()
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "call-refused", Tool: "read",
		Arguments: json.RawMessage(`{"path":"task.txt","offset":0}`)}
	digest, err := protocol.CanonicalArgumentsDigest("read", proposal.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Decision{Verdict: gate.Allow, ReasonCode: "policy_rule_allow", Tool: "read", ToolCallID: "call-refused",
		ResponseID: &correlation.Identifier{Issuer: governedResponseIssuer, Opaque: "response-refused"}, Sequence: 1,
		CanonicalArgumentsDigest: digest, PolicyDigest: "sha256:" + strings.Repeat("a", 64)}
	line, err := json.Marshal(audit.Record{Version: 1, Sequence: 1, Event: audit.Event{Kind: "test", Data: []byte(storeRecord)}})
	if err != nil {
		t.Fatal(err)
	}
	bundle := sessionrepo.EvidenceBundle{Baseline: sessionrepo.GenerationEvidence{ID: "g0", TreeDigest: "tree-g0"},
		AuditJournal: append(line, '\n')}
	return proposal, decision, bundle
}

// The absence proof rejects any retained Store record that names the refused call or its response, and accepts a
// journal that names neither. The refusal itself reproduces from the retained arguments in every case.
func TestNoEffectAbsenceProofRejectsAnyStoreRecordBoundToTheCall(t *testing.T) {
	for _, tc := range []struct {
		name, record string
		rejected     bool
	}{
		{"unrelated record", `{"call_id":"call-other"}`, false},
		{"record naming the refused call", `{"call_id":"call-refused"}`, true},
		{"record naming the refused response", `{"response_id":"response-refused"}`, true},
		{"nested operation naming the refused call", `{"operation":{"kind":"proposal_call","call_id":"call-refused"}}`, true},
		{"correlation identifier naming the refused response", `{"gate_decision":{"response_id":{"issuer":"x","opaque":"response-refused"}}}`, true},
		// Output bytes are not identity: an earlier read or command may print the next call's ID.
		{"read content that mentions the refused call", `{"content":"{\"example\":\"call-refused\"}","stdout":"call-refused"}`, false},
		{"nested output that mentions the refused call", `{"output":{"example":"call-refused"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proposal, decision, bundle := absenceProofFixture(t, tc.record)
			err := verifyDurableNoEffect(proposal, decision, governedCallIssuer, "g0", "tree-g0", bundle)
			if !tc.rejected {
				if err != nil {
					t.Fatalf("absence proof rejected an unrelated journal: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "retains a record bound to this call") {
				t.Fatalf("absence proof accepted a Store record bound to the call: %v", err)
			}
		})
	}
}
