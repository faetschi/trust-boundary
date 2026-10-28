//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

const retainedBashInputsEnv = "TBOUND_TEST_RETAINED_BASH_INPUTS"

// retainedOwnerB is a second session whose settled Bash records share one command journal with the first.
var retainedOwnerB = productionRecoveryCommandSession{RunID: "run-retained-owner-b",
	SessionID: "session-retained-owner-b", ConversationID: "conversation-retained-owner-b"}

type retainedLease struct {
	ViewID  string
	LeaseID string
}

// retainedBashRun is one persisted governed history with a single Bash call. Origin is the proposal-call identity its
// provider result authenticates, and Lease is its command lease.
type retainedBashRun struct {
	StoreRootPath       string
	StoreJournalPath    string
	ProviderJournalPath string
	CommandJournalPath  string
	Profile             piruntime.HostProfile
	Template            piruntime.D09CellTemplate
	Owner               productionRecoveryCommandSession
	Origin              delta.OperationIdentity
	Lease               retainedLease
	EffectID            string
	Promoted            bool
}

type retainedBashCase struct {
	Name string
	Run  string
	// CommandJournalPath replaces the run's command journal with a tampered, foreign or merged one.
	CommandJournalPath string
	// Owners are the command journal's pinned sessions; empty means the run's own session.
	Owners []productionRecoveryCommandSession
	// Check is the step after replay: origin, evidence, frozen, borrowed, grown, or empty for replay alone.
	Check string
	// Routing checks only that each command record routes to exactly one pinned owner, and that each lease in Leases
	// resolves to one record.
	Routing bool
	Leases  []retainedLease
	// Refusal is a fragment of the error the case must return; empty means the case must pass.
	Refusal string
}

type retainedBashInputs struct {
	Runs  map[string]retainedBashRun
	Cases []retainedBashCase
}

// TestGovernedRetainedBashRecoversInAFreshProcess recovers persisted production D09 Bash histories in a fresh process.
// The Bash call's terminal origin must resolve from the frozen history, its command evidence must verify with no live
// command cell, and a tampered or foreign command record must refuse.
func TestGovernedRetainedBashRecoversInAFreshProcess(t *testing.T) {
	if path := os.Getenv(retainedBashInputsEnv); path != "" {
		retainedBashChild(t, path)
		return
	}
	inputs := buildRetainedBashInputs(t)
	inputPath := filepath.Join(privateGovernedTestRoot(t), "retained-inputs.json")
	if err := os.WriteFile(inputPath, mustJSON(inputs), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestGovernedRetainedBashRecoversInAFreshProcess$", "-test.v", "-test.count=1")
	child.Env = []string{retainedBashInputsEnv + "=" + inputPath, "TMPDIR=" + os.TempDir()}
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh-process retained Bash: %v\n%s", err, output)
	}
	for _, test := range inputs.Cases {
		if !bytes.Contains(output, []byte("--- PASS: TestGovernedRetainedBashRecoversInAFreshProcess/"+test.Name+" ")) {
			t.Fatalf("fresh process did not pass case %q:\n%s", test.Name, output)
		}
	}
}

func retainedBashChild(t *testing.T, inputPath string) {
	t.Helper()
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	var inputs retainedBashInputs
	if err := json.Unmarshal(data, &inputs); err != nil {
		t.Fatal(err)
	}
	for _, test := range inputs.Cases {
		t.Run(test.Name, func(t *testing.T) {
			err := runRetainedBashCase(t, inputs, test)
			switch {
			case test.Refusal == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case test.Refusal != "" && (err == nil || !strings.Contains(err.Error(), test.Refusal)):
				t.Fatalf("want refusal containing %q, got %v", test.Refusal, err)
			}
		})
	}
}

