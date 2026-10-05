//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	durableCallIssuer     = "fixture/durable-tool-call/v1"
	durableResponseIssuer = "fixture/durable-response/v1"
	durableCallID         = "call-durable"
	durableResponseID     = "response-durable"
	durableWritePath      = "task.txt"
	durableWriteContent   = "durable-write"
)

var (
	durablePolicyDigest   = "sha256:" + strings.Repeat("1", 64)
	durableMetadataDigest = "sha256:" + strings.Repeat("2", 64)
	durableXattrDigest    = "sha256:" + strings.Repeat("3", 64)
)

// TestDurableDecisionPathEndToEnd wires the DurableExecutor and the
// audit-backed DecisionRecorder into the real cmd/tbound Serve decision path
// over net.Pipe, then verifies the durable settlement and its recovery.
func TestDurableDecisionPathEndToEnd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}

	base := durablePrivateBase(t)
	storePath := filepath.Join(base, "repository")
	auditDir := filepath.Join(base, "audit")
	journalPath := filepath.Join(auditDir, "journal.jsonl")
	durableMkdir(t, storePath, 0o700)
	durableMkdir(t, auditDir, 0o700)

	sourcePath := filepath.Join(base, "source")
	durableMkdir(t, sourcePath, 0o700)
	durableWriteFile(t, filepath.Join(sourcePath, "task.txt"), "baseline\n", 0o644)
	durableMkdir(t, filepath.Join(sourcePath, "build"), 0o755)
	durableWriteFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
	source := durableOpenRoot(t, sourcePath)

	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	authority := &durableReceiptAuthority{
		callIssuer: durableCallIssuer, callID: durableCallID,
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
			return errors.New("the write/edit durable path must not claim command settlement")
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
		t.Fatalf("seed g0: %v", err)
	}
	expected := durableExpectedSnapshot(t)
	seedExpected := durableSeedSnapshot(t)
	if seedExpected.TreeDigest != g0.TreeDigest() {
		t.Fatalf("seed mismatch: g0 actual %s expected fixture %s\nactual manifest %+v\nexpected manifest %+v",
			g0.TreeDigest(), seedExpected.TreeDigest, g0.Manifest().Objects, seedExpected.Manifest.Objects)
	}
	authority.setBinding(g0.TreeDigest(), expected.TreeDigest)

	policy, err := gate.NewPolicy(gate.Profile{
		Version: gate.ProfileVersion,
		Rules:   []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}},
	})
	if err != nil {
		t.Fatal(err)
	}

	arguments := json.RawMessage(`{"path":"` + durableWritePath + `","content":"` + durableWriteContent + `"}`)
	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    durableCallID, Tool: "write", Arguments: arguments,
	}
	canonicalDigest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := protocol.NewStream(durableCallIssuer, durableResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	capture := protocol.TrustedCapture{
		ResponseID: correlation.Identifier{Issuer: durableResponseIssuer, Opaque: durableResponseID},
		ToolCallID: correlation.Identifier{Issuer: durableCallIssuer, Opaque: proposal.ToolCallID},
		ToolName:   proposal.Tool, RawArguments: append(json.RawMessage(nil), proposal.Arguments...),
		CanonicalizationProfile:  protocol.CanonicalizationProfile,
		CanonicalArgumentsDigest: canonicalDigest, Sequence: 1, Generation: "g0",
	}
	if decision := stream.Capture(capture); !decision.Accepted {
		t.Fatalf("capture durable write proposal: %+v", decision)
	}

	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewAuditDecisionRecorder(journal)
	if err != nil {
		t.Fatal(err)
	}

	client, server, err := ipc.NewPipe(integrationToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()

	var mu sync.Mutex
	events := []string{}
	broker := &streamBroker{stream: stream, events: &events, mu: &mu}
	supervisor := &Supervisor{
		IPC: server, Broker: broker, Policy: policy,
		Executor: executor, Decisions: recorder,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(context.Background()) }()

	writeErr := make(chan error, 1)
	go func() { writeErr <- client.SendProposal(proposal) }()
	result, err := client.ReceiveResult()
	if err != nil {
		serveError := <-serveErr
		t.Fatalf("receive durable result: %v; supervisor error: %v; expected output %s observed output %s\nexpected objects %+v\nobserved changes %+v",
			err, serveError, expected.TreeDigest, authority.observedOutput(), expected.Manifest.Objects, authority.observedOutputChanges())
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("supervisor did not shut down cleanly: %v", err)
	}

	// (a) The result is delivered and its output reflects the settlement.
	if result.Verdict != string(gate.Allow) || result.ReasonCode != "policy_rule_allow" ||
		result.CanonicalArgumentsDigest != canonicalDigest || result.ResponseID == nil ||
		result.ResponseID.Issuer != durableResponseIssuer || result.ResponseID.Opaque != durableResponseID {
		t.Fatalf("unexpected durable decision result: %+v", result)
	}
	var summary durableMutationSummaryPayload
	if err := json.Unmarshal(result.Output, &summary); err != nil {
		t.Fatalf("decode durable executor output %q: %v", result.Output, err)
	}
	if summary.Tool != "write" || summary.Generation.ID != "g1" ||
		summary.Generation.TreeDigest != expected.TreeDigest ||
		summary.Transition.ID != "transition-000001" || summary.Transition.Sequence != 1 ||
		!strings.HasPrefix(summary.EffectID, "sessionrepo-effect-") || summary.Outcome != "success" {
		t.Fatalf("unexpected durable settlement summary: %+v", summary)
	}
	if tip := executor.Tip(); tip == nil || tip.ID() != "g1" || tip.TreeDigest() != expected.TreeDigest {
		t.Fatalf("durable executor tip did not advance to the settled g1: %+v", tip)
	}

	// (b) The journal shows the pre-effect gate_decision plus exactly one
	// durable write effect intent and outcome.
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace durable journal: %v", err)
	}
	assertOneAllowGateDecision(t, trace)
	if writeEffects := countWriteEffects(t, trace); writeEffects != 1 {
		t.Fatalf("journal has %d durable write effects; want 1", writeEffects)
	}
	journalFile, err := os.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	verified, verifyErr := audit.Verify(journalFile)
	_ = journalFile.Close()
	if verifyErr != nil {
		t.Fatalf("verify durable journal from disk: %v", verifyErr)
	}
	if len(verified.Records) != len(trace.Records) || len(verified.Effects) != len(trace.Effects) {
		t.Fatalf("on-disk journal does not match the live trace: records %d/%d effects %d/%d",
			len(verified.Records), len(trace.Records), len(verified.Effects), len(trace.Effects))
	}

	// (c) Store verification and evidence are internally consistent.
	chain, err := store.Verify()
	if err != nil {
		t.Fatalf("verify store chain: %v", err)
	}
	if chain.TransitionCount != 1 || chain.BaselineGeneration != "g0" || chain.SealedGeneration != "g1" {
		t.Fatalf("unexpected durable chain: %+v", chain)
	}
	artifact, err := store.Evidence()
	if err != nil {
		t.Fatalf("build store evidence: %v", err)
	}
	if artifact.RealProviderExchange || artifact.PiAdapterWired || artifact.CommandContainmentStatus != "not-established" {
		t.Fatalf("evidence overstates integration or containment: %+v", artifact)
	}
	if artifact.PolicyDigest != durablePolicyDigest || artifact.MetadataPolicyDigest != durableMetadataDigest {
		t.Fatalf("evidence policy commitments = %q / %q", artifact.PolicyDigest, artifact.MetadataPolicyDigest)
	}
	if len(artifact.Generations) != 2 || len(artifact.ApprovedDeltaLedger) != 1 || len(artifact.Operations) != 1 {
		t.Fatalf("evidence is incomplete: generations=%d ledger=%d operations=%d",
			len(artifact.Generations), len(artifact.ApprovedDeltaLedger), len(artifact.Operations))
	}
	if artifact.Operations[0].Tool != "write" || artifact.Operations[0].Outcome != "success" ||
		artifact.Operations[0].OutputGeneration != "g1" || artifact.Operations[0].ResultDigest == "" {
		t.Fatalf("unexpected operation evidence: %+v", artifact.Operations[0])
	}
	if artifact.AuditRecordCount == 0 || artifact.AuditHeadHash == "" {
		t.Fatalf("evidence omits the journal commitment: %+v", artifact)
	}

	// (d) After a full Close, a fresh journal Open plus sessionrepo.Open
	// re-verify the same durable prefix.
	if err := store.Close(); err != nil {
		t.Fatalf("close store before recovery: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("close journal before recovery: %v", err)
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
		t.Fatalf("recovered chain = %+v, err=%v", recoveredChain, err)
	}
	recoveredEvidence, err := recovered.Evidence()
	if err != nil || len(recoveredEvidence.Generations) != 2 ||
		len(recoveredEvidence.ApprovedDeltaLedger) != 1 || len(recoveredEvidence.Operations) != 1 {
		t.Fatalf("recovered evidence is incomplete: evidence=%+v err=%v", recoveredEvidence, err)
	}
	recoveredTrace, err := reopenedJournal.Trace()
	if err != nil {
		t.Fatalf("trace reopened journal: %v", err)
	}
	assertOneAllowGateDecision(t, recoveredTrace)
	if writeEffects := countWriteEffects(t, recoveredTrace); writeEffects != 1 {
		t.Fatalf("reopened journal has %d durable write effects; want 1", writeEffects)
	}
}

// TestDurableDenyDecisionIsRecorded checks that a DENY verdict is also durably
// recorded pre-effect and never reaches the executor.
func TestDurableDenyDecisionIsRecorded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	base := durablePrivateBase(t)
	auditDir := filepath.Join(base, "audit")
	durableMkdir(t, auditDir, 0o700)
	journal, err := audit.Open(filepath.Join(auditDir, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	recorder, err := NewAuditDecisionRecorder(journal)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{
		Version: gate.ProfileVersion,
		Rules:   []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := ipc.NewPipe(integrationToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()

	executed := false
	supervisor := &Supervisor{
		IPC: server, Broker: denyCorrelationBroker{}, Policy: policy,
		Executor: executorFunc(func(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error) {
			executed = true
			return json.RawMessage(`{}`), nil
		}),
		Decisions: recorder,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(context.Background()) }()

	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-denied", Tool: "write",
		Arguments: json.RawMessage(`{"path":"` + durableWritePath + `","content":"` + durableWriteContent + `"}`),
	}
	if err := client.SendProposal(proposal); err != nil {
		t.Fatalf("send proposal: %v", err)
	}
	result, err := client.ReceiveResult()
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != string(gate.Deny) || len(result.Output) != 0 {
		t.Fatalf("unexpected denial result: %+v", result)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("supervisor did not shut down cleanly: %v", err)
	}
	if executed {
		t.Fatal("executor ran for a denied proposal")
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace denial journal: %v", err)
	}
	gateDecisions := 0
	for _, record := range trace.Records {
		if record.Event.Kind != durableDecisionEventKind {
			continue
		}
		gateDecisions++
		var event durableDecisionEvent
		if err := json.Unmarshal(record.Event.Data, &event); err != nil {
			t.Fatalf("decode gate_decision %d: %v", record.Sequence, err)
		}
		if event.Proposal.Tool != "write" || event.Proposal.ToolCallID != "call-denied" ||
			event.Decision.Verdict != gate.Deny || event.Decision.ReasonCode != "correlation_required" {
			t.Fatalf("unexpected denial gate_decision payload: %+v", event)
		}
	}
	if gateDecisions != 1 {
		t.Fatalf("journal has %d gate_decision records; want 1", gateDecisions)
	}
	if len(trace.Effects) != 0 {
		t.Fatalf("denied proposal produced %d audit effects; want 0", len(trace.Effects))
	}
}

// TestDurableEditDecisionPath checks the sessionrepo Edit mapping: a trusted
// ALLOW edit proposal becomes one durable transition on the tip.
func TestDurableEditDecisionPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	base := durablePrivateBase(t)
	storePath := filepath.Join(base, "repository")
	auditDir := filepath.Join(base, "audit")
	durableMkdir(t, storePath, 0o700)
	durableMkdir(t, auditDir, 0o700)
	sourcePath := filepath.Join(base, "source")
	durableMkdir(t, sourcePath, 0o700)
	durableWriteFile(t, filepath.Join(sourcePath, "task.txt"), "baseline-one baseline-two\n", 0o644)
	durableMkdir(t, filepath.Join(sourcePath, "build"), 0o755)
	durableWriteFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
	source := durableOpenRoot(t, sourcePath)

	journal, err := audit.Open(filepath.Join(auditDir, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	authority := &durableReceiptAuthority{
		callIssuer: durableCallIssuer, callID: "call-durable-edit",
		policyDigest: durablePolicyDigest, metadataPolicyDigest: durableMetadataDigest,
	}
	options := sessionrepo.Options{
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		Limits:             workspace.DefaultLimits(),
		XattrVisibility:    workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
		Journal:            journal,
		AuthorizeOperation: authority.authorize,
		VerifyDecision:     authority.verifyDecision,
		VerifySettlement:   func(sessionrepo.CommandSettlement, string, string) error { return nil },
	}
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), options)
	if err != nil {
		t.Fatalf("create session repository: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	expected := durableFixtureSnapshot(t, "durable-edit baseline-two\n", "g1")
	authority.setBinding(g0.TreeDigest(), expected.TreeDigest)

	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"path":"` + durableWritePath + `","edits":[{"oldText":"baseline-one","newText":"durable-edit"}]}`)
	canonicalDigest, err := protocol.CanonicalArgumentsDigest("edit", arguments)
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Decision{
		Verdict: gate.Allow, ReasonCode: "policy_rule_allow",
		PolicyDigest:             "tbound-policy/v1:sha256:" + strings.Repeat("4", 64),
		CanonicalArgumentsDigest: canonicalDigest,
		ToolCallID:               "call-durable-edit", Tool: "edit", Sequence: 1,
		ResponseID: &correlation.Identifier{Issuer: durableResponseIssuer, Opaque: "response-durable-edit"},
	}
	output, err := executor.Execute(context.Background(), protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-durable-edit", Tool: "edit", Arguments: arguments,
	}, decision)
	if err != nil {
		t.Fatalf("durable edit: %v", err)
	}
	var summary durableMutationSummaryPayload
	if err := json.Unmarshal(output, &summary); err != nil {
		t.Fatalf("decode durable edit output %q: %v", output, err)
	}
	if summary.Tool != "edit" || summary.Generation.ID != "g1" ||
		summary.Generation.TreeDigest != expected.TreeDigest ||
		summary.Transition.ID != "transition-000001" || summary.Transition.Sequence != 1 ||
		!strings.HasPrefix(summary.EffectID, "sessionrepo-effect-") || summary.Outcome != "success" {
		t.Fatalf("unexpected durable edit summary: %+v", summary)
	}
	chain, err := store.Verify()
	if err != nil || chain.TransitionCount != 1 || chain.SealedGeneration != "g1" {
		t.Fatalf("durable edit chain = %+v, err=%v", chain, err)
	}
	artifact, err := store.Evidence()
	if err != nil || len(artifact.Operations) != 1 || artifact.Operations[0].Tool != "edit" {
		t.Fatalf("durable edit evidence is inconsistent: evidence=%+v err=%v", artifact, err)
	}
}

// TestDurableExecutorRejectsUnsupportedTool checks that a trusted ALLOW for a
// tool outside the durable read/write/edit/bash slice fails closed instead of
// guessing.
func TestDurableExecutorRejectsUnsupportedTool(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	base := durablePrivateBase(t)
	storePath := filepath.Join(base, "repository")
	auditDir := filepath.Join(base, "audit")
	durableMkdir(t, storePath, 0o700)
	durableMkdir(t, auditDir, 0o700)
	sourcePath := filepath.Join(base, "source")
	durableMkdir(t, sourcePath, 0o700)
	durableWriteFile(t, filepath.Join(sourcePath, "task.txt"), "baseline\n", 0o644)
	source := durableOpenRoot(t, sourcePath)

	journal, err := audit.Open(filepath.Join(auditDir, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	authority := &durableReceiptAuthority{
		callIssuer: durableCallIssuer, callID: durableCallID,
		policyDigest: durablePolicyDigest, metadataPolicyDigest: durableMetadataDigest,
	}
	options := sessionrepo.Options{
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		Limits:             workspace.DefaultLimits(),
		XattrVisibility:    workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
		Journal:            journal,
		AuthorizeOperation: authority.authorize,
		VerifyDecision:     authority.verifyDecision,
		VerifySettlement:   func(sessionrepo.CommandSettlement, string, string) error { return nil },
	}
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), options)
	if err != nil {
		t.Fatalf("create session repository: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	// "grep" is a Pi convenience tool that is deliberately outside the closed
	// read/write/edit/bash surface, so it must never be durable-executed.
	decision := gate.Decision{
		Verdict: gate.Allow, ReasonCode: "policy_rule_allow",
		PolicyDigest: "tbound-policy/v1:sha256:" + strings.Repeat("4", 64),
		ToolCallID:   "call-unsupported", Tool: "grep", Sequence: 1,
		ResponseID: &correlation.Identifier{Issuer: durableResponseIssuer, Opaque: "response-unsupported"},
	}
	if _, err := executor.Execute(context.Background(), protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-unsupported", Tool: "grep", Arguments: json.RawMessage(`{"pattern":"baseline"}`),
	}, decision); err == nil {
		t.Fatal("durable executor accepted an unsupported tool")
	}
	if tip := executor.Tip(); tip == nil || tip.ID() != "g0" {
		t.Fatalf("unsupported tool advanced the tip: %+v", tip)
	}
}

type durableReceiptAuthority struct {
	mu                   sync.Mutex
	callIssuer           string
	callID               string
	policyDigest         string
	metadataPolicyDigest string
	inputTreeDigest      string
	outputTreeDigest     string
	observedOutputDigest string
	observedChanges      []delta.ObjectChange
	bashLeaseID          string
	bashViewID           string
	bashExecutionContext string
}

func (a *durableReceiptAuthority) observedOutput() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.observedOutputDigest
}

func (a *durableReceiptAuthority) observedOutputChanges() []delta.ObjectChange {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]delta.ObjectChange(nil), a.observedChanges...)
}

func (a *durableReceiptAuthority) setBinding(inputTreeDigest, outputTreeDigest string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inputTreeDigest = inputTreeDigest
	a.outputTreeDigest = outputTreeDigest
}

// authorize is the fixture trusted policy authority. It accepts only the exact
// durable write/edit/bash slice the test composes and then remembers the request
// so verifyDecision can bind the transition to it.
func (a *durableReceiptAuthority) authorize(request sessionrepo.OperationRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case request.Tool != "write" && request.Tool != "edit" && request.Tool != "bash":
		return fmt.Errorf("fixture authority only grants the durable write/edit/bash slice, got %q", request.Tool)
	case request.Decision.Outcome != delta.PolicyAllow ||
		request.Decision.PolicyDigest != a.policyDigest ||
		request.Decision.MetadataPolicyDigest != a.metadataPolicyDigest ||
		!strings.HasPrefix(request.Decision.ID, "decision-"):
		return errors.New("decision commitment mismatch")
	case request.InputGeneration != "g0" || request.InputTreeDigest != a.inputTreeDigest:
		return errors.New("request does not name the seeded input generation")
	case !strings.HasPrefix(request.ArgumentDigest, "sha256:"):
		return errors.New("argument digest is not a sha256 commitment")
	case request.EffectID == "":
		return errors.New("request lacks a supervisor effect ID")
	}
	if request.Tool == "bash" {
		switch {
		case request.Operation.Kind != delta.OperationLease:
			return errors.New("fixture bash operation is not a lease")
		case !strings.HasPrefix(request.Operation.LeaseID, "lease-") ||
			request.Operation.ProposalID != "" || request.Operation.CallIssuer != "" || request.Operation.CallID != "":
			return errors.New("fixture bash lease identity is incomplete")
		case request.ViewID == "" || request.ExecutionContextDigest == "":
			return errors.New("fixture bash request lacks command-view context")
		}
		a.bashLeaseID = request.Operation.LeaseID
		a.bashViewID = request.ViewID
		a.bashExecutionContext = request.ExecutionContextDigest
		return nil
	}
	switch {
	case request.Operation.Kind != delta.OperationProposalCall:
		return errors.New("fixture operation is not a proposal call")
	case request.Operation.CallIssuer != a.callIssuer:
		return fmt.Errorf("unexpected call issuer %q", request.Operation.CallIssuer)
	case request.Operation.CallID != a.callID || !strings.HasPrefix(request.Operation.ProposalID, "proposal-"):
		return errors.New("operation call identity is incomplete")
	}
	return nil
}

// verifySettlement accepts the runner's synthetic settlement for the authorized
// bash lease. RunBash has already validated every settlement boolean; this
// fixture additionally proves the receipt names the lease and view the
// authority authorized.
func (a *durableReceiptAuthority) verifySettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case settlement.LeaseID != leaseID || settlement.ViewID != viewID:
		return errors.New("settlement receipt names a different lease or view")
	case leaseID != a.bashLeaseID || viewID != a.bashViewID:
		return errors.New("settlement does not cover the authorized bash request")
	case strings.TrimSpace(settlement.EvidenceClass) == "":
		return errors.New("settlement receipt lacks an evidence class")
	}
	return nil
}

// verifyDecision is the fixture exact-binding verifier. The request it binds
// against is the same one authorize just accepted, so it proves the transition
// covers exactly the authorized tool, arguments, operation, and input tree.
func (a *durableReceiptAuthority) verifyDecision(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observedOutputDigest = binding.OutputTreeDigest
	a.observedChanges = append([]delta.ObjectChange(nil), binding.Changes...)
	if binding.Tool == "bash" {
		switch {
		case binding.ID != "transition-000001" || binding.Sequence != 1:
			return fmt.Errorf("unexpected transition binding %q/%d", binding.ID, binding.Sequence)
		case binding.InputGeneration != "g0" || binding.InputTreeDigest != a.inputTreeDigest:
			return errors.New("binding input generation is not the seeded g0")
		case binding.OutputTreeDigest != a.outputTreeDigest:
			return errors.New("binding output tree digest does not match the expected settled tree")
		case binding.ViewID != a.bashViewID || binding.ExecutionContextDigest != a.bashExecutionContext:
			return errors.New("binding command-view context is not the authorized bash request")
		case decision.Outcome != delta.PolicyAllow || decision.PolicyDigest != a.policyDigest ||
			decision.MetadataPolicyDigest != a.metadataPolicyDigest || !strings.HasPrefix(decision.ID, "decision-"):
			return errors.New("binding decision is not the configured allow decision")
		case binding.Operation.Kind != delta.OperationLease || binding.Operation.LeaseID != a.bashLeaseID ||
			binding.Operation.ProposalID != "" || binding.Operation.CallIssuer != "" || binding.Operation.CallID != "":
			return errors.New("binding operation identity is not the authorized command lease")
		}
		return nil
	}
	switch {
	case (binding.Tool != "write" && binding.Tool != "edit") || binding.ID != "transition-000001" || binding.Sequence != 1:
		return fmt.Errorf("unexpected transition binding %q/%d", binding.ID, binding.Sequence)
	case binding.InputGeneration != "g0" || binding.InputTreeDigest != a.inputTreeDigest:
		return errors.New("binding input generation is not the seeded g0")
	case binding.OutputTreeDigest != a.outputTreeDigest:
		return errors.New("binding output tree digest does not match the expected settled tree")
	case decision.Outcome != delta.PolicyAllow || decision.PolicyDigest != a.policyDigest ||
		decision.MetadataPolicyDigest != a.metadataPolicyDigest || !strings.HasPrefix(decision.ID, "decision-"):
		return errors.New("binding decision is not the configured allow decision")
	case binding.Operation.Kind != delta.OperationProposalCall || binding.Operation.CallIssuer != a.callIssuer ||
		binding.Operation.CallID != a.callID:
		return errors.New("binding operation identity is not the authorized proposal call")
	}
	return nil
}

func assertOneAllowGateDecision(t *testing.T, trace audit.Trace) {
	t.Helper()
	gateDecisions := 0
	for _, record := range trace.Records {
		if record.Event.Kind != durableDecisionEventKind {
			continue
		}
		gateDecisions++
		var event durableDecisionEvent
		if err := json.Unmarshal(record.Event.Data, &event); err != nil {
			t.Fatalf("decode gate_decision %d: %v", record.Sequence, err)
		}
		if event.Proposal.Tool != "write" || event.Proposal.ToolCallID != durableCallID ||
			event.Decision.Verdict != gate.Allow || event.Decision.PolicyDigest == "" {
			t.Fatalf("unexpected gate_decision payload: %+v", event)
		}
	}
	if gateDecisions != 1 {
		t.Fatalf("journal has %d gate_decision records; want 1", gateDecisions)
	}
}

func countWriteEffects(t *testing.T, trace audit.Trace) int {
	t.Helper()
	writeEffects := 0
	for _, effect := range trace.Effects {
		if !strings.Contains(string(effect.Intent), `"tool":"write"`) {
			continue
		}
		writeEffects++
		if effect.Unresolved || effect.Outcome != "success" || effect.IntentSequence == 0 || effect.OutcomeSequence == 0 {
			t.Fatalf("durable write effect is not a resolved success: %+v", effect)
		}
	}
	return writeEffects
}

func durableExpectedSnapshot(t *testing.T) workspace.Snapshot {
	t.Helper()
	return durableFixtureSnapshot(t, durableWriteContent, "g1")
}

func durableSeedSnapshot(t *testing.T) workspace.Snapshot {
	t.Helper()
	return durableFixtureSnapshot(t, "baseline\n", "g0")
}

func durableFixtureSnapshot(t *testing.T, taskContent, generation string) workspace.Snapshot {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "expected")
	durableMkdir(t, rootPath, 0o700)
	durableWriteFile(t, filepath.Join(rootPath, "task.txt"), taskContent, 0o644)
	durableMkdir(t, filepath.Join(rootPath, "build"), 0o755)
	durableWriteFile(t, filepath.Join(rootPath, "build", "result.txt"), "baseline result\n", 0o644)
	root := durableOpenRoot(t, rootPath)
	snapshot, err := workspace.Scan(root, workspace.Options{
		Generation: generation, MetadataPolicyDigest: durableMetadataDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
	})
	if err != nil {
		t.Fatalf("scan expected %s fixture: %v", generation, err)
	}
	return snapshot
}

// durablePrivateBase mirrors sessionrepo's privateTestBase: it requires every
// ancestor to be non-group/other-writable so the audit journal and repository
// stay out of world-writable trees.
func durablePrivateBase(t *testing.T) string {
	t.Helper()
	candidates := make([]string, 0, 2)
	if tmp := os.TempDir(); tmp != "" {
		candidates = append(candidates, tmp)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, home)
	}
	for _, base := range candidates {
		if !durableAncestorsPrivate(base) {
			continue
		}
		directory, err := os.MkdirTemp(base, ".tbound-durable-test-")
		if err != nil {
			continue
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			_ = os.RemoveAll(directory)
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(directory) })
		return directory
	}
	t.Skip("no private base directory with a trusted ancestor chain is available")
	return ""
}

func durableAncestorsPrivate(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for {
		info, err := os.Lstat(absolute)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
			return false
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return true
		}
		absolute = parent
	}
}

func durableMkdir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func durableWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func durableOpenRoot(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}
