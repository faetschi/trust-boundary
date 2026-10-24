//go:build linux

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

const (
	workflowVerticalID           = "workflow-vertical-fresh"
	workflowVerticalSessionID    = "session-vertical-fresh"
	workflowVerticalConversation = "conversation-vertical-fresh"
	workflowVerticalProfile      = "profile-vertical-fresh"
	workflowVerticalTaskID       = "task-vertical-fresh"
	workflowVerticalPrompt       = "Read the pinned fixture file twice."
	workflowVerticalBashOutput   = "out.txt"
)

type verticalFreshWorkflowPolicy struct {
	workflow    string
	deadline    time.Time
	root        string
	incarnation workflowrecovery.OwnerIncarnation
	store       workflowrecovery.OwnerStoreIdentity
	calls       int
}

func (p *verticalFreshWorkflowPolicy) AuthorizeFreshWorkflow(ctx context.Context, workflow string, epoch uint64,
	initialDigest string, incarnation workflowrecovery.OwnerIncarnation, deadline time.Time,
	root string, store workflowrecovery.OwnerStoreIdentity) error {
	if p == nil || ctx == nil || ctx.Err() != nil || p.calls != 0 || workflow != p.workflow || epoch != 1 ||
		!isVerticalDigest(initialDigest) || !reflect.DeepEqual(incarnation, p.incarnation) ||
		!deadline.Equal(p.deadline) || deadline.Location() != time.UTC || root != p.root || store != p.store {
		return workflowrecovery.ErrFreshAuthorization
	}
	p.calls++
	return nil
}

type verticalNoPriorOwnerDrain struct{}

// verticalNoPriorOwnerDrain is valid only for a fresh fixture with no superseded
// owner. It produces no drain digests, and it refuses any owner it is asked about.
func (verticalNoPriorOwnerDrain) VerifyOwnerCellsDrainedWithProof(_ context.Context, _ string,
	owners []workflowrecovery.OwnerEpochIdentity) ([]string, error) {
	if len(owners) != 0 {
		return nil, errors.New("fresh vertical fixture has no prior owner epochs to drain")
	}
	return nil, nil
}

type verticalInitialTranscriptProgress struct {
	pins            workflowrecovery.Pins
	transcript      workflowrecovery.TranscriptPin
	store           *sessionrepo.Store
	storeJournal    *audit.Journal
	providerJournal *audit.Journal
	baseline        *sessionrepo.Generation
}