func runRetainedBashCase(t *testing.T, inputs retainedBashInputs, test retainedBashCase) error {
	t.Helper()
	run, ok := inputs.Runs[test.Run]
	if !ok {
		t.Fatalf("unknown run %q", test.Run)
	}
	commandPath, owners := run.CommandJournalPath, []productionRecoveryCommandSession{run.Owner}
	if test.CommandJournalPath != "" {
		commandPath = test.CommandJournalPath
	}
	if len(test.Owners) > 0 {
		owners = test.Owners
	}
	if test.Routing {
		history, _, err := openRetainedCommandHistory(t, run, commandPath, owners)
		if err != nil {
			return err
		}
		for _, lease := range test.Leases {
			if got := len(history.byLeaseView[lease.ViewID+"\x00"+lease.LeaseID]); got != 1 {
				return fmt.Errorf("lease %s routes to %d command records, want 1", lease.LeaseID, got)
			}
		}
		return nil
	}
	replayed, err := replayRetainedBash(t, run, commandPath, owners)
	if err != nil || test.Check == "" {
		return err
	}
	ctx := context.Background()
	bash, err := retainedBashOperation(replayed.bundle, run.EffectID)
	if err != nil {
		return err
	}
	// A fresh process has no live command cell, so its runner holds no signed evidence for any lease.
	runner := newProductionCommandRunner(func(string) (sessionrepo.CommandRunner, error) {
		return nil, errors.New("fresh process has no live command cell")
	})
	authority, err := retainedAuthority(t, replayed, runner)
	if err != nil {
		return err
	}
	switch test.Check {
	case "origin":
		terminal := replayed.bundle.ApprovedDeltaLedger[len(replayed.bundle.ApprovedDeltaLedger)-1]
		origin, err := authority.ResolveTerminalLeaseOrigin(ctx, replayed.bundle, terminal)
		if err == nil && origin != run.Origin {
			return fmt.Errorf("terminal origin %+v, want %+v", origin, run.Origin)
		}
		return err
	case "evidence":
		return runner.VerifyProductionBashEvidence(ctx, bash)
	case "frozen":
		altered := bash
		altered.ArgumentDigest = "sha256:" + strings.Repeat("9", 64)
		return runner.VerifyProductionBashEvidence(ctx, altered)
	case "borrowed":
		borrowed := bash
		borrowed.EffectID = "sessionrepo-effect-borrowed"
		return runner.VerifyProductionBashEvidence(ctx, borrowed)
	case "grown":
		// Fresh Bash progress after activation extends the journal past the frozen prefix.
		if _, err := replayed.commandJournal.Append(audit.Event{Kind: "fresh_command_progress", ID: "fresh-command-progress",
			Data: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		return runner.VerifyProductionBashEvidence(ctx, bash)
	}
	t.Fatalf("unknown check %q", test.Check)
	return nil
}

// retainedReplay is what a fresh process recovers from one persisted history.
type retainedReplay struct {
	policy         gate.Policy
	bundle         sessionrepo.EvidenceBundle
	storeJournal   *audit.Journal
	commandJournal *audit.Journal
	historical     *governedHistoricalStoreVerifier
}

// openRetainedCommandHistory opens a command journal and routes its records to owners as recovery does.
func openRetainedCommandHistory(t *testing.T, run retainedBashRun, commandPath string,
	owners []productionRecoveryCommandSession) (*productionHistoricalBashHistory, *audit.Journal, error) {
	t.Helper()
	journal, err := openExistingRecoveryJournal(commandPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	identity, err := recoveryFileIdentityDigest(commandPath, false)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	conversations := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		conversations[owner.ConversationID] = struct{}{}
	}
	history, err := newProductionHistoricalBashHistory([]productionRecoveryCommandJournal{{Journal: journal, Path: commandPath,
		Pin: productionHistoricalCommandPin{IdentityDigest: identity, Head: auditHeadHash(trace.Records),
			RecordCount: uint64(len(trace.Records))}, Sessions: owners}}, run.Profile, run.Template, workflowVerticalID, conversations)
	return history, journal, err
}

// replayRetainedBash recovers one persisted history as the recovery replay does: the Store through the read-only
// preview, the provider transcript pinned at its present head, and the command journal with its pinned owners.
func replayRetainedBash(t *testing.T, run retainedBashRun, commandPath string,
	owners []productionRecoveryCommandSession) (retainedReplay, error) {
	t.Helper()
	open := func(path string) *audit.Journal {
		t.Helper()
		journal, err := openExistingRecoveryJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = journal.Close() })
		return journal
	}
	var replayed retainedReplay
	replayed.storeJournal = open(run.StoreJournalPath)
	providerJournal := open(run.ProviderJournalPath)
	history, commandJournal, err := openRetainedCommandHistory(t, run, commandPath, owners)
	if err != nil {
		return retainedReplay{}, err
	}
	replayed.commandJournal = commandJournal
	storeRoot, err := openExistingPrivateRecoveryDirectory(run.StoreRootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer storeRoot.Close()
	managed, err := sessionlaunch.OpenRecoveryPreview(storeRoot, sessionrepo.Options{Journal: replayed.storeJournal,
		PolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy, Limits: workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy,
			Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}})
	if err != nil {
		return retainedReplay{}, fmt.Errorf("recovery preview of the persisted Store: %w", err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	if replayed.bundle, err = managed.Store().EvidenceBundle(); err != nil {
		t.Fatal(err)
	}
	if replayed.policy, err = gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow}}}); err != nil {
		t.Fatal(err)
	}
	providerIdentity, err := recoveryFileIdentityDigest(run.ProviderJournalPath, false)
	if err != nil {
		t.Fatal(err)
	}
	providerTrace, err := providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	pin := workflowrecovery.TranscriptPin{Ordinal: 1, ConversationID: workflowVerticalConversation,
		JournalIdentityDigest: providerIdentity, JournalCount: uint64(len(providerTrace.Records)),
		JournalHead: providerTrace.Records[len(providerTrace.Records)-1].Hash}
	profile := broker.Profile{ID: workflowVerticalProfile, Model: providerbridge.ApprovedModel,
		Messages:     providerbridge.InitialPromptMessages(openrouter.Route{}, governedReplaySystem, governedReplayDeveloper),
		ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer, ResponseIDIssuer: governedResponseIssuer}
	config := providerbridge.Config{WorkflowID: workflowVerticalID, ConversationID: workflowVerticalConversation,
		ProfileID: workflowVerticalProfile, SystemPrompt: governedReplaySystem, DeveloperPrompt: governedReplayDeveloper}
	transcript := GovernedRecoveryTranscript{Journal: providerJournal, JournalPath: run.ProviderJournalPath,
		IdentityDigest: providerIdentity, Pin: pin, Replay: GovernedReplayPins{
			Provider: providerbridge.RecoveryPins{Config: config, Model: providerbridge.ApprovedModel, Policy: replayed.policy},
			Profile:  profile, StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
			BashOutputPaths: []string{workflowVerticalBashOutput}}}
	if replayed.historical, err = newGovernedHistoricalStoreVerifier(replayed.storeJournal, governedReplayStorePolicy,
		governedReplayMetaPolicy, history.VerifySettlement); err != nil {
		t.Fatal(err)
	}
	if err := replayed.historical.VerifyBundle(replayed.bundle); err != nil {
		return retainedReplay{}, err
	}
	if _, _, err := ReplayGovernedHistoryForRecovery(replayed.storeJournal, managed.Store(),
		[]GovernedRecoveryTranscript{transcript}, history); err != nil {
		return retainedReplay{}, err
	}
	return replayed, nil
}

