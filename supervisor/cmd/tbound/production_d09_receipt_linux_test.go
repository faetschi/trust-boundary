//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

type d09ReceiptInvokerFixture struct {
	result       sessionrepo.CommandResult
	settlement   sessionrepo.CommandSettlement
	receipt      piruntime.D09CommandReceipt
	runErr       error
	takeErr      error
	runCalls     int
	takeCalls    int
	seenView     *sessionrepo.CommandView
	seenSpec     sessionrepo.CommandSpec
	seenResult   sessionrepo.CommandResult
	seenTerminal sessionrepo.CommandSettlement
}

type failingProductionEvidenceBinder struct {
	inner *durableBashStubRunner
	err   error
}

func (runner *failingProductionEvidenceBinder) Profile() string {
	return piruntime.D09CommandRunnerProfile
}

func (runner *failingProductionEvidenceBinder) Run(ctx context.Context, view *sessionrepo.CommandView,
	spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	return runner.inner.Run(ctx, view, spec)
}

func (runner *failingProductionEvidenceBinder) BindProductionBashEvidence(context.Context, delta.OperationIdentity,
	sessionrepo.EvidenceBundle, delta.Transition, sessionrepo.OperationEvidence) error {
	return runner.err
}

func (fixture *d09ReceiptInvokerFixture) Run(_ context.Context, view *sessionrepo.CommandView,
	spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	fixture.runCalls++
	fixture.seenView, fixture.seenSpec = view, spec
	return fixture.result, fixture.settlement, fixture.runErr
}

func (fixture *d09ReceiptInvokerFixture) TakeCommandReceipt(view *sessionrepo.CommandView, spec sessionrepo.CommandSpec,
	result sessionrepo.CommandResult, settlement sessionrepo.CommandSettlement) (piruntime.D09CommandReceipt, error) {
	fixture.takeCalls++
	fixture.seenView, fixture.seenSpec, fixture.seenResult, fixture.seenTerminal = view, spec, result, settlement
	return fixture.receipt, fixture.takeErr
}

func TestProductionD09BashRunnerWithholdsResultWhenReceiptUnavailableOrSnapshotOnly(t *testing.T) {
	for _, name := range []string{"take-refused", "snapshot-without-live-seal"} {
		t.Run(name, func(t *testing.T) {
			root := privateGovernedTestRoot(t)
			journal, err := audit.Open(filepath.Join(root, "command-audit.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			fixture := &d09ReceiptInvokerFixture{
				result: sessionrepo.CommandResult{ExitObserved: true, ExitCode: 0, Stdout: []byte("ok\n")},
				settlement: sessionrepo.CommandSettlement{LeaseID: "lease-test", ViewID: "view-test", ExitObserved: true, ExitCode: 0,
					ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true,
					EvidenceClass: piruntime.D09CommandRunnerProfile + ":fixture"},
			}
			if name == "take-refused" {
				fixture.takeErr = piruntime.ErrD09CommandReceiptInvalid
			} else {
				// This snapshot is structurally valid and carries its canonical public
				// digest. It still lacks Runtime A's private issuance seal and cannot
				// be used as a production receipt.
				fixture.receipt = structurallyValidReceiptSnapshot(t)
				if err := fixture.receipt.ValidateSnapshot(); err != nil {
					t.Fatalf("forged public snapshot fixture is not structurally valid: %v", err)
				}
				if err := fixture.receipt.Validate(); !errors.Is(err, piruntime.ErrD09CommandReceiptInvalid) {
					t.Fatalf("snapshot-only receipt unexpectedly has live issuance authority: %v", err)
				}
			}
			runner := newProductionD09BashRunnerWithInvoker("lease-test", fixture, journal,
				"run-test", "session-test", "workflow-test", "conversation-test",
				piruntime.HostProfile{Mode: piruntime.ModeProduction}, piruntime.D09CellTemplate{})
			view := &sessionrepo.CommandView{}
			spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}}
			result, settlement, err := runner.Run(context.Background(), view, spec)
			if !errors.Is(err, ErrProductionCommandEvidence) || !reflect.DeepEqual(result, sessionrepo.CommandResult{}) ||
				settlement != (sessionrepo.CommandSettlement{}) {
				t.Fatalf("invalid or absent live receipt released result/settlement: result=%+v settlement=%+v err=%v", result, settlement, err)
			}
			if fixture.runCalls != 1 || fixture.takeCalls != 1 || fixture.seenView != view ||
				!reflect.DeepEqual(fixture.seenSpec, spec) || !reflect.DeepEqual(fixture.seenResult, fixture.result) ||
				!reflect.DeepEqual(fixture.seenTerminal, fixture.settlement) {
				t.Fatalf("receipt was not taken against the exact immediately preceding invocation: %+v", fixture)
			}
			trace, err := journal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			if len(trace.Records) != 0 || len(runner.evidence) != 0 {
				t.Fatalf("failed receipt access was journaled or retained as evidence: records=%+v evidence=%+v", trace.Records, runner.evidence)
			}
		})
	}
}

