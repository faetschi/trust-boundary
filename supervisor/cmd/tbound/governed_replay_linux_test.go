//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	governedReplayWorkflow     = "governed-replay-workflow"
	governedReplayConversation = "governed-replay-conversation"
	governedReplayProfile      = "governed-replay-offline-profile"
	governedReplaySystem       = "Synthetic offline recovery test system prompt."
	governedReplayDeveloper    = "Use only the pinned synthetic offline tools."
	governedReplayStorePolicy  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	governedReplayMetaPolicy   = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	governedReplayXattrPolicy  = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type governedReplayFixture struct {
	authority       *SessionOperationAuthority
	store           *sessionrepo.Store
	storeJournal    *audit.Journal
	providerJournal *audit.Journal
	policy          gate.Policy
	durable         *DurableExecutor
	conversation    *providerbridge.Conversation
	providerExec    providerbridge.Executor
	pins            GovernedReplayPins
}

func newGovernedReplayFixture(t *testing.T, turns []providerbridge.DevelopmentProviderTurn, admitPrompt bool) *governedReplayFixture {
	t.Helper()
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow},
		{Tool: "write", Effect: gate.EffectAllow},
		{Tool: "edit", Effect: gate.EffectAllow},
		{Tool: "bash", Effect: gate.EffectAllow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return newGovernedReplayFixtureWithPolicy(t, turns, admitPrompt, policy)
}

