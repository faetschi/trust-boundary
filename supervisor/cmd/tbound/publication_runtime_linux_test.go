//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

const (
	runtimeWorkflowID    = "workflow-runtime-integration"
	runtimePublicationID = "publication-runtime-integration"
)

// TestPublicationRuntimeComposesRealBrokerIPCExecutorAndSeededSessionEvidence
// traverses the existing provider-capture/correlation/IPC/gate path, the real
// DurableExecutor and sessionrepo ledger, then finalizes the verified g1 tip.
// Its provider capture and host-trust implementation are explicit disposable
// fixtures; this is not Pi production wiring or a production security profile.
func TestPublicationRuntimeComposesRealBrokerIPCExecutorAndSeededSessionEvidence(t *testing.T) {
	fixture := newPublicationRuntimeFixture(t, nil, nil)
	proposals, generations := runtimeFourToolProposals()
	client, supervisor := fixture.supervisorFor(t, proposals, generations)
	resultChannel := make(chan runtimeRunResult, 1)
	go func() {
		result, err := fixture.runtime.Run(context.Background(), supervisor)
		resultChannel <- runtimeRunResult{result: result, err: err}
	}()
	results := make([]protocol.Result, 0, len(proposals))
	for index, proposal := range proposals {
		if err := client.SendProposal(proposal); err != nil {
			t.Fatalf("send %s proposal: %v", proposal.Tool, err)
		}
		result, err := client.ReceiveResult()
		if err != nil {
			t.Fatalf("receive %s result: %v", proposal.Tool, err)
		}
		if result.Verdict != string(gate.Allow) || result.ResponseID == nil ||
			result.ResponseID.Issuer != durableResponseIssuer || result.ResponseID.Opaque != durableResponseID || result.Sequence != uint64(index+1) {
			t.Fatalf("tool %s did not preserve broker/gate identity: %+v", proposal.Tool, result)
		}
		results = append(results, result)
	}
	var summary durableMutationSummaryPayload
	if err := json.Unmarshal(results[len(results)-1].Output, &summary); err != nil {
		t.Fatalf("decode final durable write output %q: %v", results[len(results)-1].Output, err)
	}
	if summary.Tool != "write" || summary.Generation.ID != "g4" || summary.Transition.ID != "transition-000004" ||
		!strings.HasPrefix(summary.EffectID, "sessionrepo-effect-") || summary.Outcome != "success" {
		t.Fatalf("final result lost durable effect/generation identity: %+v", summary)
	}
	if len(results[0].Output) == 0 || !bytes.Contains(results[0].Output, []byte("baseline\\n")) {
		t.Fatalf("read did not return the baseline content: %s", results[0].Output)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	run := <-resultChannel
	if run.err != nil || run.result.Status != publication.StatusSucceeded || run.result.TransactionID == "" ||
		run.result.Binding.OriginProposalID != summaryProposalID(t, proposals[len(proposals)-1], results[len(results)-1]) ||
		run.result.Binding.BaselineTreeDigest != fixture.baseline.TreeDigest() ||
		run.result.Binding.SealedTreeDigest != fixture.executor.Tip().TreeDigest() {
		t.Fatalf("finalization=%+v err=%v", run.result, run.err)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.base, "live", durableWritePath)); err != nil || string(got) != "published-final\n" {
		t.Fatalf("registered live workspace was not published: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.base, "live", "build", "result.txt")); err != nil || string(got) != "synthetic-bash-output\n" {
		t.Fatalf("bounded fixture Bash delta was not published: %q err=%v", got, err)
	}
	chain, err := fixture.store.Verify()
	if err != nil || chain.TransitionCount != 4 || chain.SealedGeneration != "g4" {
		t.Fatalf("actual session repository chain=%+v err=%v", chain, err)
	}
	evidence, err := fixture.store.Evidence()
	if err != nil || len(evidence.ApprovedDeltaLedger) != 4 || len(evidence.Operations) != 5 ||
		evidence.Operations[len(evidence.Operations)-1].EffectID != summary.EffectID || evidence.Operations[len(evidence.Operations)-1].OutputGeneration != summary.Generation.ID {
		t.Fatalf("finalized store evidence=%+v err=%v", evidence, err)
	}
	storeTrace, err := fixture.storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	gateDecisions := 0
	for _, record := range storeTrace.Records {
		if record.Event.Kind != durableDecisionEventKind {
			continue
		}
		var event durableDecisionEvent
		if err := json.Unmarshal(record.Event.Data, &event); err != nil || event.Decision.Verdict != gate.Allow || event.Decision.Sequence != uint64(gateDecisions+1) {
			t.Fatalf("integrated gate decision %d is malformed or out of order: %+v err=%v", gateDecisions, event, err)
		}
		gateDecisions++
	}
	if gateDecisions != 5 {
		t.Fatalf("session journal has %d pre-effect gate decisions, want five", gateDecisions)
	}
	if got := countWriteEffects(t, storeTrace); got != 2 {
		t.Fatalf("session journal recorded %d write-tool effects, want two", got)
	}
	publicationTrace, err := fixture.publicationJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if countRuntimeAuditKind(publicationTrace, "workspace_commit_authorized") != 1 ||
		countRuntimeAuditKind(publicationTrace, "publication_token_consumed") != 1 ||
		countRuntimeAuditKind(publicationTrace, "workflow_publication_pending") != 1 ||
		countRuntimeAuditKind(publicationTrace, "workflow_publication_resolved") != 1 {
		t.Fatalf("missing durable commit/token/root lifecycle records: %+v", publicationTrace.Records)
	}
}

func TestPublicationRuntimeFailsClosedWithoutTrustedProductionReadiness(t *testing.T) {
	fixture := newPublicationRuntimeFixture(t, nil, nil)
	options := fixture.finalizerOptions()
	options.Trust = nil
	if _, err := NewPublicationRuntime(fixture.executor, options); !errors.Is(err, workflow.ErrFinalizerConfig) {
		t.Fatalf("missing trusted closure/profile/source/offline/settlement verifier was admitted: %v", err)
	}
	if fixture.executor.Tip().ID() != "g0" {
		t.Fatal("configuration refusal changed the durable session generation")
	}
}

func TestFinalizerRejectsMismatchedCurrentPublicationRepository(t *testing.T) {
	for _, mismatch := range []string{"nil repository", "wrong journal", "wrong owner epoch", "wrong workflow", "wrong workspace"} {
		t.Run(mismatch, func(t *testing.T) {
			fixture := newPublicationRuntimeFixture(t, nil, nil)
			options := fixture.finalizerOptions()
			options.OwnerEpoch = 1
			options.PublicationID = "publication-binding-check"
			options.Trust = fixture.trust
			var otherJournal *audit.Journal
			if mismatch == "wrong journal" {
				otherDir := filepath.Join(fixture.base, "other-publication-audit")
				durableMkdir(t, otherDir, 0o700)
				var err error
				otherJournal, err = audit.Open(filepath.Join(otherDir, "journal.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				defer otherJournal.Close()
			}
			options.OpenPublication = func() (*publication.Repository, error) {
				if mismatch == "nil repository" {
					return nil, nil
				}
				workflowID, ownerEpoch, repoWorkspace, repoJournal := runtimeWorkflowID, uint64(1), fixture.workspace, fixture.publicationJournal
				switch mismatch {
				case "wrong journal":
					repoJournal = otherJournal
				case "wrong owner epoch":
					ownerEpoch = 2
				case "wrong workflow":
					workflowID = "workflow-other"
				case "wrong workspace":
					repoWorkspace.Limits.MaxObjects--
				}
				return publication.Acquire(publication.Options{WorkflowID: workflowID, OwnerEpoch: ownerEpoch,
					StateRoot: fixture.publicationState, Journal: repoJournal, Workspace: repoWorkspace})
			}
			finalizer, err := workflow.NewFinalizer(options)
			if err != nil {
				t.Fatal(err)
			}
			err = finalizer.RecoverBeforeAdmission(context.Background())
			if err == nil {
				t.Fatal("mismatched publication repository was admitted")
			}
			if mismatch == "nil repository" {
				if !strings.Contains(err.Error(), "returned nil") {
					t.Fatalf("nil repository did not fail closed explicitly: %v", err)
				}
			} else if !strings.Contains(err.Error(), "binding does not match") {
				t.Fatalf("repository mismatch was not rejected by its binding: %v", err)
			}
			if err := finalizer.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublicationRuntimeTrustedAdmissionRefusalWithholdsAllProposals(t *testing.T) {
	fixture := newPublicationRuntimeFixture(t, nil, &runtimeFixtureTrust{denyAdmission: true})
	client, supervisor, _ := fixture.supervisor(t)
	err := fixture.runtime.Serve(context.Background(), supervisor)
	if err == nil || !strings.Contains(err.Error(), "trusted workflow admission refused") {
		t.Fatalf("unestablished production runtime profile was admitted: %v", err)
	}
	_ = client.Close()
	if fixture.executor.Tip().ID() != "g0" {
		t.Fatalf("admission refusal advanced the session generation: %s", fixture.executor.Tip().ID())
	}
	trace, traceErr := fixture.storeJournal.Trace()
	if traceErr != nil || len(trace.Effects) != 1 { // baseline seed only
		t.Fatalf("admission refusal released an operation effect: effects=%+v err=%v", trace.Effects, traceErr)
	}
}

func TestPublicationRuntimeRejectsStaleCapturedGenerationBeforeExecutor(t *testing.T) {
	fixture := newPublicationRuntimeFixture(t, nil, nil)
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: durableCallID, Tool: "write",
		Arguments: json.RawMessage(`{"path":"task.txt","content":"must-not-run"}`)}
	client, supervisor := fixture.supervisorFor(t, []protocol.Proposal{proposal}, []string{"stale-generation"})
	serveErr := make(chan error, 1)
	go func() { serveErr <- fixture.runtime.Serve(context.Background(), supervisor) }()
	if err := client.SendProposal(proposal); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err == nil || !strings.Contains(err.Error(), "current DurableExecutor tip") {
		t.Fatalf("stale generation did not fail closed in the broker admission wrapper: %v", err)
	}
	_ = client.Close()
	if fixture.executor.Tip().ID() != "g0" {
		t.Fatalf("stale captured generation advanced the executor: %s", fixture.executor.Tip().ID())
	}
	if got, err := os.ReadFile(filepath.Join(fixture.base, "live", durableWritePath)); err != nil || string(got) != "baseline\n" {
		t.Fatalf("stale captured generation changed live root: %q err=%v", got, err)
	}
}

func TestPublicationRuntimeFinalizeWaitsForInflightDurableBashAndDeniesLateProposal(t *testing.T) {
	fixture := newPublicationRuntimeFixture(t, nil, nil)
	runnerEntered := make(chan struct{})
	releaseRunner := make(chan struct{})
	var releaseOnce sync.Once
	fixture.runner.entered = runnerEntered
	fixture.runner.release = releaseRunner
	defer releaseOnce.Do(func() { close(releaseRunner) })
	proposals, generations := runtimeFourToolProposals()
	client, supervisor := fixture.supervisorFor(t, proposals[:4], generations[:4])
	supervisor.ProposalLimit = 5 // deliberately leave room to prove the queued fifth call is denied
	serveDone := make(chan error, 1)
	go func() { serveDone <- fixture.runtime.Serve(context.Background(), supervisor) }()
	for _, proposal := range proposals[:3] {
		if err := client.SendProposal(proposal); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ReceiveResult(); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.SendProposal(proposals[3]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runnerEntered: // the real DurableExecutor is blocked inside Store.RunBash
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic command runner did not reach its deterministic in-flight barrier")
	}
	finalizeDone := make(chan runtimeRunResult, 1)
	go func() {
		result, err := fixture.runtime.Finalize(context.Background())
		finalizeDone <- runtimeRunResult{result: result, err: err}
	}()
	for {
		release, err := fixture.runtime.finalizer.Admission().Acquire(context.Background())
		if errors.Is(err, workflow.ErrAdmissionClosed) {
			break
		}
		if err != nil {
			t.Fatalf("observe publication admission closure: %v", err)
		}
		release()
		time.Sleep(time.Millisecond)
	}
	select {
	case finalized := <-finalizeDone:
		t.Fatalf("finalizer returned while durable Bash remained in flight: %+v", finalized)
	case <-time.After(40 * time.Millisecond):
	}
	if got, err := os.ReadFile(filepath.Join(fixture.base, "live", durableWritePath)); err != nil || string(got) != "baseline\n" {
		t.Fatalf("finalization changed live state before the accepted execution settled: %q err=%v", got, err)
	}
	lateProposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "call-after-finalize",
		Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"late-must-not-run"}`)}
	lateSend := make(chan error, 1)
	go func() { lateSend <- client.SendProposal(lateProposal) }()
	bashResultDone := make(chan protocol.Result, 1)
	bashResultErr := make(chan error, 1)
	go func() {
		result, err := client.ReceiveResult()
		bashResultDone <- result
		bashResultErr <- err
	}()
	releaseOnce.Do(func() { close(releaseRunner) })
	bashResult := <-bashResultDone
	err := <-bashResultErr
	if err != nil || bashResult.Verdict != string(gate.Allow) || bashResult.ToolCallID != proposals[3].ToolCallID {
		t.Fatalf("accepted in-flight durable Bash was not settled exactly once: result=%+v err=%v", bashResult, err)
	}
	if err := <-lateSend; err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReceiveResult(); err == nil {
		t.Fatal("late proposal received a result after finalization closed admission")
	}
	_ = client.Close()
	if err := <-serveDone; err == nil || !strings.Contains(err.Error(), "workflow proposal admission is closed") {
		t.Fatalf("late proposal did not terminate without correlation/ALLOW: %v", err)
	}
	finalized := <-finalizeDone
	if finalized.err == nil || !strings.Contains(finalized.err.Error(), "lease-only publication fails closed") || finalized.result.TransactionID != "" {
		t.Fatalf("lease-origin finalization should fail closed after draining the accepted command: %+v", finalized)
	}
	if fixture.executor.Tip().ID() != "g3" {
		t.Fatalf("accepted durable command was lost or duplicated after drain: tip=%s", fixture.executor.Tip().ID())
	}
	trace, err := fixture.storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind != durableDecisionEventKind {
			continue
		}
		var event durableDecisionEvent
		if err := json.Unmarshal(record.Event.Data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Proposal.ToolCallID == lateProposal.ToolCallID {
			t.Fatal("late proposal was durably decided after admission closed")
		}
	}
	pubTrace, err := fixture.publicationJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if countRuntimeAuditKind(pubTrace, "publication_prepare") != 0 || countRuntimeAuditKind(pubTrace, "publication_token_consumed") != 0 {
		t.Fatal("failed lease-origin finalization persisted publication authority")
	}
}

func TestPublicationRuntimeDeniedOriginDoesNotBeginPublication(t *testing.T) {
	trust := &runtimeFixtureTrust{denyOrigin: true}
	fixture := newPublicationRuntimeFixture(t, nil, trust)
	client, supervisor, proposal := fixture.supervisor(t)
	runCh := make(chan error, 1)
	go func() { runCh <- fixture.runtime.Serve(context.Background(), supervisor) }()
	if err := client.SendProposal(proposal); err != nil {
		t.Fatal(err)
	}
	toolResult, err := client.ReceiveResult()
	if err != nil || toolResult.Verdict != string(gate.Allow) {
		t.Fatalf("durable session write result=%+v err=%v", toolResult, err)
	}
	_ = client.Close()
	if err := <-runCh; err != nil {
		t.Fatal(err)
	}
	result, err := fixture.runtime.Finalize(context.Background())
	if result.TransactionID != "" || err == nil || !strings.Contains(err.Error(), "originating proposal receipt") {
		t.Fatalf("denied origin unexpectedly entered publication: result=%+v err=%v", result, err)
	}
	trace, err := fixture.publicationJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind == "publication_prepare" || record.Event.Kind == "workspace_commit_authorized" || record.Event.Kind == "publication_token_consumed" {
			t.Fatalf("origin denial persisted commit authority or publication effects: %+v", record.Event)
		}
	}
	if got, err := os.ReadFile(filepath.Join(fixture.base, "live", durableWritePath)); err != nil || string(got) != "baseline\n" {
		t.Fatalf("denied origin changed live workspace: %q err=%v", got, err)
	}
}

func TestPublicationRuntimeCrashQuarantinesRootAndRecoveryDoesNotReplay(t *testing.T) {
	fixture := newPublicationRuntimeFixture(t, func(point string, _ int) error {
		if point == "after-rename" {
			return errors.New("synthetic interrupted publication after rename")
		}
		return nil
	}, nil)
	client, supervisor, proposal := fixture.supervisor(t)
	runCh := make(chan runtimeRunResult, 1)
	go func() {
		result, err := fixture.runtime.Run(context.Background(), supervisor)
		runCh <- runtimeRunResult{result: result, err: err}
	}()
	if err := client.SendProposal(proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReceiveResult(); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	run := <-runCh
	if run.result.Status != publication.StatusUnknown || run.result.TransactionID == "" || run.err == nil {
		t.Fatalf("fault did not leave a durable unresolved publication: result=%+v err=%v", run.result, run.err)
	}
	path := filepath.Join(fixture.base, "live", durableWritePath)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var beforeStat syscall.Stat_t
	if err := syscall.Stat(path, &beforeStat); err != nil {
		t.Fatal(err)
	}
	traceBefore, err := fixture.publicationJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	_ = fixture.runtime.Close()
	options := fixture.finalizerOptions()
	options.WorkflowID, options.OwnerEpoch, options.PublicationID = "workflow-after-crash", 1, "publication-after-crash"
	options.Trust = fixture.trust
	currentRepoOpened := false
	options.OpenPublication = func() (*publication.Repository, error) {
		currentRepoOpened = true
		return nil, errors.New("new workflow must not open before old root recovery")
	}
	oldRepositoryOpened := false
	options.OpenRecovery = func(workflowID string) (workflow.RecoveryRepository, error) {
		oldRepositoryOpened = true
		if workflowID != runtimeWorkflowID {
			return nil, fmt.Errorf("unexpected recovery workflow %q", workflowID)
		}
		repository, err := publication.Acquire(publication.Options{WorkflowID: runtimeWorkflowID, OwnerEpoch: 2,
			StateRoot: fixture.publicationState, Journal: fixture.publicationJournal, Workspace: fixture.workspace})
		return repository, err
	}
	otherRuntime, err := NewPublicationRuntime(fixture.executor, options)
	if err != nil {
		t.Fatal(err)
	}
	client, supervisor, _ = fixture.supervisor(t)
	recoverErr := otherRuntime.Serve(context.Background(), supervisor)
	_ = client.Close()
	_ = supervisor.IPC.Close()
	_ = otherRuntime.Close()
	if !oldRepositoryOpened || currentRepoOpened || !errors.Is(recoverErr, workflow.ErrRootQuarantined) {
		t.Fatalf("UNKNOWN transaction did not block new workflow admission: oldRepo=%t currentRepo=%t err=%v", oldRepositoryOpened, currentRepoOpened, recoverErr)
	}
	traceAfter, err := fixture.publicationJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if countRuntimeAuditKind(traceAfter, "publication_token_consumed") != countRuntimeAuditKind(traceBefore, "publication_token_consumed") {
		t.Fatal("recovery replayed/consumed publication authority")
	}
	after, err := os.ReadFile(path)
	var afterStat syscall.Stat_t
	statErr := syscall.Stat(path, &afterStat)
	if err != nil || statErr != nil || string(after) != string(before) || beforeStat.Ino != afterStat.Ino {
		t.Fatalf("UNKNOWN recovery replayed/rolled back live bytes: before=%q after=%q inode=%d->%d err=%v stat=%v", before, after, beforeStat.Ino, afterStat.Ino, err, statErr)
	}
	ledger, err := workflow.NewRootLedger(fixture.publicationJournal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Begin(fixture.liveRoot, "different-workflow", 1, "different-publication", fixture.workspace); !errors.Is(err, workflow.ErrRootQuarantined) {
		t.Fatalf("cross-workflow crash quarantine was bypassed: %v", err)
	}
}

func TestActivePublicationOwnerBlocksRecoveryBeforeAndAfterPrepare(t *testing.T) {
	tests := []struct {
		name        string
		pauseAt     string
		wantPrepare int
	}{
		{name: "before prepare", pauseAt: "after-source-validation", wantPrepare: 0},
		{name: "after prepare", pauseAt: "after-prepare-checkpoint", wantPrepare: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entered, resume := make(chan struct{}), make(chan struct{})
			var pauseOnce sync.Once
			var resumeOnce sync.Once
			fixture := newPublicationRuntimeFixture(t, func(point string, _ int) error {
				if point == test.pauseAt {
					pauseOnce.Do(func() { close(entered) })
					<-resume
				}
				return nil
			}, nil)
			defer resumeOnce.Do(func() { close(resume) })
			client, supervisor, proposal := fixture.supervisor(t)
			runDone := make(chan runtimeRunResult, 1)
			go func() {
				result, err := fixture.runtime.Run(context.Background(), supervisor)
				runDone <- runtimeRunResult{result: result, err: err}
			}()
			if err := client.SendProposal(proposal); err != nil {
				t.Fatal(err)
			}
			if _, err := client.ReceiveResult(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("publication did not pause at %s", test.pauseAt)
			}
			ledger, err := workflow.NewRootLedger(fixture.publicationJournal)
			if err != nil {
				t.Fatal(err)
			}
			openerCalled := false
			recoveryErr := ledger.RecoverBeforeAdmission(context.Background(), fixture.liveRoot, func(string) (workflow.RecoveryRepository, error) {
				openerCalled = true
				return nil, errors.New("active owner must never be reconciled")
			})
			if !errors.Is(recoveryErr, workflow.ErrRootActive) || openerCalled {
				t.Fatalf("active publication was reconciled as abandoned: opener=%t err=%v", openerCalled, recoveryErr)
			}
			otherOptions := fixture.finalizerOptions()
			otherOptions.WorkflowID, otherOptions.OwnerEpoch, otherOptions.PublicationID = "workflow-concurrent", 1, "publication-concurrent"
			otherOptions.Trust = fixture.trust
			openedCurrentRepo := false
			otherOptions.OpenPublication = func() (*publication.Repository, error) {
				openedCurrentRepo = true
				return nil, errors.New("concurrent workflow must not open a publisher")
			}
			otherRuntime, err := NewPublicationRuntime(fixture.executor, otherOptions)
			if err != nil {
				t.Fatal(err)
			}
			otherClient, otherSupervisor, _ := fixture.supervisor(t)
			otherServeErr := otherRuntime.Serve(context.Background(), otherSupervisor)
			_ = otherClient.Close()
			_ = otherSupervisor.IPC.Close()
			_ = otherRuntime.Close()
			if !errors.Is(otherServeErr, workflow.ErrRootActive) || openedCurrentRepo {
				t.Fatalf("another workflow passed active-root recovery barrier: opened=%t err=%v", openedCurrentRepo, otherServeErr)
			}
			if _, err := ledger.Begin(fixture.liveRoot, "workflow-direct-begin", 1, "publication-direct-begin", fixture.workspace); !errors.Is(err, workflow.ErrRootActive) {
				t.Fatalf("second begin did not observe the active root reservation: %v", err)
			}
			pausedTrace, err := fixture.publicationJournal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			if countRuntimeAuditKind(pausedTrace, "workflow_publication_resolved") != 0 ||
				countRuntimeAuditKind(pausedTrace, "publication_prepare") != test.wantPrepare ||
				countRuntimeAuditKind(pausedTrace, "publication_token_consumed") != 0 {
				t.Fatalf("active owner marker/effects changed at pause: %+v", pausedTrace.Records)
			}
			if got, err := os.ReadFile(filepath.Join(fixture.base, "live", durableWritePath)); err != nil || string(got) != "baseline\n" {
				t.Fatalf("active publisher modified live tree before token/apply: %q err=%v", got, err)
			}
			_ = client.Close()
			resumeOnce.Do(func() { close(resume) })
			run := <-runDone
			if run.err != nil || run.result.Status != publication.StatusSucceeded {
				t.Fatalf("publication did not continue after the active-owner barrier test: %+v", run)
			}
		})
	}
}

func TestConcurrentCloseDuringUnknownFinalizeReleasesOwnerButKeepsQuarantine(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var pauseOnce, releaseOnce sync.Once
	fixture := newPublicationRuntimeFixture(t, func(point string, _ int) error {
		if point == "after-prepare-checkpoint" {
			pauseOnce.Do(func() { close(entered) })
			<-release
			return errors.New("synthetic pre-token UNKNOWN")
		}
		return nil
	}, nil)
	defer releaseOnce.Do(func() { close(release) })
	client, supervisor, proposal := fixture.supervisor(t)
	runDone := make(chan runtimeRunResult, 1)
	go func() {
		result, err := fixture.runtime.Run(context.Background(), supervisor)
		runDone <- runtimeRunResult{result: result, err: err}
	}()
	if err := client.SendProposal(proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReceiveResult(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("finalizer did not reach the prepared UNKNOWN barrier")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.runtime.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while Finalize still owned the active publication: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	_ = client.Close()
	releaseOnce.Do(func() { close(release) })
	run := <-runDone
	closeErr := <-closeDone
	if closeErr != nil || run.result.Status != publication.StatusUnknown || run.result.TransactionID == "" || run.err == nil {
		t.Fatalf("concurrent close/finalize outcome lost UNKNOWN state: close=%v run=%+v", closeErr, run)
	}
	ledger, err := workflow.NewRootLedger(fixture.publicationJournal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Begin(fixture.liveRoot, "workflow-after-unknown", 1, "publication-after-unknown", fixture.workspace); !errors.Is(err, workflow.ErrRootQuarantined) || errors.Is(err, workflow.ErrRootActive) {
		t.Fatalf("UNKNOWN did not release the in-process owner while retaining durable quarantine: %v", err)
	}
	trace, err := fixture.publicationJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if countRuntimeAuditKind(trace, "workflow_publication_pending") != 1 ||
		countRuntimeAuditKind(trace, "workflow_publication_resolved") != 0 ||
		countRuntimeAuditKind(trace, "publication_prepare") != 1 ||
		countRuntimeAuditKind(trace, "publication_token_consumed") != 0 {
		t.Fatalf("Close/finalize released or settled durable quarantine incorrectly: %+v", trace.Records)
	}
}

func TestPublicationRuntimeRecoversPreTokenCrashBeforeNextWorkflow(t *testing.T) {
	fixture := newPublicationRuntimeFixture(t, func(point string, _ int) error {
		if point == "after-prepare-checkpoint" {
			return errors.New("synthetic stop before artifact and token")
		}
		return nil
	}, nil)
	client, supervisor, proposal := fixture.supervisor(t)
	runCh := make(chan runtimeRunResult, 1)
	go func() {
		result, err := fixture.runtime.Run(context.Background(), supervisor)
		runCh <- runtimeRunResult{result: result, err: err}
	}()
	if err := client.SendProposal(proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReceiveResult(); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	run := <-runCh
	if run.result.TransactionID == "" || run.err == nil {
		t.Fatalf("pre-token fault was not durably identifiable: result=%+v err=%v", run.result, run.err)
	}
	_ = fixture.runtime.Close()
	ledger, err := workflow.NewRootLedger(fixture.publicationJournal)
	if err != nil {
		t.Fatal(err)
	}
	opened := false
	err = ledger.RecoverBeforeAdmission(context.Background(), fixture.liveRoot, func(workflowID string) (workflow.RecoveryRepository, error) {
		opened = true
		if workflowID != runtimeWorkflowID {
			return nil, fmt.Errorf("unexpected workflow recovery request %q", workflowID)
		}
		return publication.Acquire(publication.Options{WorkflowID: runtimeWorkflowID, OwnerEpoch: 2,
			StateRoot: fixture.publicationState, Journal: fixture.publicationJournal, Workspace: fixture.workspace})
	})
	if err != nil || !opened {
		t.Fatalf("pre-token recovery did not close the old workflow before admission: opened=%t err=%v", opened, err)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.base, "live", durableWritePath)); err != nil || string(got) != "baseline\n" {
		t.Fatalf("no-token recovery changed the live workspace: %q err=%v", got, err)
	}
	trace, err := fixture.publicationJournal.Trace()
	if err != nil || countRuntimeAuditKind(trace, "publication_token_consumed") != 0 || countRuntimeAuditKind(trace, "publication_aborted") != 1 {
		t.Fatalf("pre-token recovery consumed authority or lacks an abort record: trace=%+v err=%v", trace, err)
	}
	reservation, err := ledger.Begin(fixture.liveRoot, "workflow-after-recovery", 1, "publication-after-recovery", fixture.workspace)
	if err != nil {
		t.Fatalf("known pre-token failure left the root quarantined: %v", err)
	}
	if err := ledger.Resolve(reservation, fixture.liveRoot, "workflow-after-recovery", "publication-after-recovery", "", publication.StatusFailed); err != nil {
		t.Fatal(err)
	}
	reservation.Release()
}

type runtimeRunResult struct {
	result publication.Result
	err    error
}

type publicationRuntimeFixture struct {
	t                  *testing.T
	base               string
	store              *sessionrepo.Store
	storeJournal       *audit.Journal
	publicationJournal *audit.Journal
	publicationState   *os.File
	liveRoot           *os.File
	baseline           *sessionrepo.Generation
	executor           *DurableExecutor
	runtime            *PublicationRuntime
	workspace          workspace.Options
	authority          *runtimeSessionAuthority
	trust              *runtimeFixtureTrust
	runner             *runtimeFixtureCommandRunner
}

func newPublicationRuntimeFixture(t *testing.T, inject publication.FaultInjector, customTrust *runtimeFixtureTrust) *publicationRuntimeFixture {
	t.Helper()
	base := durablePrivateBase(t)
	for _, path := range []string{"source", "live", "session", "session-audit", "publication-state"} {
		durableMkdir(t, filepath.Join(base, path), 0o700)
	}
	durableWriteFile(t, filepath.Join(base, "source", durableWritePath), "baseline\n", 0o644)
	durableMkdir(t, filepath.Join(base, "source", "build"), 0o755)
	durableWriteFile(t, filepath.Join(base, "source", "build", "result.txt"), "baseline result\n", 0o644)
	durableWriteFile(t, filepath.Join(base, "live", durableWritePath), "baseline\n", 0o644)
	durableMkdir(t, filepath.Join(base, "live", "build"), 0o755)
	durableWriteFile(t, filepath.Join(base, "live", "build", "result.txt"), "baseline result\n", 0o644)
	storeJournal, err := audit.Open(filepath.Join(base, "session-audit", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	publicationJournal, err := audit.Open(filepath.Join(base, "publication-state", "journal.jsonl"))
	if err != nil {
		_ = storeJournal.Close()
		t.Fatal(err)
	}
	publicationState := durableOpenRoot(t, filepath.Join(base, "publication-state"))
	liveRoot := durableOpenRoot(t, filepath.Join(base, "live"))
	source := durableOpenRoot(t, filepath.Join(base, "source"))
	authority := &runtimeSessionAuthority{requests: make(map[string]sessionrepo.OperationRequest),
		callIssuer: durableCallIssuer, policyDigest: durablePolicyDigest, metadataDigest: durableMetadataDigest}
	storeOptions := sessionrepo.Options{
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		Limits:          workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
		Journal:         storeJournal, AuthorizeOperation: authority.authorize, VerifyDecision: authority.verifyDecision,
		VerifySettlement: authority.verifySettlement,
	}
	store, err := sessionrepo.Create(durableOpenRoot(t, filepath.Join(base, "session")), storeOptions)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	proposals, _ := runtimeFourToolProposals()
	bashDecision := gate.Decision{Verdict: gate.Allow, Tool: "bash", ToolCallID: proposals[3].ToolCallID, Sequence: 4,
		ResponseID: &correlation.Identifier{Issuer: durableResponseIssuer, Opaque: durableResponseID}}
	bashOperation, err := durableOperationIdentity(proposals[3], bashDecision, durableCallIssuer)
	if err != nil {
		t.Fatal(err)
	}
	commandRunner := &runtimeFixtureCommandRunner{leaseID: bashOperation.LeaseID}
	executor, err := NewDurableExecutor(DurableExecutorConfig{Store: store, Tip: baseline, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest, CommandRunner: commandRunner})
	if err != nil {
		t.Fatal(err)
	}
	workspaceOptions := workspace.Options{Generation: "g0", MetadataPolicyDigest: durableMetadataDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true}}
	trust := customTrust
	if trust == nil {
		trust = &runtimeFixtureTrust{authority: authority, callIssuer: durableCallIssuer, callID: durableCallID, sequence: 1,
			responseID: durableResponseID, responseIssuer: durableResponseIssuer, policyDigest: durablePolicyDigest,
			metadataDigest: durableMetadataDigest}
	} else {
		trust.authority, trust.callIssuer, trust.callID, trust.sequence = authority, durableCallIssuer, durableCallID, 1
		trust.responseID, trust.responseIssuer = durableResponseID, durableResponseIssuer
		trust.policyDigest, trust.metadataDigest = durablePolicyDigest, durableMetadataDigest
	}
	ledger, err := workflow.NewRootLedger(publicationJournal)
	if err != nil {
		t.Fatal(err)
	}
	openPublication := func(epoch uint64) func() (*publication.Repository, error) {
		return func() (*publication.Repository, error) {
			return publication.Acquire(publication.Options{WorkflowID: runtimeWorkflowID, OwnerEpoch: epoch,
				StateRoot: publicationState, Journal: publicationJournal, Workspace: workspaceOptions})
		}
	}
	finalizerOptions := workflow.FinalizerOptions{
		WorkflowID: runtimeWorkflowID, OwnerEpoch: 1, PublicationID: runtimePublicationID,
		LiveRoot: liveRoot, Store: store, Baseline: baseline, Workspace: workspaceOptions,
		Journal: publicationJournal, Ledger: ledger, OpenPublication: openPublication(1),
		OpenRecovery: func(workflowID string) (workflow.RecoveryRepository, error) {
			if workflowID != runtimeWorkflowID {
				return nil, fmt.Errorf("no test recovery state registered for %q", workflowID)
			}
			return openPublication(2)()
		},
		Trust: trust, FaultInjector: inject,
	}
	runtime, err := NewPublicationRuntime(executor, finalizerOptions)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &publicationRuntimeFixture{t: t, base: base, store: store, storeJournal: storeJournal,
		publicationJournal: publicationJournal, publicationState: publicationState, liveRoot: liveRoot,
		baseline: baseline, executor: executor, runtime: runtime, workspace: workspaceOptions,
		authority: authority, trust: trust, runner: commandRunner}
	t.Cleanup(func() {
		_ = runtime.Close()
		_ = store.Close()
		_ = liveRoot.Close()
		_ = publicationState.Close()
		_ = storeJournal.Close()
		_ = publicationJournal.Close()
	})
	return fixture
}

func (f *publicationRuntimeFixture) supervisor(t *testing.T) (*ipc.Client, *Supervisor, protocol.Proposal) {
	t.Helper()
	arguments := json.RawMessage(`{"path":"` + durableWritePath + `","content":"` + durableWriteContent + `"}`)
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: durableCallID,
		Tool: "write", Arguments: arguments}
	client, supervisor := f.supervisorFor(t, []protocol.Proposal{proposal}, []string{"g0"})
	return client, supervisor, proposal
}

func (f *publicationRuntimeFixture) supervisorFor(t *testing.T, proposals []protocol.Proposal, generations []string) (*ipc.Client, *Supervisor) {
	t.Helper()
	if len(proposals) == 0 || len(proposals) != len(generations) {
		t.Fatal("test provider fixture requires one generation label per captured proposal")
	}
	stream, err := protocol.NewStream(durableCallIssuer, durableResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	for index, proposal := range proposals {
		digest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		if decision := stream.Capture(protocol.TrustedCapture{
			ResponseID: correlation.Identifier{Issuer: durableResponseIssuer, Opaque: durableResponseID},
			ToolCallID: correlation.Identifier{Issuer: durableCallIssuer, Opaque: proposal.ToolCallID},
			ToolName:   proposal.Tool, RawArguments: proposal.Arguments, CanonicalizationProfile: protocol.CanonicalizationProfile,
			CanonicalArgumentsDigest: digest, Sequence: uint64(index + 1), Generation: generations[index],
		}); !decision.Accepted {
			t.Fatalf("capture trusted deterministic broker fixture: %+v", decision)
		}
	}
	last := proposals[len(proposals)-1]
	f.trust.callID = last.ToolCallID
	f.trust.sequence = uint64(len(proposals))
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow},
		{Tool: "edit", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := ipc.NewPipe(integrationToken)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewAuditDecisionRecorder(f.storeJournal)
	if err != nil {
		t.Fatal(err)
	}
	return client, &Supervisor{IPC: server, Broker: &streamBroker{stream: stream, events: &[]string{}, mu: &sync.Mutex{}},
		Policy: policy, Decisions: recorder, ProposalLimit: uint64(len(proposals))}
}

func runtimeFourToolProposals() ([]protocol.Proposal, []string) {
	proposals := []protocol.Proposal{
		{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "call-runtime-read", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "call-runtime-write", Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"after-write\n"}`)},
		{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "call-runtime-edit", Tool: "edit", Arguments: json.RawMessage(`{"path":"task.txt","edits":[{"oldText":"after-write","newText":"after-edit"}]}`)},
		{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "call-runtime-bash", Tool: "bash", Arguments: json.RawMessage(`{"command":"fixture-build"}`)},
		{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: durableCallID, Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"published-final\n"}`)},
	}
	return proposals, []string{"g0", "g0", "g1", "g2", "g3"}
}

type runtimeSessionAuthority struct {
	mu             sync.Mutex
	requests       map[string]sessionrepo.OperationRequest
	usedOperations map[delta.OperationIdentity]bool
	callIssuer     string
	policyDigest   string
	metadataDigest string
}

func (a *runtimeSessionAuthority) authorize(request sessionrepo.OperationRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if request.Decision.Outcome != delta.PolicyAllow || request.Decision.PolicyDigest != a.policyDigest ||
		request.Decision.MetadataPolicyDigest != a.metadataDigest || request.Decision.ID == "" || request.EffectID == "" ||
		request.InputGeneration == "" || request.InputTreeDigest == "" || request.ArgumentDigest == "" {
		return errors.New("test authority received an incomplete durable operation request")
	}
	switch request.Tool {
	case "read", "write", "edit":
		if request.Operation.Kind != delta.OperationProposalCall || request.Operation.CallIssuer != a.callIssuer ||
			request.Operation.CallID == "" || request.Operation.ProposalID == "" {
			return errors.New("test authority rejected a malformed proposal-call identity")
		}
	case "bash":
		if request.Operation.Kind != delta.OperationLease || request.Operation.LeaseID == "" || request.ViewID == "" || request.ExecutionContextDigest == "" {
			return errors.New("test authority rejected a malformed command lease")
		}
	default:
		return fmt.Errorf("test authority does not authorize tool %q", request.Tool)
	}
	if _, exists := a.requests[request.Decision.ID]; exists || a.usedOperations[request.Operation] {
		return errors.New("test authority detected a replayed decision or operation")
	}
	if a.usedOperations == nil {
		a.usedOperations = make(map[delta.OperationIdentity]bool)
	}
	a.requests[request.Decision.ID] = request
	a.usedOperations[request.Operation] = true
	return nil
}

func (a *runtimeSessionAuthority) verifyDecision(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	request, ok := a.requests[decision.ID]
	if !ok || decision != request.Decision || binding.Tool != request.Tool || binding.Operation != request.Operation ||
		binding.ArgumentDigest != request.ArgumentDigest || binding.InputGeneration != request.InputGeneration ||
		binding.InputTreeDigest != request.InputTreeDigest || binding.OutputGeneration == "" || binding.OutputTreeDigest == "" ||
		len(binding.Changes) == 0 || request.Tool == "bash" &&
		(binding.ViewID != request.ViewID || binding.ExecutionContextDigest != request.ExecutionContextDigest) {
		return errors.New("test authority rejected a transition not bound to its admitted durable operation")
	}
	return nil
}

func (a *runtimeSessionAuthority) verifySettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	request, ok := func() (sessionrepo.OperationRequest, bool) {
		for _, request := range a.requests {
			if request.Operation.Kind == delta.OperationLease && request.Operation.LeaseID == leaseID {
				return request, true
			}
		}
		return sessionrepo.OperationRequest{}, false
	}()
	if !ok || request.ViewID != viewID || settlement.LeaseID != leaseID || settlement.ViewID != viewID ||
		!settlement.ProcessScopeEmpty || !settlement.WritersStopped || !settlement.MountDetached || !settlement.ExitObserved ||
		settlement.EvidenceClass != "synthetic-runtime-command-runner" {
		return errors.New("synthetic command settlement did not match the one-use test lease")
	}
	return nil
}

// runtimeFixtureCommandRunner mutates only the supervisor-owned disposable
// command view through its descriptor. It checks the bounded spec and never
// launches /bin/bash or any host process; its settlement is test data, not
// evidence of production containment.
type runtimeFixtureCommandRunner struct {
	leaseID string
	calls   int
	entered chan struct{}
	release <-chan struct{}
}

func (r *runtimeFixtureCommandRunner) Profile() string {
	return "synthetic-runtime-fixture-command-runner"
}

func (r *runtimeFixtureCommandRunner) Run(ctx context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	r.calls++
	if r.calls != 1 || spec.Executable != "/bin/bash" || !reflect.DeepEqual(spec.Args, []string{"-lc", "fixture-build"}) {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("fixture runner rejected the requested command spec")
	}
	if r.entered != nil {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ctx.Err()
		}
	}
	root, err := view.MountSource()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	defer root.Close()
	fd, err := syscall.Openat(int(root.Fd()), "build/result.txt", syscall.O_WRONLY|syscall.O_TRUNC|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	file := os.NewFile(uintptr(fd), "fixture-command-output")
	defer file.Close()
	if err := syscall.Fchown(fd, syscall.Geteuid(), syscall.Getegid()); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if err := syscall.Fchmod(fd, 0o644); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if _, err := file.Write([]byte("synthetic-bash-output\n")); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if err := file.Sync(); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	settlement := sessionrepo.CommandSettlement{LeaseID: r.leaseID, ViewID: view.ID(), ProcessScopeEmpty: true,
		WritersStopped: true, MountDetached: true, ExitObserved: true, ExitCode: 0,
		EvidenceClass: "synthetic-runtime-command-runner"}
	return sessionrepo.CommandResult{ExitObserved: true, Stdout: []byte("fixture command completed\n")}, settlement, nil
}

func (f *publicationRuntimeFixture) finalizerOptions() workflow.FinalizerOptions {
	return workflow.FinalizerOptions{WorkflowID: runtimeWorkflowID, OwnerEpoch: 2, PublicationID: "publication-missing-trust",
		LiveRoot: f.liveRoot, Store: f.store, Baseline: f.baseline, CurrentTip: f.executor.Tip,
		Workspace: f.workspace, Journal: f.publicationJournal, OpenPublication: func() (*publication.Repository, error) { return nil, nil },
		OpenRecovery: func(string) (workflow.RecoveryRepository, error) { return nil, errors.New("test recovery opener") },
		Ledger:       func() *workflow.RootLedger { ledger, _ := workflow.NewRootLedger(f.publicationJournal); return ledger }()}
}

type runtimeFixtureTrust struct {
	authority      *runtimeSessionAuthority
	callIssuer     string
	callID         string
	sequence       uint64
	responseID     string
	responseIssuer string
	policyDigest   string
	metadataDigest string
	denyOrigin     bool
	denyAdmission  bool
}

func (trust *runtimeFixtureTrust) CheckAdmission(context.Context) error {
	if trust == nil || trust.denyAdmission {
		return errors.New("explicit disposable test-profile refusal")
	}
	return nil
}

func (trust *runtimeFixtureTrust) CheckPublicationPrerequisites(_ context.Context, request workflow.EvidenceRequest) error {
	if request.Bundle.RealProviderExchange || request.Bundle.PiAdapterWired || request.Bundle.CommandContainmentStatus != "not-established" || len(request.Bundle.AuditJournal) == 0 {
		return errors.New("fixture refuses to mislabel provider, Pi, or containment evidence")
	}
	for _, operation := range request.Bundle.Operations {
		if operation.Tool == "bash" && (operation.CommandContainmentStatus != "not-established" ||
			operation.CommandRunnerProfile != "synthetic-runtime-fixture-command-runner") {
			return errors.New("fixture refuses to treat synthetic Bash settlement as production containment")
		}
	}
	return nil
}

func (trust *runtimeFixtureTrust) AttestQuiescent(_ context.Context, live *os.File, baseline, tip *sessionrepo.Generation) error {
	if live == nil || baseline == nil || tip == nil || tip.ID() == baseline.ID() {
		return errors.New("fixture has no drained registered session roots")
	}
	return nil
}

func (trust *runtimeFixtureTrust) VerifyTransition(_ context.Context, decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	if trust.authority == nil {
		return errors.New("no test exact-transition authority is installed")
	}
	return trust.authority.verifyDecision(decision, binding)
}

func (trust *runtimeFixtureTrust) VerifyOrigin(_ context.Context, bundle sessionrepo.EvidenceBundle, transition delta.Transition) error {
	if trust.denyOrigin {
		return errors.New("explicit fixture origin-receipt denial")
	}
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil {
		return err
	}
	matched := 0
	for _, record := range trace.Records {
		if record.Event.Kind != durableDecisionEventKind {
			continue
		}
		var event durableDecisionEvent
		if err := json.Unmarshal(record.Event.Data, &event); err != nil {
			return err
		}
		if event.Proposal.ToolCallID != trust.callID || event.Decision.Verdict != gate.Allow || event.Decision.ResponseID == nil ||
			event.Decision.ResponseID.Issuer != trust.responseIssuer || event.Decision.ResponseID.Opaque != trust.responseID ||
			event.Decision.PolicyDigest == "" || event.Decision.Sequence != trust.sequence {
			continue
		}
		operation, err := durableOperationIdentity(event.Proposal, event.Decision, trust.callIssuer)
		if err != nil || !reflect.DeepEqual(operation, transition.Operation) {
			continue
		}
		policyDecision, err := durablePolicyDecision(event.Proposal, event.Decision, trust.policyDigest, trust.metadataDigest)
		if err != nil || policyDecision != transition.Decision {
			continue
		}
		matched++
	}
	if matched != 1 || transition.Operation.Kind != delta.OperationProposalCall || transition.Operation.ProposalID == "" {
		return fmt.Errorf("verified evidence bundle contains %d matching durable provider/gate origin receipts", matched)
	}
	return nil
}

func summaryProposalID(t *testing.T, proposal protocol.Proposal, result protocol.Result) string {
	t.Helper()
	operation, err := durableOperationIdentity(proposal, gate.Decision{Verdict: gate.Allow, Tool: proposal.Tool,
		ToolCallID: proposal.ToolCallID, Sequence: result.Sequence, ResponseID: result.ResponseID}, durableCallIssuer)
	if err != nil {
		t.Fatal(err)
	}
	return operation.ProposalID
}

func countRuntimeAuditKind(trace audit.Trace, kind string) int {
	count := 0
	for _, record := range trace.Records {
		if record.Event.Kind == kind {
			count++
		}
	}
	return count
}