func structurallyValidReceiptSnapshot(t *testing.T) piruntime.D09CommandReceipt {
	t.Helper()
	imageDigest := "sha256:" + stringsOf('a', 64)
	templateDigest := "sha256:" + stringsOf('b', 64)
	proofDigest := "sha256:" + stringsOf('c', 64)
	cellName, leaseID, viewID := "cell-test", "lease-test", "view-test"
	imageRef := "localhost/tbound/test@" + imageDigest
	spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}}
	result := sessionrepo.CommandResult{ExitObserved: true, ExitCode: 0, Stdout: []byte("ok\n")}
	settlement := sessionrepo.CommandSettlement{LeaseID: leaseID, ViewID: viewID, ExitObserved: true,
		ExitCode: 0, ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true,
		EvidenceClass: piruntime.D09CommandRunnerProfile + ":test-only"}
	proof := piruntime.D09CommandProof{UID: 1000, GID: 1000, CapEff: "0000000000000000", NoNewPrivs: true,
		Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: proofDigest, LandlockTSYNC: true, CgroupV2: true,
		LeaderPID: 101, ContainerLeaderPID: 1, LeaseID: leaseID, ViewID: viewID, CommandStarted: true,
		CommandReady: true, CommandExitObserved: true, CommandPath: spec.Executable, CommandDigest: proofDigest,
		ContainerName: cellName, ImageRef: imageRef, ImageDigest: imageDigest, TemplateDigest: templateDigest}
	evidence := piruntime.D09CommandSettlementEvidence{ContainerExitInspected: true, FinalCommandProofRead: true,
		PodmanWaitCompleted: true, OutputWritersDrained: true, ContainerRemoved: true, LeaderPidfdExited: true,
		CgroupPopulatedZero: true, MountNamespaceReleased: true, CommandOutputWithinBound: true, CommandCgroupRemoved: true}
	proofBytes, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	settlementBytes, err := json.Marshal(settlement)
	if err != nil {
		t.Fatal(err)
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	receipt := piruntime.D09CommandReceipt{SchemaVersion: "tbound-d09-command-receipt/v1", LeaseID: leaseID,
		ViewID: viewID, CellName: cellName, ContainerID: stringsOf('d', 64), ImageRef: imageRef,
		ImageDigest: imageDigest, TemplateDigest: templateDigest, CommandSpecDigest: piruntime.DigestBytes(specBytes),
		ResultDigest: piruntime.DigestBytes(resultBytes), SettlementDigest: piruntime.DigestBytes(settlementBytes),
		ProofDigest: piruntime.DigestBytes(proofBytes), ExitCode: 0, CommandSpec: spec, Proof: proof,
		Settlement: settlement, Evidence: evidence}
	type receiptBodyWire struct {
		SchemaVersion     string                                 `json:"schema_version"`
		LeaseID           string                                 `json:"lease_id"`
		ViewID            string                                 `json:"view_id"`
		CellName          string                                 `json:"cell_name"`
		ContainerID       string                                 `json:"container_id"`
		ImageRef          string                                 `json:"image_ref"`
		ImageDigest       string                                 `json:"image_digest"`
		TemplateDigest    string                                 `json:"template_digest"`
		CommandSpecDigest string                                 `json:"command_spec_digest"`
		ResultDigest      string                                 `json:"result_digest"`
		SettlementDigest  string                                 `json:"settlement_digest"`
		ProofDigest       string                                 `json:"proof_digest"`
		ExitCode          int                                    `json:"exit_code"`
		CommandSpec       sessionrepo.CommandSpec                `json:"command_spec"`
		Proof             piruntime.D09CommandProof              `json:"proof"`
		Settlement        sessionrepo.CommandSettlement          `json:"settlement"`
		Evidence          piruntime.D09CommandSettlementEvidence `json:"evidence"`
	}
	body, err := json.Marshal(receiptBodyWire{receipt.SchemaVersion, receipt.LeaseID, receipt.ViewID, receipt.CellName,
		receipt.ContainerID, receipt.ImageRef, receipt.ImageDigest, receipt.TemplateDigest, receipt.CommandSpecDigest,
		receipt.ResultDigest, receipt.SettlementDigest, receipt.ProofDigest, receipt.ExitCode, receipt.CommandSpec,
		receipt.Proof, receipt.Settlement, receipt.Evidence})
	if err != nil {
		t.Fatal(err)
	}
	receipt.ReceiptDigest = piruntime.DigestBytes(body)
	return receipt
}

