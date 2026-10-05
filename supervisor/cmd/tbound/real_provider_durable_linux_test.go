//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	realProviderDurableModel        = "nvidia/nemotron-3.5-lightning:free"
	realProviderDurableWritePath    = "provider-write.txt"
	realProviderDurableWriteContent = "from-real-provider"
	realProviderDurableCallIssuer   = "test/openrouter-durable-call/v1"
	realProviderDurableResponseIss  = "test/openrouter-durable-response/v1"
)

// TestRealProviderDurableLinux sends one real OpenRouter request, then carries
// the captured write call through a Unix IPC socket, broker correlation,
// audit-backed gate decision, and the seeded durable session repository.
func TestRealProviderDurableLinux(t *testing.T) {
	_, keyConfigured := os.LookupEnv(openrouter.APIKeyEnv)
	_, keyFileConfigured := os.LookupEnv(openrouter.APIKeyFileEnv)
	if !keyConfigured && !keyFileConfigured {
		t.Skip("set TBOUND_OPENROUTER_API_KEY or TBOUND_OPENROUTER_API_KEY_FILE to enable the real-provider durable integration")
	}

	credentials, err := openrouter.LoadCredentials()
	if err != nil {
		t.Fatalf("load explicitly configured OpenRouter credentials and model: %v", err)
	}
	if credentials.ModelID() != realProviderDurableModel {
		t.Fatalf("TBOUND_OPENROUTER_MODEL must be %q for this integration", realProviderDurableModel)
	}
	doer, err := credentials.NewHTTPDoer(openrouter.DefaultHTTPTimeout)
	if err != nil {
		t.Fatalf("configure OpenRouter transport: %s", credentials.Redact(err.Error()))
	}
	base := realProviderDurablePrivateBase(t)

	prompt := "Make exactly one tool call with the write tool: write the file provider-write.txt with the exact content from-real-provider. Do not call any other tool."
	providerBroker, err := broker.New(broker.Profile{
		ID: "real-provider-durable-linux-test", Model: credentials.ModelID(),
		Messages:         []openrouter.Message{{Role: openrouter.User, Content: &prompt}},
		ToolManifest:     broker.DeclaredToolManifest(),
		ToolCallIssuer:   realProviderDurableCallIssuer,
		ResponseIDIssuer: realProviderDurableResponseIss,
		Generation:       "g0",
	}, doer)
	if err != nil {
		t.Fatalf("register real-provider profile: %s", credentials.Redact(err.Error()))
	}
	providerContext, cancelProvider := context.WithTimeout(context.Background(), openrouter.MaxHTTPTimeout)
	captured, err := providerBroker.Exchange(providerContext)
	cancelProvider()
	if err != nil {
		t.Fatalf("real OpenRouter exchange failed: %s", credentials.Redact(err.Error()))
	}
	if captured.ResponseID() == "" || captured.Model() != credentials.ModelID() {
		t.Fatal("provider exchange did not return a response for the configured model")
	}
	capturedCall, ok := captured.ToolCall()
	if !ok {
		t.Fatal("provider response did not contain its required single write tool call")
	}
	if capturedCall.ID == "" || capturedCall.Name != "write" {
		t.Fatal("captured provider tool call has no ID or is not the required write tool")
	}
	path, content, err := durableWriteArguments(capturedCall.RawArguments)
	if err != nil || path != realProviderDurableWritePath || string(content) != realProviderDurableWriteContent {
		t.Fatal("captured provider arguments are not the required synthetic write")
	}
	providerRecords := providerBroker.Records()
	if len(providerRecords) != 1 || !providerRecords[0].HasResponse() || providerRecords[0].StatusCode() != 200 ||
		providerRecords[0].Request().Header().Get("Authorization") != "" {
		t.Fatal("provider exchange was not captured once without retaining request authentication")
	}

	// Proposal identity, tool, and arguments come only from the captured call;
	// response identity is retained for assertions from the captured response.
	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    capturedCall.ID,
		Tool:          capturedCall.Name,
		Arguments:     append(json.RawMessage(nil), capturedCall.RawArguments...),
	}
	capturedCanonicalDigest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
	if err != nil || capturedCanonicalDigest != capturedCall.CanonicalArgumentsDigest {
		t.Fatal("captured provider arguments do not retain their canonical digest")
	}

	storePath := filepath.Join(base, "repository")
	auditDir := filepath.Join(base, "audit")
	journalPath := filepath.Join(auditDir, "journal.jsonl")
	durableMkdir(t, storePath, 0o700)
	durableMkdir(t, auditDir, 0o700)
	sourcePath := filepath.Join(base, "source")
	durableMkdir(t, sourcePath, 0o700)
	durableWriteFile(t, filepath.Join(sourcePath, "seed.txt"), "baseline\n", 0o644)
	source := durableOpenRoot(t, sourcePath)

	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	authority := &durableReceiptAuthority{
		callIssuer: realProviderDurableCallIssuer, callID: capturedCall.ID,
		policyDigest: durablePolicyDigest, metadataPolicyDigest: durableMetadataDigest,
	}
	options := sessionrepo.Options{
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		Limits:             workspace.DefaultLimits(),
		XattrVisibility:    workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
		Journal:            journal,
		AuthorizeOperation: authority.authorize,
		VerifyDecision:     authority.verifyDecision,
		VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error {
			return errors.New("the write-only integration must not claim command settlement")
		},
	}
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), options)
	if err != nil {
		_ = journal.Close()
		t.Fatalf("create session repository: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = journal.Close()
	})
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed session repository: %v", err)
	}
	expectedG0 := realProviderDurableSnapshot(t, "g0", false)
	if expectedG0.TreeDigest != g0.TreeDigest() {
		t.Fatal("seeded generation did not match its independently scanned fixture")
	}
	expectedG1 := realProviderDurableSnapshot(t, "g1", true)
	authority.setBinding(g0.TreeDigest(), expectedG1.TreeDigest)

	policy, err := gate.NewPolicy(gate.Profile{
		Version: gate.ProfileVersion,
		Rules:   []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}},
	})
	if err != nil {
		t.Fatalf("compile write policy: %v", err)
	}
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: realProviderDurableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatalf("configure durable executor: %v", err)
	}
	recorder, err := NewAuditDecisionRecorder(journal)
	if err != nil {
		t.Fatalf("configure audit decision recorder: %v", err)
	}

	// Exercise the actual Linux Unix-domain IPC implementation, not net.Pipe.
	token, err := ipc.NewBindingToken()
	if err != nil {
		t.Fatalf("create IPC binding token: %v", err)
	}
	socketPath := filepath.Join(base, "provider.sock")
	listener, err := ipc.ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("listen on Unix IPC socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := ipc.DialUnix(socketPath, token)
	if err != nil {
		t.Fatalf("dial Unix IPC socket: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept(token)
	if err != nil {
		t.Fatalf("accept Unix IPC connection: %v", err)
	}
	defer server.Close()
	var transcript bytes.Buffer
	supervisor := &Supervisor{
		IPC: server, Broker: providerBroker, Policy: policy, Executor: executor,
		Decisions: recorder, Transcript: &transcript, ProposalLimit: 1,
	}
	sessionContext, cancelSession := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSession()
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(sessionContext) }()
	if err := client.SendProposal(proposal); err != nil {
		t.Fatalf("send captured proposal over Unix IPC: %v", err)
	}
	result, err := client.ReceiveResult()
	if err != nil {
		_ = client.Close()
		serveError := <-serveErr
		t.Fatalf("receive durable provider result: %v; supervisor: %v", err, serveError)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close Unix IPC client: %v", err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("supervisor did not finish cleanly: %v", err)
	}
	if result.Verdict != string(gate.Allow) || result.ReasonCode != "policy_rule_allow" ||
		result.ToolCallID != capturedCall.ID || result.Tool != capturedCall.Name || result.Sequence != 1 ||
		result.CanonicalArgumentsDigest != capturedCanonicalDigest || result.ResponseID == nil ||
		result.ResponseID.Opaque != captured.ResponseID() || result.ResponseID.Issuer != realProviderDurableResponseIss ||
		result.PolicyDigest != policy.Digest() || len(result.Output) == 0 {
		t.Fatal("Unix IPC did not deliver the correlated, audit-backed durable allow result")
	}
	var summary durableMutationSummaryPayload
	if err := json.Unmarshal(result.Output, &summary); err != nil {
		t.Fatalf("decode durable result summary: %v", err)
	}
	if summary.Tool != "write" || summary.Generation.ID != "g1" ||
		summary.Generation.TreeDigest != expectedG1.TreeDigest ||
		summary.Transition.ID != "transition-000001" || summary.Transition.Sequence != 1 ||
		!strings.HasPrefix(summary.EffectID, "sessionrepo-effect-") || summary.Outcome != "success" {
		t.Fatal("durable executor did not return the expected committed write summary")
	}
	if tip := executor.Tip(); tip == nil || tip.ID() != "g1" || tip.TreeDigest() != expectedG1.TreeDigest {
		t.Fatal("durable executor tip did not advance to the expected sealed generation")
	}
	assertRealProviderPromotedFile(t, storePath, realProviderDurableWriteContent)

	var transcriptEntryValue transcriptEntry
	if err := json.Unmarshal(bytes.TrimSpace(transcript.Bytes()), &transcriptEntryValue); err != nil {
		t.Fatalf("decode broker-to-gate transcript: %v", err)
	}
	correlation := transcriptEntryValue.Correlation
	if transcriptEntryValue.Proposal.ToolCallID != capturedCall.ID || transcriptEntryValue.Proposal.Tool != "write" ||
		string(transcriptEntryValue.Proposal.Arguments) != string(capturedCall.RawArguments) ||
		!correlation.Accepted || correlation.ReasonCode != "matched" || correlation.Proposal == nil ||
		correlation.BrokerCapture == nil || correlation.BrokerCapture.ToolCallID.Opaque != capturedCall.ID ||
		correlation.BrokerCapture.ResponseID.Opaque != captured.ResponseID() ||
		correlation.Proposal.CanonicalArgumentsDigest != capturedCanonicalDigest ||
		transcriptEntryValue.Decision.Verdict != gate.Allow ||
		!reflect.DeepEqual(transcriptEntryValue.Result, result) {
		t.Fatal("real broker correlation did not bind the IPC proposal to the captured provider call")
	}

	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace durable audit journal: %v", err)
	}
	assertRealProviderDurableAudit(t, trace, proposal, captured.ResponseID(), capturedCanonicalDigest, policy.Digest())
	if effects := countWriteEffects(t, trace); effects != 1 {
		t.Fatalf("audit journal contains %d durable write effects; want exactly one", effects)
	}
	chain, err := store.Verify()
	if err != nil || chain.TransitionCount != 1 || chain.BaselineGeneration != "g0" || chain.SealedGeneration != "g1" {
		t.Fatalf("session repository verification failed: chain=%+v err=%v", chain, err)
	}
	evidence, err := store.Evidence()
	if err != nil {
		t.Fatalf("build session repository evidence: %v", err)
	}
	proposalIdentity, durableResultDigest, sessionrepoArgumentDigest := assertRealProviderDurableEvidence(
		t, evidence, trace, transcriptEntryValue, summary, g0, expectedG1, capturedCanonicalDigest,
	)
	journalFile, err := os.Open(journalPath)
	if err != nil {
		t.Fatalf("open audit journal for verification: %v", err)
	}
	verified, verifyErr := audit.Verify(journalFile)
	_ = journalFile.Close()
	if verifyErr != nil || len(verified.Records) != len(trace.Records) || len(verified.Effects) != len(trace.Effects) {
		t.Fatalf("on-disk audit verification did not match the live trace: err=%v", verifyErr)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close session repository before recovery: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("close audit journal before recovery: %v", err)
	}
	reopenedJournal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen audit journal: %v", err)
	}
	reopenOptions := options
	reopenOptions.Journal = reopenedJournal
	recovered, err := sessionrepo.Open(durableOpenRoot(t, storePath), reopenOptions)
	if err != nil {
		_ = reopenedJournal.Close()
		t.Fatalf("reopen session repository: %v", err)
	}
	t.Cleanup(func() {
		_ = recovered.Close()
		_ = reopenedJournal.Close()
	})
	recoveredChain, err := recovered.Verify()
	if err != nil || recoveredChain.TransitionCount != 1 || recoveredChain.SealedGeneration != "g1" {
		t.Fatalf("reopened session repository did not verify the committed prefix: chain=%+v err=%v", recoveredChain, err)
	}
	assertRealProviderPromotedFile(t, storePath, realProviderDurableWriteContent)
	reopenedTrace, err := reopenedJournal.Trace()
	if err != nil {
		t.Fatalf("trace reopened audit journal: %v", err)
	}
	assertRealProviderDurableAudit(t, reopenedTrace, proposal, captured.ResponseID(), capturedCanonicalDigest, policy.Digest())
	if effects := countWriteEffects(t, reopenedTrace); effects != 1 {
		t.Fatalf("reopened audit journal contains %d durable write effects; want exactly one", effects)
	}
	recoveredEvidence, err := recovered.Evidence()
	if err != nil {
		t.Fatalf("build recovered session repository evidence: %v", err)
	}
	recoveredProposalIdentity, recoveredResultDigest, recoveredSessionrepoDigest := assertRealProviderDurableEvidence(
		t, recoveredEvidence, reopenedTrace, transcriptEntryValue, summary, g0, expectedG1, capturedCanonicalDigest,
	)
	if recoveredProposalIdentity != proposalIdentity || recoveredResultDigest != durableResultDigest ||
		recoveredSessionrepoDigest != sessionrepoArgumentDigest ||
		recoveredEvidence.AuditRecordCount != evidence.AuditRecordCount || recoveredEvidence.AuditHeadHash != evidence.AuditHeadHash ||
		!reflect.DeepEqual(recoveredEvidence.ApprovedDeltaLedger, evidence.ApprovedDeltaLedger) ||
		!reflect.DeepEqual(recoveredEvidence.Operations, evidence.Operations) {
		t.Fatal("reopened repository evidence differs from the verified durable evidence")
	}
	logRealProviderDurableSummary(t, credentials, captured, capturedCall, proposalIdentity.ProposalID,
		capturedCanonicalDigest, sessionrepoArgumentDigest, summary, recoveredEvidence, recoveredResultDigest)
}

