//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// A Bash command that the gate ALLOWs but delta admission refuses after the effect has already run
// on the real D09 command cell. The command produced a one-use settlement, but because RunBash
// returns ErrDeltaDenied the executor never reaches BindProductionBashEvidence, so the command
// journal has no production_d09_command_settled event and the runner keeps the lease and receipt
// pending. This is the reported "denied Bash command lacks a retained settlement" defect.
func TestProductionBashDeniedAfterEffectRetainsItsSettlement(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const (
		workflowID     = "denied-settlement-workflow"
		conversationID = "denied-settlement-conversation"
		providerID     = "synthetic-offline-profile-v1"
		runID          = "run-denied-settlement"
	)
	storePolicyDigest := "sha256:" + strings.Repeat("a", 64)
	metadataPolicyDigest := "sha256:" + strings.Repeat("b", 64)
	xattrDigest := "sha256:" + strings.Repeat("c", 64)
	gatePolicy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "bash", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	openJournal := func(name string) *audit.Journal {
		journal, err := audit.Open(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = journal.Close() })
		return journal
	}
	storeJournal, providerJournal, commandJournal := openJournal("store.jsonl"), openJournal("provider.jsonl"), openJournal("command.jsonl")

	profile, template, _ := productionProofFixture()
	template.CommandExecutable = "/bin/bash"
	template.CommandExecutableDigest = "sha256:" + strings.Repeat("6", 64)
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profile.ContainmentProfileDigest = templateDigest
	runner := newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		cell := &sealedD09Cell{leaseID: leaseID, template: template, templateDigest: templateDigest, readOnly: false}
		return newProductionD09BashRunnerWithInvoker(leaseID, cell, commandJournal, runID, "denied-settlement-session",
			workflowID, conversationID, profile, template), nil
	})
	// The empty output scope covers no change, so the cell's write is refused by delta admission
	// after the command has run and settled.
	authority, err := NewSessionOperationAuthority(storeJournal, providerJournal, workflowID, conversationID, providerID,
		gatePolicy, storePolicyDigest, metadataPolicyDigest, runner, []string{})
	if err != nil {
		t.Fatal(err)
	}
	authority.requireEvidencedBashContainment = true

	storeRootPath, sourcePath, adminPath := filepath.Join(root, "store"), filepath.Join(root, "source"), filepath.Join(root, "admin")
	for _, path := range []string{storeRootPath, sourcePath, adminPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storeRoot, err := os.Open(storeRootPath)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(), workspace.XattrVisibilityAttestation{
		ProfileDigest: xattrDigest, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}, nil)
	_ = storeRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := managed.Store()
	sourceRoot, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	g0, err := store.Seed(sourceRoot, sessionrepo.RootAttestation{Quiescent: true})
	_ = sourceRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindBaseline(store, g0); err != nil {
		t.Fatal(err)
	}
	adminRoot, err := os.Open(adminPath)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sessionlaunch.OpenAdminParent(adminRoot)
	_ = adminRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	git, gitProfile := pinGovernedTestGit(t)
	defer git.Close()
	prepared, err := sessionlaunch.Prepare(context.Background(), managed, g0, admin, git, gitProfile)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if err := authority.BindPreparedSession(prepared); err != nil {
		t.Fatal(err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{Store: store, Tip: g0, CallIssuer: governedCallIssuer,
		PolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest,
		BashOutputPaths: []string{}, CommandRunner: runner})
	if err != nil {
		t.Fatal(err)
	}

	const responseID, callID = "denied-response-bash", "denied-call-bash"
	arguments := json.RawMessage(`{"command":"printf 'denied-stage\\n' >> task.txt"}`)
	argumentDigest, err := protocol.CanonicalArgumentsDigest("bash", arguments)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := protocol.NewStream(governedCallIssuer, governedResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	if captured := stream.Capture(protocol.TrustedCapture{
		ResponseID: correlation.Identifier{Issuer: governedResponseIssuer, Opaque: responseID},
		ToolCallID: correlation.Identifier{Issuer: governedCallIssuer, Opaque: callID},
		ToolName:   "bash", RawArguments: append([]byte(nil), arguments...),
		CanonicalizationProfile: protocol.CanonicalizationProfile, CanonicalArgumentsDigest: argumentDigest,
		Sequence: 1, Generation: g0.ID(),
	}); !captured.Accepted || captured.StreamClosed {
		t.Fatalf("capture: %+v", captured)
	}
	ordinal := 0
	appendEvent := func(kind, id string, data any) {
		t.Helper()
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		ordinal++
		if _, err := providerJournal.Append(audit.Event{Kind: kind, ID: fmt.Sprintf("%s:%s:%d", conversationID, id, ordinal), Data: encoded}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("provider_user_admitted", "user", providerPromptEvidence{WorkflowID: workflowID,
		ConversationID: conversationID, TaskID: "denied-task", Generation: g0.ID(), Prompt: "synthetic denied-settlement fixture"})
	providerURL, err := url.Parse(openrouter.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent("provider_exchange", "exchange", providerExchangeEvidence{WorkflowID: workflowID, Turn: 1,
		Request: providerRequestEvidence{ProfileID: providerID, Method: "POST", URL: openrouter.Endpoint, Host: providerURL.Host,
			ContentLength: int64(len("synthetic-offline-request")), Headers: map[string][]string{"content-type": {"application/json"}},
			Body: []byte("synthetic-offline-request")},
		StatusCode: 200, Response: []byte("synthetic-offline-response"), ResponseIDIssuer: governedResponseIssuer,
		ToolCallIDIssuer: governedCallIssuer, ResponseID: responseID, Model: providerbridge.ApprovedModel,
		ToolCall: &struct {
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}{ID: callID, Name: "bash", Arguments: append(json.RawMessage(nil), arguments...)}})
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: callID, Tool: "bash", Arguments: arguments}
	encodedProposal, err := protocol.MarshalProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Evaluate(proposal, stream.Propose(encodedProposal), gatePolicy)
	if decision.Verdict != gate.Allow || decision.ResponseID == nil {
		t.Fatalf("gate: %+v", decision)
	}
	appendEvent("provider_gate_decision", "decision", decision)
	_, err = durable.Execute(context.Background(), proposal, decision)
	var denied *protocol.AdmissionDeniedError
	if !errors.As(err, &denied) || denied.EffectID == "" || denied.ReasonCode != sessionrepo.DenyReasonUncoveredPath {
		t.Fatalf("denied Bash command was not released as an admission denial: %v", err)
	}

	trace, err := commandJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	settled := 0
	for _, record := range trace.Records {
		if record.Event.Kind == "production_d09_command_settled" {
			settled++
		}
	}
	if settled != 1 || runner.Pending() != 0 {
		t.Fatalf("denied Bash command has no retained settlement: settled events=%d pending leases/receipts=%d", settled, runner.Pending())
	}
}