func TestDurableExecutorWithholdsResultAndQuarantinesWhenD09EvidenceJournalFails(t *testing.T) {
	store, _, _, baseline := durableBashFixture(t)
	command := "printf bash-output"
	proposal, decision := durableBashProposal(`{"command":"` + command + `","timeout":5}`)
	leaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	journalErr := errors.New("simulated command proof-journal sync failure")
	runner := &failingProductionEvidenceBinder{inner: durableConsistentBashRunner(leaseID), err: journalErr}
	executor, err := NewDurableExecutor(DurableExecutorConfig{Store: store, Tip: baseline,
		CallIssuer: durableCallIssuer, PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		BashOutputPaths: []string{},
		CommandRunner:   runner})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), proposal, decision)
	if !errors.Is(err, journalErr) || len(result) != 0 {
		bundle, bundleErr := store.EvidenceBundle()
		t.Fatalf("D09 evidence journal failure released the Bash result: result=%q err=%v bundleErr=%v operations=%+v ledger=%+v",
			result, err, bundleErr, bundle.Operations, bundle.ApprovedDeltaLedger)
	}
	if executor.Tip() == nil || executor.Tip().ID() != "g1" {
		t.Fatalf("test expected private Store mutation before evidence journal refusal, tip=%v", executor.Tip())
	}
	if _, err := executor.Execute(context.Background(), proposal, decision); err == nil || !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("DurableExecutor did not quarantine after unknown proof-journal disposition: %v", err)
	}
}