// TestSyntheticDurableEvidenceAssertionsLinux exercises the evidence assertions
// against a real seeded store and audit journal without contacting a provider.
// Its explicitly synthetic call identity is not used by the real-provider test.
func TestSyntheticDurableEvidenceAssertionsLinux(t *testing.T) {
	const (
		syntheticCallID     = "synthetic-evidence-call-001"
		syntheticResponseID = "synthetic-evidence-response-001"
	)
	base := realProviderDurablePrivateBase(t)
	storePath := filepath.Join(base, "repository")
	auditDir := filepath.Join(base, "audit")
	durableMkdir(t, storePath, 0o700)
	durableMkdir(t, auditDir, 0o700)
	sourcePath := filepath.Join(base, "source")
	durableMkdir(t, sourcePath, 0o700)
	durableWriteFile(t, filepath.Join(sourcePath, "seed.txt"), "baseline\n", 0o644)
	source := durableOpenRoot(t, sourcePath)

	journal, err := audit.Open(filepath.Join(auditDir, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open synthetic audit journal: %v", err)
	}
	authority := &durableReceiptAuthority{
		callIssuer: realProviderDurableCallIssuer, callID: syntheticCallID,
		policyDigest: durablePolicyDigest, metadataPolicyDigest: durableMetadataDigest,
	}
	options := sessionrepo.Options{
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		Limits:             workspace.DefaultLimits(),
		XattrVisibility:    workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
		Journal:            journal,
		AuthorizeOperation: authority.authorize,
		VerifyDecision:     authority.verifyDecision,
		VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error {
			return errors.New("synthetic write fixture does not perform command settlement")
		},
	}
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), options)
	if err != nil {
		_ = journal.Close()
		t.Fatalf("create synthetic session repository: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = journal.Close()
	})
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed synthetic session repository: %v", err)
	}
	expectedG0 := realProviderDurableSnapshot(t, "g0", false)
	if g0.TreeDigest() != expectedG0.TreeDigest {
		t.Fatal("synthetic fixture seed does not match its expected tree")
	}
	expectedG1 := realProviderDurableSnapshot(t, "g1", true)
	authority.setBinding(g0.TreeDigest(), expectedG1.TreeDigest)

	arguments, err := json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{realProviderDurableWritePath, realProviderDurableWriteContent})
	if err != nil {
		t.Fatalf("encode synthetic captured arguments: %v", err)
	}
	capturedCanonicalDigest, err := protocol.CanonicalArgumentsDigest("write", arguments)
	if err != nil {
		t.Fatalf("digest synthetic captured arguments: %v", err)
	}
	stream, err := protocol.NewStream(realProviderDurableCallIssuer, realProviderDurableResponseIss)
	if err != nil {
		t.Fatalf("create synthetic correlation stream: %v", err)
	}
	captureDecision := stream.Capture(protocol.TrustedCapture{
		ResponseID: correlation.Identifier{Issuer: realProviderDurableResponseIss, Opaque: syntheticResponseID},
		ToolCallID: correlation.Identifier{Issuer: realProviderDurableCallIssuer, Opaque: syntheticCallID},
		ToolName:   "write", RawArguments: append(json.RawMessage(nil), arguments...),
		CanonicalizationProfile:  protocol.CanonicalizationProfile,
		CanonicalArgumentsDigest: capturedCanonicalDigest, Sequence: 1, Generation: "g0",
	})
	if !captureDecision.Accepted {
		t.Fatal("synthetic provider-call capture was rejected")
	}
	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    syntheticCallID,
		Tool:          "write",
		Arguments:     append(json.RawMessage(nil), arguments...),
	}
	policy, err := gate.NewPolicy(gate.Profile{
		Version: gate.ProfileVersion,
		Rules:   []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}},
	})
	if err != nil {
		t.Fatalf("compile synthetic write policy: %v", err)
	}
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: realProviderDurableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatalf("configure synthetic durable executor: %v", err)
	}
	recorder, err := NewAuditDecisionRecorder(journal)
	if err != nil {
		t.Fatalf("configure synthetic audit decision recorder: %v", err)
	}
	client, server, err := ipc.NewPipe(integrationToken)
	if err != nil {
		t.Fatalf("create synthetic IPC pipe: %v", err)
	}
	defer client.Close()
	defer server.Close()
	var eventMu sync.Mutex
	events := []string{}
	var transcript bytes.Buffer
	supervisor := &Supervisor{
		IPC: server, Broker: &streamBroker{stream: stream, events: &events, mu: &eventMu},
		Policy: policy, Executor: executor, Decisions: recorder,
		Transcript: &transcript, ProposalLimit: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(ctx) }()
	if err := client.SendProposal(proposal); err != nil {
		t.Fatalf("send synthetic proposal: %v", err)
	}
	result, err := client.ReceiveResult()
	if err != nil {
		t.Fatalf("receive synthetic durable result: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close synthetic IPC client: %v", err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("synthetic supervisor did not finish cleanly: %v", err)
	}
	if result.Verdict != string(gate.Allow) || result.ToolCallID != syntheticCallID ||
		result.CanonicalArgumentsDigest != capturedCanonicalDigest || result.ResponseID == nil ||
		result.ResponseID.Opaque != syntheticResponseID || len(result.Output) == 0 {
		t.Fatal("synthetic supervisor did not deliver the captured-call durable result")
	}
	var mutationSummary durableMutationSummaryPayload
	if err := json.Unmarshal(result.Output, &mutationSummary); err != nil || mutationSummary.Outcome != "success" || mutationSummary.Tool != "write" {
		t.Fatal("synthetic durable executor did not return a successful write summary")
	}
	var transcriptEntryValue transcriptEntry
	if err := json.Unmarshal(bytes.TrimSpace(transcript.Bytes()), &transcriptEntryValue); err != nil ||
		!reflect.DeepEqual(transcriptEntryValue.Result, result) {
		t.Fatal("synthetic transcript does not preserve the delivered supervisor result")
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace synthetic audit journal: %v", err)
	}
	assertRealProviderDurableAudit(t, trace, proposal, syntheticResponseID, capturedCanonicalDigest, policy.Digest())
	if _, err := store.Verify(); err != nil {
		t.Fatalf("verify synthetic session repository: %v", err)
	}
	evidence, err := store.Evidence()
	if err != nil {
		t.Fatalf("build synthetic session repository evidence: %v", err)
	}
	_, _, normalizedArgumentDigest := assertRealProviderDurableEvidence(
		t, evidence, trace, transcriptEntryValue, mutationSummary, g0, expectedG1, capturedCanonicalDigest,
	)
	if normalizedArgumentDigest == capturedCanonicalDigest {
		t.Fatal("synthetic fixture did not distinguish captured and sessionrepo argument digests")
	}
	assertRealProviderPromotedFile(t, storePath, realProviderDurableWriteContent)
}