// retainedAuthority binds the recovered history to a new session's authority as activation does: the Store prefix and
// a fresh provider journal for the new conversation.
func retainedAuthority(t *testing.T, replayed retainedReplay, runner *productionCommandRunner) (*SessionOperationAuthority, error) {
	t.Helper()
	retained, err := newGovernedRetainedHistory(replayed.bundle, retainedBashPins(replayed.bundle), replayed.historical)
	if err != nil {
		return nil, err
	}
	fresh, err := audit.Open(filepath.Join(t.TempDir(), "fresh-provider.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	authority, err := NewSessionOperationAuthority(replayed.storeJournal, fresh, workflowVerticalID, "conversation-fresh-process",
		workflowVerticalProfile, replayed.policy, governedReplayStorePolicy, governedReplayMetaPolicy, runner,
		[]string{workflowVerticalBashOutput})
	if err != nil {
		return nil, err
	}
	authority.requireEvidencedBashContainment = true
	authority.retained = retained
	return authority, nil
}

func retainedBashPins(bundle sessionrepo.EvidenceBundle) workflowrecovery.Pins {
	tip := bundle.Generations[len(bundle.Generations)-1]
	return workflowrecovery.Pins{StoreJournalCount: bundle.AuditRecordCount, StoreJournalHead: bundle.AuditHeadHash,
		AdmissionJournalCount: bundle.AdmissionRecordCount, AdmissionJournalHead: bundle.AdmissionHeadHash,
		StoreTipGenerationID: tip.ID, StoreTipTreeDigest: bareStoreTreeDigest(tip.TreeDigest)}
}

func retainedBashOperation(bundle sessionrepo.EvidenceBundle, effectID string) (sessionrepo.OperationEvidence, error) {
	for _, operation := range bundle.Operations {
		if operation.Tool == "bash" && operation.EffectID == effectID {
			return operation, nil
		}
	}
	return sessionrepo.OperationEvidence{}, fmt.Errorf("retained history has no Bash effect %q", effectID)
}

func retainedBashTurns(target string) []providerbridge.DevelopmentProviderTurn {
	return []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "retained-r1", CallID: "retained-bash", Tool: "bash",
			Arguments: json.RawMessage(`{"command":"printf 'bash-stage\\n' >> ` + target + `"}`)},
		{ResponseID: "retained-r2", CallID: "retained-read", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "retained-r3", Text: "done"},
	}
}