func newGovernedReplayFixtureWithPolicy(t *testing.T, turns []providerbridge.DevelopmentProviderTurn, admitPrompt bool, policy gate.Policy) *governedReplayFixture {
	t.Helper()
	root := privateGovernedTestRoot(t)
	sourcePath := filepath.Join(root, "source")
	storePath := filepath.Join(root, "store")
	adminPath := filepath.Join(root, "admin")
	for _, path := range []string{sourcePath, storePath, adminPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "empty.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	storeJournal, err := audit.Open(filepath.Join(root, "store-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeJournal.Close() })
	providerJournal, err := audit.Open(filepath.Join(root, "provider-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = providerJournal.Close() })

	runner := newDevelopmentFixtureCommandRunner()
	authority, err := NewSessionOperationAuthority(storeJournal, providerJournal,
		governedReplayWorkflow, governedReplayConversation, governedReplayProfile,
		policy, governedReplayStorePolicy, governedReplayMetaPolicy, runner, []string{"task.txt"})
	if err != nil {
		t.Fatal(err)
	}
	storeRoot, err := os.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(),
		workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}, nil)
	_ = storeRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	store := managed.Store()
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindBaseline(store, g0); err != nil {
		t.Fatalf("bind the synthetic baseline to retained Store evidence: %v", err)
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
	t.Cleanup(func() { _ = admin.Close() })
	git, gitProfile := pinGovernedTestGit(t)
	t.Cleanup(func() { _ = git.Close() })
	prepared, err := sessionlaunch.Prepare(context.Background(), managed, g0, admin, git, gitProfile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	if err := authority.BindPreparedSession(prepared); err != nil {
		t.Fatalf("bind the test D06 source fixture before tool activity: %v", err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: governedCallIssuer,
		PolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
		BashOutputPaths: []string{"task.txt"}, CommandRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := providerbridge.Config{
		WorkflowID: governedReplayWorkflow, ConversationID: governedReplayConversation, ProfileID: governedReplayProfile,
		InitialGenerationID: g0.ID(), InitialTreeDigest: g0.TreeDigest(),
		SystemPrompt: governedReplaySystem, DeveloperPrompt: governedReplayDeveloper,
	}
	conversation, err := providerbridge.NewDevelopmentOfflineConversation(config, providerJournal, turns)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conversation.Close() })
	authority.mu.Lock()
	authority.conversation = conversation
	authority.mu.Unlock()
	providerExec, err := providerbridge.NewExecutor(conversation, durable)
	if err != nil {
		t.Fatal(err)
	}
	if admitPrompt {
		if err := authority.AdmitPrompt(conversation, durable, "replay-test-task", "Read the fixture and apply the pinned operations."); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	systemCopy, developerCopy := config.SystemPrompt, config.DeveloperPrompt
	profile := broker.Profile{
		ID: config.ProfileID, Model: providerbridge.ApprovedModel,
		Messages: []openrouter.Message{
			{Role: openrouter.System, Content: &systemCopy},
			{Role: openrouter.Developer, Content: &developerCopy},
		},
		ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
		ResponseIDIssuer: governedResponseIssuer, Generation: g0.ID(),
	}
	pins := GovernedReplayPins{
		Provider: providerbridge.RecoveryPins{Config: config, Model: providerbridge.ApprovedModel, Policy: policy},
		Profile:  profile, Baseline: bundle.Baseline,
		StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
		BashOutputPaths: []string{"task.txt"},
	}
	return &governedReplayFixture{
		authority: authority,
		store:     store, storeJournal: storeJournal, providerJournal: providerJournal,
		policy: policy, durable: durable, conversation: conversation, providerExec: providerExec, pins: pins,
	}
}

func executeGovernedReplayTurn(t *testing.T, fixture *governedReplayFixture, releaseProviderResult bool) (protocol.Proposal, gate.Decision) {
	t.Helper()
	ctx := context.Background()
	turn, err := fixture.conversation.NextTurn(ctx)
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("expected a captured provider tool call, got %+v, err=%v", turn, err)
	}
	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    turn.ToolCall.ID, Tool: turn.ToolCall.Name,
		Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...),
	}
	matched, err := fixture.conversation.Correlate(ctx, proposal)
	if err != nil || !matched.Accepted || matched.StreamClosed {
		t.Fatalf("correlate captured provider call: %+v, err=%v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, fixture.policy)
	if decision.Verdict != gate.Allow {
		t.Fatalf("fixture policy did not allow %s: %+v", proposal.Tool, decision)
	}
	if err := fixture.conversation.RecordDecision(ctx, proposal, decision); err != nil {
		t.Fatalf("record gate decision: %v", err)
	}
	if releaseProviderResult {
		if _, err := fixture.providerExec.Execute(ctx, proposal, decision); err != nil {
			t.Fatalf("execute and release provider result for %s: %v", proposal.Tool, err)
		}
	} else if _, err := fixture.durable.Execute(ctx, proposal, decision); err != nil {
		t.Fatalf("commit Store operation without releasing provider result for %s: %v", proposal.Tool, err)
	}
	return proposal, decision
}

func TestReplayGovernedTranscriptCrosslinksAllToolsAndEmptyRead(t *testing.T) {
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "replay-response-empty", CallID: "replay-call-empty", Tool: "read", Arguments: json.RawMessage(`{"path":"empty.txt"}`)},
		{ResponseID: "replay-response-read-g0", CallID: "replay-call-read-g0", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "replay-response-write", CallID: "replay-call-write", Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"written\n"}`)},
		{ResponseID: "replay-response-edit", CallID: "replay-call-edit", Tool: "edit", Arguments: json.RawMessage(`{"path":"task.txt","edits":[{"oldText":"written\n","newText":"edited\n"}]}`)},
		{ResponseID: "replay-response-bash", CallID: "replay-call-bash", Tool: "bash", Arguments: json.RawMessage(`{"command":"printf 'bash >><&\\n' >> task.txt"}`)},
		{ResponseID: "replay-response-read-final", CallID: "replay-call-read-final", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	}, true)
	for index := 0; index < 6; index++ {
		executeGovernedReplayTurn(t, fixture, true)
	}
	projection, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
	if err != nil {
		t.Fatalf("replay actual governed Store/provider evidence: %v", err)
	}
	if projection.VerificationScope != "store_crosslinked" || projection.Provider.VerificationScope != providerbridge.ProjectionScopeStoreCrosslink ||
		projection.Provider.ExchangeCount != 6 || projection.Provider.CapturedCallCount != 6 ||
		projection.Provider.DecisionCount != 6 || projection.Provider.ResultCount != 6 ||
		projection.StoreOperationCount != 6 || projection.Provider.Disposition != providerbridge.DispositionAwaitingProviderExchange ||
		projection.Provider.ProviderGenerationID != projection.StoreHeadGeneration ||
		projection.Provider.ProviderTreeDigest != projection.StoreHeadTreeDigest ||
		projection.StoreHeadGeneration != "g3" || !VerifyGovernedTranscriptProjectionCommitment(projection) {
		t.Fatalf("crosslinked projection did not preserve exact counts and separate tips: %+v", projection)
	}
	trace, err := fixture.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	emptyReadVerified := false
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_tool_result" {
			continue
		}
		var evidence providerResultEvidence
		if err := unmarshalGovernedJSON(record.Event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.ToolCallID != "replay-call-empty" {
			continue
		}
		result, err := protocol.DecodeResult(evidence.Result)
		if err != nil {
			t.Fatal(err)
		}
		var summary durableReadSummaryPayload
		if err := decodeGovernedResult(result.Output, &summary); err != nil {
			t.Fatal(err)
		}
		emptyReadVerified = summary.Tool == "read" && summary.Path == "empty.txt" && summary.Size == 0 &&
			summary.Content == "" && summary.ContentDigest == durableContentDigest(nil)
	}
	if !emptyReadVerified {
		t.Fatal("the zero-byte Store read did not produce a crosslinked durable read result")
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{governedReplaySystem, governedReplayDeveloper, "before\n", "written\n", "edited\n", "printf 'bash\\n'"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("sanitized projection exposed transcript or Store content %q", secret)
		}
	}
	tampered := projection
	tampered.StoreHeadGeneration = "g99"
	if VerifyGovernedTranscriptProjectionCommitment(tampered) {
		t.Fatal("outer projection commitment accepted a changed Store tip")
	}
}

func TestReplayGovernedTranscriptPendingStoreSuccessKeepsProviderPredecessor(t *testing.T) {
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "replay-response-pending", CallID: "replay-call-pending", Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"after\n"}`)},
	}, true)
	executeGovernedReplayTurn(t, fixture, false)
	projection, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
	if err != nil {
		t.Fatalf("classify Store-success/provider-result-missing prefix: %v", err)
	}
	if projection.Provider.Disposition != providerbridge.DispositionPendingProviderResult || projection.Provider.ResultCount != 0 ||
		projection.Provider.ProviderGenerationID != fixture.pins.Baseline.ID || projection.Provider.ProviderTreeDigest != fixture.pins.Baseline.TreeDigest ||
		projection.StoreHeadGeneration == projection.Provider.ProviderGenerationID ||
		projection.StoreHeadGeneration != "g1" || projection.VerificationScope != "store_crosslinked" ||
		!VerifyGovernedTranscriptProjectionCommitment(projection) {
		t.Fatalf("pending ALLOW falsely advanced provider state or lost Store tip: %+v", projection)
	}
}