func realProviderDurablePrivateBase(t *testing.T) string {
	t.Helper()
	candidates := make([]string, 0, 2)
	if temp := os.TempDir(); temp != "" {
		candidates = append(candidates, temp)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, home)
	}
	for _, candidate := range candidates {
		if !durableAncestorsPrivate(candidate) {
			continue
		}
		info, err := os.Lstat(candidate)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			continue
		}
		ownedDir, err := os.MkdirTemp(candidate, ".tbound-real-provider-durable-")
		if err != nil {
			continue
		}
		if err := os.Chmod(ownedDir, 0o700); err != nil {
			_ = os.RemoveAll(ownedDir)
			continue
		}
		createdInfo, err := os.Lstat(ownedDir)
		if err != nil || !createdInfo.IsDir() || createdInfo.Mode()&os.ModeSymlink != 0 || createdInfo.Mode().Perm() != 0o700 {
			_ = os.RemoveAll(ownedDir)
			continue
		}
		createdStat, ok := createdInfo.Sys().(*syscall.Stat_t)
		if !ok || createdStat.Uid != uint32(os.Geteuid()) {
			_ = os.RemoveAll(ownedDir)
			continue
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(ownedDir); err != nil {
				t.Errorf("remove owned private test directory: %v", err)
			}
		})
		return ownedDir
	}
	t.Fatal("no current-user-owned private test base with a trusted ancestor chain is available")
	return ""
}