// buildRetainedBashRun drives one Bash call through the governed vertical with the production D09 runner for owner. The
// cell appends to target: an authorized file promotes the call, an unauthorized one is refused after its effect.
func buildRetainedBashRun(t *testing.T, owner productionRecoveryCommandSession, target string, promoted bool) retainedBashRun {
	t.Helper()
	commandJournalPath := filepath.Join(privateGovernedTestRoot(t), "command.jsonl")
	commandJournal, err := audit.Open(commandJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commandJournal.Close() })
	profile, template := newBashRunProfile(t)
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runner := newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		cell := &sealedD09Cell{leaseID: leaseID, template: template, templateDigest: templateDigest, target: target}
		return newProductionD09BashRunnerWithInvoker(leaseID, cell, commandJournal, owner.RunID, owner.SessionID,
			workflowVerticalID, owner.ConversationID, profile, template), nil
	})
	f := newWorkflowVerticalFixture(t, false, 2, retainedBashTurns(target), runner)
	bashProposal, bashDecision := ledgerDenyTurn(t, f, "retained-bash", gate.Allow)
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	_, execErr := executor.Execute(f.ctx, bashProposal, bashDecision)
	var denied *protocol.AdmissionDeniedError
	if promoted && execErr != nil {
		t.Fatalf("authorized Bash call refused: %v", execErr)
	}
	if !promoted && (!errors.As(execErr, &denied) || denied.ReasonCode != sessionrepo.DenyReasonUncoveredPath) {
		t.Fatalf("unauthorized Bash call was not refused after its effect: %v", execErr)
	}
	if owner.RunID == replayDeniedBashRunID {
		readProposal, readDecision := ledgerDenyTurn(t, f, "retained-read", gate.Allow)
		if _, err := executor.Execute(f.ctx, readProposal, readDecision); err != nil {
			t.Fatal(err)
		}
		requireFinalAnswer(t, f, "done", 5, 3072)
	}
	bundle, err := f.store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	var bashOp sessionrepo.OperationEvidence
	for _, operation := range bundle.Operations {
		if operation.Tool == "bash" {
			bashOp = operation
		}
	}
	proposalID, err := durableTrustedIdentity("proposal", bashProposal, bashDecision)
	if err != nil {
		t.Fatal(err)
	}
	f.conversation.Close()
	for _, closer := range []func() error{f.store.Close, f.storeJournal.Close, f.providerJournal.Close, commandJournal.Close} {
		if err := closer(); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Dir(f.providerJournalPath)
	return retainedBashRun{StoreRootPath: filepath.Join(dir, "store"), StoreJournalPath: filepath.Join(dir, "store-audit.jsonl"),
		ProviderJournalPath: f.providerJournalPath, CommandJournalPath: commandJournalPath, Profile: profile, Template: template,
		Owner: owner, Origin: delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: proposalID,
			CallIssuer: governedCallIssuer, CallID: bashProposal.ToolCallID},
		Lease: retainedLease{ViewID: bashOp.ViewID, LeaseID: bashOp.Operation.LeaseID}, EffectID: bashOp.EffectID, Promoted: promoted}
}

