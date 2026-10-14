//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sandbox"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

// This exercises the real Store/DurableExecutor/privateGit/development-sandbox
// composition against an explicitly synthetic provider audit prefix, then
// publishes the real lease-ending g0->g2 Store chain with a test-only quiescence
// Trust over a private temporary live root. It is not an actual Pi/provider run
// and deliberately makes no production containment/G1 claim.
func TestSyntheticProviderAuditBindsRealE05StoreAndLeaseOrigin(t *testing.T) {
	root := privateGovernedTestRoot(t)
	runner, err := sessionlaunch.NewDevelopmentCommandRunner(sandbox.Limits{})
	if err != nil {
		t.Skipf("measured non-claim-bearing development sandbox unavailable: %v", err)
	}
	const (
		workflowID     = "governed-e05-workflow"
		conversationID = "governed-e05-conversation"
		profileID      = "synthetic-offline-profile-v1"
	)
	storePolicyDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	metadataPolicyDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	xattrDigest := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	gatePolicy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "edit", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(root, "session-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	providerJournal, err := audit.Open(filepath.Join(root, "provider-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer providerJournal.Close()
	authority, err := NewSessionOperationAuthority(journal, providerJournal, workflowID, conversationID, profileID, gatePolicy,
		storePolicyDigest, metadataPolicyDigest, runner, []string{"task.txt"})
	if err != nil {
		t.Fatal(err)
	}
	storeRootPath, sourcePath, adminPath := filepath.Join(root, "store"), filepath.Join(root, "source"), filepath.Join(root, "private-admin")
	for _, path := range []string{storeRootPath, sourcePath, adminPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sourcePath, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	storeRoot, err := os.Open(storeRootPath)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(), workspace.XattrVisibilityAttestation{ProfileDigest: xattrDigest, Complete: true}, nil)
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
	g0, err := store.Seed(sourceRoot, sessionrepo.RootAttestation{Quiescent: true}) // synthetic private source only
	_ = sourceRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindBaseline(store, g0); err != nil {
		t.Fatalf("bind actual g0 to retained session evidence: %v", err)
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
		t.Fatalf("prepare D06 source snapshot: %v", err)
	}
	defer prepared.Close()
	if err := authority.BindPreparedSession(prepared); err != nil {
		t.Fatalf("bind durable D06 provenance before tool/session activity: %v", err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: governedCallIssuer,
		PolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest, BashOutputPaths: []string{"task.txt"}, CommandRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}

	calls := []struct {
		responseID string
		callID     string
		tool       string
		generation string
		arguments  json.RawMessage
	}{
		{"e05-response-read-1", "e05-call-read-1", "read", "g0", json.RawMessage(`{"path":"task.txt"}`)},
		{"e05-response-edit", "e05-call-edit", "edit", "g0", json.RawMessage(`{"path":"task.txt","edits":[{"oldText":"baseline\n","newText":"edited\n"}]}`)},
		{"e05-response-bash", "e05-call-bash", "bash", "g1", json.RawMessage(`{"command":"printf 'bash-stage\\n' >> task.txt"}`)},
		{"e05-response-read-2", "e05-call-read-2", "read", "g2", json.RawMessage(`{"path":"task.txt"}`)},
	}
	stream, err := protocol.NewStream(governedCallIssuer, governedResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	for index, call := range calls {
		digest, err := protocol.CanonicalArgumentsDigest(call.tool, call.arguments)
		if err != nil {
			t.Fatal(err)
		}
		captured := stream.Capture(protocol.TrustedCapture{
			ResponseID: correlation.Identifier{Issuer: governedResponseIssuer, Opaque: call.responseID},
			ToolCallID: correlation.Identifier{Issuer: governedCallIssuer, Opaque: call.callID},
			ToolName:   call.tool, RawArguments: append([]byte(nil), call.arguments...),
			CanonicalizationProfile: protocol.CanonicalizationProfile, CanonicalArgumentsDigest: digest,
			Sequence: uint64(index + 1), Generation: call.generation,
		})
		if !captured.Accepted || captured.StreamClosed {
			t.Fatalf("register synthetic independent capture %d: %+v", index+1, captured)
		}
	}

	ordinal := uint64(0)
	appendEvent := func(kind, id string, data any) {
		t.Helper()
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		ordinal++
		if _, err := providerJournal.Append(audit.Event{Kind: kind, ID: fmt.Sprintf("%s:%s:%d", conversationID, id, ordinal), Data: encoded}); err != nil {
			t.Fatalf("append synthetic provider event %s: %v", kind, err)
		}
	}
	appendEvent("provider_user_admitted", "user", providerPromptEvidence{
		WorkflowID: workflowID, ConversationID: conversationID, TaskID: "e05-task", Generation: g0.ID(), Prompt: "synthetic approved E05 fixture",
	})
	providerURL, err := url.Parse(openrouter.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	current := g0
	for index, call := range calls {
		digest, _ := protocol.CanonicalArgumentsDigest(call.tool, call.arguments)
		turn := uint64(index + 1)
		appendEvent("provider_exchange", "exchange", providerExchangeEvidence{
			WorkflowID: workflowID, Turn: turn,
			Request: providerRequestEvidence{ProfileID: profileID, Method: "POST", URL: openrouter.Endpoint, Host: providerURL.Host,
				ContentLength: int64(len("synthetic-offline-request")), Headers: map[string][]string{"content-type": {"application/json"}}, Body: []byte("synthetic-offline-request")},
			StatusCode: 200, Response: []byte("synthetic-offline-response"), ResponseIDIssuer: governedResponseIssuer,
			ToolCallIDIssuer: governedCallIssuer, ResponseID: call.responseID, Model: providerbridge.ApprovedModel,
			ToolCall: &struct {
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}{
				ID: call.callID, Name: call.tool, Arguments: append(json.RawMessage(nil), call.arguments...),
			},
		})
		proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: call.callID, Tool: call.tool, Arguments: call.arguments}
		encodedProposal, err := protocol.MarshalProposal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		matched := stream.Propose(encodedProposal)
		decision := gate.Evaluate(proposal, matched, gatePolicy)
		if decision.Verdict != gate.Allow || decision.CanonicalArgumentsDigest != digest || decision.ResponseID == nil {
			t.Fatalf("synthetic gate failed exact capture %d: %+v", turn, decision)
		}
		appendEvent("provider_gate_decision", "decision", decision)
		generationBefore, treeBefore := durable.Tip().ID(), durable.Tip().TreeDigest()
		output, err := durable.Execute(context.Background(), proposal, decision)
		if err != nil {
			t.Fatalf("durable %s call %d: %v", call.tool, turn, err)
		}
		generationAfter, treeAfter := durable.Tip().ID(), durable.Tip().TreeDigest()
		result := protocol.Result{
			SchemaVersion: protocol.ResultSchemaVersion, ResponseID: decision.ResponseID, ToolCallID: call.callID,
			Tool: call.tool, Sequence: decision.Sequence, CanonicalArgumentsDigest: decision.CanonicalArgumentsDigest,
			Verdict: string(decision.Verdict), ReasonCode: decision.ReasonCode, PolicyDigest: decision.PolicyDigest,
			Output: append(json.RawMessage(nil), output...),
		}
		encodedResult, err := protocol.MarshalResult(result)
		if err != nil {
			t.Fatal(err)
		}
		resultDigest := digestBytes(encodedResult)
		evidence := providerResultEvidence{
			WorkflowID: workflowID, ConversationID: conversationID, ResponseIDIssuer: governedResponseIssuer,
			ToolCallIDIssuer: governedCallIssuer, ResponseID: call.responseID, ToolCallID: call.callID,
			Tool: call.tool, ArgumentDigest: digest, Sequence: turn, GenerationFrom: generationBefore, GenerationTo: generationAfter,
			TreeDigestFrom: treeBefore, TreeDigestTo: treeAfter, ResultID: stableProviderResultID(workflowID, conversationID, call.responseID, call.callID),
			ResultDigest: resultDigest, Result: encodedResult,
		}
		if call.tool != "read" {
			var mutation durableMutationSummaryPayload
			if err := decodeGovernedResult(output, &mutation); err != nil {
				t.Fatal(err)
			}
			evidence.TransitionID, evidence.TransitionSeq, evidence.EffectID = mutation.Transition.ID, mutation.Transition.Sequence, mutation.EffectID
		}
		appendEvent("provider_tool_result", evidence.ResultID, evidence)
		current = durable.Tip()
		if current.ID() != generationAfter || current.TreeDigest() != treeAfter {
			t.Fatalf("current tip changed after result record: %s", call.tool)
		}
	}
	if current.ID() != "g2" {
		t.Fatalf("E05 generation sequence ended at %s; want g2", current.ID())
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.ApprovedDeltaLedger) != 2 || bundle.ApprovedDeltaLedger[0].Operation.Kind != delta.OperationProposalCall ||
		bundle.ApprovedDeltaLedger[1].Operation.Kind != delta.OperationLease || bundle.Generations[len(bundle.Generations)-1].ID != "g2" {
		t.Fatalf("durable E05 chain lost edit/Bash origins: %+v", bundle.ApprovedDeltaLedger)
	}
	terminalLease := bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1]
	if err := authority.VerifyOrigin(bundle, terminalLease); err != nil {
		t.Fatalf("providerbridge durable Bash result did not authenticate its proposal-call origin: %v", err)
	}
	resolvedOrigin, err := authority.ResolveTerminalLeaseOrigin(context.Background(), bundle, terminalLease)
	if err != nil || resolvedOrigin.Kind != delta.OperationProposalCall ||
		resolvedOrigin.CallIssuer != governedCallIssuer || resolvedOrigin.CallID != "e05-call-bash" {
		t.Fatalf("terminal Bash origin was not resolved from its actual captured provider call: %+v err=%v", resolvedOrigin, err)
	}
	if resolvedOrigin.ProposalID == bundle.ApprovedDeltaLedger[0].Operation.ProposalID {
		t.Fatal("terminal Bash origin resolved to the prior edit proposal")
	}
	if err := authority.CheckFinalizerOrigin(bundle); err != nil {
		t.Fatalf("actual terminal Bash origin failed finalizer authentication: %v", err)
	}
	if !prepared.HasDurableD06Provenance() {
		t.Fatal("D06 provenance did not survive actual E05 generations")
	}
	finalBytes, err := os.ReadFile(filepath.Join(storeRootPath, "generations", "g2", "task.txt"))
	if err != nil || string(finalBytes) != "edited\nbash-stage\n" {
		t.Fatalf("durable E05 output = %q, err=%v", finalBytes, err)
	}

	// Publish the same real Store/authority E05 chain through the versioned
	// terminal-lease origin binding. Only the host-side quiescence assertion is a
	// deterministic fixture over this exclusively owned temporary live root; it
	// does not establish production quiescence or G1.
	livePath := filepath.Join(root, "live")
	if err := os.Mkdir(livePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeNativeFixtureWorkspace(livePath, "baseline\n"); err != nil {
		t.Fatal(err)
	}
	liveRoot, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer liveRoot.Close()
	publicationStatePath := filepath.Join(root, "publication-state")
	if err := os.Mkdir(publicationStatePath, 0o700); err != nil {
		t.Fatal(err)
	}
	publicationState := durableOpenRoot(t, publicationStatePath)
	defer publicationState.Close()
	publicationJournal, err := audit.Open(filepath.Join(root, "publication-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer publicationJournal.Close()
	rootLedger, err := workflow.NewRootLedger(publicationJournal)
	if err != nil {
		t.Fatal(err)
	}
	workspaceOptions := workspace.Options{Generation: g0.ID(), MetadataPolicyDigest: metadataPolicyDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: xattrDigest, Complete: true}}
	// Seed one known, already-resolved root owner so the subsequent managed lease
	// and successful publication exercise a strictly increasing epoch. The
	// negative-origin assertions below compare against this exact prior prefix.
	epochReservation, err := rootLedger.Begin(liveRoot, "e05-owner-epoch-seed", 1, "e05-owner-epoch-seed-publication", workspaceOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := rootLedger.Resolve(epochReservation, liveRoot, "e05-owner-epoch-seed", "e05-owner-epoch-seed-publication", "", publication.StatusFailed); err != nil {
		t.Fatal(err)
	}
	epochReservation.Release()
	beforeWrongOriginTrace, err := publicationJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	publicationTrust := &e05PublicationTrust{authority: authority, liveRoot: liveRoot, baseline: g0,
		workspace: workspaceOptions, policyDigest: storePolicyDigest, metadataDigest: metadataPolicyDigest,
		commandRunnerProfile: runner.Profile()}
	openPublication := func(stateRoot *os.File, epoch uint64) func() (*publication.Repository, error) {
		return func() (*publication.Repository, error) {
			return publication.Acquire(publication.Options{WorkflowID: workflowID, OwnerEpoch: epoch,
				StateRoot: stateRoot, Journal: publicationJournal, Workspace: workspaceOptions})
		}
	}
	finalizerOptions := func(stateRoot *os.File, epoch uint64, publicationID string, trust workflow.Trust) workflow.FinalizerOptions {
		return workflow.FinalizerOptions{
			WorkflowID: workflowID, OwnerEpoch: epoch, PublicationID: publicationID,
			LiveRoot: liveRoot, Store: store, Baseline: g0, CurrentTip: durable.Tip, Workspace: workspaceOptions,
			Journal: publicationJournal, Ledger: rootLedger, OpenPublication: openPublication(stateRoot, epoch),
			OpenRecovery: func(id string) (workflow.RecoveryRepository, error) {
				if id != workflowID {
					return nil, fmt.Errorf("no E05 publication recovery state for %q", id)
				}
				return openPublication(stateRoot, epoch+1)()
			}, Trust: trust,
		}
	}
	// First, prove an origin mismatch/refusal stops before root-ledger begin and
	// publication prepare. The test resolver returns the actual Bash call once,
	// then simulates a stale/wrong call association on re-verification.
	wrongOriginTrust := *publicationTrust
	wrongOriginTrust.wrongResolveCall = 2
	wrongStatePath := filepath.Join(root, "publication-state-wrong-origin")
	if err := os.Mkdir(wrongStatePath, 0o700); err != nil {
		t.Fatal(err)
	}
	wrongState := durableOpenRoot(t, wrongStatePath)
	defer wrongState.Close()
	wrongRuntime, err := NewPublicationRuntime(durable,
		finalizerOptions(wrongState, 1, "e05-wrong-terminal-origin", &wrongOriginTrust))
	if err != nil {
		t.Fatalf("construct wrong-origin E05 negative publication finalizer: %v", err)
	}
	releaseWrongSession, err := wrongRuntime.BeginSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	releaseWrongSession()
	if result, err := wrongRuntime.Finalize(context.Background()); result.TransactionID != "" || !errors.Is(err, publication.ErrTerminalLeaseOriginBinding) {
		t.Fatalf("wrong terminal call origin was not refused before prepare: result=%+v err=%v", result, err)
	}
	if err := wrongRuntime.Close(); err != nil {
		t.Fatal(err)
	}
	beforeTrace, err := publicationJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if lenRuntimeEvents(beforeTrace, "publication_prepare") != 0 || lenRuntimeEvents(beforeTrace, "publication_token_consumed") != 0 {
		t.Fatal("wrong terminal origin persisted prepare/token authority")
	}
	if lenRuntimeEvents(beforeTrace, "workflow_publication_pending") != 1 ||
		lenRuntimeEvents(beforeTrace, "workflow_publication_resolved") != 1 ||
		lenRuntimeEvents(beforeTrace, "workflow_publication_transaction") != 0 {
		t.Fatalf("wrong-origin refusal changed RootLedger event counts: %+v", beforeTrace.Records)
	}
	if len(beforeTrace.Records) < len(beforeWrongOriginTrace.Records) {
		t.Fatal("wrong-origin refusal truncated the pre-existing lifecycle journal")
	}
	for index, prior := range beforeWrongOriginTrace.Records {
		if beforeTrace.Records[index].Sequence != prior.Sequence || beforeTrace.Records[index].Hash != prior.Hash ||
			beforeTrace.Records[index].Event.Kind != prior.Event.Kind || beforeTrace.Records[index].Event.ID != prior.Event.ID {
			t.Fatalf("wrong-origin refusal altered prior lifecycle frame %d: before=%+v after=%+v", index, prior, beforeTrace.Records[index])
		}
	}
	liveIdentity, err := measureManagedLiveRootDescriptor(liveRoot)
	if err != nil {
		t.Fatal(err)
	}
	wrongRootEventID := managedWorkflowEventID(workflow.RootIdentity{Device: liveIdentity.Device, Inode: liveIdentity.Inode},
		workflowID, "e05-wrong-terminal-origin")
	for _, record := range beforeTrace.Records {
		if record.Event.ID == wrongRootEventID || record.Event.ID == wrongRootEventID+"/transaction" || record.Event.ID == wrongRootEventID+"/resolved" {
			t.Fatalf("wrong terminal origin persisted root-ledger authority: %+v", record)
		}
	}
	const managedRunID = "e05-managed-live-root-run"
	managedAuthority := newFakeManagedLiveRootAuthority()
	managedLease, err := managedAuthority.acquire(context.Background(), liveRoot,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest}, publicationJournal,
		managedRunID, workflowID, "e05-terminal-lease-publication", 2)
	if err != nil {
		t.Fatalf("acquire test-only managed-root registration boundary: %v", err)
	}
	publicationTrust.managedLease = managedLease

	publicationRuntime, err := NewPublicationRuntime(durable,
		finalizerOptions(publicationState, 2, "e05-terminal-lease-publication", publicationTrust))
	if err != nil {
		t.Fatalf("construct real Store E05 publication finalizer: %v", err)
	}
	defer publicationRuntime.Close()
	releaseSession, err := publicationRuntime.BeginSession(context.Background())
	if err != nil {
		t.Fatalf("begin protected E05 publication session: %v", err)
	}
	if err := managedLease.completeAdmissionRecovery(context.Background()); err != nil {
		t.Fatalf("complete managed-root RootLedger recovery before E05 finalization: %v", err)
	}
	liveBefore, err := os.ReadFile(filepath.Join(livePath, "task.txt"))
	if err != nil || string(liveBefore) != "baseline\n" {
		t.Fatalf("live root changed before E05 finalization: %q err=%v", liveBefore, err)
	}
	liveBeforeHash := sha256.Sum256(liveBefore)
	releaseSession()
	publicationResult, publicationErr := publicationRuntime.Finalize(context.Background())
	if publicationErr != nil || publicationResult.Status != publication.StatusSucceeded || publicationResult.TransactionID == "" {
		t.Fatalf("real Store/authority terminal-lease publication failed: result=%+v err=%v", publicationResult, publicationErr)
	}
	if err := managedLease.recordDurableTerminal(context.Background(), publicationJournal,
		publicationResult.Status, publicationResult.TransactionID); err != nil {
		t.Fatalf("managed live-root lease did not accept the durable E05 terminal status: %v", err)
	}
	if publicationResult.Binding.SchemaVersion != publication.CommitBindingV2Schema || publicationResult.Binding.TerminalLeaseOrigin == nil ||
		publicationResult.Binding.TerminalLeaseOrigin.ProposalCall.CallID != "e05-call-bash" ||
		publicationResult.Binding.TerminalLeaseOrigin.LeaseOperation != terminalLease.Operation {
		t.Fatalf("published binding does not retain the exact Bash lease/call association: %+v", publicationResult.Binding)
	}
	liveAfter, err := os.ReadFile(filepath.Join(livePath, "task.txt"))
	liveAfterHash := sha256.Sum256(liveAfter)
	if err != nil || string(liveAfter) != "edited\nbash-stage\n" || liveAfterHash == liveBeforeHash {
		t.Fatalf("terminal lease publication did not apply/hash-change the real E05 Store tip: %q err=%v before=%x after=%x", liveAfter, err, liveBeforeHash, liveAfterHash)
	}
	publicationTrace, err := publicationJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if lenRuntimeEvents(publicationTrace, "publication_prepare") != 1 || lenRuntimeEvents(publicationTrace, "publication_token_consumed") != 1 {
		t.Fatalf("terminal lease publication lacks durable prepare/token evidence: %+v", publicationTrace.Records)
	}
	if err := publicationRuntime.Close(); err != nil {
		t.Fatalf("close E05 publication runtime before releasing managed root: %v", err)
	}
	if err := managedLease.close(); err != nil {
		t.Fatalf("release managed root after known durable terminal: %v", err)
	}
}

func TestResolveFromTraceRequiresImmediateBoundProviderReservation(t *testing.T) {
	const (
		workflowID     = "resolver-workflow"
		conversationID = "resolver-conversation"
		profileID      = "resolver-profile"
	)
	providerURL, err := url.Parse(openrouter.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	authority := &SessionOperationAuthority{
		workflowID: workflowID, conversationID: conversationID, providerProfileID: profileID,
		initialGeneration: "g0", initialTreeDigest: "sha256:" + strings.Repeat("a", 64), baselineBound: true,
	}
	request := providerRequestEvidence{
		ProfileID: profileID, Method: "POST", URL: openrouter.Endpoint, Host: providerURL.Host,
		ContentLength: int64(len("exact request bytes")), Headers: map[string][]string{"content-type": {"application/json"}},
		Body: []byte("exact request bytes"),
	}
	binding := providerbridge.HostCallBinding{CallID: "host-call-1", RequestDigest: providerRequestEvidenceDigest(request), TokenUpperBound: 73}
	reservation := providerHostCallReservationEvidence{WorkflowID: workflowID, ConversationID: conversationID,
		ProfileID: profileID, Turn: 1, Binding: binding, Request: request}
	exchange := providerExchangeEvidence{WorkflowID: workflowID, Turn: 1, Request: request,
		StatusCode: 200, Response: []byte("offline response"), ResponseIDIssuer: governedResponseIssuer,
		ToolCallIDIssuer: governedCallIssuer, ResponseID: "response-1", Model: providerbridge.ApprovedModel,
		HostCall: &binding}

	buildTrace := func(events ...struct {
		kind  string
		label string
		data  any
	}) audit.Trace {
		t.Helper()
		records := make([]audit.Record, 0, len(events))
		for index, event := range events {
			encoded, err := json.Marshal(event.data)
			if err != nil {
				t.Fatal(err)
			}
			sequence := uint64(index + 1)
			records = append(records, audit.Record{Version: 1, Sequence: sequence,
				Event: audit.Event{Kind: event.kind,
					ID: fmt.Sprintf("%s:%s:%d", conversationID, event.label, sequence), Data: encoded},
				Hash: fmt.Sprintf("record-hash-%d", sequence)})
		}
		return audit.Trace{Records: records}
	}
	type event struct {
		kind  string
		label string
		data  any
	}
	call := func(trace audit.Trace) error {
		t.Helper()
		_, err := authority.resolveFromTrace(trace, "unused-decision")
		return err
	}
	validTrace := buildTrace(event{"provider_host_call_reserved", "host-call-reserved", reservation},
		event{"provider_exchange", "exchange", exchange})
	if err := call(validTrace); err == nil || !strings.Contains(err.Error(), "maps to 0 provider captures") {
		t.Fatalf("valid immediate request reservation did not reach the empty-capture result: %v", err)
	}
	idleExchange := providerExchangeEvidence{WorkflowID: workflowID, Turn: 1, Request: request,
		StatusCode: 200, Response: []byte("assistant text"), ResponseIDIssuer: governedResponseIssuer,
		ToolCallIDIssuer: governedCallIssuer, ResponseID: "response-idle", Model: providerbridge.ApprovedModel}
	idlePrefix := buildTrace(
		event{"provider_user_admitted", "user", providerPromptEvidence{WorkflowID: workflowID,
			ConversationID: conversationID, TaskID: "task", Generation: "g0", Prompt: "prompt"}},
		event{"provider_exchange", "exchange", idleExchange})
	idleRetirement := providerLineageRetirementEvidence{SchemaVersion: "tbound-provider-lineage-retirement/v1",
		WorkflowID: workflowID, ConversationID: conversationID, OwnerEpoch: 2, PreviousOwnerEpoch: 1,
		RetirementKind: "host_idle_lineage_retired/v1", ProviderSequence: 1,
		PreviousRecordSequence: 2, PreviousRecordHash: idlePrefix.Records[1].Hash}
	retiredIdleTrace := buildTrace(
		event{"provider_user_admitted", "user", providerPromptEvidence{WorkflowID: workflowID,
			ConversationID: conversationID, TaskID: "task", Generation: "g0", Prompt: "prompt"}},
		event{"provider_exchange", "exchange", idleExchange},
		event{"provider_lineage_retired", "lineage_retired", idleRetirement})
	if err := call(retiredIdleTrace); err == nil || !strings.Contains(err.Error(), "maps to 0 provider captures") {
		t.Fatalf("valid idle lineage retirement was not accepted as a terminal history record: %v", err)
	}

	mutatedRequest := request
	mutatedRequest.Body = []byte("mutated request bytes")
	mutatedExchange := exchange
	mutatedExchange.Request = mutatedRequest
	mutations := []struct {
		name  string
		trace audit.Trace
	}{
		{name: "orphan managed exchange", trace: buildTrace(event{"provider_exchange", "exchange", exchange})},
		{name: "duplicate reservation", trace: buildTrace(event{"provider_host_call_reserved", "host-call-reserved", reservation},
			event{"provider_host_call_reserved", "host-call-reserved", reservation})},
		{name: "interleaved reservation", trace: buildTrace(event{"provider_host_call_reserved", "host-call-reserved", reservation},
			event{"provider_user_admitted", "user", providerPromptEvidence{WorkflowID: workflowID,
				ConversationID: conversationID, TaskID: "task", Generation: "g0", Prompt: "prompt"}})},
		{name: "wrong next turn", trace: buildTrace(event{"provider_host_call_reserved", "host-call-reserved", func() providerHostCallReservationEvidence {
			wrong := reservation
			wrong.Turn = 2
			return wrong
		}()})},
		{name: "mutated exchange request", trace: buildTrace(event{"provider_host_call_reserved", "host-call-reserved", reservation},
			event{"provider_exchange", "exchange", mutatedExchange})},
		{name: "reservation digest does not recompute", trace: buildTrace(event{"provider_host_call_reserved", "host-call-reserved", func() providerHostCallReservationEvidence {
			wrong := reservation
			wrong.Binding.RequestDigest = strings.Repeat("f", 64)
			return wrong
		}()})},
		{name: "unknown event kind", trace: buildTrace(event{"provider_unrecognized", "unknown", struct{}{}})},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			if err := call(test.trace); err == nil || strings.Contains(err.Error(), "maps to 0 provider captures") {
				t.Fatalf("resolver accepted malformed provider protocol history: %v", err)
			}
		})
	}
}

type e05PublicationTrust struct {
	authority            *SessionOperationAuthority
	managedLease         *managedLiveRootLease
	liveRoot             *os.File
	baseline             *sessionrepo.Generation
	workspace            workspace.Options
	policyDigest         string
	metadataDigest       string
	commandRunnerProfile string
	wrongResolveCall     int
	resolveCalls         int
}

func (trust *e05PublicationTrust) CheckAdmission(ctx context.Context) error {
	if trust == nil || trust.authority == nil || ctx == nil {
		return ErrGovernedSessionEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if trust.managedLease != nil {
		if err := trust.managedLease.matchesFinalizer(ctx, trust.liveRoot, trust.managedLease.runID,
			trust.managedLease.workflowID, trust.managedLease.ownerEpoch); err != nil {
			return err
		}
	}
	return trust.authority.requirePreparedSource()
}

func (trust *e05PublicationTrust) CheckPublicationPrerequisites(ctx context.Context, request workflow.EvidenceRequest) error {
	if trust == nil || trust.authority == nil || ctx == nil {
		return ErrGovernedSessionEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Bundle.PolicyDigest != trust.policyDigest || request.Bundle.MetadataPolicyDigest != trust.metadataDigest ||
		request.Bundle.RealProviderExchange || request.Bundle.PiAdapterWired || request.Bundle.CommandContainmentStatus != "not-established" {
		return errors.New("E05 fixture trust refuses production provider/Pi/containment labels")
	}
	for _, operation := range request.Bundle.Operations {
		if operation.Tool == "bash" && (operation.CommandContainmentStatus != "not-established" ||
			operation.CommandRunnerProfile != trust.commandRunnerProfile) {
			return errors.New("E05 fixture trust refuses mismatched synthetic Bash evidence")
		}
	}
	if err := trust.authority.requirePreparedSource(); err != nil {
		return err
	}
	if trust.managedLease != nil {
		return trust.managedLease.authorizePublication(ctx, trust.liveRoot, request, trust.baseline)
	}
	return nil
}

func (trust *e05PublicationTrust) AttestQuiescent(ctx context.Context, live *os.File, baseline, tip *sessionrepo.Generation) error {
	if trust == nil || ctx == nil || live == nil || baseline == nil || tip == nil || trust.baseline == nil ||
		baseline.ID() != trust.baseline.ID() || baseline.TreeDigest() != trust.baseline.TreeDigest() || tip.ID() == baseline.ID() {
		return errors.New("E05 fixture quiescence check lacks the registered temporary roots")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if trust.managedLease != nil {
		if err := trust.managedLease.issuePublicationPermit(ctx, live, baseline, tip); err != nil {
			return err
		}
	}
	options := trust.workspace
	options.Generation, options.QuiescentRoot = baseline.ID(), true
	snapshot, err := workspace.Scan(live, options)
	if err != nil || snapshot.TreeDigest != baseline.TreeDigest() || !sameManifest(snapshot.Manifest, baseline.Manifest()) {
		return errors.Join(errors.New("E05 test live root changed before finalization"), err)
	}
	return nil // Test-only assertion: test exclusively owns this private temp root.
}

func (trust *e05PublicationTrust) VerifyTransition(ctx context.Context, decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	if trust == nil || trust.authority == nil {
		return ErrGovernedSessionEvidence
	}
	if trust.managedLease != nil {
		if err := trust.managedLease.verifyPermitForTransition(ctx, trust.liveRoot); err != nil {
			return err
		}
	}
	return trust.authority.VerifyTransition(decision, binding)
}

func (trust *e05PublicationTrust) VerifyOrigin(ctx context.Context, bundle sessionrepo.EvidenceBundle, transition delta.Transition) error {
	if trust == nil || trust.authority == nil {
		return ErrGovernedSessionEvidence
	}
	if trust.managedLease != nil {
		if err := trust.managedLease.verifyPermitForTransition(ctx, trust.liveRoot); err != nil {
			return err
		}
	}
	return trust.authority.VerifyOrigin(bundle, transition)
}

func (trust *e05PublicationTrust) ResolveTerminalLeaseOrigin(ctx context.Context, bundle sessionrepo.EvidenceBundle, transition delta.Transition) (delta.OperationIdentity, error) {
	if trust == nil || trust.authority == nil {
		return delta.OperationIdentity{}, ErrGovernedSessionEvidence
	}
	if trust.managedLease != nil {
		if err := trust.managedLease.verifyPermitForTransition(ctx, trust.liveRoot); err != nil {
			return delta.OperationIdentity{}, err
		}
	}
	resolved, err := trust.authority.ResolveTerminalLeaseOrigin(ctx, bundle, transition)
	if err != nil {
		return delta.OperationIdentity{}, err
	}
	trust.resolveCalls++
	if trust.wrongResolveCall != 0 && trust.resolveCalls == trust.wrongResolveCall {
		for _, candidate := range bundle.ApprovedDeltaLedger {
			if candidate.Operation.Kind == delta.OperationProposalCall {
				return candidate.Operation, nil // negative test injects a stale edit-origin resolution
			}
		}
		return delta.OperationIdentity{}, errors.New("no prior proposal-call candidate for negative origin test")
	}
	return resolved, nil
}

func lenRuntimeEvents(trace audit.Trace, kind string) int {
	count := 0
	for _, record := range trace.Records {
		if record.Event.Kind == kind {
			count++
		}
	}
	return count
}

func TestSyntheticProviderConversationAuditDrivesActualDurableE05Executor(t *testing.T) {
	root := privateGovernedTestRoot(t)
	runner, err := sessionlaunch.NewDevelopmentCommandRunner(sandbox.Limits{})
	if err != nil {
		t.Skipf("measured non-claim-bearing development sandbox unavailable: %v", err)
	}
	const workflowID, conversationID, providerProfileID = "e05-workflow", "e05-conversation", "e05-profile"
	storePolicyDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	metadataPolicyDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	xattrDigest := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "edit", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(root, "e05-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	providerJournal, err := audit.Open(filepath.Join(root, "e05-provider-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer providerJournal.Close()
	authority, err := NewSessionOperationAuthority(journal, providerJournal, workflowID, conversationID, providerProfileID, policy,
		storePolicyDigest, metadataPolicyDigest, runner, []string{"task.txt"})
	if err != nil {
		t.Fatal(err)
	}
	storeRootPath, sourcePath, adminPath := filepath.Join(root, "store"), filepath.Join(root, "source"), filepath.Join(root, "admin")
	for _, path := range []string{storeRootPath, sourcePath, adminPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sourcePath, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	storeRoot, err := os.Open(storeRootPath)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(), workspace.XattrVisibilityAttestation{ProfileDigest: xattrDigest, Complete: true}, nil)
	_ = storeRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := managed.Store()
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true}) // synthetic, supervisor-private source only
	_ = source.Close()
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
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: governedCallIssuer,
		PolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest, BashOutputPaths: []string{"task.txt"}, CommandRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}

	calls := []struct {
		responseID string
		callID     string
		tool       string
		generation string
		arguments  json.RawMessage
	}{
		{"response-read-1", "call-read-1", "read", "g0", json.RawMessage(`{"path":"task.txt"}`)},
		{"response-edit", "call-edit", "edit", "g0", json.RawMessage(`{"path":"task.txt","edits":[{"oldText":"baseline\n","newText":"edited\n"}]}`)},
		{"response-bash", "call-bash", "bash", "g1", json.RawMessage(`{"command":"printf 'bash-stage\\n' >> task.txt"}`)},
		{"response-read-2", "call-read-2", "read", "g2", json.RawMessage(`{"path":"task.txt"}`)},
	}
	stream, err := protocol.NewStream(governedCallIssuer, governedResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	for sequence, call := range calls {
		digest, err := protocol.CanonicalArgumentsDigest(call.tool, call.arguments)
		if err != nil {
			t.Fatal(err)
		}
		captured := stream.Capture(protocol.TrustedCapture{
			ResponseID: correlation.Identifier{Issuer: governedResponseIssuer, Opaque: call.responseID},
			ToolCallID: correlation.Identifier{Issuer: governedCallIssuer, Opaque: call.callID},
			ToolName:   call.tool, RawArguments: append([]byte(nil), call.arguments...),
			CanonicalizationProfile: protocol.CanonicalizationProfile, CanonicalArgumentsDigest: digest,
			Sequence: uint64(sequence + 1), Generation: call.generation,
		})
		if !captured.Accepted {
			t.Fatalf("capture %s: %+v", call.callID, captured)
		}
	}
	appendProvider := newGovernedTestJournalAppender(t, providerJournal, conversationID)
	appendProvider("provider_user_admitted", "user", providerPromptEvidence{
		WorkflowID: workflowID, ConversationID: conversationID, TaskID: "synthetic-e05-task", Generation: "g0", Prompt: "synthetic offline E05",
	})
	providerURL, err := url.Parse(openrouter.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	for sequence, call := range calls {
		turn := uint64(sequence + 1)
		digest, _ := protocol.CanonicalArgumentsDigest(call.tool, call.arguments)
		appendProvider("provider_exchange", "exchange", providerExchangeEvidence{
			WorkflowID: workflowID, Turn: turn,
			Request: providerRequestEvidence{ProfileID: providerProfileID, Method: "POST", URL: openrouter.Endpoint,
				Host: providerURL.Host, ContentLength: int64(len("synthetic no-network request")),
				Headers: map[string][]string{"content-type": {"application/json"}}, Body: []byte("synthetic no-network request")},
			StatusCode: 200, Response: []byte("synthetic offline response"),
			ResponseIDIssuer: governedResponseIssuer, ToolCallIDIssuer: governedCallIssuer,
			ResponseID: call.responseID, Model: providerbridge.ApprovedModel,
			ToolCall: &struct {
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}{
				ID: call.callID, Name: call.tool, Arguments: append(json.RawMessage(nil), call.arguments...),
			},
		})
		proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: call.callID, Tool: call.tool, Arguments: call.arguments}
		encodedProposal, err := protocol.MarshalProposal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		matched := stream.Propose(encodedProposal)
		decision := gate.Evaluate(proposal, matched, policy)
		if decision.Verdict != gate.Allow || decision.CanonicalArgumentsDigest != digest {
			t.Fatalf("synthetic exact broker match failed for %s: %+v", call.callID, decision)
		}
		appendProvider("provider_gate_decision", "decision", decision)
		if sequence == 0 {
			forged := sessionrepo.OperationRequest{Tool: "read",
				Operation: delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: "caller-forged-proposal", CallIssuer: governedCallIssuer, CallID: call.callID},
				Decision: delta.PolicyDecision{ID: "caller-forged-decision", Outcome: delta.PolicyAllow,
					PolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest},
				InputGeneration: "g0", InputTreeDigest: durable.Tip().TreeDigest(), ArgumentDigest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", EffectID: "caller-forged-effect"}
			if err := authority.AuthorizeOperation(forged); !errors.Is(err, ErrGovernedSessionEvidence) {
				t.Fatalf("caller-asserted operation bypassed synced provider/gate receipts: %v", err)
			}
		}
		fromGeneration, fromTree := durable.Tip().ID(), durable.Tip().TreeDigest()
		output, err := durable.Execute(context.Background(), proposal, decision)
		if err != nil {
			t.Fatalf("actual DurableExecutor %s[%s]: %v", call.tool, call.callID, err)
		}
		toGeneration, toTree := durable.Tip().ID(), durable.Tip().TreeDigest()
		result := protocol.Result{SchemaVersion: protocol.ResultSchemaVersion, ResponseID: decision.ResponseID,
			ToolCallID: proposal.ToolCallID, Tool: proposal.Tool, Sequence: decision.Sequence,
			CanonicalArgumentsDigest: decision.CanonicalArgumentsDigest, Verdict: string(decision.Verdict),
			ReasonCode: decision.ReasonCode, PolicyDigest: decision.PolicyDigest, Output: append(json.RawMessage(nil), output...)}
		resultBytes, err := protocol.MarshalResult(result)
		if err != nil {
			t.Fatal(err)
		}
		evidence := providerResultEvidence{
			WorkflowID: workflowID, ConversationID: conversationID, ResponseIDIssuer: governedResponseIssuer,
			ToolCallIDIssuer: governedCallIssuer, ResponseID: call.responseID, ToolCallID: call.callID,
			Tool: call.tool, ArgumentDigest: digest, Sequence: turn,
			GenerationFrom: fromGeneration, GenerationTo: toGeneration,
			TreeDigestFrom: fromTree, TreeDigestTo: toTree,
			ResultID:     stableProviderResultID(workflowID, conversationID, call.responseID, call.callID),
			ResultDigest: digestBytes(resultBytes), Result: resultBytes,
		}
		if call.tool != "read" {
			var mutation durableMutationSummaryPayload
			if err := decodeGovernedResult(output, &mutation); err != nil {
				t.Fatal(err)
			}
			evidence.TransitionID, evidence.TransitionSeq, evidence.EffectID = mutation.Transition.ID, mutation.Transition.Sequence, mutation.EffectID
		}
		appendProvider("provider_tool_result", evidence.ResultID, evidence)
	}
	verified, err := store.Verify()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if verified.SealedGeneration != "g2" || len(bundle.ApprovedDeltaLedger) != 2 ||
		bundle.ApprovedDeltaLedger[0].Tool != "edit" || bundle.ApprovedDeltaLedger[1].Tool != "bash" ||
		bundle.ApprovedDeltaLedger[1].Operation.Kind != delta.OperationLease {
		t.Fatalf("actual durable E05 store lineage mismatch: verified=%+v ledger=%+v", verified, bundle.ApprovedDeltaLedger)
	}
	terminal := bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1]
	if err := authority.VerifyOrigin(bundle, terminal); err != nil {
		t.Fatalf("durable provider capture did not authenticate Bash lease origin: %v", err)
	}
	if err := authority.CheckFinalizerOrigin(bundle); err != nil {
		t.Fatalf("actual terminal Bash origin failed finalizer authentication: %v", err)
	}
	if !prepared.HasDurableD06Provenance() {
		t.Fatal("D06 source/private-Git provenance did not survive E05 effect history")
	}
	content, err := os.ReadFile(filepath.Join(storeRootPath, "generations", "g2", "task.txt"))
	if err != nil || string(content) != "edited\nbash-stage\n" {
		t.Fatalf("actual sessionrepo g2 bytes = %q, err=%v", content, err)
	}
}

func TestSessionAuthorityRefusesStoreBuiltWithForeignCallbacks(t *testing.T) {
	rootPath := privateGovernedTestRoot(t)
	storePath, sourcePath := filepath.Join(rootPath, "foreign-store"), filepath.Join(rootPath, "foreign-source")
	for _, path := range []string{storePath, sourcePath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sourcePath, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(rootPath, "foreign-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	providerJournal, err := audit.Open(filepath.Join(rootPath, "foreign-provider-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer providerJournal.Close()
	gatePolicy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "read", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewSessionOperationAuthority(journal, providerJournal, "foreign-workflow", "foreign-conversation", "foreign-profile", gatePolicy,
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	foreignStore, err := sessionrepo.Create(root, sessionrepo.Options{
		PolicyDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Limits:               workspace.DefaultLimits(),
		XattrVisibility:      workspace.XattrVisibilityAttestation{ProfileDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Complete: true},
		Journal:              journal, AuthorizeOperation: func(sessionrepo.OperationRequest) error { return errors.New("foreign authorizer refuses") },
		VerifyDecision: func(delta.PolicyDecision, delta.TransitionBinding) error {
			return errors.New("foreign verifier refuses")
		},
		VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error {
			return errors.New("foreign settlement verifier refuses")
		},
	})
	_ = root.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer foreignStore.Close()
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	g0, err := foreignStore.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindBaseline(foreignStore, g0); err == nil {
		t.Fatal("authority accepted a Store constructed with foreign callbacks")
	}
}

func newGovernedTestJournalAppender(t *testing.T, journal *audit.Journal, conversationID string) func(kind, id string, data any) {
	t.Helper()
	var providerOrdinal uint64
	return func(kind, id string, data any) {
		t.Helper()
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		providerOrdinal++
		eventID := fmt.Sprintf("%s:%s:%d", conversationID, id, providerOrdinal)
		if _, err := journal.Append(audit.Event{Kind: kind, ID: eventID, Data: encoded}); err != nil {
			t.Fatalf("append synthetic provider bridge event %s: %v", kind, err)
		}
	}
}

func privateGovernedTestRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, "tbound-governed-e05-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func pinGovernedTestGit(t *testing.T) (privategit.PinnedGit, sessionlaunch.GitProfile) {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("installed Git is unavailable: %v", err)
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	identity := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	boundary := "test-only-governed-session-private-admin"
	git, err := privategit.PinGitExecutable(path, privategit.GitExecutableApproval{
		ExpectedDigest: identity, Boundary: boundary,
		Verifier: func(actual privategit.GitExecutableIdentity) error {
			if actual.Digest != identity || actual.Mode&0o022 != 0 {
				return errors.New("synthetic pinned-Git verifier rejected executable bytes/mode")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = git.Close() })
	return git, sessionlaunch.GitProfile{ExecutableDigest: identity, ExecutionBoundary: boundary}
}

func appendGovernedTestEvent(t *testing.T, journal *audit.Journal, workflowID, conversationID, eventID string, data any) {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(audit.Event{Kind: eventID, ID: fmt.Sprintf("%s:%s:%d", conversationID, eventID, lenMustTrace(t, journal)+1), Data: encoded}); err != nil {
		t.Fatalf("append synthetic %s event: %v", eventID, err)
	}
}

func lenMustTrace(t *testing.T, journal *audit.Journal) int {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return len(trace.Records)
}

func appendGovernedToolResult(t *testing.T, journal *audit.Journal, workflowID, conversationID, responseID, callID string,
	sequence uint64, proposal protocol.Proposal, decision gate.Decision, output json.RawMessage,
	fromGeneration string, fromTree string, toGeneration string, toTree string) providerResultEvidence {
	t.Helper()
	result := protocol.Result{
		SchemaVersion: protocol.ResultSchemaVersion, ResponseID: decision.ResponseID,
		ToolCallID: proposal.ToolCallID, Tool: proposal.Tool, Sequence: decision.Sequence,
		CanonicalArgumentsDigest: decision.CanonicalArgumentsDigest, Verdict: string(decision.Verdict),
		ReasonCode: decision.ReasonCode, PolicyDigest: decision.PolicyDigest, Output: append(json.RawMessage(nil), output...),
	}
	encodedResult, err := protocol.MarshalResult(result)
	if err != nil {
		t.Fatal(err)
	}
	evidence := providerResultEvidence{
		WorkflowID: workflowID, ConversationID: conversationID,
		ResponseIDIssuer: governedResponseIssuer, ToolCallIDIssuer: governedCallIssuer,
		ResponseID: responseID, ToolCallID: callID, Tool: proposal.Tool,
		ArgumentDigest: decision.CanonicalArgumentsDigest, Sequence: sequence,
		GenerationFrom: fromGeneration, GenerationTo: toGeneration,
		TreeDigestFrom: fromTree, TreeDigestTo: toTree,
		ResultID:     stableProviderResultID(workflowID, conversationID, responseID, callID),
		ResultDigest: digestBytes(encodedResult), Result: encodedResult,
	}
	if proposal.Tool != "read" {
		var summary durableMutationSummaryPayload
		if err := decodeGovernedResult(output, &summary); err != nil {
			t.Fatalf("decode durable mutation result: %v", err)
		}
		evidence.TransitionID, evidence.TransitionSeq, evidence.EffectID = summary.Transition.ID, summary.Transition.Sequence, summary.EffectID
	}
	appendGovernedTestEvent(t, journal, workflowID, conversationID, "provider_tool_result", evidence)
	return evidence
}