func realProviderDurableSnapshot(t *testing.T, generation string, includeWrite bool) workspace.Snapshot {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "expected")
	durableMkdir(t, rootPath, 0o700)
	durableWriteFile(t, filepath.Join(rootPath, "seed.txt"), "baseline\n", 0o644)
	if includeWrite {
		durableWriteFile(t, filepath.Join(rootPath, realProviderDurableWritePath), realProviderDurableWriteContent, 0o644)
	}
	root := durableOpenRoot(t, rootPath)
	snapshot, err := workspace.Scan(root, workspace.Options{
		Generation: generation, MetadataPolicyDigest: durableMetadataDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
	})
	if err != nil {
		t.Fatalf("scan expected %s generation: %v", generation, err)
	}
	return snapshot
}

func assertRealProviderPromotedFile(t *testing.T, storePath, want string) {
	t.Helper()
	path := filepath.Join(storePath, "generations", "g1", realProviderDurableWritePath)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("durable write was not promoted as a regular file in the sealed generation")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != want {
		t.Fatal("promoted sealed file content did not match the captured provider arguments")
	}
}

func assertRealProviderDurableAudit(t *testing.T, trace audit.Trace, proposal protocol.Proposal, responseID, capturedCanonicalDigest, policyDigest string) {
	t.Helper()
	var gateSequence uint64
	gateCount := 0
	for _, record := range trace.Records {
		if record.Event.Kind != durableDecisionEventKind {
			continue
		}
		gateCount++
		gateSequence = record.Sequence
		var event durableDecisionEvent
		if err := json.Unmarshal(record.Event.Data, &event); err != nil {
			t.Fatalf("decode durable gate decision: %v", err)
		}
		if event.Proposal.ToolCallID != proposal.ToolCallID || event.Proposal.Tool != "write" ||
			string(event.Proposal.Arguments) != string(proposal.Arguments) ||
			event.Decision.Verdict != gate.Allow || event.Decision.ReasonCode != "policy_rule_allow" ||
			event.Decision.ResponseID == nil || event.Decision.ResponseID.Opaque != responseID ||
			event.Decision.CanonicalArgumentsDigest != capturedCanonicalDigest || event.Decision.PolicyDigest != policyDigest {
			t.Fatal("audit gate decision did not preserve the captured call and allow outcome")
		}
	}
	if gateCount != 1 || gateSequence == 0 {
		t.Fatalf("audit journal has %d gate decisions; want exactly one", gateCount)
	}
	writeEffects := 0
	for _, effect := range trace.Effects {
		if !strings.Contains(string(effect.Intent), `"tool":"write"`) {
			continue
		}
		writeEffects++
		if effect.Unresolved || effect.Outcome != "success" || effect.IntentSequence <= gateSequence ||
			effect.OutcomeSequence <= effect.IntentSequence {
			t.Fatal("audit order must be gate decision, durable write intent, then successful outcome")
		}
	}
	if writeEffects != 1 {
		t.Fatalf("audit journal has %d write intents; want exactly one", writeEffects)
	}
}