func TestReplayGovernedTranscriptCrosslinksDenyWithoutStoreOperation(t *testing.T) {
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectDeny},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newGovernedReplayFixtureWithPolicy(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "replay-response-deny", CallID: "replay-call-deny", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	}, true, policy)
	turn, err := fixture.conversation.NextTurn(context.Background())
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("capture denied read proposal: %+v, %v", turn, err)
	}
	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
		Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...),
	}
	matched, err := fixture.conversation.Correlate(context.Background(), proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate denied read proposal: %+v, %v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, policy)
	if decision.Verdict != gate.Deny {
		t.Fatalf("fixture did not deny read: %+v", decision)
	}
	if err := fixture.conversation.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatalf("record denied proposal: %v", err)
	}
	projection, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
	if err != nil {
		t.Fatalf("crosslink denied decision to the absence of Store authorization: %v", err)
	}
	if projection.VerificationScope != "store_crosslinked" || projection.Provider.VerificationScope != providerbridge.ProjectionScopeStoreCrosslink ||
		projection.Provider.Disposition != providerbridge.DispositionAwaitingProviderExchange ||
		projection.Provider.ResultCount != 1 || projection.Provider.ProviderGenerationID != fixture.pins.Baseline.ID ||
		projection.Provider.ProviderTreeDigest != fixture.pins.Baseline.TreeDigest || projection.StoreOperationCount != 0 ||
		projection.StoreHeadGeneration != fixture.pins.Baseline.ID || projection.StoreHeadTreeDigest != fixture.pins.Baseline.TreeDigest ||
		!VerifyGovernedTranscriptProjectionCommitment(projection) {
		t.Fatalf("DENY replay advanced Store state or omitted cross-link scope: %+v", projection)
	}
}