func (v verticalInitialTranscriptProgress) VerifyProgress(_ context.Context, previous workflowrecovery.Checkpoint,
	next workflowrecovery.Pins) error {
	if v.store == nil || v.storeJournal == nil || v.providerJournal == nil || v.baseline == nil ||
		!reflect.DeepEqual(previous.Pins, v.pins) {
		return workflowrecovery.ErrObservation
	}
	want := cloneWorkflowPins(v.pins)
	want.ProviderTranscripts = []workflowrecovery.TranscriptPin{v.transcript}
	want.ProviderProjectionDigest = workflowrecovery.TranscriptPinsDigest(want.ProviderTranscripts)
	want.ProviderDisposition = workflowrecovery.DispositionAwaitingHostPrompt
	if !reflect.DeepEqual(want, next) {
		return workflowrecovery.ErrObservation
	}
	providerTrace, err := v.providerJournal.Trace()
	if err != nil || len(providerTrace.Records) != 0 || len(providerTrace.Effects) != 0 {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	bundle, err := v.store.EvidenceBundle()
	if err != nil {
		return err
	}
	storeTrace, err := v.storeJournal.Trace()
	if err != nil || uint64(len(storeTrace.Records)) != next.StoreJournalCount ||
		auditHeadHash(storeTrace.Records) != next.StoreJournalHead ||
		bundle.Baseline.ID != v.baseline.ID() || bundle.Baseline.TreeDigest != v.baseline.TreeDigest() {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	admissionTrace, err := audit.Verify(strings.NewReader(string(bundle.AdmissionJournal)))
	if err != nil || uint64(len(admissionTrace.Records)) != next.AdmissionJournalCount ||
		auditHeadHash(admissionTrace.Records) != next.AdmissionJournalHead {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil || disposition.Quarantined || disposition.Unknowns != 0 || len(disposition.Operations) != 0 ||
		disposition.Sealed != v.baseline.ID() {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	return nil
}

type verticalObserveStoreTerminal struct {
	inner         providerbridge.Executor
	ledger        *workflowrecovery.Ledger
	store         *sessionrepo.Store
	providerAudit *audit.Journal
	seen          int
}

func (v *verticalObserveStoreTerminal) Execute(ctx context.Context, proposal protocol.Proposal,
	decision gate.Decision) (json.RawMessage, error) {
	output, err := v.inner.Execute(ctx, proposal, decision)
	if err != nil {
		return nil, err
	}
	if v.ledger == nil || v.store == nil || v.providerAudit == nil {
		return nil, workflowrecovery.ErrObservation
	}
	checkpoint, _ := v.ledger.Snapshot()
	reservation := checkpoint.Pins.EffectReservation
	captured := checkpoint.Pins.HostCallReservation
	if reservation == nil || reservation.Disposition != workflowrecovery.EffectReservationTerminal ||
		reservation.Binding.ToolCallID != proposal.ToolCallID || !isVerticalDigest(reservation.OutcomeDigest) ||
		captured == nil || captured.Disposition != workflowrecovery.HostCallTerminal || captured.CapturedCall == nil ||
		captured.CapturedCall.Disposition != workflowrecovery.CapturedCallConsumed ||
		captured.CapturedCall.ToolCallID != proposal.ToolCallID ||
		checkpoint.Pins.PendingCallDisposition != workflowrecovery.PendingCallCaptured ||
		checkpoint.Pins.ProviderDisposition != workflowrecovery.DispositionAwaitingGateDecision {
		return nil, errors.New("Store terminal checkpoint was not committed before provider result release")
	}
	bundle, err := v.store.EvidenceBundle()
	if err != nil {
		return nil, err
	}
	var foundTerminal bool
	for _, operation := range bundle.Operations {
		if operation.EffectID == reservation.Binding.OperationID && operation.Tool == proposal.Tool && operation.Outcome == "success" {
			foundTerminal = true
		}
	}
	if !foundTerminal {
		return nil, errors.New("terminal owner receipt has no corresponding durable Store operation")
	}
	trace, err := v.providerAudit.Trace()
	if err != nil {
		return nil, err
	}
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_tool_result" {
			continue
		}
		var result providerResultEvidence
		if unmarshalGovernedJSON(record.Event.Data, &result) == nil && result.ToolCallID == proposal.ToolCallID {
			return nil, errors.New("provider result was journaled before the Store terminal checkpoint observation")
		}
	}
	v.seen++
	return output, nil
}

func verifyWorkflowVerticalCapturedOperationMismatches(t *testing.T, ctx context.Context,
	authority *SessionOperationAuthority, ledger *workflowrecovery.Ledger,
	proposal protocol.Proposal, decision gate.Decision, toolCallID string) {
	t.Helper()
	checkpoint, _ := ledger.Snapshot()
	decisionBinding, err := durablePolicyDecision(proposal, decision, governedReplayStorePolicy, governedReplayMetaPolicy)
	if err != nil {
		t.Fatalf("derive captured Store decision ID for mismatch checks: %v", err)
	}
	proof, err := authority.proofForDecisionID(decisionBinding.ID)
	if err != nil {
		t.Fatalf("resolve captured ALLOW for mismatch checks: %v", err)
	}
	operation, storeDecision, typedDigest, err := authority.bindOperation(proof)
	if err != nil {
		t.Fatalf("bind captured ALLOW to Store operation for mismatch checks: %v", err)
	}
	outputPaths, err := governedOperationOutputScope(proposal, nil)
	if err != nil {
		t.Fatalf("derive captured operation output scope: %v", err)
	}
	request := sessionrepo.OperationRequest{
		Tool: proposal.Tool, Operation: operation, Decision: storeDecision,
		InputGeneration: proof.generation, InputTreeDigest: proof.treeDigest,
		ArgumentDigest: typedDigest, EffectID: "vertical-negative-effect-" + toolCallID,
		OutputScopePaths: outputPaths,
	}
	captured := checkpoint.Pins.HostCallReservation.CapturedCall
	binding := workflowrecovery.EffectBinding{
		OperationID: request.EffectID, SpecDigest: bareSHA256Digest(typedDigest),
		WorkflowID: checkpoint.Pins.WorkflowID, ConversationID: captured.ConversationID,
		HostCallID: captured.HostCallID, ToolCallID: captured.ToolCallID,
		HostCallOutcomeDigest: captured.OutcomeDigest,
		RecoveryGeneration:    captured.RecoveryGeneration, OwnerEpoch: captured.OwnerEpoch,
	}
	verifier := capturedOperationVerifier{authority: authority, request: request}
	if err := verifier.VerifyCapturedOperation(ctx, checkpoint, binding); err != nil {
		t.Fatalf("exact captured operation failed its read-only binding check: %v", err)
	}
	wrongGeneration := request
	wrongGeneration.InputGeneration += "-mismatch"
	if err := (capturedOperationVerifier{authority: authority, request: wrongGeneration}).VerifyCapturedOperation(ctx, checkpoint, binding); err == nil {
		t.Fatal("captured operation verifier accepted a different Store generation")
	}
	wrongTree := request
	wrongTree.InputTreeDigest = delta.TreeDigestProfile + strings.Repeat("f", 64)
	if err := (capturedOperationVerifier{authority: authority, request: wrongTree}).VerifyCapturedOperation(ctx, checkpoint, binding); err == nil {
		t.Fatal("captured operation verifier accepted a different Store tree")
	}
	wrongSourceCheckpoint := checkpoint
	wrongSourceCheckpoint.Pins = cloneWorkflowPins(checkpoint.Pins)
	wrongSourceCheckpoint.Pins.StoreIdentityDigest = strings.Repeat("e", 64)
	if err := verifier.VerifyCapturedOperation(ctx, wrongSourceCheckpoint, binding); err == nil {
		t.Fatal("captured operation verifier accepted a different pinned Store source")
	}
	wrongSpecBinding := binding
	wrongSpecBinding.SpecDigest = strings.Repeat("0", 64)
	if err := verifier.VerifyCapturedOperation(ctx, checkpoint, wrongSpecBinding); err == nil {
		t.Fatal("captured operation verifier accepted a different typed effect digest")
	}
}

func TestWorkflowVerticalFreshPromptMultiTurnStoreTerminal(t *testing.T) {
	runWorkflowVertical(t, false, 0)
}

// Two retryable provider failures precede the first read: each attempt is its own reserved host call
// and token reservation, settled in the ledger, and the run then proceeds exactly as without them.
func TestWorkflowVerticalRetriesRetryableProviderFailuresBeforeTheFirstRead(t *testing.T) {
	runWorkflowVertical(t, true, 2)
}

// A real launch journals the worker's TCB proof in the Store journal after the workflow checkpoint
// and before the first provider exchange, so the first exchange sees the journal one record past
// the checkpointed count. The checkpoint must absorb exactly that record.
func TestWorkflowVerticalFirstTurnAfterTheLaunchProofRecord(t *testing.T) {
	runWorkflowVertical(t, true, 0)
}

// workflowVerticalFixture is a fresh workflow with a real owner store, protected root and ledger, a
// Store seeded from one fixture file, and an offline provider conversation over turns whose first
// prompt is admitted.
type workflowVerticalFixture struct {
	ctx                 context.Context
	authority           *SessionOperationAuthority
	ledger              *workflowrecovery.Ledger
	conversation        *providerbridge.Conversation
	durable             *DurableExecutor
	policy              gate.Policy
	store               *sessionrepo.Store
	storeJournal        *audit.Journal
	providerJournal     *audit.Journal
	providerJournalPath string
	limits              workflowrecovery.Limits
	ownerStoreIdentity  workflowrecovery.OwnerStoreIdentity
	// config is the provider conversation configuration startConversation passes to the authority.
	config           providerbridge.Config
	storePath        string
	storeJournalPath string
	// owner, protectedRoot and their paths let a test close the workflow owner and reopen it elsewhere.
	owner         *workflowrecovery.SQLiteOwner
	protectedRoot *workflowrecovery.ProtectedTrustedRoot
	ownerPath     string
	rootPath      string
	ownerStoreDir string
	closers       []func()
}

// workflowVerticalOptions shapes prepareWorkflowVertical. rules replace the default read-only rule
// set; predecessor is pinned into the workflow checkpoint and set on the conversation config;
// withoutLedger leaves the authority without a workflow ledger, as a governed session without
// recovery runs.
type workflowVerticalOptions struct {
	launchProof   bool
	retries       int
	bash          *productionCommandRunner
	rules         []gate.Rule
	predecessor   providerbridge.PredecessorContext
	withoutLedger bool
	// maxHostCalls and maxTokens, when nonzero, replace the retry-derived workflow limits.
	maxHostCalls, maxTokens uint64
}

// close closes every handle the fixture opened, newest first. It runs again harmlessly at cleanup.
func (f *workflowVerticalFixture) close() {
	for index := len(f.closers) - 1; index >= 0; index-- {
		f.closers[index]()
	}
	f.closers = nil
}

func runWorkflowVertical(t *testing.T, launchProof bool, retries int) {
	turns := make([]providerbridge.DevelopmentProviderTurn, 0, 2+retries)
	for attempt := 0; attempt < retries; attempt++ {
		turns = append(turns, providerbridge.DevelopmentProviderTurn{FailStatus: 503 - attempt%2,
			FailBody: `{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`})
	}
	turns = append(turns, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "vertical-response-1", CallID: "vertical-tool-call-1", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "vertical-response-2", CallID: "vertical-tool-call-2", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	}...)
	f := newWorkflowVerticalFixture(t, launchProof, retries, turns)
	ctx, authority, ledger, conversation := f.ctx, f.authority, f.ledger, f.conversation
	durable, policy, store, storeJournal, providerJournal := f.durable, f.policy, f.store, f.storeJournal, f.providerJournal
	var checkpoint workflowrecovery.Checkpoint
	observer := &verticalObserveStoreTerminal{inner: durable, ledger: ledger, store: store, providerAudit: providerJournal}
	providerExecutor, err := providerbridge.NewExecutor(conversation, observer)
	if err != nil {
		t.Fatal(err)
	}
	var previousHostCall string
	for index, expectedToolCall := range []string{"vertical-tool-call-1", "vertical-tool-call-2"} {
		turn, err := conversation.NextTurn(ctx)
		if err != nil || turn.ToolCall == nil || turn.ToolCall.ID != expectedToolCall || turn.ToolCall.Name != "read" {
			t.Fatalf("provider turn %d did not return its exact scripted read: turn=%+v err=%v", index+1, turn, err)
		}
		checkpoint, _ = ledger.Snapshot()
		reservation := checkpoint.Pins.HostCallReservation
		priorEffectSettled := checkpoint.Pins.EffectReservation == nil
		if index > 0 {
			priorEffect := checkpoint.Pins.EffectReservation
			priorEffectSettled = priorEffect != nil &&
				priorEffect.Disposition == workflowrecovery.EffectReservationTerminal &&
				priorEffect.OutcomeDigest != "" &&
				priorEffect.Binding.WorkflowID == workflowVerticalID &&
				priorEffect.Binding.ConversationID == workflowVerticalConversation &&
				priorEffect.Binding.HostCallID == previousHostCall &&
				priorEffect.Binding.ToolCallID == "vertical-tool-call-1"
		}
		if reservation == nil || reservation.Disposition != workflowrecovery.HostCallTerminal ||
			reservation.Binding.CallID == "" || reservation.Binding.CallID == previousHostCall ||
			reservation.Binding.TokenUpperBound != 1024 || reservation.CapturedCall == nil ||
			reservation.CapturedCall.Disposition != workflowrecovery.CapturedCallAvailable ||
			reservation.CapturedCall.ToolCallID != expectedToolCall ||
			checkpoint.Counters.HostCalls != uint64(index*2+1+retries) || checkpoint.Counters.Tokens != uint64(index+1+retries)*1024 ||
			checkpoint.Pins.PendingCallDisposition != workflowrecovery.PendingCallCaptured ||
			checkpoint.Pins.ProviderDisposition != workflowrecovery.DispositionAwaitingGateDecision ||
			!priorEffectSettled {
			t.Fatalf("provider turn %d was not charged and captured in a nonterminal phase: pins=%+v counters=%+v",
				index+1, checkpoint.Pins, checkpoint.Counters)
		}
		previousHostCall = reservation.Binding.CallID
		proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion,
			ToolCallID: turn.ToolCall.ID, Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
		matched, err := conversation.Correlate(ctx, proposal)
		if err != nil || !matched.Accepted || matched.StreamClosed {
			t.Fatalf("correlate captured provider call %s: matched=%+v err=%v", expectedToolCall, matched, err)
		}
		decision := gate.Evaluate(proposal, matched, policy)
		if decision.Verdict != gate.Allow {
			t.Fatalf("read proposal was not allowed by the pinned test policy: %+v", decision)
		}
		if err := conversation.RecordDecision(ctx, proposal, decision); err != nil {
			t.Fatalf("durably record exact ALLOW decision: %v", err)
		}
		verifyWorkflowVerticalCapturedOperationMismatches(t, ctx, authority, ledger, proposal, decision, expectedToolCall)
		if _, err := providerExecutor.Execute(ctx, proposal, decision); err != nil {
			t.Fatalf("execute Store effect and release provider result for turn %d: %v", index+1, err)
		}
		checkpoint, _ = ledger.Snapshot()
		reservation = checkpoint.Pins.HostCallReservation
		if reservation == nil || reservation.Disposition != workflowrecovery.HostCallTerminal || reservation.CapturedCall != nil ||
			checkpoint.Pins.EffectReservation == nil ||
			checkpoint.Pins.EffectReservation.Disposition != workflowrecovery.EffectReservationTerminal ||
			checkpoint.Pins.PendingCallDisposition != workflowrecovery.PendingCallTerminal ||
			checkpoint.Pins.ProviderDisposition != workflowrecovery.DispositionAwaitingProviderExchange ||
			checkpoint.Counters.HostCalls != uint64((index+1)*2+retries) || checkpoint.Counters.Tokens != uint64(index+1+retries)*1024 {
			t.Fatalf("provider result was not checkpointed after the terminal Store outcome on turn %d: pins=%+v counters=%+v",
				index+1, checkpoint.Pins, checkpoint.Counters)
		}
		var authorizedRequest sessionrepo.OperationRequest
		authority.mu.Lock()
		for _, proof := range authority.operationProofs {
			if proof.capture.proposal.ToolCallID == expectedToolCall {
				authorizedRequest = proof.request
				break
			}
		}
		authority.mu.Unlock()
		if authorizedRequest.EffectID == "" {
			t.Fatalf("no durable authorization request was retained for %s", expectedToolCall)
		}
		for _, replayRequest := range []sessionrepo.OperationRequest{
			authorizedRequest,
			func() sessionrepo.OperationRequest {
				changed := authorizedRequest
				changed.EffectID += "-replay"
				return changed
			}(),
		} {
			beforeCheckpoint, beforeDigest := ledger.Snapshot()
			beforeStoreTrace, err := storeJournal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			if err := authority.AuthorizeOperation(replayRequest); err == nil {
				t.Fatalf("replayed or rebound effect authorization was accepted for %s", expectedToolCall)
			}
			afterCheckpoint, afterDigest := ledger.Snapshot()
			afterStoreTrace, err := storeJournal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			if beforeDigest != afterDigest || !reflect.DeepEqual(beforeCheckpoint.Counters, afterCheckpoint.Counters) ||
				auditHeadHash(beforeStoreTrace.Records) != auditHeadHash(afterStoreTrace.Records) ||
				len(beforeStoreTrace.Records) != len(afterStoreTrace.Records) ||
				len(beforeStoreTrace.Effects) != len(afterStoreTrace.Effects) {
				t.Fatalf("rejected duplicate authorization changed durable state for %s", expectedToolCall)
			}
		}
	}
	providedTrace, traceErr := providerJournal.Trace()
	if traceErr != nil {
		t.Fatal(traceErr)
	}
	retryRecords := 0
	for _, record := range providedTrace.Records {
		if record.Event.Kind == "provider_exchange_retryable_failure" {
			retryRecords++
		}
	}
	if retryRecords != retries {
		t.Fatalf("provider journal holds %d retryable-failure records, want %d", retryRecords, retries)
	}
	if observer.seen != 2 {
		t.Fatalf("observed Store-terminal-before-provider-result checkpoints = %d, want 2", observer.seen)
	}
}

// newWorkflowVerticalFixture builds a read-only workflow; a production command runner also allows Bash
// with the output scope workflowVerticalBashOutput.
func newWorkflowVerticalFixture(t *testing.T, launchProof bool, retries int,
	turns []providerbridge.DevelopmentProviderTurn, bash ...*productionCommandRunner) *workflowVerticalFixture {
	t.Helper()
	options := workflowVerticalOptions{launchProof: launchProof, retries: retries}
	if len(bash) == 1 {
		options.bash = bash[0]
	}
	f := prepareWorkflowVertical(t, options)
	f.startConversation(t, func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		return providerbridge.NewDevelopmentOfflineConversation(config, journal, turns)
	})
	return f
}

