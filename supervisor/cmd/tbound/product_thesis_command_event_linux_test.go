//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

// The goldens under testdata/thesis-command-event are the bytes the baseline 21881490 produces for the
// production command events. Each embeds the receipt snapshot golden written by the piruntime supplementary
// test, so the receipt bytes come from the same baseline run.
const thesisCommandEventGoldenDir = "testdata/thesis-command-event"

const thesisCommandEventReceiptGolden = "../../internal/piruntime/testdata/thesis-d09-supplementary/receipt-snapshot.json"

// thesisCommandEventArtifacts returns the exact bytes of the success and denied production command events.
// The runner profile is the signed D09 host profile of the thesis path, and the identities follow the
// shapes production admits: a proposal-call provider identity, a lease-scoped operation, and a view that matches
// the receipt. The success is a committed allow; the denied event is a refused candidate with an admission deny.
func thesisCommandEventArtifacts(t *testing.T) map[string][]byte {
	t.Helper()
	receiptSnapshot, err := os.ReadFile(thesisCommandEventReceiptGolden)
	if err != nil {
		t.Fatalf("read the piruntime receipt golden: %v", err)
	}
	var receiptFields struct {
		ReceiptDigest string `json:"receipt_digest"`
	}
	if err := json.Unmarshal(receiptSnapshot, &receiptFields); err != nil {
		t.Fatalf("decode the piruntime receipt golden: %v", err)
	}
	profile := piruntime.DefaultLinuxHostProfileTemplate()
	profileSigningBytes, err := piruntime.LinuxHostProfileSigningBytes(profile)
	if err != nil {
		t.Fatalf("derive the D09 host profile signing bytes: %v", err)
	}
	profile.Digest = piruntime.DigestBytes(profileSigningBytes)
	runner := &productionD09BashRunner{
		runID: "run-bash-1", sessionID: "session-bash-1", workflowID: "workflow-bash-1", conversationID: "conversation-bash-1",
		profile: profile,
	}
	digest := func(seed string) string { return digestBytes([]byte(seed)) }
	treeDigest := func(seed string) string { return delta.TreeDigestProfile + strings.TrimPrefix(digest(seed), "sha256:") }
	providerCall := delta.OperationIdentity{
		Kind: delta.OperationProposalCall, ProposalID: "proposal-bash-1", CallIssuer: governedCallIssuer, CallID: "call-bash-1",
	}
	leaseOperation := delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: "lease-1"}
	transition := delta.Transition{
		ID: "transition-bash-1", Sequence: 1, Tool: "bash", ArgumentDigest: digest("bash-arguments"), ViewID: "view-1",
		ExecutionContextDigest: digest("execution-context"), InputGeneration: "generation-input-1",
		OutputGeneration: "generation-output-1", InputTreeDigest: treeDigest("input-tree"),
		OutputTreeDigest: treeDigest("output-tree"), Operation: leaseOperation,
	}
	exitCode := 0
	shared := sessionrepo.OperationEvidence{
		Tool: "bash", Operation: leaseOperation, ArgumentDigest: transition.ArgumentDigest, ViewID: "view-1",
		ExecutionContextDigest: transition.ExecutionContextDigest, InputGeneration: transition.InputGeneration,
		InputTreeDigest: transition.InputTreeDigest, ExitCode: &exitCode, CommandContainmentStatus: "not-established",
		CommandRunnerProfile: piruntime.D09CommandRunnerProfile,
	}
	success := shared
	success.AuditSequence, success.EffectID, success.DecisionID = 2, "effect-bash-1", "decision-bash-1"
	success.OutputGeneration, success.OutputTreeDigest = transition.OutputGeneration, transition.OutputTreeDigest
	success.ResultDigest, success.Outcome = digest("result-success"), "success"
	success.Admission = &sessionrepo.AdmissionReference{
		Sequence: 1, RecordHash: digest("admission-allow-record"), Outcome: "allow",
		IntentDigest: digest("admission-allow-intent"), AdmissionPolicyDigest: digest("admission-policy"),
	}
	denied := shared
	denied.AuditSequence, denied.EffectID, denied.DecisionID = 3, "effect-bash-2", "decision-bash-2"
	denied.ResultDigest, denied.Outcome = digest("result-denied"), "denied_after_effect"
	denied.Admission = &sessionrepo.AdmissionReference{
		Sequence: 2, RecordHash: digest("admission-deny-record"), Outcome: "deny",
		IntentDigest: digest("admission-deny-intent"), AdmissionPolicyDigest: digest("admission-policy"),
	}
	receipt := productionD09RunEvidence{
		leaseID: "lease-1", viewID: "view-1",
		receiptSnapshot: receiptSnapshot, receiptDigest: receiptFields.ReceiptDigest,
	}
	successJSON, err := marshalProductionD09CommandEvent(runner, providerCall, transition, success, receipt)
	if err != nil {
		t.Fatalf("marshal the success command event: %v", err)
	}
	deniedJSON, err := runner.marshalProductionD09Event(providerCall, transition, denied, receipt, true)
	if err != nil {
		t.Fatalf("marshal the denied command event: %v", err)
	}
	return map[string][]byte{
		"command-settled-success.json": successJSON,
		"command-settled-denied.json":  deniedJSON,
	}
}

func TestProductThesisCommandEventBytesUnchanged(t *testing.T) {
	artifacts := thesisCommandEventArtifacts(t)
	names := make([]string, 0, len(artifacts))
	for name := range artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		got := artifacts[name]
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(thesisCommandEventGoldenDir, name))
			if err != nil {
				t.Fatalf("read golden %s (generate it from baseline 21881490): %v", name, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s drifted from the baseline golden:\n got %q\nwant %q", name, got, want)
			}
		})
	}
}
