//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	_ "unsafe" // go:linkname to Runtime A's receipt sealing below

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// sealD09CommandReceipt is Runtime A's own receipt issuance (nonce + seal). Linking it from a
// test keeps the production package free of any exported way to mint a sealed receipt.
//
//go:linkname sealD09CommandReceipt tbound/supervisor/internal/piruntime.newD09CommandReceipt
func sealD09CommandReceipt(leaseID, viewID, cellName, containerID string, spec sessionrepo.CommandSpec,
	result sessionrepo.CommandResult, settlement sessionrepo.CommandSettlement, proof piruntime.D09CommandProof,
	evidence piruntime.D09CommandSettlementEvidence) (piruntime.D09CommandReceipt, error)

// sealedD09Cell stands in for the Podman/crun cell at the productionD09CommandInvoker seam: it
// performs the command's effect on the view and returns the sealed receipt a settled rootless cell
// reports (its own /work mount ID, the view's device/inode). Everything above the seam is production.
type sealedD09Cell struct {
	leaseID        string
	template       piruntime.D09CellTemplate
	templateDigest string
	receipt        piruntime.D09CommandReceipt
}

func (c *sealedD09Cell) Run(_ context.Context, view *sessionrepo.CommandView,
	spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	source, err := view.MountSource()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	defer source.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(source.Fd()), &stat); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	task, err := os.OpenFile(fmt.Sprintf("/proc/self/fd/%d/task.txt", source.Fd()), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	_, writeErr := task.WriteString("bash-stage\n")
	if err := task.Close(); writeErr != nil || err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("cell write: %v %v", writeErr, err)
	}
	const cellName, cellMountID = "cell-success-path", 706
	result := sessionrepo.CommandResult{ExitObserved: true, ExitCode: 0, Stdout: []byte("ok\n")}
	settlement := sessionrepo.CommandSettlement{LeaseID: c.leaseID, ViewID: view.ID(), ExitObserved: true,
		ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true,
		EvidenceClass: piruntime.D09CommandRunnerProfile + ":kill-at-settlement"}
	proof := piruntime.D09CommandProof{Schema: "tbound-d09-command-proof/v1", UID: c.template.CellUID, GID: c.template.CellGID,
		CapEff: "0000000000000000", NoNewPrivs: true, Seccomp: 2, LandlockABI: c.template.LandlockABI,
		LandlockRulesetDigest: c.template.LandlockRulesetDigest, LandlockTSYNC: true, CgroupV2: true,
		LeaderPID: 4242, ContainerLeaderPID: 1, LeaseID: c.leaseID, ViewID: view.ID(),
		CommandStarted: true, CommandReady: true, CommandExitObserved: true, CommandExitCode: result.ExitCode,
		CommandPath: spec.Executable, CommandDigest: c.template.CommandExecutableDigest, CommandWorkdir: ".",
		ContainerName: cellName, ImageRef: registeredD09ImageDigestRef(c.template), ImageDigest: c.template.ExpectedImageDigest,
		TemplateDigest: c.templateDigest, ConsumerMountsVerified: true, RawSourceFDsClosed: true,
		WorkerMountID: cellMountID, RuntimeMountID: cellMountID, WorkerDevice: uint64(stat.Dev), WorkerInode: stat.Ino,
		RuntimeDevice: uint64(stat.Dev), RuntimeInode: stat.Ino, NoExternalRoutes: true}
	evidence := piruntime.D09CommandSettlementEvidence{ContainerExitInspected: true, FinalCommandProofRead: true,
		PodmanWaitCompleted: true, OutputWritersDrained: true, ContainerRemoved: true, LeaderPidfdExited: true,
		CgroupPopulatedZero: true, MountNamespaceReleased: true, CommandOutputWithinBound: true, CommandCgroupRemoved: true}
	c.receipt, err = sealD09CommandReceipt(c.leaseID, view.ID(), cellName, strings.Repeat("e", 64), spec,
		result, settlement, proof, evidence)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	return result, settlement, nil
}

func (c *sealedD09Cell) TakeCommandReceipt(*sessionrepo.CommandView, sessionrepo.CommandSpec,
	sessionrepo.CommandResult, sessionrepo.CommandSettlement) (piruntime.D09CommandReceipt, error) {
	receipt := c.receipt
	c.receipt = piruntime.D09CommandReceipt{}
	return receipt, nil
}

