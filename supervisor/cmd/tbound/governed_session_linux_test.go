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
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sandbox"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// This exercises the real Store/DurableExecutor/privateGit/development-sandbox
// composition against an explicitly synthetic provider audit prefix. It is not
// an actual Pi/provider run and deliberately does not claim publication/G1.
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
		storePolicyDigest, metadataPolicyDigest, runner)
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
		PolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest, CommandRunner: runner,
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
	if err := authority.CheckFinalizerOrigin(bundle); !errors.Is(err, ErrLeaseOriginUnsupported) {
		t.Fatalf("known Finalizer lease-origin blocker was hidden or fabricated: %v", err)
	}
	if !prepared.HasDurableD06Provenance() {
		t.Fatal("D06 provenance did not survive actual E05 generations")
	}
	finalBytes, err := os.ReadFile(filepath.Join(storeRootPath, "generations", "g2", "task.txt"))
	if err != nil || string(finalBytes) != "edited\nbash-stage\n" {
		t.Fatalf("durable E05 output = %q, err=%v", finalBytes, err)
	}
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
		storePolicyDigest, metadataPolicyDigest, runner)
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
		PolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest, CommandRunner: runner,
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
	if err := authority.CheckFinalizerOrigin(bundle); !errors.Is(err, ErrLeaseOriginUnsupported) {
		t.Fatalf("terminal lease publication blocker was hidden or misrepresented: %v", err)
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