func TestProductionD09CommandEventBindsCallOperationDeltaAndSnapshot(t *testing.T) {
	runner := &productionD09BashRunner{runID: "run-1", sessionID: "session-1", workflowID: "workflow-1",
		conversationID: "conversation-1", profile: piruntime.HostProfile{ID: "profile-1", Digest: "sha256:" + stringsOf('a', 64)}}
	providerCall := delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: "proposal-1",
		CallIssuer: "provider-calls/v1", CallID: "call-1"}
	operation := sessionrepo.OperationEvidence{Tool: "bash", EffectID: "effect-1",
		Operation: delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: "lease-1"},
		ViewID:    "view-1", ArgumentDigest: "sha256:" + stringsOf('b', 64),
		InputGeneration: "g1", InputTreeDigest: "tree-in", OutputGeneration: "g2", OutputTreeDigest: "tree-out",
		ResultDigest: "sha256:" + stringsOf('c', 64), Outcome: "success", ExitCode: intPointer(0),
		CommandContainmentStatus: "not-established", CommandRunnerProfile: piruntime.D09CommandRunnerProfile}
	transition := delta.Transition{ID: "transition-1", Sequence: 2, Tool: "bash", ArgumentDigest: operation.ArgumentDigest,
		ViewID: operation.ViewID, InputGeneration: operation.InputGeneration, OutputGeneration: operation.OutputGeneration,
		InputTreeDigest: operation.InputTreeDigest, OutputTreeDigest: operation.OutputTreeDigest,
		Operation: operation.Operation, Changes: []delta.ObjectChange{{Path: "task.txt"}}}
	retained := productionD09RunEvidence{receiptDigest: "sha256:" + stringsOf('d', 64),
		receiptSnapshot: []byte(`{"snapshot":"public"}`)}
	baseline := productionD09CommandSettledEvent{SchemaVersion: "tbound-production-d09-command-settled/v2",
		RunID: runner.runID, SessionID: runner.sessionID, WorkflowID: runner.workflowID, ConversationID: runner.conversationID,
		ProfileID: runner.profile.ID, ProfileDigest: runner.profile.Digest, ProviderCall: providerCall,
		Operation: operation, Transition: transition, ReceiptDigest: retained.receiptDigest,
		Receipt: append([]byte(nil), retained.receiptSnapshot...)}
	if err := compareProductionD09CommandEvent(runner, baseline, retained, providerCall, operation, transition); err != nil {
		t.Fatalf("exact event binding refused: %v", err)
	}
	for name, mutate := range map[string]func(*productionD09CommandSettledEvent){
		"schema": func(event *productionD09CommandSettledEvent) { event.SchemaVersion += "/unknown" },
		"run":    func(event *productionD09CommandSettledEvent) { event.RunID = "other-run" },
		"call":   func(event *productionD09CommandSettledEvent) { event.ProviderCall.CallID = "earlier-edit-call" },
		"lease":  func(event *productionD09CommandSettledEvent) { event.Operation.Operation.LeaseID = "other-lease" },
		"view":   func(event *productionD09CommandSettledEvent) { event.Operation.ViewID = "other-view" },
		"spec digest": func(event *productionD09CommandSettledEvent) {
			event.Operation.ArgumentDigest = "wrong-spec-digest"
		},
		"result digest": func(event *productionD09CommandSettledEvent) {
			event.Operation.ResultDigest = "wrong-result-digest"
		},
		"settlement exit": func(event *productionD09CommandSettledEvent) {
			event.Operation.ExitCode = intPointer(9)
		},
		"input-tree": func(event *productionD09CommandSettledEvent) {
			event.Operation.InputTreeDigest = "wrong-tree"
		},
		"delta": func(event *productionD09CommandSettledEvent) {
			event.Transition.Changes[0].Path = "different.txt"
		},
		"output":         func(event *productionD09CommandSettledEvent) { event.Operation.OutputGeneration = "g9" },
		"receipt-digest": func(event *productionD09CommandSettledEvent) { event.ReceiptDigest = "known-public-hash" },
		"snapshot":       func(event *productionD09CommandSettledEvent) { event.Receipt = []byte(`{"snapshot":"forged"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			event := baseline
			event.Transition = transition
			event.Transition.Changes = append([]delta.ObjectChange(nil), transition.Changes...)
			mutate(&event)
			if err := compareProductionD09CommandEvent(runner, event, retained, providerCall, operation, transition); err == nil {
				t.Fatal("altered command event passed exact call/operation/delta/receipt comparison")
			}
		})
	}
	goodRecord := audit.Record{Event: audit.Event{Kind: "production_d09_command_settled", ID: "event-1"},
		Sequence: 7, Hash: "record-hash"}
	receiptState := productionD09RunEvidence{eventID: "event-1", journalSeq: 7, journalHash: "record-hash"}
	if err := validateProductionD09JournalRecord(&goodRecord, receiptState, audit.Record{}); err != nil {
		t.Fatalf("exact durable journal frame was refused: %v", err)
	}
	for _, bad := range []*audit.Record{
		func() *audit.Record { copy := goodRecord; copy.Sequence++; return &copy }(),
		func() *audit.Record { copy := goodRecord; copy.Hash = "other-hash"; return &copy }(),
		func() *audit.Record { copy := goodRecord; copy.Event.ID = "replayed-event"; return &copy }(),
	} {
		if err := validateProductionD09JournalRecord(bad, receiptState, audit.Record{}); err == nil {
			t.Fatal("wrong/replayed journal sequence/hash/event ID was accepted")
		}
	}
	wrongExpected := goodRecord
	wrongExpected.Sequence++
	if err := validateProductionD09JournalRecord(&goodRecord, receiptState, wrongExpected); err == nil {
		t.Fatal("journal frame was accepted against the wrong expected sequence")
	}
}

func intPointer(value int) *int { return &value }