// prepareWorkflowVertical builds the workflow up to, not including, its provider conversation.
func prepareWorkflowVertical(t *testing.T, options workflowVerticalOptions) *workflowVerticalFixture {
	t.Helper()
	f := &workflowVerticalFixture{}
	t.Cleanup(f.close)
	launchProof, retries := options.launchProof, options.retries
	ctx := context.Background()
	root := privateGovernedTestRoot(t)
	sourcePath, storePath, adminPath := filepath.Join(root, "source"), filepath.Join(root, "store"), filepath.Join(root, "admin")
	recoveryRootDir, ownerStoreDir := filepath.Join(root, "recovery-root"), filepath.Join(root, "owner-store")
	for _, path := range []string{sourcePath, storePath, adminPath, recoveryRootDir, ownerStoreDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("vertical fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	storeJournalPath := filepath.Join(root, "store-audit.jsonl")
	storeJournal, err := audit.Open(storeJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	f.closers = append(f.closers, func() { _ = storeJournal.Close() })
	providerJournalPath := filepath.Join(root, "provider-audit.jsonl")
	providerJournal, err := audit.Open(providerJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	f.closers = append(f.closers, func() { _ = providerJournal.Close() })
	commandJournalPath := filepath.Join(root, "command-audit.jsonl")
	commandJournal, err := audit.Open(commandJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	f.closers = append(f.closers, func() { _ = commandJournal.Close() })

	rules := options.rules
	if rules == nil {
		rules = []gate.Rule{{Tool: "read", Effect: gate.EffectAllow}}
	}
	var runner governedCommandRunner = newDevelopmentFixtureCommandRunner()
	var commandRunner sessionrepo.CommandRunner
	var bashOutput []string
	if options.bash != nil {
		rules = append(rules, gate.Rule{Tool: "bash", Effect: gate.EffectAllow})
		runner, commandRunner, bashOutput = options.bash, options.bash, []string{workflowVerticalBashOutput}
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewSessionOperationAuthority(storeJournal, providerJournal,
		workflowVerticalID, workflowVerticalConversation, workflowVerticalProfile,
		policy, governedReplayStorePolicy, governedReplayMetaPolicy, runner, bashOutput)
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
	f.closers = append(f.closers, func() { _ = managed.Close() })
	store := managed.Store()
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindBaseline(store, baseline); err != nil {
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
	f.closers = append(f.closers, func() { _ = admin.Close() })
	git, gitProfile := pinGovernedTestGit(t)
	f.closers = append(f.closers, func() { _ = git.Close() })
	prepared, err := sessionlaunch.Prepare(ctx, managed, baseline, admin, git, gitProfile)
	if err != nil {
		t.Fatal(err)
	}
	f.closers = append(f.closers, func() { _ = prepared.Close() })
	if err := authority.BindPreparedSession(prepared); err != nil {
		t.Fatal(err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: baseline, CallIssuer: governedCallIssuer,
		PolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
		AfterDurableOperation: authority.SettleDurableOperation,
		CommandRunner:         commandRunner, BashOutputPaths: bashOutput,
	})
	if err != nil {
		t.Fatal(err)
	}

	workflowRootPath := filepath.Join(recoveryRootDir, "workflow-root.sqlite")
	deadline := time.Now().UTC().Add(30 * time.Minute).Round(0)
	freshPolicy := &verticalFreshWorkflowPolicy{workflow: workflowVerticalID, deadline: deadline}
	boundary, err := workflowrecovery.NewLinuxHostBoundary(ownerStoreDir, uint32(os.Geteuid()),
		verticalNoPriorOwnerDrain{}, freshPolicy)
	if err != nil {
		t.Fatal(err)
	}
	protectedRoot, err := workflowrecovery.CreateFreshProtectedRoot(workflowRootPath, ownerStoreDir, boundary)
	if err != nil {
		t.Fatal(err)
	}
	f.closers = append(f.closers, func() { _ = protectedRoot.Close() })
	owner, err := workflowrecovery.CreateFreshSQLiteOwner(filepath.Join(ownerStoreDir, "workflow.sqlite"), protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	f.closers = append(f.closers, func() { _ = owner.Close() })
	ownerStoreIdentity, err := owner.OwnerStoreIdentity(ctx, workflowVerticalID)
	if err != nil {
		t.Fatal(err)
	}
	freshPolicy.root = owner.TrustedRootIdentityDigest()
	freshPolicy.incarnation = owner.OwnerIncarnation()
	freshPolicy.store = ownerStoreIdentity

	storeRootForIdentity, err := os.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	storeIdentity, err := recoveryDirectoryIdentityDigest(storeRootForIdentity)
	_ = storeRootForIdentity.Close()
	if err != nil {
		t.Fatal(err)
	}
	adminRootForIdentity, err := os.Open(adminPath)
	if err != nil {
		t.Fatal(err)
	}
	adminIdentity, err := recoveryDirectoryIdentityDigest(adminRootForIdentity)
	_ = adminRootForIdentity.Close()
	if err != nil {
		t.Fatal(err)
	}
	commandIdentity, err := recoveryFileIdentityDigest(commandJournalPath, false)
	if err != nil {
		t.Fatal(err)
	}
	providerIdentity, err := recoveryFileIdentityDigest(providerJournalPath, false)
	if err != nil {
		t.Fatal(err)
	}
	admissionIdentity, err := recoveryFileIdentityDigest(filepath.Join(storePath, "delta-admission.jsonl"), false)
	if err != nil {
		t.Fatal(err)
	}
	d06Digest, gitIdentityDigest, _, err := prepared.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	storeTrace, err := storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	admissionTrace, err := audit.Verify(strings.NewReader(string(bundle.AdmissionJournal)))
	if err != nil {
		t.Fatal(err)
	}
	taskDigest := digestBytes([]byte("vertical task source identity"))
	limits := workflowrecovery.Limits{MaxPreEffectRetries: 2, MaxCleanRestarts: 2,
		MaxHostCalls: uint64(4 + 2*retries), MaxTokens: uint64(2048 + 1024*retries), Deadline: deadline}
	if options.maxHostCalls != 0 {
		limits.MaxHostCalls, limits.MaxTokens = options.maxHostCalls, options.maxTokens
	}
	pins := workflowrecovery.Pins{
		WorkflowID: workflowVerticalID, SessionID: workflowVerticalSessionID,
		RecoveryGeneration: 1, OwnerEpoch: 1,
		OwnerIncarnationDigest: owner.OwnerIncarnationDigest(), RootIdentityDigest: owner.TrustedRootIdentityDigest(),
		StoreIdentityDigest: storeIdentity, AdmissionIdentityDigest: admissionIdentity,
		CommandIdentityDigest: commandIdentity, AdminIdentityDigest: adminIdentity,
		TaskDigest: bareWorkflowDigest(taskDigest), PromptDigest: bareWorkflowDigest(digestBytes([]byte(workflowVerticalPrompt))),
		ImportedPrefixDigest:         bareWorkflowDigest(options.predecessor.Digest()),
		PredecessorTurnReceiptDigest: bareWorkflowDigest(options.predecessor.PredecessorReceiptDigest()),
		PolicyDigest:                 barePolicyDigest(policy.Digest()), ProfileDigest: bareWorkflowDigest(digestBytes([]byte(workflowVerticalProfile))),
		ImageDigest:         bareWorkflowDigest(digestBytes([]byte("vertical-image"))),
		RuntimeDigest:       bareWorkflowDigest(digestBytes([]byte("vertical-runtime"))),
		D06ProvenanceDigest: bareWorkflowDigest(d06Digest), PrivateGitIdentityDigest: bareWorkflowDigest(gitIdentityDigest),
		StoreBaselineGenerationID: baseline.ID(), StoreBaselineTreeDigest: strings.TrimPrefix(baseline.TreeDigest(), delta.TreeDigestProfile),
		StoreTipGenerationID: baseline.ID(), StoreTipTreeDigest: strings.TrimPrefix(baseline.TreeDigest(), delta.TreeDigestProfile),
		StoreJournalHead: auditHeadHash(storeTrace.Records), StoreJournalCount: uint64(len(storeTrace.Records)),
		AdmissionJournalHead: auditHeadHash(admissionTrace.Records), AdmissionJournalCount: uint64(len(admissionTrace.Records)),
		ProviderProjectionDigest: workflowrecovery.TranscriptPinsDigest(nil),
		ProviderDisposition:      workflowrecovery.DispositionNone, PendingCallDisposition: workflowrecovery.PendingCallNone,
	}
	pins.RecoveryLineageDigest = workflowrecovery.ComputeRecoveryLineageDigest("", workflowrecovery.Pins{}, pins)
	if _, err := protectedRoot.NextOwnerEpoch(ctx, workflowVerticalID); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowrecovery.InitializeFresh(ctx, owner, pins, limits); err != nil {
		t.Fatalf("initialize one original checkpoint through the protected host root: %v; pins=%+v", err, pins)
	}
	if freshPolicy.calls != 1 {
		t.Fatalf("host fresh-workflow catalog authorizations = %d, want exactly one", freshPolicy.calls)
	}
	if _, err := workflowrecovery.InitializeFresh(ctx, owner, pins, limits); err == nil {
		t.Fatal("duplicate fresh initialization reset the same workflow owner store")
	}
	ledger, err := workflowrecovery.Open(ctx, owner, workflowrecovery.OpenOptions{
		WorkflowID: workflowVerticalID, OwnerEpoch: 1, RetainedPins: pins, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	transcript := workflowrecovery.TranscriptPin{Ordinal: 1, ConversationID: workflowVerticalConversation,
		JournalIdentityDigest: providerIdentity, Disposition: workflowrecovery.DispositionAwaitingHostPrompt}
	if _, err := ledger.RegisterInitialTranscript(ctx, verticalInitialTranscriptProgress{
		pins: pins, transcript: transcript, store: store, storeJournal: storeJournal,
		providerJournal: providerJournal, baseline: baseline,
	}, transcript); err != nil {
		t.Fatalf("register the first empty provider transcript before any request: %v", err)
	}
	if options.withoutLedger {
		ledger = nil
	} else {
		checkpoint, _ := ledger.Snapshot()
		if err := authority.BindWorkflowLedger(ledger, checkpoint.Pins, 1024); err != nil {
			t.Fatalf("bind the mandatory actual workflow ledger: %v", err)
		}
		if err := authority.BindInitialWorkflowPrompt(workflowVerticalTaskID, workflowVerticalPrompt, taskDigest); err != nil {
			t.Fatalf("bind the exact host-catalog task prompt: %v", err)
		}
		if err := authority.BindInitialWorkflowPrompt(workflowVerticalTaskID, workflowVerticalPrompt+" changed", taskDigest); err == nil {
			t.Fatal("changed initial prompt replaced the exact fresh catalog prompt")
		}
	}
	if launchProof {
		before, _ := ledger.Snapshot()
		proofData, err := json.Marshal(productionLaunchTCBProofEvent{SchemaVersion: "tbound-production-launch-tcb-proof/v1",
			RunID: "vertical-run", SessionID: workflowVerticalSessionID, WorkflowID: workflowVerticalID,
			ConversationID: workflowVerticalConversation})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := storeJournal.Append(audit.Event{Kind: "production_tcb_proof", ID: "production-tcb-proof:vertical-run", Data: proofData}); err != nil {
			t.Fatal(err)
		}
		if trace, err := storeJournal.Trace(); err != nil || uint64(len(trace.Records)) != before.Pins.StoreJournalCount+1 {
			t.Fatalf("the launch proof did not put the Store journal one record past the checkpoint: %v", err)
		}
	}
	f.config = providerbridge.Config{WorkflowID: workflowVerticalID, ConversationID: workflowVerticalConversation,
		ProfileID: workflowVerticalProfile, InitialGenerationID: baseline.ID(), InitialTreeDigest: baseline.TreeDigest(),
		SystemPrompt: governedReplaySystem, DeveloperPrompt: governedReplayDeveloper, Predecessor: options.predecessor}
	f.ctx, f.authority, f.ledger, f.durable, f.policy, f.store = ctx, authority, ledger, durable, policy, store
	f.storeJournal, f.providerJournal, f.providerJournalPath = storeJournal, providerJournal, providerJournalPath
	f.storePath, f.storeJournalPath = storePath, storeJournalPath
	f.limits, f.ownerStoreIdentity = limits, ownerStoreIdentity
	f.owner, f.protectedRoot, f.ownerStoreDir = owner, protectedRoot, ownerStoreDir
	f.ownerPath, f.rootPath = filepath.Join(ownerStoreDir, "workflow.sqlite"), workflowRootPath
	return f
}

// startConversation constructs the provider conversation through the authority and admits the
// fixture's exact prompt.
func (f *workflowVerticalFixture) startConversation(t *testing.T,
	factory func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error)) {
	t.Helper()
	conversation, err := f.authority.newProviderConversation(f.config, factory)
	if err != nil {
		t.Fatal(err)
	}
	f.closers = append(f.closers, func() { conversation.Close() })
	f.conversation = conversation
	if f.ledger != nil {
		if err := f.authority.AdmitPrompt(conversation, f.durable, workflowVerticalTaskID, workflowVerticalPrompt+" changed"); err == nil {
			t.Fatal("changed initial prompt reached the provider conversation")
		}
	}
	if err := f.authority.AdmitPrompt(conversation, f.durable, workflowVerticalTaskID, workflowVerticalPrompt); err != nil {
		t.Fatalf("admit the exact host-catalog prompt: %v", err)
	}
	if err := f.authority.AdmitPrompt(conversation, f.durable, workflowVerticalTaskID, workflowVerticalPrompt); err == nil {
		t.Fatal("duplicate initial prompt was admitted to the provider conversation")
	}
}

func TestWorkflowDigestDomainsFailClosed(t *testing.T) {
	hexDigest := strings.Repeat("a", 64)
	sha256Digest := "sha256:" + hexDigest
	policyDigest := gate.ProfileVersion + ":sha256:" + hexDigest
	treeDigest := delta.TreeDigestProfile + hexDigest
	if bareWorkflowDigest(sha256Digest) != hexDigest || bareSHA256Digest(sha256Digest) != hexDigest ||
		barePolicyDigest(policyDigest) != hexDigest || bareStoreTreeDigest(treeDigest) != hexDigest ||
		sha256DigestFromBarePin(hexDigest) != sha256Digest {
		t.Fatal("known digest domains did not normalize to their exact bare pins")
	}
	for name, value := range map[string]string{
		"policy used as ordinary SHA256":       policyDigest,
		"tree used as ordinary SHA256":         treeDigest,
		"protocol args profile used as SHA256": "tbound-args-jcs-rfc8785/v1:" + sha256Digest,
		"multiple SHA256 prefixes":             "sha256:" + sha256Digest,
		"uppercase hex":                        "sha256:" + strings.Repeat("A", 64),
		"unknown SHA256 namespace":             "other-domain/v1:" + sha256Digest,
	} {
		t.Run(name, func(t *testing.T) {
			if got := bareWorkflowDigest(value); got != "" {
				t.Fatalf("bareWorkflowDigest(%q) = %q, want rejection", value, got)
			}
		})
	}
	for name, value := range map[string]string{
		"ordinary digest in policy slot": sha256Digest,
		"wrong policy profile":           "tbound-policy/v9:sha256:" + hexDigest,
		"policy double prefix":           gate.ProfileVersion + ":sha256:sha256:" + hexDigest,
	} {
		t.Run("policy/"+name, func(t *testing.T) {
			if got := barePolicyDigest(value); got != "" {
				t.Fatalf("barePolicyDigest(%q) = %q, want rejection", value, got)
			}
		})
	}
	for name, value := range map[string]string{
		"ordinary digest in tree slot": sha256Digest,
		"wrong tree profile":           "tbound-tree-other/v1:sha256:" + hexDigest,
		"tree double prefix":           delta.TreeDigestProfile + "sha256:" + hexDigest,
	} {
		t.Run("tree/"+name, func(t *testing.T) {
			if got := bareStoreTreeDigest(value); got != "" {
				t.Fatalf("bareStoreTreeDigest(%q) = %q, want rejection", value, got)
			}
		})
	}
	if bareSHA256Digest(hexDigest) != "" || sha256DigestFromBarePin(sha256Digest) != "" {
		t.Fatal("bare/prefixed pin conversion accepted the wrong direction")
	}
}

func isVerticalDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}