func assertRealProviderDurableEvidence(
	t *testing.T,
	evidence sessionrepo.EvidenceArtifact,
	trace audit.Trace,
	transcript transcriptEntry,
	result durableMutationSummaryPayload,
	g0 *sessionrepo.Generation,
	expectedG1 workspace.Snapshot,
	capturedCanonicalDigest string,
) (delta.OperationIdentity, string, string) {
	t.Helper()
	proposal := transcript.Proposal
	decision := transcript.Decision
	operation, err := durableOperationIdentity(proposal, decision, realProviderDurableCallIssuer)
	if err != nil {
		t.Fatalf("derive durable operation identity from the delivered gate decision: %v", err)
	}
	durableDecision, err := durablePolicyDecision(proposal, decision, durablePolicyDigest, durableMetadataDigest)
	if err != nil {
		t.Fatalf("derive durable policy receipt from the delivered gate decision: %v", err)
	}
	if operation.Kind != delta.OperationProposalCall || operation.ProposalID == "" ||
		operation.CallIssuer != realProviderDurableCallIssuer || operation.CallID != proposal.ToolCallID ||
		proposal.ToolCallID == "" || proposal.Tool != "write" ||
		decision.Verdict != gate.Allow || decision.ToolCallID != proposal.ToolCallID || decision.Tool != proposal.Tool ||
		decision.ResponseID == nil || decision.Sequence == 0 {
		t.Fatal("delivered gate decision did not yield a complete captured-call operation identity")
	}
	path, content, err := durableWriteArguments(proposal.Arguments)
	if err != nil || path != realProviderDurableWritePath || string(content) != realProviderDurableWriteContent {
		t.Fatal("delivered proposal is not the expected exact write")
	}
	actualCapturedDigest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
	if err != nil || actualCapturedDigest != capturedCanonicalDigest || decision.CanonicalArgumentsDigest != capturedCanonicalDigest {
		t.Fatal("gate evidence does not retain the captured canonical proposal-argument digest")
	}
	sessionrepoArgumentDigest, err := realProviderSessionrepoWriteArgumentDigest(path, content)
	if err != nil || sessionrepoArgumentDigest == capturedCanonicalDigest {
		t.Fatal("session repository write digest was not independently derived in its normalized namespace")
	}
	if evidence.RealProviderExchange || evidence.PiAdapterWired || evidence.CommandContainmentStatus != "not-established" {
		t.Fatal("session repository evidence overstates provider, Pi, or containment guarantees")
	}
	if evidence.AuditRecordCount != uint64(len(trace.Records)) || len(trace.Records) == 0 ||
		evidence.AuditHeadHash != trace.Records[len(trace.Records)-1].Hash || evidence.AuditHeadHash == "" {
		t.Fatal("session repository evidence does not match the verified audit head")
	}
	if len(evidence.Generations) != 2 || evidence.Generations[0].ID != "g0" ||
		evidence.Generations[0].TreeDigest != g0.TreeDigest() || evidence.Generations[1].ID != "g1" ||
		evidence.Generations[1].TreeDigest != expectedG1.TreeDigest || len(evidence.ApprovedDeltaLedger) != 1 ||
		len(evidence.Operations) != 1 {
		t.Fatal("session repository evidence is missing the seeded generation, approved transition, or operation")
	}
	transition := evidence.ApprovedDeltaLedger[0]
	if transition.Tool != "write" || transition.ArgumentDigest != sessionrepoArgumentDigest ||
		transition.InputGeneration != "g0" || transition.OutputGeneration != "g1" ||
		transition.InputTreeDigest != g0.TreeDigest() || transition.OutputTreeDigest != expectedG1.TreeDigest ||
		transition.Operation != operation || transition.Decision != durableDecision || len(transition.Changes) != 1 {
		t.Fatal("approved delta ledger does not bind the captured call, delivered decision, and sealed tree")
	}
	change := transition.Changes[0]
	if change.Path != realProviderDurableWritePath || change.Before.Exists || !change.After.Exists ||
		change.After.Type != delta.ObjectRegular || change.After.ContentDigest != durableContentDigest([]byte(realProviderDurableWriteContent)) {
		t.Fatal("approved delta ledger does not bind the captured write's exact file change")
	}
	operationEvidence := evidence.Operations[0]
	if operationEvidence.Tool != "write" || operationEvidence.Operation != operation ||
		operationEvidence.DecisionID != durableDecision.ID || operationEvidence.ArgumentDigest != sessionrepoArgumentDigest ||
		operationEvidence.EffectID != result.EffectID || operationEvidence.Outcome != "success" ||
		operationEvidence.InputGeneration != "g0" || operationEvidence.InputTreeDigest != g0.TreeDigest() ||
		operationEvidence.OutputGeneration != "g1" || operationEvidence.OutputTreeDigest != expectedG1.TreeDigest ||
		operationEvidence.ResultDigest == "" || !strings.HasPrefix(operationEvidence.ResultDigest, "sha256:") {
		t.Fatal("operation evidence does not bind the captured call and successful durable write")
	}
	var durableEffect *audit.EffectTrace
	for index := range trace.Effects {
		if trace.Effects[index].ID == result.EffectID {
			durableEffect = &trace.Effects[index]
			break
		}
	}
	if durableEffect == nil || durableEffect.Unresolved || durableEffect.Outcome != "success" ||
		durableEffect.IntentSequence >= durableEffect.OutcomeSequence ||
		operationEvidence.AuditSequence != durableEffect.OutcomeSequence ||
		operationEvidence.ResultDigest != durableContentDigest(durableEffect.Result) {
		t.Fatal("effect ID or result digest does not match the successful audit outcome")
	}
	var operationRequest sessionrepo.OperationRequest
	if err := json.Unmarshal(durableEffect.Intent, &operationRequest); err != nil ||
		operationRequest.Tool != "write" || operationRequest.Operation != operation ||
		operationRequest.ArgumentDigest != sessionrepoArgumentDigest || operationRequest.EffectID != result.EffectID {
		t.Fatal("durable effect intent does not preserve the normalized write operation binding")
	}
	return operation, operationEvidence.ResultDigest, sessionrepoArgumentDigest
}