type acceptingProviderReplayVerifier struct{}

func (acceptingProviderReplayVerifier) VerifyDecision(providerbridge.ProviderDecisionBinding) error {
	return nil
}
func (acceptingProviderReplayVerifier) VerifyResult(providerbridge.ProviderResultBinding) error {
	return nil
}
func (acceptingProviderReplayVerifier) VerifyAdmissionDenial(providerbridge.ProviderAdmissionDenialBinding) error {
	return nil
}

func TestReplayGovernedTranscriptRejectsSelfConsistentForgedProviderOutput(t *testing.T) {
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "replay-response-tamper", CallID: "replay-call-tamper", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	}, true)
	executeGovernedReplayTurn(t, fixture, true)
	original, err := fixture.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(privateGovernedTestRoot(t), "self-consistent-tamper.audit")
	tamperedJournal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tamperedJournal.Close() })
	tamperedResult := false
	for _, record := range original.Records {
		event := record.Event
		if event.Kind == "provider_tool_result" && record.Sequence == uint64(len(original.Records)) {
			var evidence providerResultEvidence
			if err := unmarshalGovernedJSON(event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			result, err := protocol.DecodeResult(evidence.Result)
			if err != nil {
				t.Fatal(err)
			}
			var summary durableReadSummaryPayload
			if err := decodeGovernedResult(result.Output, &summary); err != nil {
				t.Fatal(err)
			}
			summary.Content = "forged-but-well-formed\n"
			summary.Size = len(summary.Content)
			summary.ContentDigest = durableContentDigest([]byte(summary.Content))
			result.Output, err = json.Marshal(summary)
			if err != nil {
				t.Fatal(err)
			}
			evidence.Result, err = protocol.MarshalResult(result)
			if err != nil {
				t.Fatal(err)
			}
			evidence.ResultDigest = digestBytes(evidence.Result)
			event.Data, err = json.Marshal(evidence)
			if err != nil {
				t.Fatal(err)
			}
			tamperedResult = true
		}
		if _, err := tamperedJournal.Append(event); err != nil {
			t.Fatalf("append self-consistent rehashed event %d: %v", record.Sequence, err)
		}
	}
	if !tamperedResult {
		t.Fatal("fixture did not end at a provider_tool_result to tamper")
	}
	if _, err := providerbridge.ReplayTranscript(tamperedJournal, fixture.pins.Provider, acceptingProviderReplayVerifier{}); err != nil {
		t.Fatalf("tampered output is not internally self-consistent provider evidence: %v", err)
	}
	if _, err := ReplayGovernedTranscript(tamperedJournal, fixture.storeJournal, fixture.store, fixture.pins); err == nil {
		t.Fatal("governed replay accepted a validly rehashed provider read output that differs from Store bytes")
	}
}

func TestReplayGovernedTranscriptAcceptsBaselineOnlyPrefixAndCommitsScope(t *testing.T) {
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "unused-response", CallID: "unused-call", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	}, false)
	projection, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
	if err != nil {
		t.Fatalf("replay callback-free verified baseline prefix: %v", err)
	}
	if projection.VerificationScope != "baseline_crosslinked" || projection.Provider.VerificationScope != providerbridge.ProjectionScopeProviderOnly ||
		projection.Provider.Disposition != providerbridge.DispositionAwaitingHostPrompt ||
		projection.StoreOperationCount != 0 || projection.StoreHeadGeneration != fixture.pins.Baseline.ID ||
		projection.Provider.ProviderGenerationID != fixture.pins.Baseline.ID || !VerifyGovernedTranscriptProjectionCommitment(projection) {
		t.Fatalf("baseline-only prefix falsely claimed operation replay: %+v", projection)
	}
	tampered := projection
	tampered.VerificationScope = "store_crosslinked"
	if VerifyGovernedTranscriptProjectionCommitment(tampered) {
		t.Fatal("outer projection commitment accepted a changed verification scope")
	}
}