// buildRetainedBashInputs persists the promoted, denied and second-owner histories, and derives the tampered,
// foreign, merged and grown command journals the cases read.
func buildRetainedBashInputs(t *testing.T) retainedBashInputs {
	t.Helper()
	owner := productionRecoveryCommandSession{RunID: replayDeniedBashRunID, SessionID: workflowVerticalSessionID,
		ConversationID: workflowVerticalConversation}
	promoted := buildRetainedBashRun(t, owner, workflowVerticalBashOutput, true)
	denied := buildRetainedBashRun(t, owner, "task.txt", false)
	second := buildRetainedBashRun(t, retainedOwnerB, workflowVerticalBashOutput, true)
	writeJournal := func(name string, journal *audit.Journal) string {
		t.Helper()
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		data, err := marshalGovernedAuditRecords(trace.Records)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(privateGovernedTestRoot(t), name+".jsonl")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	promotedTrace := func(path string) audit.Trace {
		journal, err := openExistingRecoveryJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		return trace
	}
	// alter derives a command journal in which each settled record of the promoted run is changed by change.
	alter := func(name string, change func(*productionD09CommandSettledEvent)) string {
		t.Helper()
		derived := derivedJournal(t, promotedTrace(promoted.CommandJournalPath), func(_ int, event audit.Event) (audit.Event, bool) {
			if event.Kind == productionD09SettledKind {
				var payload productionD09CommandSettledEvent
				if err := unmarshalGovernedJSON(event.Data, &payload); err != nil {
					t.Fatal(err)
				}
				change(&payload)
				event.Data = mustJSON(payload)
			}
			return event, true
		})
		return writeJournal(name, derived)
	}
	alteredOrigin := alter("altered-origin", func(p *productionD09CommandSettledEvent) { p.ProviderCall.CallID = "forged-call" })
	alteredReceipt := alter("altered-receipt", func(p *productionD09CommandSettledEvent) {
		var receipt piruntime.D09CommandReceipt
		if err := unmarshalGovernedJSON(p.Receipt, &receipt); err != nil {
			t.Fatal(err)
		}
		receipt.ExitCode++
		p.Receipt = mustJSON(receipt)
	})
	alteredOperation := alter("altered-operation", func(p *productionD09CommandSettledEvent) {
		p.Operation.ResultDigest = "sha256:" + strings.Repeat("7", 64)
	})
	foreignOwner := alter("foreign-owner", func(p *productionD09CommandSettledEvent) { p.RunID = "run-foreign" })
	grown := alter("grown", func(p *productionD09CommandSettledEvent) {})
	merged := func(name string, paths ...string) string {
		t.Helper()
		path := filepath.Join(privateGovernedTestRoot(t), name+".jsonl")
		journal, err := audit.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		for _, source := range paths {
			for _, record := range promotedTrace(source).Records {
				if _, err := journal.Append(record.Event); err != nil {
					t.Fatal(err)
				}
			}
		}
		return path
	}
	owners := []productionRecoveryCommandSession{owner, retainedOwnerB}
	both := merged("merged-owners", promoted.CommandJournalPath, second.CommandJournalPath)
	return retainedBashInputs{
		Runs: map[string]retainedBashRun{"promoted": promoted, "denied": denied, "second": second},
		Cases: []retainedBashCase{
			{Name: "promoted_origin_resolves_from_history", Run: "promoted", Check: "origin"},
			{Name: "promoted_evidence_without_live_runner", Run: "promoted", Check: "evidence"},
			{Name: "denied_evidence_without_live_runner", Run: "denied", Check: "evidence"},
			{Name: "promoted_frozen_evidence_altered_refused", Run: "promoted", Check: "frozen", Refusal: "frozen"},
			{Name: "promoted_borrowed_effect_refused", Run: "promoted", Check: "borrowed", Refusal: "lease-verify-no-runner"},
			{Name: "promoted_grown_journal_keeps_prefix", Run: "promoted", CommandJournalPath: grown, Check: "grown"},
			{Name: "promoted_altered_origin_refused", Run: "promoted", CommandJournalPath: alteredOrigin,
				Refusal: "does not bind the exact provider proposal"},
			{Name: "promoted_altered_receipt_refused", Run: "promoted", CommandJournalPath: alteredReceipt,
				Refusal: "validate retained D09 command receipt"},
			{Name: "promoted_altered_operation_refused", Run: "promoted", CommandJournalPath: alteredOperation,
				Refusal: "does not bind the exact provider proposal and Store operation"},
			{Name: "promoted_foreign_owner_refused", Run: "promoted", CommandJournalPath: foreignOwner, Refusal: "is not bound to"},
			{Name: "two_owners_route_both", Run: "promoted", CommandJournalPath: both, Owners: owners, Routing: true,
				Leases: []retainedLease{promoted.Lease, second.Lease}},
			{Name: "two_owners_missing_owner_refused", Run: "promoted", CommandJournalPath: both,
				Owners: []productionRecoveryCommandSession{owner}, Routing: true, Refusal: "is not bound to"},
		},
	}
}