// realProviderSessionrepoWriteArgumentDigest mirrors sessionrepo.Write's
// typed, normalized argumentsDigest input; it is intentionally distinct from
// the RFC 8785 digest of the captured Pi/provider arguments.
func realProviderSessionrepoWriteArgumentDigest(path string, content []byte) (string, error) {
	encoded, err := json.Marshal(struct {
		Path          string `json:"path"`
		ContentDigest string `json:"content_digest"`
		ContentBytes  int    `json:"content_bytes"`
	}{path, durableContentDigest(content), len(content)})
	if err != nil {
		return "", err
	}
	return durableContentDigest(encoded), nil
}

func logRealProviderDurableSummary(
	t *testing.T,
	credentials openrouter.Credentials,
	captured openrouter.CapturedResponse,
	capturedCall openrouter.ToolCall,
	proposalID, capturedCanonicalDigest, sessionrepoArgumentDigest string,
	result durableMutationSummaryPayload,
	evidence sessionrepo.EvidenceArtifact,
	resultDigest string,
) {
	t.Helper()
	safe := credentials.Redact
	entry := struct {
		Model                              string `json:"model"`
		ResponseID                         string `json:"response_id"`
		CallID                             string `json:"call_id"`
		ProposalID                         string `json:"proposal_id"`
		CapturedCanonicalArgumentsDigest   string `json:"captured_canonical_arguments_digest"`
		SessionrepoOperationArgumentDigest string `json:"sessionrepo_operation_argument_digest"`
		EffectID                           string `json:"effect_id"`
		JournalHeadHash                    string `json:"journal_head_hash"`
		JournalRecordCount                 uint64 `json:"journal_record_count"`
		SealedTreeDigest                   string `json:"sealed_tree_digest"`
		DurableResultDigest                string `json:"durable_result_digest"`
		ProviderExchangeSucceeded          bool   `json:"provider_exchange_succeeded"`
		UnixIPCResultDelivered             bool   `json:"unix_ipc_result_delivered"`
		BrokerCorrelationSucceeded         bool   `json:"broker_correlation_succeeded"`
		GateAuditOrdered                   bool   `json:"gate_audit_ordered"`
		AuditChainVerified                 bool   `json:"audit_chain_verified"`
		DurableWriteSucceeded              bool   `json:"durable_write_succeeded"`
		StoreVerified                      bool   `json:"store_verified"`
		ReopenVerified                     bool   `json:"reopen_verified"`
		SessionrepoProviderExchangeClaimed bool   `json:"sessionrepo_provider_exchange_claimed"`
		CommandContainmentStatus           string `json:"command_containment_status"`
		G1DurabilityEstablished            bool   `json:"g1_durability_established"`
		PiAdapterWired                     bool   `json:"pi_adapter_wired"`
	}{
		Model: safe(credentials.ModelID()), ResponseID: safe(captured.ResponseID()),
		CallID: safe(capturedCall.ID), ProposalID: safe(proposalID),
		CapturedCanonicalArgumentsDigest:   safe(capturedCanonicalDigest),
		SessionrepoOperationArgumentDigest: safe(sessionrepoArgumentDigest),
		EffectID:                           safe(result.EffectID),
		JournalHeadHash:                    safe(evidence.AuditHeadHash), JournalRecordCount: evidence.AuditRecordCount,
		SealedTreeDigest:    safe(evidence.Generations[len(evidence.Generations)-1].TreeDigest),
		DurableResultDigest: safe(resultDigest), ProviderExchangeSucceeded: true,
		UnixIPCResultDelivered: true, BrokerCorrelationSucceeded: true, GateAuditOrdered: true,
		AuditChainVerified: true, DurableWriteSucceeded: result.Outcome == "success",
		StoreVerified: true, ReopenVerified: true,
		SessionrepoProviderExchangeClaimed: evidence.RealProviderExchange,
		CommandContainmentStatus:           "not-established", G1DurabilityEstablished: false,
		PiAdapterWired: evidence.PiAdapterWired,
	}
	encoded, err := json.Marshal(entry)
	if err != nil || len(encoded) > 4096 {
		t.Fatal("real-provider summary could not be encoded within its bound")
	}
	t.Logf("real_provider_durable_summary=%s", encoded)
}