// TestProductionBashSuccessPathSettlesPublishesAndResolvesOrigin drives one governed Bash call
// through the production D09 wiring with a sealed receipt: productionCommandRunner →
// productionD09BashRunner → Store.RunBash → BindProductionBashEvidence (DurableExecutor) → owner
// settlement with requireEvidencedBashContainment → the Bash-evidence half of
// CheckPublicationPrerequisites → ResolveTerminalLeaseOrigin. Only the cell itself is a fixture.
func TestProductionBashSuccessPathSettlesPublishesAndResolvesOrigin(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const (
		workflowID     = "success-path-workflow"
		conversationID = "success-path-conversation"
		providerID     = "synthetic-offline-profile-v1"
		runID          = "run-success-path"
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
		cell := &sealedD09Cell{leaseID: leaseID, template: template, templateDigest: templateDigest}
		return newProductionD09BashRunnerWithInvoker(leaseID, cell, commandJournal, runID, "success-path-session",
			workflowID, conversationID, profile, template), nil
	})
	authority, err := NewSessionOperationAuthority(storeJournal, providerJournal, workflowID, conversationID, providerID,
		gatePolicy, storePolicyDigest, metadataPolicyDigest, runner, []string{"task.txt"})
	if err != nil {
		t.Fatal(err)
	}
	authority.requireEvidencedBashContainment = true // as production_binder sets it

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
		BashOutputPaths: []string{"task.txt"}, CommandRunner: runner})
	if err != nil {
		t.Fatal(err)
	}

	// One captured provider Bash call, gated and executed exactly as the provider bridge does.
	const responseID, callID = "success-response-bash", "success-call-bash"
	arguments := json.RawMessage(`{"command":"printf 'bash-stage\\n' >> task.txt"}`)
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
		ConversationID: conversationID, TaskID: "success-task", Generation: g0.ID(), Prompt: "synthetic success-path fixture"})
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
	output, err := durable.Execute(context.Background(), proposal, decision)
	if err != nil {
		t.Fatalf("production Bash success path refused at Store/BindProductionBashEvidence: %v", err)
	}
	if runner.Settlements() != 1 || runner.Pending() != 0 {
		t.Fatalf("BindProductionBashEvidence did not settle the lease: settled=%d pending=%d", runner.Settlements(), runner.Pending())
	}
	tip := durable.Tip()
	encodedResult, err := protocol.MarshalResult(protocol.Result{SchemaVersion: protocol.ResultSchemaVersion,
		ResponseID: decision.ResponseID, ToolCallID: callID, Tool: "bash", Sequence: decision.Sequence,
		CanonicalArgumentsDigest: decision.CanonicalArgumentsDigest, Verdict: string(decision.Verdict),
		ReasonCode: decision.ReasonCode, PolicyDigest: decision.PolicyDigest, Output: append(json.RawMessage(nil), output...)})
	if err != nil {
		t.Fatal(err)
	}
	var mutation durableMutationSummaryPayload
	if err := decodeGovernedResult(output, &mutation); err != nil {
		t.Fatal(err)
	}
	resultEvidence := providerResultEvidence{WorkflowID: workflowID, ConversationID: conversationID,
		ResponseIDIssuer: governedResponseIssuer, ToolCallIDIssuer: governedCallIssuer, ResponseID: responseID, ToolCallID: callID,
		Tool: "bash", ArgumentDigest: argumentDigest, Sequence: 1, GenerationFrom: g0.ID(), GenerationTo: tip.ID(),
		TreeDigestFrom: g0.TreeDigest(), TreeDigestTo: tip.TreeDigest(),
		ResultID:     stableProviderResultID(workflowID, conversationID, responseID, callID),
		ResultDigest: digestBytes(encodedResult), Result: encodedResult,
		TransitionID: mutation.Transition.ID, TransitionSeq: mutation.Transition.Sequence, EffectID: mutation.EffectID}
	appendEvent("provider_tool_result", resultEvidence.ResultID, resultEvidence)

	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.ApprovedDeltaLedger) != 1 || len(bundle.Operations) != 1 {
		t.Fatalf("want one Bash transition/operation: %+v %+v", bundle.ApprovedDeltaLedger, bundle.Operations)
	}
	terminal, operation := bundle.ApprovedDeltaLedger[0], bundle.Operations[0]
	proposalID, err := durableTrustedIdentity("proposal", proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	providerCall := delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: proposalID,
		CallIssuer: governedCallIssuer, CallID: callID}
	ctx := context.Background()

	// Owner settlement: the verifier VerifyOrigin calls under requireEvidencedBashContainment.
	if err := runner.VerifyProductionBashEvidenceWithOrigin(ctx, bundle, providerCall, terminal, operation); err != nil {
		t.Fatalf("owner settlement refused the sealed D09 receipt: %v", err)
	}
	if err := authority.VerifyOrigin(bundle, terminal); err != nil {
		t.Fatalf("evidenced VerifyOrigin refused the Bash lease: %v", err)
	}
	// CheckPublicationPrerequisites' per-Bash-operation checks, in its order. Its lifecycle/launch-proof
	// and managed live-root permit checks need a launched Pi worker and a registered writer domain.
	matched, err := productionTransitionForOperation(bundle, operation)
	if err != nil || matched.ID != terminal.ID {
		t.Fatalf("publication transition lookup: %+v err=%v", matched, err)
	}
	if err := authority.VerifyOrigin(bundle, matched); err != nil {
		t.Fatalf("publication origin revalidation: %v", err)
	}
	if err := verifyProductionBashEvidence(ctx, runner, operation); err != nil {
		t.Fatalf("publication Bash evidence: %v", err)
	}
	resolved, err := authority.ResolveTerminalLeaseOrigin(ctx, bundle, terminal)
	if err != nil || resolved != providerCall {
		t.Fatalf("terminal lease origin = %+v err=%v; want %+v", resolved, err, providerCall)
	}
	if final, err := os.ReadFile(filepath.Join(storeRootPath, "generations", tip.ID(), "task.txt")); err != nil ||
		string(final) != "baseline\nbash-stage\n" {
		t.Fatalf("Bash output generation = %q err=%v", final, err)
	}
}
